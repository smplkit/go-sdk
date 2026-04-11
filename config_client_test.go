package smplkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
)

// Test IDs — slug-style identifiers.
const (
	testID0 = "my-service"
	testID1 = "test-config"
	testID2 = "my-config"
	testID3 = "test"
	testID4 = "has-children"
	testID5 = "env-test"
	testID6 = "other-config"
)

// sampleConfigJSON returns a JSON:API single-resource response body.
// Items use the typed format: {key: {"value": raw, "type": "STRING"}}.
// Environment overrides use: {envName: {"values": {key: {"value": raw}}}}.
func sampleConfigJSON(id, name string) string {
	return `{
		"data": {
			"id": "` + id + `",
			"type": "config",
			"attributes": {
				"name": "` + name + `",
				"description": "A test config",
				"parent": null,
				"items": {"log_level": {"value": "info", "type": "STRING"}},
				"environments": {"production": {"values": {"log_level": {"value": "warn"}}}},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}
	}`
}

func newTestClient(t *testing.T, handler http.HandlerFunc) *smplkit.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	return client
}

func TestConfigClient_Get(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/configs/my-service", r.URL.Path)
		assert.Equal(t, "Bearer sk_test_key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Accept"))
		assert.Contains(t, r.Header.Get("User-Agent"), "smplkit-go-sdk")

		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON("my-service", "My Service")))
	})

	cfg, err := client.Config().Get(context.Background(), "my-service")
	require.NoError(t, err)
	assert.Equal(t, "my-service", cfg.ID)
	assert.Equal(t, "My Service", cfg.Name)
}

func TestConfigClient_Get_NotFound(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})

	_, err := client.Config().Get(context.Background(), "nonexistent")
	require.Error(t, err)

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestConfigClient_List(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/configs", r.URL.Path)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "a", "type": "config", "attributes": {"name": "A", "items": {}, "environments": {}}},
				{"id": "b", "type": "config", "attributes": {"name": "B", "items": {}, "environments": {}}}
			]
		}`))
	})

	configs, err := client.Config().List(context.Background())
	require.NoError(t, err)
	require.Len(t, configs, 2)
	assert.Equal(t, "A", configs[0].Name)
	assert.Equal(t, "B", configs[1].Name)
}

func TestConfigClient_List_Empty(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})

	configs, err := client.Config().List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, configs)
}

func TestConfigClient_New_Save(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/configs", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		data := body["data"].(map[string]interface{})
		assert.Equal(t, "config", data["type"])
		assert.Equal(t, "new-config", data["id"])

		attrs := data["attributes"].(map[string]interface{})
		assert.Equal(t, "New Config", attrs["name"])

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON("new-config", "New Config")))
	})

	cfg := client.Config().New("new-config",
		smplkit.WithConfigName("New Config"),
		smplkit.WithConfigDescription("A new config"),
	)
	cfg.Items = map[string]interface{}{"enabled": true}
	err := cfg.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "new-config", cfg.ID)
	assert.Equal(t, "New Config", cfg.Name)
}

func TestConfigClient_Save_CreatePath(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/configs", r.URL.Path)

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleConfigJSON("server-assigned-id", "New Config")))
	})

	cfg := client.Config().New("temp-id", smplkit.WithConfigName("New Config"))
	cfg.ID = "" // Clear ID to trigger create path
	err := cfg.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "server-assigned-id", cfg.ID)
	assert.Equal(t, "New Config", cfg.Name)
}

func TestConfigClient_Save_CreatePath_NetworkError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("dial failed")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	cfg := client.Config().New("temp", smplkit.WithConfigName("Test"))
	cfg.ID = ""
	err = cfg.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_Save_CreatePath_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	cfg := client.Config().New("temp", smplkit.WithConfigName("Test"))
	cfg.ID = ""
	err = cfg.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_Save_CreatePath_HTTPError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"validation error"}]}`))
	})

	cfg := client.Config().New("temp", smplkit.WithConfigName("Test"))
	cfg.ID = ""
	err := cfg.Save(context.Background())
	require.Error(t, err)
	var valErr *smplkit.SmplValidationError
	require.True(t, errors.As(err, &valErr))
}

func TestConfigClient_Save_CreatePath_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	})

	cfg := client.Config().New("temp", smplkit.WithConfigName("Test"))
	cfg.ID = ""
	err := cfg.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestConfigClient_New_Save_WithEnvironments(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		data := body["data"].(map[string]interface{})
		attrs := data["attributes"].(map[string]interface{})
		assert.NotNil(t, attrs["environments"])

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON("new-config", "New Config")))
	})

	cfg := client.Config().New("new-config", smplkit.WithConfigName("New Config"))
	cfg.Environments = map[string]map[string]interface{}{
		"production": {"values": map[string]interface{}{"debug": false}},
	}
	err := cfg.Save(context.Background())
	require.NoError(t, err)
}

func TestConfigClient_Delete(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/v1/configs/my-config", r.URL.Path)
		assert.Equal(t, "Bearer sk_test_key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.Config().Delete(context.Background(), "my-config")
	require.NoError(t, err)
}

func TestConfigClient_Save_Update(t *testing.T) {
	configID := testID0

	// Use a single server that handles both GET and PUT (update).
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "My Service")))
		} else {
			assert.Equal(t, "PUT", r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			data := body["data"].(map[string]interface{})
			attrs := data["attributes"].(map[string]interface{})
			assert.Equal(t, "Updated Name", attrs["name"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Updated Name")))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	cfg.Name = "Updated Name"
	desc := "Updated description"
	cfg.Description = &desc
	err = cfg.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", cfg.Name)
}

func TestConfigClient_Save_NotFound(t *testing.T) {
	configID := testID3

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Test")))
		} else {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.Save(context.Background())
	require.Error(t, err)
	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestConfig_MutateItems_Save(t *testing.T) {
	configID := "svc"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		} else {
			assert.Equal(t, "PUT", r.Method)
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			attrs := body["data"].(map[string]interface{})["attributes"].(map[string]interface{})
			items := attrs["items"].(map[string]interface{})
			logItem := items["log_level"].(map[string]interface{})
			assert.Equal(t, "debug", logItem["value"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	cfg.Items["log_level"] = "debug"
	err = cfg.Save(context.Background())
	require.NoError(t, err)
}

func TestConfig_MutateEnvironment_Save(t *testing.T) {
	configID := "svc"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		} else {
			assert.Equal(t, "PUT", r.Method)
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			attrs := body["data"].(map[string]interface{})["attributes"].(map[string]interface{})
			envs := attrs["environments"].(map[string]interface{})
			prodEnv := envs["production"].(map[string]interface{})
			vals := prodEnv["values"].(map[string]interface{})
			logOverride := vals["log_level"].(map[string]interface{})
			assert.Equal(t, "warn", logOverride["value"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	cfg.Environments["production"] = map[string]interface{}{
		"values": map[string]interface{}{"log_level": "warn"},
	}
	err = cfg.Save(context.Background())
	require.NoError(t, err)
}

func TestConfig_MutateAddItem_Save(t *testing.T) {
	configID := "svc"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		} else {
			assert.Equal(t, "PUT", r.Method)
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			attrs := body["data"].(map[string]interface{})["attributes"].(map[string]interface{})
			items := attrs["items"].(map[string]interface{})
			// The new key should be present.
			debugItem := items["debug"].(map[string]interface{})
			assert.Equal(t, true, debugItem["value"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	cfg.Items["debug"] = true
	err = cfg.Save(context.Background())
	require.NoError(t, err)
}

func TestConfigClient_404_NotFoundError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})

	_, err := client.Config().Get(context.Background(), "nonexistent")
	require.Error(t, err)

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
	assert.Equal(t, 404, notFound.StatusCode)

	// Should also match the base error.
	var base *smplkit.SmplError
	require.True(t, errors.As(err, &base))
}

func TestConfigClient_409_ConflictError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"has children"}]}`))
	})

	err := client.Config().Delete(context.Background(), "has-children")
	require.Error(t, err)

	var conflict *smplkit.SmplConflictError
	require.True(t, errors.As(err, &conflict))
	assert.Equal(t, 409, conflict.StatusCode)
}

func TestConfigClient_422_ValidationError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"name is required"}]}`))
	})

	cfg := client.Config().New("bad-config", smplkit.WithConfigName(""))
	err := cfg.Save(context.Background())
	require.Error(t, err)

	var validation *smplkit.SmplValidationError
	require.True(t, errors.As(err, &validation))
	assert.Equal(t, 422, validation.StatusCode)
}

func TestConfigClient_NetworkError_ConnectionError(t *testing.T) {
	// Use a listener that immediately closes to simulate a connection error.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	listener.Close()

	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://"+addr),
	)
	require.NoError(t, err)

	_, listErr := client.Config().List(context.Background())
	require.Error(t, listErr)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(listErr, &connErr))
}

func TestConfigClient_ContextTimeout_TimeoutError(t *testing.T) {
	// Server that delays longer than the context deadline.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = client.Config().List(ctx)
	require.Error(t, err)

	var timeoutErr *smplkit.SmplTimeoutError
	require.True(t, errors.As(err, &timeoutErr))
	assert.Contains(t, timeoutErr.Error(), "timed out")
}

func TestConfigClient_AuthHeader(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		assert.Equal(t, "Bearer sk_test_key", auth)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})

	_, err := client.Config().List(context.Background())
	require.NoError(t, err)
}

func TestConfigClient_UserAgent(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		assert.True(t, strings.HasPrefix(ua, "smplkit-go-sdk/"), "User-Agent should start with smplkit-go-sdk/")

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})

	_, err := client.Config().List(context.Background())
	require.NoError(t, err)
}

func TestConfigClient_ContentType(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON("test-key", "Name")))
	})

	cfg := client.Config().New("test-key", smplkit.WithConfigName("Test"))
	err := cfg.Save(context.Background())
	require.NoError(t, err)
}

func TestConfigClient_ContextCanceled_TimeoutError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err = client.Config().List(ctx)
	require.Error(t, err)

	var timeoutErr *smplkit.SmplTimeoutError
	require.True(t, errors.As(err, &timeoutErr))
}

func TestConfigClient_GenericHTTPError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
	})

	_, err := client.Config().List(context.Background())
	require.Error(t, err)

	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Equal(t, 500, smplErr.StatusCode)
}

func TestConfigClient_GenericError_FallsBackToConnectionError(t *testing.T) {
	// Use a custom RoundTripper that returns a generic (non-net, non-context) error.
	transport := &errorRoundTripper{err: fmt.Errorf("some unknown error")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().List(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "error")
}

// errorRoundTripper is a test helper that always returns the given error.
type errorRoundTripper struct {
	err error
}

func (t *errorRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, t.err
}

func TestConfigClient_ParsesEnvironments(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Return a single resource response with environment data.
		_, _ = w.Write([]byte(`{"data": {
			"id": "` + testID5 + `",
			"type": "config",
			"attributes": {
				"name": "Env Test",
				"description": "A test config",
				"parent": null,
				"items": {"log_level": {"value": "info", "type": "STRING"}},
				"environments": {"production": {"values": {"log_level": {"value": "warn"}}}},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}}`))
	})

	cfg, err := client.Config().Get(context.Background(), "env-test")
	require.NoError(t, err)
	require.Contains(t, cfg.Environments, "production")
	prodEnv := cfg.Environments["production"]
	require.Contains(t, prodEnv, "values")
	vals := prodEnv["values"].(map[string]interface{})
	assert.Equal(t, "warn", vals["log_level"])
}

func TestConfigClient_Get_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid}`))
	})

	_, err := client.Config().Get(context.Background(), "some-key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestConfigClient_Get_NetworkError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("some network error")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().Get(context.Background(), "some-key")
	require.Error(t, err)
}

func TestConfigClient_New_Save_UnmarshalableValues(t *testing.T) {
	// Channels cannot be JSON-marshaled — exercises the marshal error path.
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service")
	require.NoError(t, err)

	cfg := client.Config().New("test", smplkit.WithConfigName("Test"))
	cfg.Items = map[string]interface{}{"ch": make(chan int)}
	err = cfg.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type")
}

func TestConfigClient_New_Save_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	})

	cfg := client.Config().New("test", smplkit.WithConfigName("Test"))
	err := cfg.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestConfigClient_List_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid}`))
	})

	_, err := client.Config().List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestConfigClient_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().List(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_InvalidURL_RequestCreateError(t *testing.T) {
	// A URL containing a null byte causes request creation to fail.
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://bad\x00host"),
	)
	require.NoError(t, err)

	_, err = client.Config().List(context.Background())
	require.Error(t, err)
}

func TestClassifyError_NetErrorTimeout(t *testing.T) {
	transport := &timeoutNetErrorRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().List(context.Background())
	require.Error(t, err)

	var timeoutErr *smplkit.SmplTimeoutError
	require.True(t, errors.As(err, &timeoutErr), "expected SmplTimeoutError, got %T: %v", err, err)
}

// brokenBodyRoundTripper returns a 200 response whose body fails on Read.
type brokenBodyRoundTripper struct{}

func (t *brokenBodyRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(&errReader{err: fmt.Errorf("simulated read error")}),
		Header:     make(http.Header),
	}, nil
}

type errReader struct{ err error }

func (r *errReader) Read(_ []byte) (int, error) { return 0, r.err }

// timeoutNetErrorRoundTripper returns a net.Error with Timeout()=true.
type timeoutNetErrorRoundTripper struct{}

func (t *timeoutNetErrorRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, &mockTimeoutNetError{}
}

type mockTimeoutNetError struct{}

func (e *mockTimeoutNetError) Error() string   { return "mock timeout" }
func (e *mockTimeoutNetError) Timeout() bool   { return true }
func (e *mockTimeoutNetError) Temporary() bool { return true }

// --- Additional tests for 100% coverage ---

func TestConfigClient_Get_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().Get(context.Background(), "some-key")
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_Get_HTTPError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server error"}`))
	})

	_, err := client.Config().Get(context.Background(), "some-key")
	require.Error(t, err)
	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Equal(t, 500, smplErr.StatusCode)
}

func TestConfigClient_New_Save_NetworkError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("dial failed")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	cfg := client.Config().New("test", smplkit.WithConfigName("Test"))
	err = cfg.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_New_Save_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	cfg := client.Config().New("test", smplkit.WithConfigName("Test"))
	err = cfg.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_Delete_NotFound(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})

	err := client.Config().Delete(context.Background(), "nonexistent")
	require.Error(t, err)
	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestConfigClient_Delete_NetworkError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("dial failed")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	err = client.Config().Delete(context.Background(), "some-key")
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_Delete_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	err = client.Config().Delete(context.Background(), "some-key")
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_Save_MarshalError(t *testing.T) {
	configID := testID0

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	// Set items with an unmarshalable type (channel).
	cfg.Items = map[string]interface{}{"ch": make(chan int)}
	err = cfg.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type")
}

func TestConfigClient_Save_NetworkError(t *testing.T) {
	configID := testID0

	// First call succeeds (GET to fetch config), second (PUT) triggers network error.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/"+configID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
			return
		}
		// Close connection without response to trigger network error.
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.Save(context.Background())
	require.Error(t, err)
}

func TestConfigClient_Save_ReadBodyError(t *testing.T) {
	configID := testID0

	// Use a transport that returns a proper single resource response for GET but a broken body for PUT.
	transport := &methodAwareRoundTripper{
		getHandler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(sampleConfigJSON(configID, "Svc"))),
				Header:     http.Header{"Content-Type": {"application/vnd.api+json"}},
			}, nil
		},
		putHandler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(&errReader{err: fmt.Errorf("simulated read error")}),
				Header:     make(http.Header),
			}, nil
		},
	}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_Save_MalformedResponse(t *testing.T) {
	configID := testID0

	updateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		} else if r.Method == "PUT" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{invalid`))
		}
	}))
	defer updateServer.Close()

	updateClient, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithBaseURL(updateServer.URL))
	require.NoError(t, err)
	cfg, err := updateClient.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

// methodAwareRoundTripper dispatches to different handlers based on HTTP method.
type methodAwareRoundTripper struct {
	getHandler func(req *http.Request) (*http.Response, error)
	putHandler func(req *http.Request) (*http.Response, error)
}

func (t *methodAwareRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case "PUT":
		if t.putHandler != nil {
			return t.putHandler(req)
		}
	default:
		if t.getHandler != nil {
			return t.getHandler(req)
		}
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestConfig_MutateEnvItem_Save(t *testing.T) {
	configID := "svc"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			// Return single resource response with environment data.
			_, _ = w.Write([]byte(`{"data": {
				"id": "` + configID + `",
				"type": "config",
				"attributes": {
					"name": "Svc",
					"items": {"log_level": {"value": "info", "type": "STRING"}},
					"environments": {"production": {"values": {"log_level": {"value": "warn"}}}},
					"created_at": "2024-01-01T00:00:00Z",
					"updated_at": "2024-06-15T12:00:00Z"
				}
			}}`))
		} else {
			assert.Equal(t, "PUT", r.Method)
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			attrs := body["data"].(map[string]interface{})["attributes"].(map[string]interface{})
			envs := attrs["environments"].(map[string]interface{})
			prodEnv := envs["production"].(map[string]interface{})
			vals := prodEnv["values"].(map[string]interface{})
			// The existing log_level should be preserved.
			logOverride := vals["log_level"].(map[string]interface{})
			assert.Equal(t, "warn", logOverride["value"])
			// And the new key should be present.
			debugOverride := vals["debug"].(map[string]interface{})
			assert.Equal(t, true, debugOverride["value"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	// Mutate environment values directly.
	if vals, ok := cfg.Environments["production"]["values"].(map[string]interface{}); ok {
		vals["debug"] = true
	}
	err = cfg.Save(context.Background())
	require.NoError(t, err)
}

func TestConfig_MutateNewEnv_Save(t *testing.T) {
	configID := "svc"

	// Server returns a config with no environments.
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{}}}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"values":{"debug":{"value":true}}}}}}}`))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	// Add a new environment.
	cfg.Environments["staging"] = map[string]interface{}{
		"values": map[string]interface{}{"debug": true},
	}
	err = cfg.Save(context.Background())
	require.NoError(t, err)
}

func TestConfig_MutateExistingEnvMerge_Save(t *testing.T) {
	configID := "svc"

	// Server returns a config with an environment that has existing override keys.
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"values":{"other":{"value":"data"}}}}}}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"values":{"other":{"value":"data"},"debug":{"value":true}}}}}}}`))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	// Add a new key to the existing environment values.
	if vals, ok := cfg.Environments["staging"]["values"].(map[string]interface{}); ok {
		vals["debug"] = true
	}
	err = cfg.Save(context.Background())
	require.NoError(t, err)
}

// --- Connect + GetValue tests ---

func TestConfigClient_GetValue_NotConnected(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service")
	require.NoError(t, err)

	_, err = client.Config().GetValue(context.Background(), "my-config")
	require.Error(t, err)
}

func TestClient_Connect_And_GetValue(t *testing.T) {
	mux := http.NewServeMux()

	// Flags list endpoint (returns empty)
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	// Config list endpoint
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{
			"id": "db",
			"type": "config",
			"attributes": {
				"name": "DB",
				"items": {"host": {"value": "localhost", "type": "STRING"}, "port": {"value": 5432, "type": "NUMBER"}},
				"environments": {"test": {"values": {"host": {"value": "testdb"}}}},
				"parent": null
			}
		}]}`))
	})

	// Config by ID endpoint (for fetchChain)
	mux.HandleFunc("/api/v1/configs/db", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{
			"id": "db",
			"type": "config",
			"attributes": {
				"name": "DB",
				"items": {"host": {"value": "localhost", "type": "STRING"}, "port": {"value": 5432, "type": "NUMBER"}},
				"environments": {"test": {"values": {"host": {"value": "testdb"}}}},
				"parent": null
			}
		}}`))
	})

	// Catch-all for WS, etc.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	ctx := context.Background()

	// GetValue with configID only — returns all resolved values
	allVals, err := client.Config().GetValue(ctx, "db")
	require.NoError(t, err)
	require.NotNil(t, allVals)
	m := allVals.(map[string]interface{})
	assert.Equal(t, "testdb", m["host"]) // environment override
	assert.Equal(t, float64(5432), m["port"])

	// GetValue with configID + itemKey
	host, err := client.Config().GetValue(ctx, "db", "host")
	require.NoError(t, err)
	assert.Equal(t, "testdb", host)

	// GetValue for missing config
	missing, err := client.Config().GetValue(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, missing)

	// GetValue for missing item key
	missingItem, err := client.Config().GetValue(ctx, "db", "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, missingItem)
}
