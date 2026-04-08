package smplkit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
)

// --- helpers ---

// makeConfigResource builds a JSON:API config resource for test servers.
func makeConfigResource(id, key string, items map[string]interface{}, envs map[string]interface{}, parent *string) map[string]interface{} {
	wrappedItems := map[string]interface{}{}
	for k, v := range items {
		wrappedItems[k] = map[string]interface{}{"value": v, "type": "JSON"}
	}

	wrappedEnvs := map[string]interface{}{}
	for envName, envEntry := range envs {
		entryMap := envEntry.(map[string]interface{})
		if vals, ok := entryMap["values"]; ok {
			valsMap := vals.(map[string]interface{})
			wrappedVals := map[string]interface{}{}
			for vk, vv := range valsMap {
				wrappedVals[vk] = map[string]interface{}{"value": vv}
			}
			wrappedEnvs[envName] = map[string]interface{}{"values": wrappedVals}
		}
	}

	attrs := map[string]interface{}{
		"name":         key,
		"key":          key,
		"description":  nil,
		"parent":       parent,
		"items":        wrappedItems,
		"environments": wrappedEnvs,
		"created_at":   "2024-01-01T00:00:00Z",
		"updated_at":   "2024-01-01T00:00:00Z",
	}

	return map[string]interface{}{
		"id":         id,
		"type":       "config",
		"attributes": attrs,
	}
}

// startTestServer returns an httptest.Server that serves config list and
// individual config responses.
func startTestServer(t *testing.T, configs []map[string]interface{}) *httptest.Server {
	t.Helper()

	configsByID := map[string]map[string]interface{}{}
	for _, c := range configs {
		configsByID[c["id"].(string)] = c
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": configs}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/configs/", func(w http.ResponseWriter, r *http.Request) {
		// Extract ID from path: /api/v1/configs/{id}
		id := r.URL.Path[len("/api/v1/configs/"):]
		cfg, ok := configsByID[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": cfg}) //nolint:errcheck
	})
	// Flags endpoints needed by lazy init
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/contexts/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// connectClient creates a client pointed at the test server for lazy init testing.
func connectClient(t *testing.T, server *httptest.Server) *smplkit.Client {
	t.Helper()
	client, err := smplkit.NewClient("sk_api_test", "production", "test-service",
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	return client
}

// --- typed accessors ---

func TestTypedAccessors_GetString(t *testing.T) {
	cfgs := []map[string]interface{}{
		makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"name": "Acme", "count": 42},
			map[string]interface{}{}, nil),
	}
	server := startTestServer(t, cfgs)
	client := connectClient(t, server)

	ctx := context.Background()

	val, err := client.Config().GetString(ctx, "app", "name")
	assert.NoError(t, err)
	assert.Equal(t, "Acme", val)

	// Wrong type returns default
	val, err = client.Config().GetString(ctx, "app", "count", "fallback")
	assert.NoError(t, err)
	assert.Equal(t, "fallback", val)

	// Missing key returns empty string
	val, err = client.Config().GetString(ctx, "app", "missing")
	assert.NoError(t, err)
	assert.Equal(t, "", val)
}

func TestTypedAccessors_GetInt(t *testing.T) {
	cfgs := []map[string]interface{}{
		makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"port": 8080, "name": "Acme"},
			map[string]interface{}{}, nil),
	}
	server := startTestServer(t, cfgs)
	client := connectClient(t, server)

	ctx := context.Background()

	val, err := client.Config().GetInt(ctx, "app", "port")
	assert.NoError(t, err)
	assert.Equal(t, 8080, val)

	// Wrong type returns default
	val, err = client.Config().GetInt(ctx, "app", "name", 99)
	assert.NoError(t, err)
	assert.Equal(t, 99, val)

	// Missing key returns 0
	val, err = client.Config().GetInt(ctx, "app", "missing")
	assert.NoError(t, err)
	assert.Equal(t, 0, val)
}

func TestTypedAccessors_GetBool(t *testing.T) {
	cfgs := []map[string]interface{}{
		makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"enabled": true, "name": "Acme"},
			map[string]interface{}{}, nil),
	}
	server := startTestServer(t, cfgs)
	client := connectClient(t, server)

	ctx := context.Background()

	val, err := client.Config().GetBool(ctx, "app", "enabled")
	assert.NoError(t, err)
	assert.True(t, val)

	// Wrong type returns default
	val, err = client.Config().GetBool(ctx, "app", "name", false)
	assert.NoError(t, err)
	assert.False(t, val)

	// Missing key returns false
	val, err = client.Config().GetBool(ctx, "app", "missing")
	assert.NoError(t, err)
	assert.False(t, val)
}

func TestTypedAccessors_NotConnected(t *testing.T) {
	client, err := smplkit.NewClient("sk_api_test", "production", "test-service",
		smplkit.WithBaseURL("http://localhost:0"),
	)
	require.NoError(t, err)

	ctx := context.Background()

	_, err = client.Config().GetString(ctx, "app", "name")
	assert.Error(t, err)

	_, err = client.Config().GetInt(ctx, "app", "port")
	assert.Error(t, err)

	_, err = client.Config().GetBool(ctx, "app", "flag")
	assert.Error(t, err)
}

// --- refresh ---

func TestRefresh_UpdatesCache(t *testing.T) {
	refreshed := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		var retries interface{}
		if !refreshed {
			retries = 3
		} else {
			retries = 7
		}
		cfg := makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"retries": retries},
			map[string]interface{}{}, nil)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{cfg}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/configs/", func(w http.ResponseWriter, r *http.Request) {
		var retries interface{}
		if !refreshed {
			retries = 3
		} else {
			retries = 7
		}
		cfg := makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"retries": retries},
			map[string]interface{}{}, nil)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": cfg}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/contexts/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := smplkit.NewClient("sk_api_test", "production", "test-service",
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	ctx := context.Background()

	val, _ := client.Config().GetInt(ctx, "app", "retries")
	assert.Equal(t, 3, val)

	refreshed = true
	require.NoError(t, client.Config().Refresh(ctx))

	val, _ = client.Config().GetInt(ctx, "app", "retries")
	assert.Equal(t, 7, val)
}

func TestRefresh_ListError(t *testing.T) {
	failList := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		if failList {
			w.WriteHeader(500)
			return
		}
		cfg := makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"a": 1},
			map[string]interface{}{}, nil)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{cfg}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/configs/", func(w http.ResponseWriter, r *http.Request) {
		cfg := makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"a": 1},
			map[string]interface{}{}, nil)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": cfg}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/contexts/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := smplkit.NewClient("sk_api_test", "production", "test-service",
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Trigger lazy init by reading a value
	_, _ = client.Config().GetInt(ctx, "app", "a")

	failList = true
	err = client.Config().Refresh(ctx)
	assert.Error(t, err)
}

func TestRefresh_FetchChainError(t *testing.T) {
	refreshCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		cfg := makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"a": 1},
			map[string]interface{}{}, nil)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{cfg}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/configs/", func(w http.ResponseWriter, r *http.Request) {
		refreshCount++
		if refreshCount <= 1 {
			cfg := makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
				map[string]interface{}{"a": 1},
				map[string]interface{}{}, nil)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"data": cfg}) //nolint:errcheck
		} else {
			w.WriteHeader(500)
		}
	})
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/contexts/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := smplkit.NewClient("sk_api_test", "production", "test-service",
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Trigger lazy init by reading a value
	_, _ = client.Config().GetInt(ctx, "app", "a")

	err = client.Config().Refresh(ctx)
	assert.Error(t, err)
}

func TestRefresh_NotConnected(t *testing.T) {
	client, err := smplkit.NewClient("sk_api_test", "production", "test-service",
		smplkit.WithBaseURL("http://localhost:0"),
	)
	require.NoError(t, err)

	err = client.Config().Refresh(context.Background())
	assert.Error(t, err)
}

// --- onChange ---

func TestOnChange_FiresOnRefresh(t *testing.T) {
	refreshed := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		var retries interface{}
		if !refreshed {
			retries = 3
		} else {
			retries = 7
		}
		cfg := makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"retries": retries},
			map[string]interface{}{}, nil)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{cfg}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/configs/", func(w http.ResponseWriter, r *http.Request) {
		var retries interface{}
		if !refreshed {
			retries = 3
		} else {
			retries = 7
		}
		cfg := makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"retries": retries},
			map[string]interface{}{}, nil)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": cfg}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/contexts/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := smplkit.NewClient("sk_api_test", "production", "test-service",
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Trigger lazy init
	_, _ = client.Config().GetInt(ctx, "app", "retries")

	var events []*smplkit.ConfigChangeEvent
	client.Config().OnChange(func(evt *smplkit.ConfigChangeEvent) {
		events = append(events, evt)
	})

	refreshed = true
	require.NoError(t, client.Config().Refresh(ctx))

	require.Len(t, events, 1)
	assert.Equal(t, "app", events[0].ConfigKey)
	assert.Equal(t, "retries", events[0].ItemKey)
	assert.Equal(t, "manual", events[0].Source)
}

func TestOnChange_FilteredByConfigAndItem(t *testing.T) {
	refreshed := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		var retries, timeout interface{}
		if !refreshed {
			retries = 3
			timeout = 1000
		} else {
			retries = 7
			timeout = 2000
		}
		cfg := makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"retries": retries, "timeout": timeout},
			map[string]interface{}{}, nil)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{cfg}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/configs/", func(w http.ResponseWriter, r *http.Request) {
		var retries, timeout interface{}
		if !refreshed {
			retries = 3
			timeout = 1000
		} else {
			retries = 7
			timeout = 2000
		}
		cfg := makeConfigResource("00000000-0000-0000-0000-000000000001", "app",
			map[string]interface{}{"retries": retries, "timeout": timeout},
			map[string]interface{}{}, nil)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": cfg}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
	})
	mux.HandleFunc("/api/v1/contexts/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := smplkit.NewClient("sk_api_test", "production", "test-service",
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Trigger lazy init
	_, _ = client.Config().GetInt(ctx, "app", "retries")

	var retriesEvents []*smplkit.ConfigChangeEvent
	client.Config().OnChange(func(evt *smplkit.ConfigChangeEvent) {
		retriesEvents = append(retriesEvents, evt)
	}, smplkit.WithConfigKey("app"), smplkit.WithItemKey("retries"))

	refreshed = true
	require.NoError(t, client.Config().Refresh(ctx))

	// Should only get the retries change, not timeout
	require.Len(t, retriesEvents, 1)
	assert.Equal(t, "retries", retriesEvents[0].ItemKey)
}

// --- singleton accessor identity ---

func TestSingletonAccessor(t *testing.T) {
	client, err := smplkit.NewClient("sk_api_test", "production", "test-service",
		smplkit.WithBaseURL("http://localhost:0"),
	)
	require.NoError(t, err)

	assert.Same(t, client.Config(), client.Config())
	assert.Same(t, client.Flags(), client.Flags())
}
