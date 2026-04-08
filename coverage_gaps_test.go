package smplkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genapp "github.com/smplkit/go-sdk/internal/generated/app"
	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
	genflags "github.com/smplkit/go-sdk/internal/generated/flags"
)

// newTestFullClient builds a Client with both config and flags sub-clients
// pointed at the given test server.
func newTestFullClient(t *testing.T, server *httptest.Server, service string) *Client {
	t.Helper()
	httpClient := &http.Client{}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: "sk_test", base: base}

	headerEditor := genconfig.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genConfigClient, _ := genconfig.NewClient(server.URL,
		genconfig.WithHTTPClient(httpClient),
		headerEditor,
	)

	flagsHeaderEditor := genflags.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genFlagsClient, _ := genflags.NewClient(server.URL,
		genflags.WithHTTPClient(httpClient),
		flagsHeaderEditor,
	)

	appHeaderEditor := genapp.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genAppClient, _ := genapp.NewClient(server.URL,
		genapp.WithHTTPClient(httpClient),
		appHeaderEditor,
	)

	c := &Client{
		apiKey:       "sk_test",
		environment:  "test",
		service:      service,
		baseURL:      server.URL,
		httpClient:   httpClient,
		appGenerated: genAppClient,
	}
	c.config = &ConfigClient{client: c, generated: genConfigClient}
	c.flags = &FlagsClient{client: c, generated: genFlagsClient, appGenerated: genAppClient}
	c.flags.runtime = newFlagsRuntime(c.flags)
	return c
}

// --- Lazy init error paths ---

func TestFlagsLazyInit_Error(t *testing.T) {
	mux := http.NewServeMux()
	// flags endpoint returns an error
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"flags down"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	c := newTestFullClient(t, server, "test-service")
	// Lazy init triggered by evaluateHandle should return default on error.
	h := c.flags.runtime.BooleanFlag("test-flag", true)
	result := h.Get(context.Background())
	assert.True(t, result) // Returns default on init failure.
}

func TestConfigLazyInit_Error(t *testing.T) {
	mux := http.NewServeMux()
	// configs endpoint fails
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"configs down"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	c := newTestFullClient(t, server, "test-service")
	_, err := c.config.GetValue(context.Background(), "test-config")
	require.Error(t, err)
}

// --- registerServiceContext error path ---

func TestRegisterServiceContext_HTTPDoError(t *testing.T) {
	// Use a server URL that is immediately closed so httpClient.Do returns a connection error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	serverURL := server.URL
	server.Close() // Close immediately so connections fail.

	httpClient := &http.Client{}
	genAppClient, _ := genapp.NewClient(serverURL, genapp.WithHTTPClient(httpClient))

	c := &Client{
		apiKey:       "sk_test",
		service:      "my-svc",
		baseURL:      serverURL,
		httpClient:   httpClient,
		appGenerated: genAppClient,
	}
	// Should not panic — errors are logged and swallowed.
	c.registerServiceContext(context.Background())
}

func TestRegisterServiceContext_InvalidURL(t *testing.T) {
	// Use an unreachable address to trigger an HTTP error.
	httpClient := &http.Client{}
	genAppClient, _ := genapp.NewClient("http://localhost:1", genapp.WithHTTPClient(httpClient))

	c := &Client{
		apiKey:       "sk_test",
		service:      "my-svc",
		baseURL:      "http://localhost:1",
		httpClient:   httpClient,
		appGenerated: genAppClient,
	}
	// Should not panic — errors are silently swallowed.
	c.registerServiceContext(context.Background())
}

// --- ConfigClient.ensureInit fetchChain error ---

func TestConfigClient_EnsureInit_FetchChainError(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	// List configs returns one config
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"550e8400-e29b-41d4-a716-446655440000","type":"config","attributes":{"name":"Test","key":"test","description":"desc","parent":null,"items":{},"environments":{},"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}}]}`))
	})
	// Get config by ID (fetchChain) returns error
	mux.HandleFunc("/api/v1/configs/550e8400-e29b-41d4-a716-446655440000", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"fetch chain failed"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	c := newTestFullClient(t, server, "test-service")
	_, err := c.config.GetValue(context.Background(), "test")
	require.Error(t, err)
	assert.Greater(t, callCount, 0)
}

// --- NewClient appHeaderEditor coverage ---

func TestNewClient_AppHeaderEditorCoverage(t *testing.T) {
	// Create a real client via NewClient (with custom base URL pointing to a test server),
	// then exercise the app client to trigger the appHeaderEditor closure.
	var appHeaderSeen bool
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/vnd.api+json" {
			appHeaderSeen = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"contexts":[]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c, err := NewClient("sk_test_key", "test", "test-service", WithBaseURL(server.URL))
	require.NoError(t, err)

	// Exercise registerServiceContext which goes through the appGenerated client,
	// triggering the appHeaderEditor closure.
	c.registerServiceContext(context.Background())
	assert.True(t, appHeaderSeen)
}

// --- FlagsRuntime.Evaluate uncovered paths ---

func TestEvaluate_ServiceAutoInjection(t *testing.T) {
	// Set up a runtime with a flagsClient that has a service set.
	fc, _ := newTestFlagsClient(t, nil)
	fc.client.service = "my-svc"
	rt := fc.runtime

	rt.mu.Lock()
	rt.flagStore = map[string]map[string]interface{}{
		"feature-x": {
			"default": false,
			"environments": map[string]interface{}{
				"prod": map[string]interface{}{
					"enabled": true,
					"rules": []interface{}{
						map[string]interface{}{
							"logic": map[string]interface{}{
								"==": []interface{}{map[string]interface{}{"var": "service.key"}, "my-svc"},
							},
							"value": true,
						},
					},
				},
			},
		},
	}
	rt.mu.Unlock()

	// Evaluate without providing service context — should auto-inject.
	result := rt.Evaluate(context.Background(), "feature-x", "prod", nil)
	assert.Equal(t, true, result)
}

func TestEvaluate_ServiceAutoInjection_AlreadyProvided(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.client.service = "my-svc"
	rt := fc.runtime

	rt.mu.Lock()
	rt.flagStore = map[string]map[string]interface{}{
		"feature-x": {
			"default": false,
			"environments": map[string]interface{}{
				"prod": map[string]interface{}{
					"enabled": true,
					"rules": []interface{}{
						map[string]interface{}{
							"logic": map[string]interface{}{
								"==": []interface{}{map[string]interface{}{"var": "service.key"}, "other-svc"},
							},
							"value": true,
						},
					},
				},
			},
		},
	}
	rt.mu.Unlock()

	// Provide an explicit service context — should NOT be overridden.
	contexts := []Context{
		{Type: "service", Key: "other-svc"},
	}
	result := rt.Evaluate(context.Background(), "feature-x", "prod", contexts)
	assert.Equal(t, true, result)
}

func TestEvaluate_NotConnected_FetchesList(t *testing.T) {
	flagsJSON := map[string]interface{}{
		"data": []map[string]interface{}{
			{
				"id":   "550e8400-e29b-41d4-a716-446655440000",
				"type": "flag",
				"attributes": map[string]interface{}{
					"name":        "My Flag",
					"key":         "my-flag",
					"description": "A test flag",
					"default":     "default-val",
					"environments": map[string]interface{}{
						"prod": map[string]interface{}{
							"enabled": true,
							"default": "env-val",
							"rules":   []interface{}{},
						},
					},
					"values": []interface{}{},
				},
			},
		},
	}

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		b, _ := json.Marshal(flagsJSON)
		_, _ = w.Write(b)
	}))
	rt := fc.runtime

	// rt is NOT connected, so Evaluate should fetch.
	result := rt.Evaluate(context.Background(), "my-flag", "prod", nil)
	assert.Equal(t, "env-val", result)
}

func TestEvaluate_NotConnected_FlagNotFound(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	rt := fc.runtime

	// Not connected, flag not in fetched list.
	result := rt.Evaluate(context.Background(), "nonexistent", "prod", nil)
	assert.Nil(t, result)
}

func TestEvaluate_NotConnected_FetchError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server error"}`))
	}))
	rt := fc.runtime

	result := rt.Evaluate(context.Background(), "my-flag", "prod", nil)
	assert.Nil(t, result)
}
