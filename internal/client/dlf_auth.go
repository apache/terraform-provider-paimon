// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements. See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership. The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied. See the License for the
// specific language governing permissions and limitations
// under the License.

package client

import (
	"context"
	"crypto/hmac"
	"crypto/md5" // #nosec G501 -- required by both Alibaba Cloud DLF signing protocols.
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- required by the Alibaba Cloud ROA signing protocol.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	AuthProviderBearer = "bear"
	AuthProviderDLF    = "dlf"

	DLFSigningDefault = "default"
	DLFSigningOpenAPI = "openapi"

	DLFTokenLoaderLocalFile = "local_file"
	DLFTokenLoaderECS       = "ecs"

	defaultECSMetadataURL = "http://100.100.100.200/latest/meta-data/Ram/security-credentials/"
	defaultRefreshBefore  = time.Hour
)

type DLFConfig struct {
	Region           string
	SigningAlgorithm string
	AccessKeyID      string
	AccessKeySecret  string
	SecurityToken    string
	TokenPath        string
	TokenLoader      string
	ECSMetadataURL   string
	ECSRoleName      string

	// The fields below are dependency injection points for tests and embedding.
	RefreshBefore time.Duration
	HTTPClient    *http.Client
	Now           func() time.Time
	Nonce         func() (string, error)
}

type dlfCredentials struct {
	AccessKeyID     string `json:"AccessKeyId"`
	AccessKeySecret string `json:"AccessKeySecret"`
	SecurityToken   string `json:"SecurityToken"`
	Expiration      string `json:"Expiration"`

	expiresAt *time.Time
}

func (c *dlfCredentials) validate() error {
	if strings.TrimSpace(c.AccessKeyID) == "" || strings.TrimSpace(c.AccessKeySecret) == "" {
		return errors.New("DLF credentials must contain AccessKeyId and AccessKeySecret")
	}
	if c.Expiration != "" {
		expiresAt, err := time.Parse(time.RFC3339, c.Expiration)
		if err != nil {
			return errors.New("DLF credential expiration is not RFC3339")
		}
		c.expiresAt = &expiresAt
	}

	return nil
}

type requestAuthenticator interface {
	Apply(context.Context, *http.Request, string, url.Values, []byte) error
}

type bearerAuthenticator struct {
	token string
}

func (a *bearerAuthenticator) Apply(_ context.Context, request *http.Request, _ string, _ url.Values, _ []byte) error {
	request.Header.Set("Authorization", "Bearer "+a.token)

	return nil
}

type dlfAuthenticator struct {
	algorithm   string
	region      string
	host        string
	credentials dlfCredentialProvider
	now         func() time.Time
	nonce       func() (string, error)
}

func newDLFAuthenticator(endpoint *url.URL, config DLFConfig, defaultHTTPClient *http.Client) (*dlfAuthenticator, error) {
	algorithm := strings.ToLower(strings.TrimSpace(config.SigningAlgorithm))
	if algorithm == "" {
		algorithm = DLFSigningDefault
		if strings.Contains(strings.ToLower(endpoint.String()), "dlfnext") {
			algorithm = DLFSigningOpenAPI
		}
	}
	if algorithm != DLFSigningDefault && algorithm != DLFSigningOpenAPI {
		return nil, fmt.Errorf("unsupported DLF signing algorithm %q; expected default or openapi", algorithm)
	}

	region := strings.TrimSpace(config.Region)
	if region == "" && algorithm == DLFSigningDefault {
		region = parseDLFRegion(endpoint.Hostname())
	}
	if algorithm == DLFSigningDefault && region == "" {
		return nil, errors.New("DLF region is required when it cannot be inferred from the endpoint")
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = defaultHTTPClient
	}
	httpClient = noRedirectHTTPClient(httpClient)
	provider, err := newDLFCredentialProvider(config, httpClient)
	if err != nil {
		return nil, err
	}

	now := config.Now
	if now == nil {
		now = time.Now
	}
	nonce := config.Nonce
	if nonce == nil {
		nonce = uniqueDLFNonce
	}

	return &dlfAuthenticator{
		algorithm:   algorithm,
		region:      region,
		host:        endpoint.Host,
		credentials: provider,
		now:         now,
		nonce:       nonce,
	}, nil
}

func (a *dlfAuthenticator) Apply(ctx context.Context, request *http.Request, resourcePath string, query url.Values, body []byte) error {
	credentials, err := a.credentials.Credentials(ctx)
	if err != nil {
		return fmt.Errorf("load DLF credentials: %w", err)
	}

	var headers map[string]string
	switch a.algorithm {
	case DLFSigningDefault:
		headers, err = signDLFDefault(request.Method, resourcePath, query, body, credentials, a.region, a.now().UTC())
	case DLFSigningOpenAPI:
		var nonce string
		nonce, err = a.nonce()
		if err == nil {
			headers, err = signDLFOpenAPI(request.Method, resourcePath, query, body, credentials, a.host, a.now().UTC(), nonce)
		}
	}
	if err != nil {
		return fmt.Errorf("sign DLF request: %w", err)
	}
	for key, value := range headers {
		if strings.EqualFold(key, "Host") {
			request.Host = value

			continue
		}
		request.Header.Set(key, value)
	}

	return nil
}

type dlfCredentialProvider interface {
	Credentials(context.Context) (dlfCredentials, error)
}

type staticDLFCredentialProvider struct {
	credentials dlfCredentials
}

func (p *staticDLFCredentialProvider) Credentials(context.Context) (dlfCredentials, error) {
	return p.credentials, nil
}

type dlfTokenLoader interface {
	Load(context.Context) (dlfCredentials, error)
}

type refreshingDLFCredentialProvider struct {
	loader        dlfTokenLoader
	refreshBefore time.Duration
	now           func() time.Time

	mu      sync.Mutex
	current *dlfCredentials
}

func (p *refreshingDLFCredentialProvider) Credentials(ctx context.Context) (dlfCredentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != nil && !p.shouldRefresh(*p.current) {
		return *p.current, nil
	}
	credentials, err := p.loader.Load(ctx)
	if err != nil {
		if p.current != nil && p.currentUsable(*p.current) {
			return *p.current, nil
		}

		return dlfCredentials{}, err
	}
	if err := credentials.validate(); err != nil {
		if p.current != nil && p.currentUsable(*p.current) {
			return *p.current, nil
		}

		return dlfCredentials{}, err
	}
	if !p.currentUsable(credentials) {
		if p.current != nil && p.currentUsable(*p.current) {
			return *p.current, nil
		}

		return dlfCredentials{}, errors.New("loaded DLF credentials are expired")
	}
	p.current = &credentials

	return credentials, nil
}

func (p *refreshingDLFCredentialProvider) shouldRefresh(credentials dlfCredentials) bool {
	if credentials.expiresAt == nil {
		return false
	}

	return credentials.expiresAt.Sub(p.now()) < p.refreshBefore
}

func (p *refreshingDLFCredentialProvider) currentUsable(credentials dlfCredentials) bool {
	return credentials.expiresAt == nil || credentials.expiresAt.After(p.now())
}

func newDLFCredentialProvider(config DLFConfig, httpClient *http.Client) (dlfCredentialProvider, error) {
	loaderName := strings.ToLower(strings.TrimSpace(config.TokenLoader))
	if loaderName == "" && config.TokenPath != "" {
		loaderName = DLFTokenLoaderLocalFile
	}

	hasStatic := config.AccessKeyID != "" || config.AccessKeySecret != "" || config.SecurityToken != ""
	hasFile := config.TokenPath != "" || loaderName == DLFTokenLoaderLocalFile
	hasECS := loaderName == DLFTokenLoaderECS
	sourceCount := 0
	for _, configured := range []bool{hasStatic, hasFile, hasECS} {
		if configured {
			sourceCount++
		}
	}
	if sourceCount > 1 {
		return nil, errors.New("configure exactly one DLF credential source: static AK/STS, token file, or ECS role")
	}

	if loaderName != "" && loaderName != DLFTokenLoaderLocalFile && loaderName != DLFTokenLoaderECS {
		return nil, fmt.Errorf("unsupported DLF token loader %q; expected local_file or ecs", loaderName)
	}
	if sourceCount == 0 {
		return nil, errors.New("DLF authentication requires static AK/STS credentials, a token path, or the ECS token loader")
	}

	if hasStatic {
		credentials := dlfCredentials{
			AccessKeyID:     config.AccessKeyID,
			AccessKeySecret: config.AccessKeySecret,
			SecurityToken:   config.SecurityToken,
		}
		if err := credentials.validate(); err != nil {
			return nil, err
		}

		return &staticDLFCredentialProvider{credentials: credentials}, nil
	}

	var loader dlfTokenLoader
	switch loaderName {
	case DLFTokenLoaderLocalFile:
		if strings.TrimSpace(config.TokenPath) == "" {
			return nil, errors.New("DLF local_file token loader requires a token path")
		}
		loader = &fileDLFTokenLoader{path: config.TokenPath, maxAttempts: 5, retryBase: time.Second}
	case DLFTokenLoaderECS:
		metadataURL := strings.TrimSpace(config.ECSMetadataURL)
		if metadataURL == "" {
			metadataURL = defaultECSMetadataURL
		}
		parsed, err := url.Parse(metadataURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, errors.New("DLF ECS metadata URL must be an absolute HTTP(S) URL")
		}
		loader = &ecsDLFTokenLoader{
			metadataURL: strings.TrimRight(metadataURL, "/") + "/",
			roleName:    strings.TrimSpace(config.ECSRoleName),
			httpClient:  httpClient,
			now:         config.Now,
		}
	}

	refreshBefore := config.RefreshBefore
	if refreshBefore <= 0 {
		refreshBefore = defaultRefreshBefore
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &refreshingDLFCredentialProvider{
		loader:        loader,
		refreshBefore: refreshBefore,
		now:           now,
	}, nil
}

type fileDLFTokenLoader struct {
	path        string
	maxAttempts int
	retryBase   time.Duration
}

func (l *fileDLFTokenLoader) Load(ctx context.Context) (dlfCredentials, error) {
	attempts := l.maxAttempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		contents, err := readDLFTokenFile(l.path)
		if err == nil {
			var credentials dlfCredentials
			if decodeErr := json.Unmarshal(contents, &credentials); decodeErr == nil {
				if validationErr := credentials.validate(); validationErr == nil {
					return credentials, nil
				}
			}
			err = errors.New("failed to parse DLF token file")
		}
		lastErr = err
		if attempt == attempts {
			break
		}
		delay := time.Duration(attempt) * l.retryBase
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()

			return dlfCredentials{}, ctx.Err()
		case <-timer.C:
		}
	}

	return dlfCredentials{}, lastErr
}

func readDLFTokenFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("failed to read DLF token file")
	}
	defer file.Close()

	contents, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, errors.New("failed to read DLF token file")
	}
	if len(contents) > 1<<20 {
		return nil, errors.New("DLF token file is larger than 1 MiB")
	}

	return contents, nil
}

type ecsDLFTokenLoader struct {
	metadataURL string
	roleName    string
	httpClient  *http.Client

	mu             sync.Mutex
	token          string
	tokenExpiresAt time.Time
	now            func() time.Time
}

func (l *ecsDLFTokenLoader) Load(ctx context.Context) (dlfCredentials, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.roleName == "" {
		role, err := l.getText(ctx, l.metadataURL, 4<<10)
		if err != nil {
			return dlfCredentials{}, fmt.Errorf("load ECS RAM role name: %w", err)
		}
		l.roleName = strings.TrimSpace(role)
		if l.roleName == "" {
			return dlfCredentials{}, errors.New("ECS metadata service returned an empty RAM role name")
		}
	}
	contents, err := l.getText(ctx, l.metadataURL+l.roleName, 1<<20)
	if err != nil {
		return dlfCredentials{}, fmt.Errorf("load ECS RAM role credentials: %w", err)
	}
	var credentials dlfCredentials
	if err := json.Unmarshal([]byte(contents), &credentials); err != nil {
		return dlfCredentials{}, errors.New("failed to parse ECS credential response")
	}
	if err := credentials.validate(); err != nil {
		return dlfCredentials{}, err
	}

	return credentials, nil
}

const ecsMetadataTokenTTL = 6 * time.Hour

func (l *ecsDLFTokenLoader) metadataToken(ctx context.Context) (string, error) {
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.token != "" && now.Before(l.tokenExpiresAt.Add(-time.Minute)) {
		return l.token, nil
	}
	endpoint, err := url.Parse(l.metadataURL)
	if err != nil {
		return "", errors.New("invalid ECS metadata endpoint")
	}
	endpoint.Path = "/latest/api/token"
	endpoint.RawPath, endpoint.RawQuery, endpoint.Fragment = "", "", ""
	token, _, err := l.metadataRequest(ctx, http.MethodPut, endpoint.String(), "", 4<<10)
	if err != nil {
		return "", fmt.Errorf("obtain ECS metadata token: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return r < 0x21 || r > 0x7e }) >= 0 {
		return "", errors.New("ECS metadata returned an invalid access token")
	}
	l.token, l.tokenExpiresAt = token, now.Add(ecsMetadataTokenTTL)

	return token, nil
}

func (l *ecsDLFTokenLoader) getText(ctx context.Context, endpoint string, limit int64) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := l.metadataToken(ctx)
		if err != nil {
			return "", err
		}
		contents, status, err := l.metadataRequest(ctx, http.MethodGet, endpoint, token, limit)
		if attempt == 0 && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
			l.token = ""

			continue
		}

		return contents, err
	}

	return "", errors.New("ECS metadata authentication failed")
}

func (l *ecsDLFTokenLoader) metadataRequest(ctx context.Context, method, endpoint, token string, limit int64) (string, int, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return "", 0, errors.New("create metadata request")
	}
	if method == http.MethodPut {
		request.Header.Set("X-aliyun-ecs-metadata-token-ttl-seconds", "21600")
	} else {
		request.Header.Set("X-aliyun-ecs-metadata-token", token)
	}
	response, err := l.httpClient.Do(request)
	if err != nil {
		return "", 0, errors.New("call metadata service")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", response.StatusCode, fmt.Errorf("metadata service returned HTTP %d", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", response.StatusCode, errors.New("read metadata response")
	}
	if int64(len(contents)) > limit {
		return "", response.StatusCode, errors.New("metadata response exceeded size limit")
	}

	return string(contents), response.StatusCode, nil
}

func signDLFDefault(method, resourcePath string, query url.Values, body []byte, credentials dlfCredentials, region string, now time.Time) (map[string]string, error) {
	dateTime := now.UTC().Format("20060102T150405Z")
	date := dateTime[:8]
	headers := map[string]string{
		"x-dlf-date":           dateTime,
		"x-dlf-content-sha256": "UNSIGNED-PAYLOAD",
		"x-dlf-version":        "v1",
	}
	if len(body) > 0 {
		headers["Content-Type"] = "application/json"
		headers["Content-MD5"] = md5Base64(body)
	}
	if credentials.SecurityToken != "" {
		headers["x-dlf-security-token"] = credentials.SecurityToken
	}

	canonicalRequest := buildDLFDefaultCanonicalRequest(method, resourcePath, query, headers)
	hash := sha256.Sum256([]byte(canonicalRequest))
	scope := strings.Join([]string{date, region, "DlfNext", "aliyun_v4_request"}, "/")
	stringToSign := "DLF4-HMAC-SHA256\n" + dateTime + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	dateKey := hmacSHA256([]byte("aliyun_v4"+credentials.AccessKeySecret), date)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, "DlfNext")
	signingKey := hmacSHA256(serviceKey, "aliyun_v4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	headers["Authorization"] = "DLF4-HMAC-SHA256 Credential=" + credentials.AccessKeyID + "/" + scope + ",Signature=" + signature

	return headers, nil
}

func buildDLFDefaultCanonicalRequest(method, resourcePath string, query url.Values, headers map[string]string) string {
	lines := []string{method, resourcePath, canonicalDLFQuery(query)}
	signedNames := map[string]struct{}{
		"content-md5": {}, "content-type": {}, "x-dlf-content-sha256": {},
		"x-dlf-date": {}, "x-dlf-version": {}, "x-dlf-security-token": {},
	}
	canonicalHeaders := make(map[string]string)
	for key, value := range headers {
		name := strings.ToLower(key)
		if _, ok := signedNames[name]; ok {
			canonicalHeaders[name] = strings.TrimSpace(value)
		}
	}
	names := make([]string, 0, len(canonicalHeaders))
	for name := range canonicalHeaders {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lines = append(lines, name+":"+canonicalHeaders[name])
	}
	lines = append(lines, headers["x-dlf-content-sha256"])

	return strings.Join(lines, "\n")
}

func canonicalDLFQuery(query url.Values) string {
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := ""
		if values := query[key]; len(values) > 0 {
			value = javaFormEncode(values[0])
		}
		part := strings.TrimSpace(key)
		if value != "" {
			part += "=" + strings.TrimSpace(value)
		}
		parts = append(parts, part)
	}

	return strings.Join(parts, "&")
}

func signDLFOpenAPI(method, resourcePath string, query url.Values, body []byte, credentials dlfCredentials, host string, now time.Time, nonce string) (map[string]string, error) {
	if nonce == "" {
		return nil, errors.New("DLF OpenAPI nonce must not be empty")
	}
	headers := map[string]string{
		"Date":                    now.UTC().Format(http.TimeFormat),
		"Accept":                  "application/json",
		"Host":                    host,
		"x-acs-signature-method":  "HMAC-SHA1",
		"x-acs-signature-nonce":   nonce,
		"x-acs-signature-version": "1.0",
		"x-acs-version":           "2026-01-18",
	}
	if len(body) > 0 {
		headers["Content-MD5"] = md5Base64(body)
		headers["Content-Type"] = "application/json"
	}
	if credentials.SecurityToken != "" {
		headers["x-acs-security-token"] = credentials.SecurityToken
	}

	canonicalHeaders := canonicalACSHeaders(headers)
	canonicalResource, err := canonicalACSResource(resourcePath, query)
	if err != nil {
		return nil, err
	}
	stringToSign := strings.Join([]string{
		method,
		headers["Accept"],
		headers["Content-MD5"],
		headers["Content-Type"],
		headers["Date"],
	}, "\n") + "\n" + canonicalHeaders + canonicalResource
	mac := hmac.New(sha1.New, []byte(credentials.AccessKeySecret)) // #nosec G505 -- protocol requirement.
	_, _ = mac.Write([]byte(stringToSign))
	headers["Authorization"] = "acs " + credentials.AccessKeyID + ":" + base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return headers, nil
}

func canonicalACSHeaders(headers map[string]string) string {
	values := make(map[string]string)
	for key, value := range headers {
		name := strings.ToLower(key)
		if strings.HasPrefix(name, "x-acs-") {
			values[name] = strings.TrimSpace(value)
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		builder.WriteString(name)
		builder.WriteByte(':')
		builder.WriteString(values[name])
		builder.WriteByte('\n')
	}

	return builder.String()
}

func canonicalACSResource(resourcePath string, query url.Values) (string, error) {
	path, err := url.QueryUnescape(resourcePath)
	if err != nil {
		return "", errors.New("decode DLF resource path")
	}
	if len(query) == 0 {
		return path, nil
	}
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := ""
		if values := query[key]; len(values) > 0 {
			value = values[0]
		}
		part := key
		if value != "" {
			part += "=" + value
		}
		parts = append(parts, part)
	}

	return path + "?" + strings.Join(parts, "&"), nil
}

func md5Base64(data []byte) string {
	digest := md5.Sum(data) // #nosec G401 -- required by the DLF signing protocol.

	return base64.StdEncoding.EncodeToString(digest[:])
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))

	return mac.Sum(nil)
}

func parseDLFRegion(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if endpoint, err := url.Parse(host); err == nil && endpoint.Hostname() != "" {
		host = endpoint.Hostname()
	} else if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	pattern := regexp.MustCompile(`^[a-z]{2}-[a-z0-9]+(?:-[a-z0-9]+)*$`)
	for _, label := range strings.Split(host, ".") {
		candidate := strings.TrimPrefix(label, "pre-")
		candidate = strings.TrimSuffix(candidate, "-vpc")
		if pattern.MatchString(candidate) {
			return candidate
		}
	}

	return ""
}

func javaFormEncode(value string) string {
	var builder strings.Builder
	for _, b := range []byte(value) {
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '.', b == '-', b == '*', b == '_':
			builder.WriteByte(b)
		case b == ' ':
			builder.WriteByte('+')
		default:
			builder.WriteByte('%')
			builder.WriteString(strings.ToUpper(hex.EncodeToString([]byte{b})))
		}
	}

	return builder.String()
}

var nonceCounter atomic.Uint64

func uniqueDLFNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errors.New("generate random DLF nonce")
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	uuid := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])

	return uuid + strconv.FormatInt(time.Now().UnixMilli(), 10) + strconv.FormatUint(nonceCounter.Add(1), 10), nil
}
