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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDLFDefaultSignatureMatchesPaimonJavaVector(t *testing.T) {
	credentials := dlfCredentials{
		AccessKeyID:     "access-key-id",
		AccessKeySecret: "access-key-secret",
		SecurityToken:   "securityToken",
	}
	body := []byte(`{"name":"database","options":{"a":"b"}}`)
	headers, err := signDLFDefault(
		http.MethodPost,
		"/v1/paimon/databases",
		url.Values{"k1": {"v1"}, "k2": {"v2"}},
		body,
		credentials,
		"cn-hangzhou",
		time.Date(2023, 12, 3, 12, 12, 12, 0, time.UTC),
	)
	require.NoError(t, err)

	assert.Equal(t, "20231203T121212Z", headers["x-dlf-date"])
	assert.Equal(t, "UNSIGNED-PAYLOAD", headers["x-dlf-content-sha256"])
	assert.Equal(t, "v1", headers["x-dlf-version"])
	assert.Equal(t, "application/json", headers["Content-Type"])
	assert.Equal(t, "zlW/6hSKXQKL2z6i931tsg==", headers["Content-MD5"])
	assert.Equal(t, "securityToken", headers["x-dlf-security-token"])
	assert.Equal(t,
		"DLF4-HMAC-SHA256 Credential=access-key-id/20231203/cn-hangzhou/DlfNext/aliyun_v4_request,Signature=c72caf1d40b55b1905d891ee3e3de48a2f8bebefa7e39e4f277acc93c269c5e3",
		headers["Authorization"],
	)
}

func TestDLFOpenAPISignature(t *testing.T) {
	credentials := dlfCredentials{
		AccessKeyID:     "access-key-id",
		AccessKeySecret: "access-key-secret",
		SecurityToken:   "securityToken",
	}
	body := []byte(`{"name":"database","options":{"a":"b"}}`)
	headers, err := signDLFOpenAPI(
		http.MethodPost,
		"/v1/paimon/databases/%24sales+west",
		url.Values{"z": {"$x"}, "a": {"hello world"}},
		body,
		credentials,
		"dlfnext.cn-hangzhou.aliyuncs.com",
		time.Date(2023, 12, 3, 12, 12, 12, 0, time.UTC),
		"fixed-nonce",
	)
	require.NoError(t, err)

	assert.Equal(t, "Sun, 03 Dec 2023 12:12:12 GMT", headers["Date"])
	assert.Equal(t, "dlfnext.cn-hangzhou.aliyuncs.com", headers["Host"])
	assert.Equal(t, "fixed-nonce", headers["x-acs-signature-nonce"])
	assert.Equal(t, "2026-01-18", headers["x-acs-version"])
	assert.Equal(t, "acs access-key-id:b+eUZ2n0jYyvapHDjFqcKLSwf+I=", headers["Authorization"])
}

func TestDLFPathAndQueryEncodingMatchesJava(t *testing.T) {
	assert.Equal(t,
		"/v1/catalog/databases/%24sales+west%2F%E6%9D%B1%E4%BA%AC",
		logicalResourcePath([]string{"v1", "catalog", "databases", "$sales west/東京"}),
	)
	assert.Equal(t, "a=hello+world&empty&z=%24x%7Ey", canonicalDLFQuery(url.Values{
		"z":     {"$x~y"},
		"empty": {""},
		"a":     {"hello world"},
	}))
	assert.Equal(t, "cn-hangzhou", parseDLFRegion("pre-cn-hangzhou.example.com"))
	assert.Equal(t, "cn-hangzhou", parseDLFRegion("dlf-vpc.cn-hangzhou.aliyuncs.com"))
	assert.Equal(t, "cn-hangzhou", parseDLFRegion("cn-hangzhou-vpc.dlf.aliyuncs.com"))
	assert.Equal(t, "cn-shanghai", parseDLFRegion("https://dlf.cn-shanghai.aliyuncs.com/base"))
	assert.Empty(t, parseDLFRegion("dlf-vpc.aliyuncs.com"))
}

type sequenceDLFTokenLoader struct {
	mu          sync.Mutex
	credentials []dlfCredentials
	loads       int
}

type errorDLFTokenLoader struct {
	err error
}

func (l errorDLFTokenLoader) Load(context.Context) (dlfCredentials, error) {
	return dlfCredentials{}, l.err
}

func (l *sequenceDLFTokenLoader) Load(context.Context) (dlfCredentials, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.credentials) == 0 {
		return dlfCredentials{}, errors.New("no credentials")
	}
	index := l.loads
	if index >= len(l.credentials) {
		index = len(l.credentials) - 1
	}
	l.loads++

	return l.credentials[index], nil
}

func (l *sequenceDLFTokenLoader) loadCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.loads
}

func TestRefreshingDLFCredentialProvider(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	loader := &sequenceDLFTokenLoader{credentials: []dlfCredentials{
		{
			AccessKeyID:     "first-id",
			AccessKeySecret: "first-secret",
			Expiration:      now.Add(2 * time.Hour).Format(time.RFC3339),
		},
		{
			AccessKeyID:     "second-id",
			AccessKeySecret: "second-secret",
			Expiration:      now.Add(4 * time.Hour).Format(time.RFC3339),
		},
	}}
	provider := &refreshingDLFCredentialProvider{
		loader:        loader,
		refreshBefore: time.Hour,
		now:           func() time.Time { return now },
	}

	credentials, err := provider.Credentials(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "first-id", credentials.AccessKeyID)

	now = now.Add(time.Hour)
	credentials, err = provider.Credentials(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "first-id", credentials.AccessKeyID, "exactly one hour remaining is still cached")
	assert.Equal(t, 1, loader.loadCount())

	now = now.Add(time.Second)
	credentials, err = provider.Credentials(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "second-id", credentials.AccessKeyID)
	assert.Equal(t, 2, loader.loadCount())
}

func TestRefreshingDLFCredentialProviderUsesUnexpiredCredentialsWhenRefreshFails(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	expiresAt := now.Add(30 * time.Minute)
	provider := &refreshingDLFCredentialProvider{
		loader:        errorDLFTokenLoader{err: errors.New("temporary loader failure")},
		refreshBefore: time.Hour,
		now:           func() time.Time { return now },
		current: &dlfCredentials{
			AccessKeyID:     "cached-id",
			AccessKeySecret: "cached-secret",
			expiresAt:       &expiresAt,
		},
	}

	credentials, err := provider.Credentials(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cached-id", credentials.AccessKeyID)

	now = expiresAt
	_, err = provider.Credentials(context.Background())
	require.EqualError(t, err, "temporary loader failure")
}

func TestRefreshingDLFCredentialProviderRejectsExpiredRefresh(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	cachedExpiration := now.Add(30 * time.Minute)
	loader := &sequenceDLFTokenLoader{credentials: []dlfCredentials{{
		AccessKeyID:     "expired-id",
		AccessKeySecret: "expired-secret",
		Expiration:      now.Add(-time.Minute).Format(time.RFC3339),
	}}}
	provider := &refreshingDLFCredentialProvider{
		loader:        loader,
		refreshBefore: time.Hour,
		now:           func() time.Time { return now },
		current: &dlfCredentials{
			AccessKeyID:     "cached-id",
			AccessKeySecret: "cached-secret",
			expiresAt:       &cachedExpiration,
		},
	}

	credentials, err := provider.Credentials(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cached-id", credentials.AccessKeyID)

	provider.current = nil
	_, err = provider.Credentials(context.Background())
	require.EqualError(t, err, "loaded DLF credentials are expired")
}

func TestFileDLFTokenLoaderLoadsSTSAndDoesNotLeakMalformedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	loader := &fileDLFTokenLoader{path: path, maxAttempts: 1}
	require.NoError(t, os.WriteFile(path, []byte(`{
		"AccessKeyId":"dynamic-id",
		"AccessKeySecret":"dynamic-secret",
		"SecurityToken":"dynamic-sts",
		"Expiration":"2026-08-20T12:00:00Z"
	}`), 0o600))

	credentials, err := loader.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "dynamic-id", credentials.AccessKeyID)
	assert.Equal(t, "dynamic-secret", credentials.AccessKeySecret)
	assert.Equal(t, "dynamic-sts", credentials.SecurityToken)
	require.NotNil(t, credentials.expiresAt)

	require.NoError(t, os.WriteFile(path, []byte(`{"AccessKeySecret":"must-not-appear-in-error"`), 0o600))
	_, err = loader.Load(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "must-not-appear-in-error")
}

func TestFileDLFTokenLoaderRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", (1<<20)+1)), 0o600))

	loader := &fileDLFTokenLoader{path: path, maxAttempts: 1}
	_, err := loader.Load(context.Background())
	require.EqualError(t, err, "DLF token file is larger than 1 MiB")
}

func TestECSDLFTokenLoaderDiscoversAndCachesRole(t *testing.T) {
	var roleRequests atomic.Int32
	var credentialRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/api/token":
			assert.Equal(t, http.MethodPut, r.Method)
			_, _ = w.Write([]byte("metadata-token"))
		case "/metadata/":
			roleRequests.Add(1)
			_, _ = w.Write([]byte("role-a\n"))
		case "/metadata/role-a":
			credentialRequests.Add(1)
			_ = json.NewEncoder(w).Encode(dlfCredentials{
				AccessKeyID:     "ecs-id",
				AccessKeySecret: "ecs-secret",
				SecurityToken:   "ecs-sts",
				Expiration:      "2026-08-20T12:00:00Z",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	loader := &ecsDLFTokenLoader{
		metadataURL: server.URL + "/metadata/",
		httpClient:  server.Client(),
	}
	for range 2 {
		credentials, err := loader.Load(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "ecs-id", credentials.AccessKeyID)
		assert.Equal(t, "ecs-sts", credentials.SecurityToken)
	}
	assert.Equal(t, int32(1), roleRequests.Load())
	assert.Equal(t, int32(2), credentialRequests.Load())
}

func TestDLFClientSignsConfigAndCatalogRequests(t *testing.T) {
	fixedTime := time.Date(2023, 12, 3, 12, 12, 12, 0, time.UTC)
	var signedRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		assert.True(t, strings.HasPrefix(authorization, "DLF4-HMAC-SHA256 Credential=access-key-id/"))
		assert.Equal(t, "securityToken", r.Header.Get("x-dlf-security-token"))
		assert.Equal(t, "20231203T121212Z", r.Header.Get("x-dlf-date"))
		signedRequests.Add(1)

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/config":
			assert.Equal(t, "warehouse-a", r.URL.Query().Get("warehouse"))
			writeJSON(t, w, ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/catalog/databases":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := New(Config{
		URI:          server.URL,
		Warehouse:    "warehouse-a",
		AuthProvider: AuthProviderDLF,
		DLF: &DLFConfig{
			Region:          "cn-hangzhou",
			AccessKeyID:     "access-key-id",
			AccessKeySecret: "access-key-secret",
			SecurityToken:   "securityToken",
			Now:             func() time.Time { return fixedTime },
		},
	})
	require.NoError(t, err)
	require.NoError(t, api.CreateDatabase(context.Background(), "analytics", nil))
	assert.Equal(t, int32(2), signedRequests.Load())
}

func TestDLFClientSignsFinalPathIncludingBasePath(t *testing.T) {
	fixedTime := time.Date(2023, 12, 3, 12, 12, 12, 0, time.UTC)
	credentials := dlfCredentials{AccessKeyID: "access-key-id", AccessKeySecret: "access-key-secret"}
	for _, algorithm := range []string{DLFSigningDefault, DLFSigningOpenAPI} {
		t.Run(algorithm, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, readErr := io.ReadAll(r.Body)
				require.NoError(t, readErr)
				var expected map[string]string
				var err error
				switch algorithm {
				case DLFSigningDefault:
					expected, err = signDLFDefault(r.Method, r.URL.EscapedPath(), r.URL.Query(), body, credentials, "cn-hangzhou", fixedTime)
				case DLFSigningOpenAPI:
					expected, err = signDLFOpenAPI(r.Method, r.URL.EscapedPath(), r.URL.Query(), body, credentials, r.Host, fixedTime, "fixed-nonce")
				}
				require.NoError(t, err)
				assert.Equal(t, expected["Authorization"], r.Header.Get("Authorization"))

				switch r.URL.Path {
				case "/gateway/v1/config":
					writeJSON(t, w, ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})
				case "/gateway/v1/catalog/databases":
					w.WriteHeader(http.StatusOK)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			api, err := New(Config{
				URI:          server.URL + "/gateway/",
				AuthProvider: AuthProviderDLF,
				DLF: &DLFConfig{
					Region:           "cn-hangzhou",
					SigningAlgorithm: algorithm,
					AccessKeyID:      credentials.AccessKeyID,
					AccessKeySecret:  credentials.AccessKeySecret,
					Now:              func() time.Time { return fixedTime },
					Nonce:            func() (string, error) { return "fixed-nonce", nil },
				},
			})
			require.NoError(t, err)
			require.NoError(t, api.CreateDatabase(context.Background(), "analytics", nil))
		})
	}
}

func TestDLFClientReloadsRotatedTokenFile(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	tokenPath := filepath.Join(t.TempDir(), "token.json")
	writeToken := func(accessKeyID, accessKeySecret, securityToken string, expiration time.Time) {
		t.Helper()
		contents, err := json.Marshal(dlfCredentials{
			AccessKeyID:     accessKeyID,
			AccessKeySecret: accessKeySecret,
			SecurityToken:   securityToken,
			Expiration:      expiration.Format(time.RFC3339),
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(tokenPath, contents, 0o600))
	}
	writeToken("first-id", "first-secret", "first-sts", now.Add(30*time.Minute))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/config":
			assert.Contains(t, r.Header.Get("Authorization"), "Credential=first-id/")
			assert.Equal(t, "first-sts", r.Header.Get("x-dlf-security-token"))
			writeToken("second-id", "second-secret", "second-sts", now.Add(4*time.Hour))
			writeJSON(t, w, ConfigResponse{Defaults: map[string]string{"prefix": "catalog"}})
		case "/v1/catalog/databases":
			assert.Contains(t, r.Header.Get("Authorization"), "Credential=second-id/")
			assert.Equal(t, "second-sts", r.Header.Get("x-dlf-security-token"))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := New(Config{
		URI:          server.URL,
		AuthProvider: AuthProviderDLF,
		DLF: &DLFConfig{
			Region:    "cn-hangzhou",
			TokenPath: tokenPath,
			Now:       func() time.Time { return now },
		},
	})
	require.NoError(t, err)
	require.NoError(t, api.CreateDatabase(context.Background(), "analytics", nil))
}

func TestNewDLFAuthenticatorInfersSigningSettings(t *testing.T) {
	openAPIEndpoint, err := url.Parse("https://dlfnext.cn-hangzhou.aliyuncs.com:8443/base")
	require.NoError(t, err)
	authenticator, err := newDLFAuthenticator(openAPIEndpoint, DLFConfig{
		AccessKeyID:     "id",
		AccessKeySecret: "secret",
	}, http.DefaultClient)
	require.NoError(t, err)
	assert.Equal(t, DLFSigningOpenAPI, authenticator.algorithm)
	assert.Equal(t, "dlfnext.cn-hangzhou.aliyuncs.com:8443", authenticator.host)

	defaultEndpoint, err := url.Parse("https://dlf.cn-shanghai.aliyuncs.com")
	require.NoError(t, err)
	authenticator, err = newDLFAuthenticator(defaultEndpoint, DLFConfig{
		AccessKeyID:     "id",
		AccessKeySecret: "secret",
	}, http.DefaultClient)
	require.NoError(t, err)
	assert.Equal(t, DLFSigningDefault, authenticator.algorithm)
	assert.Equal(t, "cn-shanghai", authenticator.region)
}

func TestNewRejectsConflictingDLFCredentialSources(t *testing.T) {
	_, err := New(Config{
		URI:          "https://dlf.cn-hangzhou.aliyuncs.com",
		AuthProvider: AuthProviderDLF,
		DLF: &DLFConfig{
			AccessKeyID:     "id",
			AccessKeySecret: "secret",
			TokenPath:       "/tmp/token.json",
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one DLF credential source")
	assert.NotContains(t, err.Error(), "secret")
}

func TestUniqueDLFNonceConcurrent(t *testing.T) {
	const count = 100
	values := make(chan string, count)
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			nonce, err := uniqueDLFNonce()
			assert.NoError(t, err)
			values <- nonce
		}()
	}
	group.Wait()
	close(values)

	unique := make(map[string]struct{}, count)
	for value := range values {
		assert.NotEmpty(t, value)
		unique[value] = struct{}{}
	}
	assert.Len(t, unique, count)
}

func TestECSMetadataTokenRefreshAndRequiredMode(t *testing.T) {
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	var tokenRequests, roleRequests atomic.Int32
	var rejectToken atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest/api/token" {
			require.Equal(t, http.MethodPut, r.Method)
			require.Equal(t, "21600", r.Header.Get("X-aliyun-ecs-metadata-token-ttl-seconds"))
			count := tokenRequests.Add(1)
			_, _ = fmt.Fprintf(w, "token-%d", count)

			return
		}
		expected := fmt.Sprintf("token-%d", tokenRequests.Load())
		if r.Header.Get("X-aliyun-ecs-metadata-token") != expected || rejectToken.Swap(false) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("metadata-secret-must-not-escape"))

			return
		}
		if r.URL.Path == "/metadata/" {
			roleRequests.Add(1)
			_, _ = w.Write([]byte("role-a"))

			return
		}
		writeJSON(t, w, dlfCredentials{AccessKeyID: "id", AccessKeySecret: "secret", SecurityToken: "sts"})
	}))
	defer server.Close()
	loader := &ecsDLFTokenLoader{metadataURL: server.URL + "/metadata/", httpClient: noRedirectHTTPClient(server.Client()), now: func() time.Time { return now }}
	for range 2 {
		creds, err := loader.Load(context.Background())
		require.NoError(t, err)
		require.Equal(t, "sts", creds.SecurityToken)
	}
	require.Equal(t, int32(1), tokenRequests.Load())
	now = now.Add(ecsMetadataTokenTTL)
	_, err := loader.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, int32(2), tokenRequests.Load())
	rejectToken.Store(true)
	_, err = loader.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, int32(3), tokenRequests.Load())
	require.Equal(t, int32(1), roleRequests.Load())
}

func TestECSMetadataTokenFailureDoesNotDowngrade(t *testing.T) {
	var ordinaryRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			ordinaryRequests.Add(1)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("metadata-secret-must-not-escape"))
	}))
	defer server.Close()
	loader := &ecsDLFTokenLoader{metadataURL: server.URL + "/metadata/", httpClient: server.Client()}
	_, err := loader.Load(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "metadata-secret-must-not-escape")
	require.Zero(t, ordinaryRequests.Load())
}
