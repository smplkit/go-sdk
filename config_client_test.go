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

	smplkit "github.com/smplkit/go-sdk/v3"
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
// Environment overrides use the flat format per ADR-024 §2.4:
// {envName: {key: rawValue}}.
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
				"environments": {"production": {"log_level": "warn"}},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}
	}`
}

func newTestClient(t *testing.T, handler http.HandlerFunc) *smplkit.SmplClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true}, smplkit.WithBaseURL(server.URL))
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
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})

	_, err := client.Config().Get(context.Background(), "nonexistent")
	require.Error(t, err)

	var notFound *smplkit.NotFoundError
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
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})

	configs, err := client.Config().List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, configs)
}

func TestConfigClient_List_WithPaginationOptions(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "3", r.URL.Query().Get("page[number]"))
		assert.Equal(t, "25", r.URL.Query().Get("page[size]"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})

	_, err := client.Config().List(context.Background(), smplkit.WithPageNumber(3), smplkit.WithPageSize(25))
	require.NoError(t, err)
}

func TestConfigClient_New_Save(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/configs", r.URL.Path)
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Content-Type"))

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
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	cfg := client.Config().New("temp", smplkit.WithConfigName("Test"))
	cfg.ID = ""
	err = cfg.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_Save_CreatePath_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	cfg := client.Config().New("temp", smplkit.WithConfigName("Test"))
	cfg.ID = ""
	err = cfg.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_Save_CreatePath_HTTPError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"validation error"}]}`))
	})

	cfg := client.Config().New("temp", smplkit.WithConfigName("Test"))
	cfg.ID = ""
	err := cfg.Save(context.Background())
	require.Error(t, err)
	var valErr *smplkit.ValidationError
	require.True(t, errors.As(err, &valErr))
}

func TestConfigClient_Save_CreatePath_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
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
		"production": {"debug": false},
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

	err := client.Config().Delete(context.Background(), testID2)
	require.NoError(t, err)
}

func TestConfigClient_Save_Update(t *testing.T) {
	configID := testID0

	// Single server handles both GET and PUT (update).
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "My Service")))
		} else {
			assert.Equal(t, "PUT", r.Method)
			assert.Equal(t, "application/vnd.api+json", r.Header.Get("Content-Type"))

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
	var notFound *smplkit.NotFoundError
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
			// Flat per-env shape per ADR-024 §2.4.
			assert.Equal(t, "warn", prodEnv["log_level"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	cfg.Environments["production"] = map[string]interface{}{
		"log_level": "warn",
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
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})

	_, err := client.Config().Get(context.Background(), "nonexistent")
	require.Error(t, err)

	var notFound *smplkit.NotFoundError
	require.True(t, errors.As(err, &notFound))
	assert.Equal(t, 404, notFound.Base.StatusCode)

	// Should also match the base error.
	var base *smplkit.Error
	require.True(t, errors.As(err, &base))
}

func TestConfigClient_409_ConflictError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"has children"}]}`))
	})

	err := client.Config().Delete(context.Background(), testID4)
	require.Error(t, err)

	var conflict *smplkit.ConflictError
	require.True(t, errors.As(err, &conflict))
	assert.Equal(t, 409, conflict.Base.StatusCode)
}

func TestConfigClient_422_ValidationError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"name is required"}]}`))
	})

	cfg := client.Config().New("bad-config", smplkit.WithConfigName(""))
	err := cfg.Save(context.Background())
	require.Error(t, err)

	var validation *smplkit.ValidationError
	require.True(t, errors.As(err, &validation))
	assert.Equal(t, 422, validation.Base.StatusCode)
}

func TestConfigClient_NetworkError_ConnectionError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	listener.Close()

	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://"+addr))
	require.NoError(t, err)

	_, listErr := client.Config().List(context.Background())
	require.Error(t, listErr)

	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(listErr, &connErr))
}

func TestConfigClient_ContextTimeout_TimeoutError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true}, smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = client.Config().List(ctx)
	require.Error(t, err)

	var timeoutErr *smplkit.TimeoutError
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
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Content-Type"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON("test-key", "Name")))
	})

	cfg := client.Config().New("test-key", smplkit.WithConfigName("Test"))
	err := cfg.Save(context.Background())
	require.NoError(t, err)
}

func TestConfigClient_ContextCanceled_TimeoutError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true}, smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err = client.Config().List(ctx)
	require.Error(t, err)

	var timeoutErr *smplkit.TimeoutError
	require.True(t, errors.As(err, &timeoutErr))
}

func TestConfigClient_GenericHTTPError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
	})

	_, err := client.Config().List(context.Background())
	require.Error(t, err)

	var smplErr *smplkit.Error
	require.True(t, errors.As(err, &smplErr))
	assert.Equal(t, 500, smplErr.StatusCode)
}

func TestConfigClient_GenericError_FallsBackToConnectionError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("some unknown error")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	_, err = client.Config().List(context.Background())
	require.Error(t, err)

	var connErr *smplkit.ConnectionError
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
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": {
			"id": "` + testID5 + `",
			"type": "config",
			"attributes": {
				"name": "Env Test",
				"description": "A test config",
				"parent": null,
				"items": {"log_level": {"value": "info", "type": "STRING"}},
				"environments": {"production": {"log_level": "warn"}},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}}`))
	})

	cfg, err := client.Config().Get(context.Background(), testID5)
	require.NoError(t, err)
	require.Contains(t, cfg.Environments, "production")
	prodEnv := cfg.Environments["production"]
	assert.Equal(t, "warn", prodEnv["log_level"])
}

func TestConfigClient_Get_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
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
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	_, err = client.Config().Get(context.Background(), "some-key")
	require.Error(t, err)
}

func TestConfigClient_New_Save_UnmarshalableValues(t *testing.T) {
	// Channels cannot be JSON-marshaled — exercises the marshal error path.
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)

	cfg := client.Config().New("test", smplkit.WithConfigName("Test"))
	cfg.Items = map[string]interface{}{"ch": make(chan int)}
	err = cfg.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type")
}

func TestConfigClient_New_Save_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	})

	cfg := client.Config().New("test", smplkit.WithConfigName("Test"))
	err := cfg.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestConfigClient_List_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
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
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	_, err = client.Config().List(context.Background())
	require.Error(t, err)

	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_InvalidURL_RequestCreateError(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://bad\x00host"))
	require.NoError(t, err)

	_, err = client.Config().List(context.Background())
	require.Error(t, err)
}

func TestClassifyError_NetErrorTimeout(t *testing.T) {
	transport := &timeoutNetErrorRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	_, err = client.Config().List(context.Background())
	require.Error(t, err)

	var timeoutErr *smplkit.TimeoutError
	require.True(t, errors.As(err, &timeoutErr), "expected TimeoutError, got %T: %v", err, err)
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
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	_, err = client.Config().Get(context.Background(), "some-key")
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_Get_HTTPError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server error"}`))
	})

	_, err := client.Config().Get(context.Background(), "some-key")
	require.Error(t, err)
	var smplErr *smplkit.Error
	require.True(t, errors.As(err, &smplErr))
	assert.Equal(t, 500, smplErr.StatusCode)
}

func TestConfigClient_New_Save_NetworkError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("dial failed")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	cfg := client.Config().New("test", smplkit.WithConfigName("Test"))
	err = cfg.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_New_Save_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	cfg := client.Config().New("test", smplkit.WithConfigName("Test"))
	err = cfg.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_Delete_NotFound(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})

	err := client.Config().Delete(context.Background(), "nonexistent")
	require.Error(t, err)
	var notFound *smplkit.NotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestConfigClient_Delete_NetworkError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("dial failed")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	err = client.Config().Delete(context.Background(), "some-key")
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_Delete_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	err = client.Config().Delete(context.Background(), "some-key")
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_Save_MarshalError(t *testing.T) {
	configID := testID0

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	cfg.Items = map[string]interface{}{"ch": make(chan int)}
	err = cfg.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type")
}

func TestConfigClient_Save_NetworkError(t *testing.T) {
	configID := testID0

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/"+configID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true}, smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.Save(context.Background())
	require.Error(t, err)
}

func TestConfigClient_Save_ReadBodyError(t *testing.T) {
	configID := testID0

	transport := &methodAwareRoundTripper{
		getHandler: func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(sampleConfigJSON(configID, "Svc"))),
				Header:     http.Header{"Content-Type": {"application/vnd.api+json"}},
			}, nil
		},
		putHandler: func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(&errReader{err: fmt.Errorf("simulated read error")}),
				Header:     make(http.Header),
			}, nil
		},
	}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_Save_MalformedResponse(t *testing.T) {
	configID := testID0

	updateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		case "PUT":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{invalid`))
		}
	}))
	defer updateServer.Close()

	updateClient, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true}, smplkit.WithBaseURL(updateServer.URL))
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
			_, _ = w.Write([]byte(`{"data": {
				"id": "` + configID + `",
				"type": "config",
				"attributes": {
					"name": "Svc",
					"items": {"log_level": {"value": "info", "type": "STRING"}},
					"environments": {"production": {"log_level": "warn"}},
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
			assert.Equal(t, "warn", prodEnv["log_level"])
			assert.Equal(t, true, prodEnv["debug"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "Svc")))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	cfg.Environments["production"]["debug"] = true
	err = cfg.Save(context.Background())
	require.NoError(t, err)
}

func TestConfig_MutateNewEnv_Save(t *testing.T) {
	configID := "svc"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{}}}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"debug":true}}}}}`))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	cfg.Environments["staging"] = map[string]interface{}{
		"debug": true,
	}
	err = cfg.Save(context.Background())
	require.NoError(t, err)
}

func TestConfig_MutateExistingEnvMerge_Save(t *testing.T) {
	configID := "svc"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"other":"data"}}}}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"other":"data","debug":true}}}}}`))
		}
	})

	cfg, err := client.Config().Get(context.Background(), configID)
	require.NoError(t, err)

	cfg.Environments["staging"]["debug"] = true
	err = cfg.Save(context.Background())
	require.NoError(t, err)
}

// --- Subscribe + GetValue (live surface) ---

func TestConfigClient_GetValue_NotConnected(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)

	_, err = client.Config().GetValue(context.Background(), "my-config", "k")
	require.Error(t, err)
}

func TestClient_Subscribe_And_GetValue(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{
			"id": "db",
			"type": "config",
			"attributes": {
				"name": "DB",
				"items": {"host": {"value": "localhost", "type": "STRING"}, "port": {"value": 5432, "type": "NUMBER"}},
				"environments": {"test": {"host": "testdb"}},
				"parent": null
			}
		}]}`))
	})

	mux.HandleFunc("/api/v1/configs/db", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{
			"id": "db",
			"type": "config",
			"attributes": {
				"name": "DB",
				"items": {"host": {"value": "localhost", "type": "STRING"}, "port": {"value": 5432, "type": "NUMBER"}},
				"environments": {"test": {"host": "testdb"}},
				"parent": null
			}
		}}`))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true}, smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	ctx := context.Background()

	// Full-config live view via Subscribe (raises on missing).
	proxy, err := client.Config().Subscribe(ctx, "db")
	require.NoError(t, err)
	values := proxy.Value()
	assert.Equal(t, "testdb", values["host"]) // environment override
	assert.Equal(t, float64(5432), values["port"])

	// Repeat Subscribe returns the same handle (parent-by-reference).
	proxy2, err := client.Config().Subscribe(ctx, "db")
	require.NoError(t, err)
	assert.Same(t, proxy, proxy2)

	// GetValue with configID + itemKey.
	host, err := client.Config().GetValue(ctx, "db", "host")
	require.NoError(t, err)
	assert.Equal(t, "testdb", host)

	// Subscribe for a missing config — an error.
	_, err = client.Config().Subscribe(ctx, "nonexistent")
	require.Error(t, err)
	var notFound *smplkit.NotFoundError
	require.True(t, errors.As(err, &notFound))

	// GetValue for a missing item key — an error.
	_, err = client.Config().GetValue(ctx, "db", "nonexistent")
	require.Error(t, err)

	// The editable record is still reachable via Get.
	editable, err := client.Config().Get(ctx, "db")
	require.NoError(t, err)
	assert.Equal(t, "db", editable.ID)
}

// --- Standalone NewConfigClient (builds its own transport + WebSocket) ---

func TestNewConfigClient_Standalone_CRUD(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/svc", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer sk_standalone", r.Header.Get("Authorization"))
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Accept"))
		assert.Contains(t, r.Header.Get("User-Agent"), "smplkit-go-sdk")
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON("svc", "Svc")))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cc, err := smplkit.NewConfigClient(
		smplkit.Config{APIKey: "sk_standalone", Environment: "prod", Service: "billing", DisableTelemetry: true},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	cfg, err := cc.Get(context.Background(), "svc")
	require.NoError(t, err)
	assert.Equal(t, "svc", cfg.ID)
	assert.Equal(t, "Svc", cfg.Name)
}

func TestNewConfigClient_Standalone_ResolveError(t *testing.T) {
	// resolveConfig requires environment + service + apikey; omit the api key
	// (and clear env vars so nothing leaks in) to exercise the error path.
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("HOME", t.TempDir())

	_, err := smplkit.NewConfigClient(smplkit.Config{Environment: "prod", Service: "svc"})
	require.Error(t, err)
}

func TestNewConfigClient_Standalone_DebugEnables(t *testing.T) {
	// Config.Debug=true takes the debug.Enable() branch in NewConfigClient.
	t.Cleanup(func() { smplkit.SetDebugEnabled(false) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	cc, err := smplkit.NewConfigClient(
		smplkit.Config{APIKey: "sk_dbg", Environment: "prod", Service: "svc", Debug: true, DisableTelemetry: true},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	require.NotNil(t, cc)
	assert.True(t, smplkit.IsDebugEnabled())
}

func TestNewConfigClient_Standalone_LiveSurface_OpensOwnWS(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"db","type":"config","attributes":{"name":"DB","items":{"host":{"value":"localhost","type":"STRING"}},"environments":{},"parent":null}}]}`))
	})
	mux.HandleFunc("/api/v1/configs/db", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"db","type":"config","attributes":{"name":"DB","items":{"host":{"value":"localhost","type":"STRING"}},"environments":{},"parent":null}}}`))
	})
	mux.HandleFunc("/api/v1/configs/bulk", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Catch-all (the standalone client opens its own WebSocket against appURL).
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cc, err := smplkit.NewConfigClient(
		smplkit.Config{APIKey: "sk_standalone", Environment: "prod", Service: "billing", DisableTelemetry: true},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	// First live use flushes discovery, fetches/resolves configs, and opens
	// the client's OWN WebSocket via ensureWS (no parent SmplClient).
	proxy, err := cc.Subscribe(context.Background(), "db")
	require.NoError(t, err)
	assert.Equal(t, "localhost", proxy.Value()["host"])
}

func TestNewConfigClient_Standalone_GetValueOr_DefaultOnMiss(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := httptest.NewServer(mux)
	defer server.Close()

	cc, err := smplkit.NewConfigClient(
		smplkit.Config{APIKey: "sk_standalone", Environment: "prod", Service: "billing", DisableTelemetry: true},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	got := cc.GetValueOr(context.Background(), "missing", "k", "fallback")
	assert.Equal(t, "fallback", got)
}
