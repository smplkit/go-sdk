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

// Test UUIDs — all valid RFC 4122 UUIDs.
const (
	testUUID0 = "550e8400-e29b-41d4-a716-446655440000"
	testUUID1 = "550e8400-e29b-41d4-a716-446655440001"
	testUUID2 = "550e8400-e29b-41d4-a716-446655440002"
	testUUID3 = "550e8400-e29b-41d4-a716-446655440003"
	testUUID4 = "550e8400-e29b-41d4-a716-446655440004"
	testUUID5 = "550e8400-e29b-41d4-a716-446655440005"
	testUUID6 = "550e8400-e29b-41d4-a716-446655440006"
)

// sampleConfigJSON returns a JSON:API single-resource response body.
// Items use the typed format: {key: {"value": raw, "type": "STRING"}}.
// Environment overrides use: {envName: {"values": {key: {"value": raw}}}}.
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
				"items": {"log_level": {"value": "info", "type": "STRING"}},
				"environments": {"production": {"values": {"log_level": {"value": "warn"}}}},
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
				"items": {},
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
	client, err := smplkit.NewClient("sk_test_key", "test", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	return client
}

func TestConfigClient_GetByID(t *testing.T) {
	configID := testUUID0

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
	assert.Equal(t, "info", cfg.Items["log_level"])
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
	uid := testUUID1
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/configs/"+uid, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON(uid, "test", "Test")))
	})

	cfg, err := client.Config().Get(context.Background(), smplkit.WithID(uid))
	require.NoError(t, err)
	assert.Equal(t, uid, cfg.ID)
}

func TestConfigClient_Get_NeitherKeyNorID(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test")
	require.NoError(t, err)
	_, err = client.Config().Get(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestConfigClient_Get_BothKeyAndID(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test")
	require.NoError(t, err)
	_, err = client.Config().Get(context.Background(), smplkit.WithKey("k"), smplkit.WithID(testUUID1))
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
				{"id": "1", "type": "config", "attributes": {"name": "A", "key": "a", "items": {}, "environments": {}}},
				{"id": "2", "type": "config", "attributes": {"name": "B", "key": "b", "items": {}, "environments": {}}}
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
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

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
		Items:       map[string]interface{}{"enabled": true},
	})
	require.NoError(t, err)
	assert.Equal(t, "new-id", cfg.ID)
	assert.Equal(t, "New Config", cfg.Name)
}

func TestConfigClient_Create_WithEnvironments(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		data := body["data"].(map[string]interface{})
		attrs := data["attributes"].(map[string]interface{})
		assert.NotNil(t, attrs["environments"])

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleConfigJSON("new-id", "new-config", "New Config")))
	})

	_, err := client.Config().Create(context.Background(), smplkit.CreateConfigParams{
		Name: "New Config",
		Environments: map[string]map[string]interface{}{
			"production": {"values": map[string]interface{}{"debug": false}},
		},
	})
	require.NoError(t, err)
}

func TestConfigClient_Delete(t *testing.T) {
	configID := testUUID2

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/v1/configs/"+configID, r.URL.Path)
		assert.Equal(t, "Bearer sk_test_key", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusNoContent)
	})

	err := client.Config().Delete(context.Background(), configID)
	require.NoError(t, err)
}

func TestConfigClient_Update(t *testing.T) {
	configID := testUUID0
	desc := "Updated description"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		assert.Equal(t, "/api/v1/configs/"+configID, r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		data := body["data"].(map[string]interface{})
		attrs := data["attributes"].(map[string]interface{})
		assert.Equal(t, "Updated Name", attrs["name"])

		w.WriteHeader(http.StatusOK)
		updated := sampleConfigJSON(configID, "my-service", "Updated Name")
		_, _ = w.Write([]byte(updated))
	})

	// Use GetByID to get a properly initialized Config (with back-reference set),
	// then call Update on it.
	var gotConfig *smplkit.Config
	fetchClient := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "my-service", "My Service")))
		} else {
			assert.Equal(t, "PUT", r.Method)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "my-service", "Updated Name")))
		}
	})
	var fetchErr error
	gotConfig, fetchErr = fetchClient.Config().GetByID(context.Background(), configID)
	require.NoError(t, fetchErr)

	newName := "Updated Name"
	err := gotConfig.Update(context.Background(), smplkit.UpdateConfigParams{
		Name:        &newName,
		Description: &desc,
	})
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", gotConfig.Name)
	_ = client // suppress unused warning (client was built for the PUT assertion)
}

func TestConfigClient_Update_NotFound(t *testing.T) {
	configID := testUUID3

	fetchAndUpdateClient := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "test", "Test")))
		} else {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
		}
	})

	cfg, err := fetchAndUpdateClient.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.Update(context.Background(), smplkit.UpdateConfigParams{})
	require.Error(t, err)
	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestConfig_SetValues_Base(t *testing.T) {
	configID := testUUID0

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
		} else {
			assert.Equal(t, "PUT", r.Method)
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			attrs := body["data"].(map[string]interface{})["attributes"].(map[string]interface{})
			items := attrs["items"].(map[string]interface{})
			logItem := items["log_level"].(map[string]interface{})
			assert.Equal(t, "debug", logItem["value"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
		}
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.SetValues(context.Background(), map[string]interface{}{"log_level": "debug"}, "")
	require.NoError(t, err)
}

func TestConfig_SetValues_Environment(t *testing.T) {
	configID := testUUID0

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
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
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
		}
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.SetValues(context.Background(), map[string]interface{}{"log_level": "warn"}, "production")
	require.NoError(t, err)
}

func TestConfig_SetValue(t *testing.T) {
	configID := testUUID0

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
		} else {
			assert.Equal(t, "PUT", r.Method)
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			attrs := body["data"].(map[string]interface{})["attributes"].(map[string]interface{})
			items := attrs["items"].(map[string]interface{})
			// Should still have the original log_level from sampleConfigJSON.
			logItem := items["log_level"].(map[string]interface{})
			assert.Equal(t, "info", logItem["value"])
			// And the new key.
			debugItem := items["debug"].(map[string]interface{})
			assert.Equal(t, true, debugItem["value"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
		}
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.SetValue(context.Background(), "debug", true, "")
	require.NoError(t, err)
}

func TestConfigClient_404_NotFoundError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})

	_, err := client.Config().GetByID(context.Background(), testUUID3)
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

	err := client.Config().Delete(context.Background(), testUUID4)
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

	client, err := smplkit.NewClient("sk_test_key", "test",
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

	client, err := smplkit.NewClient("sk_test_key", "test", smplkit.WithBaseURL(server.URL))
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

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleConfigJSON("id", "key", "Name")))
	})

	_, err := client.Config().Create(context.Background(), smplkit.CreateConfigParams{Name: "Test"})
	require.NoError(t, err)
}

func TestConfigClient_ContextCanceled_TimeoutError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", "test", smplkit.WithBaseURL(server.URL))
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
	client, err := smplkit.NewClient("sk_test_key", "test",
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
	configID := testUUID5

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleConfigJSON(configID, "env-test", "Env Test")))
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)
	require.Contains(t, cfg.Environments, "production")
	prodEnv := cfg.Environments["production"]
	require.Contains(t, prodEnv, "values")
	vals := prodEnv["values"].(map[string]interface{})
	assert.Equal(t, "warn", vals["log_level"])
}

func TestConfigClient_GetByID_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid json}`))
	})

	_, err := client.Config().GetByID(context.Background(), testUUID6)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestConfigClient_GetByKey_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid}`))
	})

	_, err := client.Config().GetByKey(context.Background(), "some-key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestConfigClient_GetByKey_NetworkError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("some network error")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().GetByKey(context.Background(), "some-key")
	require.Error(t, err)
}

func TestConfigClient_Create_UnmarshalableValues(t *testing.T) {
	// Channels cannot be JSON-marshaled — exercises the marshal error path.
	client, err := smplkit.NewClient("sk_test_key", "test")
	require.NoError(t, err)

	_, err = client.Config().Create(context.Background(), smplkit.CreateConfigParams{
		Name:  "Test",
		Items: map[string]interface{}{"ch": make(chan int)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal request body")
}

func TestConfigClient_Create_MalformedJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	})

	_, err := client.Config().Create(context.Background(), smplkit.CreateConfigParams{Name: "Test"})
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
	client, err := smplkit.NewClient("sk_test_key", "test",
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
	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL("http://bad\x00host"),
	)
	require.NoError(t, err)

	_, err = client.Config().List(context.Background())
	require.Error(t, err)
}

func TestClassifyError_NetErrorTimeout(t *testing.T) {
	transport := &timeoutNetErrorRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test",
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

func TestConfigClient_GetByID_InvalidUUID(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test")
	require.NoError(t, err)
	_, err = client.Config().GetByID(context.Background(), "not-a-uuid")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid config ID")
}

func TestConfigClient_GetByID_NetworkError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("dial failed")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().GetByID(context.Background(), testUUID0)
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_GetByID_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().GetByID(context.Background(), testUUID0)
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_GetByKey_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().GetByKey(context.Background(), "some-key")
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_GetByKey_HTTPError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server error"}`))
	})

	_, err := client.Config().GetByKey(context.Background(), "some-key")
	require.Error(t, err)
	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Equal(t, 500, smplErr.StatusCode)
}

func TestConfigClient_Create_NetworkError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("dial failed")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().Create(context.Background(), smplkit.CreateConfigParams{Name: "Test"})
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_Create_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	_, err = client.Config().Create(context.Background(), smplkit.CreateConfigParams{Name: "Test"})
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_Delete_InvalidUUID(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test")
	require.NoError(t, err)
	err = client.Config().Delete(context.Background(), "not-a-uuid")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid config ID")
}

func TestConfigClient_Delete_NetworkError(t *testing.T) {
	transport := &errorRoundTripper{err: fmt.Errorf("dial failed")}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	err = client.Config().Delete(context.Background(), testUUID0)
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestConfigClient_Delete_ReadBodyError(t *testing.T) {
	transport := &brokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	err = client.Config().Delete(context.Background(), testUUID0)
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_UpdateByID_InvalidUUID(t *testing.T) {
	// updateByID is exercised through Config.Update. We need a Config with an
	// invalid ID to trigger the uuid.Parse error path.
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Return a list response with a non-UUID ID.
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"not-a-uuid","type":"config","attributes":{"name":"Test","key":"test","items":{},"environments":{}}}]}`))
	})

	// Use GetByKey to get a Config (GetByKey uses list endpoint, doesn't validate UUID).
	cfg, err := client.Config().GetByKey(context.Background(), "test")
	require.NoError(t, err)

	err = cfg.Update(context.Background(), smplkit.UpdateConfigParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid config ID")
}

func TestConfigClient_UpdateByID_MarshalError(t *testing.T) {
	configID := testUUID0

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
		}
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	// Set items with an unmarshalable type (channel).
	err = cfg.Update(context.Background(), smplkit.UpdateConfigParams{
		Items: map[string]interface{}{"ch": make(chan int)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal request body")
}

func TestConfigClient_UpdateByID_NetworkError(t *testing.T) {
	configID := testUUID0
	var callCount int

	// First call succeeds (GET to fetch config), second (PUT) uses error transport.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/"+configID, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
		} else {
			// Close connection without response to trigger network error.
			hj, ok := w.(http.Hijacker)
			if !ok {
				return
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", "test", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.Update(context.Background(), smplkit.UpdateConfigParams{})
	require.Error(t, err)
}

func TestConfigClient_UpdateByID_ReadBodyError(t *testing.T) {
	configID := testUUID0

	// Use a transport that returns a proper response for GET but a broken body for PUT.
	var callCount int
	transport := &methodAwareRoundTripper{
		getHandler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(sampleConfigJSON(configID, "svc", "Svc"))),
				Header:     http.Header{"Content-Type": {"application/vnd.api+json"}},
			}, nil
		},
		putHandler: func(req *http.Request) (*http.Response, error) {
			callCount++
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(&errReader{err: fmt.Errorf("simulated read error")}),
				Header:     make(http.Header),
			}, nil
		},
	}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient),
	)
	require.NoError(t, err)

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.Update(context.Background(), smplkit.UpdateConfigParams{})
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestConfigClient_UpdateByID_MalformedResponse(t *testing.T) {
	configID := testUUID0

	updateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
		} else if r.Method == "PUT" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{invalid`))
		}
	}))
	defer updateServer.Close()

	updateClient, err := smplkit.NewClient("sk_test_key", "test", smplkit.WithBaseURL(updateServer.URL))
	require.NoError(t, err)
	cfg, err := updateClient.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.Update(context.Background(), smplkit.UpdateConfigParams{})
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

func TestConfigClient_FetchChain_Error(t *testing.T) {
	// fetchChain is called inside client.Connect(). If GetByID fails during
	// chain walk, the error should propagate.
	childID := testUUID1
	parentID := testUUID0

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.URL.Path == "/api/v1/configs" {
			// List returns the child config which has a parent
			callCount++
			resp := fmt.Sprintf(`{"data":[{"id":"%s","type":"config","attributes":{"name":"child","key":"child","parent":"%s","items":{"y":{"value":2,"type":"NUMBER"}},"environments":{}}}]}`, childID, parentID)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		} else if r.URL.Path == "/api/v1/configs/"+childID {
			resp := fmt.Sprintf(`{"data":{"id":"%s","type":"config","attributes":{"name":"child","key":"child","parent":"%s","items":{"y":{"value":2,"type":"NUMBER"}},"environments":{}}}}`, childID, parentID)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		} else if r.URL.Path == "/api/v1/configs/"+parentID {
			// Parent returns error.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
		} else if r.URL.Path == "/api/v1/flags" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		} else {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", "test", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	err = client.Connect(context.Background())
	require.Error(t, err)
}

func TestConfig_SetValue_WithEnvironment(t *testing.T) {
	configID := testUUID0

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
		} else {
			assert.Equal(t, "PUT", r.Method)
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			attrs := body["data"].(map[string]interface{})["attributes"].(map[string]interface{})
			envs := attrs["environments"].(map[string]interface{})
			prodEnv := envs["production"].(map[string]interface{})
			vals := prodEnv["values"].(map[string]interface{})
			// The production env in sampleConfigJSON has {"log_level": "warn"}.
			// SetValue should merge "debug":true into those existing values.
			// Values are wrapped as {"value": raw} for the API.
			logOverride := vals["log_level"].(map[string]interface{})
			assert.Equal(t, "warn", logOverride["value"])
			debugOverride := vals["debug"].(map[string]interface{})
			assert.Equal(t, true, debugOverride["value"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleConfigJSON(configID, "svc", "Svc")))
		}
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.SetValue(context.Background(), "debug", true, "production")
	require.NoError(t, err)
}

func TestConfig_SetValue_WithEnvironment_NoExistingEnv(t *testing.T) {
	configID := testUUID0

	// Server returns a config with no environments.
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","key":"svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{}}}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","key":"svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"values":{"debug":{"value":true}}}}}}}`))
		}
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	// SetValue for a non-existing environment should create the env entry.
	err = cfg.SetValue(context.Background(), "debug", true, "staging")
	require.NoError(t, err)
}

func TestConfig_SetValue_WithEnvironment_ExistingEnvKeys(t *testing.T) {
	configID := testUUID0

	// Server returns a config with an environment that has existing override keys.
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","key":"svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"values":{"other":{"value":"data"}}}}}}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","key":"svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"values":{"other":{"value":"data"},"debug":{"value":true}}}}}}}`))
		}
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.SetValue(context.Background(), "debug", true, "staging")
	require.NoError(t, err)
}

func TestConfig_SetValue_WithEnvironment_NonMapOverride(t *testing.T) {
	configID := testUUID0

	// Server returns a config with an environment whose override value is not a wrapped map (edge case).
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","key":"svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"values":null}}}}}`))
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"` + configID + `","type":"config","attributes":{"name":"Svc","key":"svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{"staging":{"values":{"debug":{"value":true}}}}}}}`))
		}
	})

	cfg, err := client.Config().GetByID(context.Background(), configID)
	require.NoError(t, err)

	err = cfg.SetValue(context.Background(), "debug", true, "staging")
	require.NoError(t, err)
}

// --- Connect + GetValue tests ---

func TestConfigClient_GetValue_NotConnected(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test")
	require.NoError(t, err)

	_, err = client.Config().GetValue("my-config")
	require.Error(t, err)

	var notConnErr *smplkit.SmplNotConnectedError
	require.True(t, errors.As(err, &notConnErr))
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
		if r.URL.Query().Get("filter[key]") != "" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{
			"id": "` + testUUID0 + `",
			"type": "config",
			"attributes": {
				"name": "DB",
				"key": "db",
				"items": {"host": {"value": "localhost", "type": "STRING"}, "port": {"value": 5432, "type": "NUMBER"}},
				"environments": {"test": {"values": {"host": {"value": "testdb"}}}},
				"parent": null
			}
		}]}`))
	})

	// Config by ID endpoint (for fetchChain)
	mux.HandleFunc("/api/v1/configs/"+testUUID0, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{
			"id": "` + testUUID0 + `",
			"type": "config",
			"attributes": {
				"name": "DB",
				"key": "db",
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

	client, err := smplkit.NewClient("sk_test_key", "test", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)

	err = client.Connect(context.Background())
	require.NoError(t, err)

	// GetValue with configKey only — returns all resolved values
	allVals, err := client.Config().GetValue("db")
	require.NoError(t, err)
	require.NotNil(t, allVals)
	m := allVals.(map[string]interface{})
	assert.Equal(t, "testdb", m["host"]) // environment override
	assert.Equal(t, float64(5432), m["port"])

	// GetValue with configKey + itemKey
	host, err := client.Config().GetValue("db", "host")
	require.NoError(t, err)
	assert.Equal(t, "testdb", host)

	// GetValue for missing config
	missing, err := client.Config().GetValue("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, missing)

	// GetValue for missing item key
	missingItem, err := client.Config().GetValue("db", "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, missingItem)

	// Connect is idempotent
	err = client.Connect(context.Background())
	require.NoError(t, err)
}

func TestClient_Connect_WithService(t *testing.T) {
	var serviceRegistered bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/contexts/bulk", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		var body map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		contexts := body["contexts"].([]interface{})
		require.Len(t, contexts, 1)
		ctx0 := contexts[0].(map[string]interface{})
		assert.Equal(t, "service:my-svc", ctx0["id"])
		serviceRegistered = true
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", "test",
		smplkit.WithBaseURL(server.URL),
		smplkit.WithService("my-svc"),
	)
	require.NoError(t, err)

	err = client.Connect(context.Background())
	require.NoError(t, err)
	assert.True(t, serviceRegistered, "service context should have been registered")
}
