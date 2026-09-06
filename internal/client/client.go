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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	userAgent              = "terraform-provider-paimon"
	maxAPIResponseBodySize = 16 << 20
	maxReadAttempts        = 3
	baseReadRetryDelay     = 100 * time.Millisecond
	maxReadRetryDelay      = 2 * time.Second
)

var errMutationOutcomeUncertain = errors.New("call Paimon REST API")

type Config struct {
	URI             string
	Warehouse       string
	AuthProvider    string
	Token           string
	DLF             *DLFConfig
	Prefix          string
	Headers         map[string]string
	HTTPClient      *http.Client
	RequestTimeout  time.Duration
	RecoveryTimeout time.Duration
}

type Client struct {
	baseURL         *url.URL
	warehouse       string
	userPrefix      string
	httpClient      *http.Client
	auth            requestAuthenticator
	recoveryTimeout time.Duration

	mu         sync.Mutex
	configured bool
	prefix     string
	headers    map[string]string
}

type APIError struct {
	StatusCode   int     `json:"-"`
	Code         int     `json:"code"`
	Message      string  `json:"message"`
	ResourceType *string `json:"resourceType"`
	ResourceName *string `json:"resourceName"`
	RequestID    string  `json:"-"`
}

func (e *APIError) Error() string {
	detail := fmt.Sprintf("Paimon REST API returned HTTP %d", e.StatusCode)
	if e.Code != 0 {
		detail += fmt.Sprintf(" with code %d", e.Code)
	}
	if e.RequestID != "" {
		detail += " (request ID " + e.RequestID + ")"
	}

	return detail
}

var requestIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Only expose conventional UUID correlation IDs, never arbitrary response text
// or an echoed authentication header.
func safeResponseRequestID(response *http.Response) string {
	for _, name := range []string{"X-Acs-Request-Id", "X-Dlf-Request-Id", "X-Request-Id"} {
		candidate := response.Header.Get(name)
		if !requestIDPattern.MatchString(candidate) {
			continue
		}
		if response.Request != nil {
			for _, values := range response.Request.Header {
				for _, value := range values {
					if strings.Contains(value, candidate) {
						return ""
					}
				}
			}
		}

		return candidate
	}

	return ""
}

func IsNotFound(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// IsMutationOutcomeUncertain reports whether a failed mutation may have been
// accepted remotely despite the client receiving an error.
func IsMutationOutcomeUncertain(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= http.StatusInternalServerError
	}

	return errors.Is(err, errMutationOutcomeUncertain)
}

func New(config Config) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.URI))
	if err != nil {
		return nil, fmt.Errorf("parse Paimon REST URI: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, errors.New("Paimon REST URI must use http or https")
	}
	if baseURL.Host == "" {
		return nil, errors.New("Paimon REST URI must include a host")
	}
	if baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("Paimon REST URI must not include a query or fragment")
	}

	httpClient := noRedirectHTTPClient(config.HTTPClient)
	if config.RequestTimeout < 0 || config.RecoveryTimeout < 0 {
		return nil, errors.New("request and recovery timeouts must be positive")
	}
	if config.RequestTimeout > 0 {
		httpClient.Timeout = config.RequestTimeout
	}

	configuredAuth := strings.ToLower(strings.TrimSpace(config.AuthProvider))
	if configuredAuth == "" && config.Token != "" {
		configuredAuth = AuthProviderBearer
	}
	var auth requestAuthenticator
	switch configuredAuth {
	case "":
		if config.DLF != nil {
			return nil, errors.New("DLF configuration requires auth provider dlf")
		}
	case AuthProviderBearer:
		if config.Token == "" {
			return nil, errors.New("bear auth provider requires a token")
		}
		if config.DLF != nil {
			return nil, errors.New("bear auth provider cannot be combined with DLF configuration")
		}
		auth = &bearerAuthenticator{token: config.Token}
	case AuthProviderDLF:
		if config.Token != "" {
			return nil, errors.New("DLF auth provider cannot be combined with a bearer token")
		}
		if config.DLF == nil {
			return nil, errors.New("DLF auth provider requires DLF configuration")
		}
		auth, err = newDLFAuthenticator(baseURL, *config.DLF, httpClient)
		if err != nil {
			return nil, fmt.Errorf("configure DLF authentication: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported auth provider %q; expected bear or dlf", configuredAuth)
	}

	return &Client{
		baseURL:         baseURL,
		warehouse:       config.Warehouse,
		userPrefix:      config.Prefix,
		httpClient:      httpClient,
		recoveryTimeout: config.RecoveryTimeout,
		auth:            auth,
		prefix:          config.Prefix,
		headers:         cloneMap(config.Headers),
	}, nil
}

// RecoveryTimeout bounds provider reconciliation separately from each request.
func (c *Client) RecoveryTimeout() time.Duration {
	if c == nil || c.recoveryTimeout == 0 {
		return 5 * time.Second
	}

	return c.recoveryTimeout
}

func (c *Client) CreateDatabase(ctx context.Context, name string, options map[string]string) error {
	request := createDatabaseRequest{Name: name, Options: nonNilMap(options)}

	return c.do(ctx, http.MethodPost, []string{"v1", c.catalogPrefix(), "databases"}, nil, request, nil)
}

func (c *Client) GetDatabase(ctx context.Context, name string) (*Database, error) {
	var response Database
	err := c.do(ctx, http.MethodGet, []string{"v1", c.catalogPrefix(), "databases", name}, nil, nil, &response)

	return &response, err
}

func (c *Client) AlterDatabase(ctx context.Context, name string, removals []string, updates map[string]string) error {
	sort.Strings(removals)
	request := alterDatabaseRequest{Removals: nonNilSlice(removals), Updates: nonNilMap(updates)}

	return c.do(ctx, http.MethodPost, []string{"v1", c.catalogPrefix(), "databases", name}, nil, request, nil)
}

func (c *Client) DropDatabase(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, []string{"v1", c.catalogPrefix(), "databases", name}, nil, nil, nil)
}

func (c *Client) CreateTable(ctx context.Context, database, name string, schema Schema) error {
	request := createTableRequest{
		Identifier: Identifier{Database: database, Object: name},
		Schema:     schema,
	}

	return c.do(ctx, http.MethodPost, []string{"v1", c.catalogPrefix(), "databases", database, "tables"}, nil, request, nil)
}

func (c *Client) GetTable(ctx context.Context, database, name string) (*Table, error) {
	var response Table
	err := c.do(ctx, http.MethodGet, []string{"v1", c.catalogPrefix(), "databases", database, "tables", name}, nil, nil, &response)

	return &response, err
}

func (c *Client) AlterTable(ctx context.Context, database, name string, changes []SchemaChange) error {
	request := alterTableRequest{Changes: nonNilSlice(changes)}

	return c.do(ctx, http.MethodPost, []string{"v1", c.catalogPrefix(), "databases", database, "tables", name}, nil, request, nil)
}

func (c *Client) DropTable(ctx context.Context, database, name string) error {
	return c.do(ctx, http.MethodDelete, []string{"v1", c.catalogPrefix(), "databases", database, "tables", name}, nil, nil, nil)
}

// catalogPrefix is evaluated after ensureConfigured in do. Returning the user
// value here is harmless because do rebuilds the path after configuration.
func (c *Client) catalogPrefix() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.prefix
}

func (c *Client) do(ctx context.Context, method string, segments []string, query url.Values, body, result any) error {
	if err := c.ensureConfigured(ctx); err != nil {
		return err
	}

	// Callers construct paths before the config request. Replace the prefix
	// segment with the server-merged prefix before issuing the operation.
	if len(segments) > 1 {
		segments[1] = c.catalogPrefix()
	}

	return c.doRaw(ctx, method, segments, query, body, result, c.requestHeaders())
}

func (c *Client) ensureConfigured(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.configured {
		return nil
	}

	query := make(url.Values)
	if c.warehouse != "" {
		query.Set("warehouse", c.warehouse)
	}

	var response ConfigResponse
	if err := c.doRaw(ctx, http.MethodGet, []string{"v1", "config"}, query, nil, &response, c.headers); err != nil {
		return fmt.Errorf("load Paimon REST catalog config: %w", err)
	}

	merged := cloneMap(response.Defaults)
	if c.userPrefix != "" {
		merged["prefix"] = c.userPrefix
	}
	for key, value := range c.headers {
		merged["header."+key] = value
	}
	for key, value := range response.Overrides {
		merged[key] = value
	}

	c.prefix = merged["prefix"]
	configuredHeaders := make(map[string]string)
	for key, value := range merged {
		if strings.HasPrefix(key, "header.") && len(key) > len("header.") {
			configuredHeaders[strings.TrimPrefix(key, "header.")] = value
		}
	}
	c.headers = configuredHeaders
	c.configured = true

	return nil
}

func (c *Client) requestHeaders() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return cloneMap(c.headers)
}

func (c *Client) doRaw(ctx context.Context, method string, segments []string, query url.Values, body, result any, headers map[string]string) error {
	var requestBody []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Paimon REST request: %w", err)
		}
		requestBody = encoded
	}

	attempts := 1
	if method == http.MethodGet || method == http.MethodHead {
		attempts = maxReadAttempts
	}
	var response *http.Response
	for attempt := 0; attempt < attempts; attempt++ {
		endpoint := c.endpoint(segments, query)
		request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(requestBody))
		if err != nil {
			return errors.New("create Paimon REST request")
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", userAgent)
		if len(requestBody) > 0 {
			request.Header.Set("Content-Type", "application/json")
		}
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		if c.auth != nil {
			if err := c.auth.Apply(ctx, request, request.URL.EscapedPath(), request.URL.Query(), requestBody); err != nil {
				return fmt.Errorf("authenticate Paimon REST request: %w", err)
			}
		}

		response, err = c.httpClient.Do(request)
		if err != nil {
			if strings.Contains(err.Error(), "Paimon REST API redirects are not allowed") {
				return errors.New("call Paimon REST API: redirects are not allowed")
			}
			if attempt < attempts-1 && ctx.Err() == nil {
				if err := waitForReadRetry(ctx, baseReadRetryDelay<<attempt); err != nil {
					return err
				}

				continue
			}

			return errMutationOutcomeUncertain
		}
		if attempt < attempts-1 && retryableReadStatus(response.StatusCode) {
			delay := readRetryDelay(response.Header.Get("Retry-After"), attempt, time.Now())
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if err := waitForReadRetry(ctx, delay); err != nil {
				return err
			}

			continue
		}

		break
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		apiErr := &APIError{StatusCode: response.StatusCode, RequestID: safeResponseRequestID(response)}
		limited := io.LimitReader(response.Body, 1<<20)
		if err := json.NewDecoder(limited).Decode(apiErr); err != nil && err != io.EOF {
			apiErr.Message = http.StatusText(response.StatusCode)
		}

		return apiErr
	}

	// Mutations do not consume a response representation. Once the server has
	// confirmed success, a truncated unused body must not turn it into a failed
	// create with no Terraform identity. The resource verifies state by reading it.
	if result == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBodySize+1))
	if err != nil {
		return errors.New("read Paimon REST response")
	}
	if len(contents) > maxAPIResponseBodySize {
		return errors.New("Paimon REST response exceeded 16 MiB size limit")
	}

	if err := json.Unmarshal(contents, result); err != nil {
		return fmt.Errorf("decode Paimon REST response: %w", err)
	}

	return nil
}

func retryableReadStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func readRetryDelay(retryAfter string, attempt int, now time.Time) time.Duration {
	delay := baseReadRetryDelay << attempt
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds >= 0 {
		delay = time.Duration(seconds) * time.Second
	} else if retryAt, err := http.ParseTime(retryAfter); err == nil {
		delay = retryAt.Sub(now)
		if delay < 0 {
			delay = 0
		}
	}
	if delay > maxReadRetryDelay {
		return maxReadRetryDelay
	}

	return delay
}

func waitForReadRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func noRedirectHTTPClient(input *http.Client) *http.Client {
	if input == nil {
		input = &http.Client{Timeout: 30 * time.Second}
	}
	output := *input
	output.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("Paimon REST API redirects are not allowed")
	}

	return &output
}

func (c *Client) endpoint(segments []string, query url.Values) string {
	endpoint := *c.baseURL
	escapedPath := strings.TrimRight(endpoint.EscapedPath(), "/")
	escapedPath += logicalResourcePath(segments)
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		decodedPath = escapedPath
	}
	endpoint.Path = decodedPath
	endpoint.RawPath = escapedPath
	endpoint.RawQuery = query.Encode()

	return endpoint.String()
}

func logicalResourcePath(segments []string) string {
	var builder strings.Builder
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		builder.WriteByte('/')
		builder.WriteString(javaFormEncode(segment))
	}

	return builder.String()
}

func cloneMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}

	return output
}

func nonNilMap(input map[string]string) map[string]string {
	if input == nil {
		return map[string]string{}
	}

	return input
}

func nonNilSlice[T any](input []T) []T {
	if input == nil {
		return []T{}
	}

	return input
}
