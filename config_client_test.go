package smplkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/smplkit/go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleConfigJSON returns a JSON:API single-resource response body.
func sampleConfigJSON(id, key, name string) string {
	return `{
		"data": {
			"id": "` + id + `",
			"type": "config",
			"attributes": {
				"name": "` + name + `",
				"key": "` + key + `",
				"description": "A test config",
				"parent": null,
				"values": {"log_level": "info"},
				"environments": {"production": {"values": {"log_level": "warn"}}},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}
	}`
}

// sampleListJSON returns a JSON:API list response body with one item.
func sampleListJSON(id, key, name string) string {
	return `{
		"data": [{
			"id": "` + id + `",
			"type": "config",
			"attributes": {
				"name": "` + name + `",
				"key": "` + key + `",
				"description": null,
				"parent": null,
				"values": {},
				"environments": {},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": null
			}
		}]
	}`
}

func newTestClient(t *testing.T, handler http.HandlerFunc) *smplkit.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
}

func TestConfigClient_GetByID(t *testing.T) {
	configID := "550e8400-e29b-41d4-a716-446655440000"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/configs/"+configID, r.URL.Path)
		assert.Equal(t, "Bearer sk_test_key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Accept"))
		assert.Contains(t, r.Header.Get("User-Agent"), "smplkit-go-sdk")

		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON(configID, "my-service", "My Service")))
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)
	assert.Equal(t, configID, cfg.ID)
	assert.Equal(t, "my-service", cfg.Key)
	assert.Equal(t, "My Service", cfg.Name)
	require.NotNil(t, cfg.Description)
	assert.Equal(t, "A test config", *cfg.Description)
	assert.Nil(t, cfg.Parent)
	assert.Equal(t, "info", cfg.Values["log_level"])
	require.NotNil(t, cfg.CreatedAt)
	assert.Equal(t, 2024, cfg.CreatedAt.Year())
	require.NotNil(t, cfg.UpdatedAt)
}

func TestConfigClient_GetByKey(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/configs", r.URL.Path)
		assert.Equal(t, "my-service", r.URL.Query().Get("filter[key]"))

		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleListJSON("abc-123", "my-service", "My Service")))
	})

	cfg, err := client.Config().GetByKey(context.Background(), "my-service")
	require.NoError(t, err)
	assert.Equal(t, "abc-123", cfg.ID)
	assert.Equal(t, "my-service", cfg.Key)
}

func TestConfigClient_GetByKey_NotFound(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})

	_, err := client.Config().GetByKey(context.Background(), "nonexistent")
	require.Error(t, err)

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestConfigClient_Get_WithKey(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "my-key", r.URL.Query().Get("filter[key]"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleListJSON("id-1", "my-key", "Test")))
	})

	cfg, err := client.Config().Get(context.Background(), smplkit.WithKey("my-key"))
	require.NoError(t, err)
	assert.Equal(t, "my-key", cfg.Key)
}

func TestConfigClient_Get_WithID(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/configs/uuid-1", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON("uuid-1", "test", "Test")))
	})

	cfg, err := client.Config().Get(context.Background(), smplkit.WithID("uuid-1"))
	require.NoError(t, err)
	assert.Equal(t, "uuid-1", cfg.ID)
}

func TestConfigClient_Get_NeitherKeyNorID(t *testing.T) {
	client := smplkit.NewClient("sk_test_key")
	_, err := client.Config().Get(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestConfigClient_Get_BothKeyAndID(t *testing.T) {
	client := smplkit.NewClient("sk_test_key")
	_, err := client.Config().Get(context.Background(), smplkit.WithKey("k"), smplkit.WithID("id"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestConfigClient_List(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/configs", r.URL.Path)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "1", "type": "config", "attributes": {"name": "A", "key": "a", "values": {}, "environments": {}}},
				{"id": "2", "type": "config", "attributes": {"name": "B", "key": "b", "values": {}, "environments": {}}}
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

func TestConfigClient_Create(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/configs", r.URL.Path)
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Content-Type"))

		var body map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		data := body["data"].(map[string]interface{})
		assert.Equal(t, "config", data["type"])

		attrs := data["attributes"].(map[string]interface{})
		assert.Equal(t, "New Config", attrs["name"])

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleConfigJSON("new-id", "new-config", "New Config")))
	})

	key := "new-config"
	desc := "A new config"
	cfg, err := client.Config().Create(context.Background(), smplkit.CreateConfigParams{
		Name:        "New Config",
		Key:         &key,
		Description: &desc,
		Values:      map[string]interface{}{"enabled": true},
	})
	require.NoError(t, err)
	assert.Equal(t, "new-id", cfg.ID)
	assert.Equal(t, "New Config", cfg.Name)
}

func TestConfigClient_Delete(t *testing.T) {
	configID := "delete-me-id"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/v1/configs/"+configID, r.URL.Path)
		assert.Equal(t, "Bearer sk_test_key", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusNoContent)
	})

	err := client.Config().Delete(context.Background(), configID)
	require.NoError(t, err)
}

func TestConfigClient_404_NotFoundError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})

	_, err := client.Config().GetByID(context.Background(), "nonexistent")
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

	err := client.Config().Delete(context.Background(), "parent-id")
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

	_, err := client.Config().Create(context.Background(), smplkit.CreateConfigParams{
		Name: "",
	})
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

	client := smplkit.NewClient("sk_test_key",
		smplkit.WithBaseURL("http://"+addr),
	)

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

	client := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := client.Config().List(ctx)
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
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Content-Type"))

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleConfigJSON("id", "key", "Name")))
	})

	_, err := client.Config().Create(context.Background(), smplkit.CreateConfigParams{Name: "Test"})
	require.NoError(t, err)
}

func TestConfigClient_ParsesEnvironments(t *testing.T) {
	configID := "env-test-id"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON(configID, "env-test", "Env Test")))
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)
	require.Contains(t, cfg.Environments, "production")
	prodEnv := cfg.Environments["production"]
	require.Contains(t, prodEnv, "values")
}
