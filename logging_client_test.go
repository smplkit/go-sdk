package smplkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
)

// Test IDs for logging tests — slug-style identifiers.
const (
	logID0 = "my-logger"
	logID1 = "infra"
	logID2 = "database"
)

// sampleLoggerJSON returns a JSON:API single-resource response for a logger.
func sampleLoggerJSON(id, name, level string, managed bool) string {
	managedStr := "true"
	if !managed {
		managedStr = "false"
	}
	levelStr := "null"
	if level != "" {
		levelStr = `"` + level + `"`
	}
	return `{
		"data": {
			"id": "` + id + `",
			"type": "logger",
			"attributes": {
				"id": "` + id + `",
				"name": "` + name + `",
				"level": ` + levelStr + `,
				"managed": ` + managedStr + `,
				"environments": {},
				"sources": [],
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}
	}`
}

// sampleLogGroupJSON returns a JSON:API single-resource response for a log group.
func sampleLogGroupJSON(id, name, level string) string {
	levelStr := "null"
	if level != "" {
		levelStr = `"` + level + `"`
	}
	return `{
		"data": {
			"id": "` + id + `",
			"type": "log_group",
			"attributes": {
				"id": "` + id + `",
				"name": "` + name + `",
				"level": ` + levelStr + `,
				"environments": {},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}
	}`
}

// sampleLogGroupListJSON returns a JSON:API list response for log groups.
func sampleLogGroupListJSON(id, name, level string) string {
	levelStr := "null"
	if level != "" {
		levelStr = `"` + level + `"`
	}
	return `{
		"data": [{
			"id": "` + id + `",
			"type": "log_group",
			"attributes": {
				"id": "` + id + `",
				"name": "` + name + `",
				"level": ` + levelStr + `,
				"environments": {},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}]
	}`
}

func newLoggingTestClient(t *testing.T, handler http.Handler) *smplkit.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithBaseURL(server.URL), smplkit.DisableTelemetry())
	require.NoError(t, err)
	return client
}

// --- Accessor test ---

func TestLoggingClient_Accessor(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	logging := client.Logging()
	require.NotNil(t, logging)
	// Calling Logging() multiple times returns the same sub-client.
	assert.Same(t, logging, client.Logging())
}

// --- Factory method tests ---

func TestLoggingClient_New(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)

	logger := client.Logging().Management().New("my.logger")
	assert.Equal(t, "my.logger", logger.ID)
	assert.Equal(t, "My.logger", logger.Name) // keyToDisplayName does not split on "."
	assert.True(t, logger.Managed)
	assert.NotNil(t, logger.Environments)
}

func TestLoggingClient_New_WithOptions(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)

	logger := client.Logging().Management().New("checkout-v2",
		smplkit.WithLoggerName("Checkout Logger"),
		smplkit.WithLoggerManaged(false),
	)
	assert.Equal(t, "checkout-v2", logger.ID)
	assert.Equal(t, "Checkout Logger", logger.Name)
	assert.False(t, logger.Managed)
}

func TestLoggingClient_NewGroup(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)

	group := client.Logging().Management().NewGroup("infra")
	assert.Equal(t, "infra", group.ID)
	assert.Equal(t, "Infra", group.Name)
	assert.NotNil(t, group.Environments)
}

func TestLoggingClient_NewGroup_WithOptions(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)

	parentID := logID1
	group := client.Logging().Management().NewGroup("database",
		smplkit.WithLogGroupName("Database Group"),
		smplkit.WithLogGroupParent(parentID),
	)
	assert.Equal(t, "database", group.ID)
	assert.Equal(t, "Database Group", group.Name)
	require.NotNil(t, group.Group)
	assert.Equal(t, parentID, *group.Group)
}

// --- Logger Save tests ---

func TestLogger_Save_Create(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			assert.Equal(t, "/api/v1/loggers", r.URL.Path)

			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			data := body["data"].(map[string]interface{})
			assert.Equal(t, "logger", data["type"])
			assert.Equal(t, "my.logger", data["id"])

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "My Logger", "INFO", true)))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))

	logger := client.Logging().Management().New("my.logger", smplkit.WithLoggerName("My Logger"))
	err := logger.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "my.logger", logger.ID)
	assert.Equal(t, "My Logger", logger.Name)
}

func TestLogger_Save_CreatePath_EmptyID(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/loggers", r.URL.Path)

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleLoggerJSON("server-assigned", "My Logger", "INFO", true)))
	}))

	logger := client.Logging().Management().New("temp-id", smplkit.WithLoggerName("My Logger"))
	logger.ID = "" // Clear ID to trigger create (POST) path
	err := logger.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "server-assigned", logger.ID)
	assert.Equal(t, "My Logger", logger.Name)
}

func TestLogger_Save_Update(t *testing.T) {
	mux := http.NewServeMux()

	// GET /api/v1/loggers/my.logger — returns single resource.
	mux.HandleFunc("/api/v1/loggers/my.logger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "My Logger", "INFO", true)))
			return
		}
		if r.Method == "PUT" {
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			data := body["data"].(map[string]interface{})
			attrs := data["attributes"].(map[string]interface{})
			assert.Equal(t, "Updated Logger", attrs["name"])

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "Updated Logger", "WARN", true)))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	client := newLoggingTestClient(t, mux)

	// Fetch the logger first.
	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)
	assert.Equal(t, "my.logger", logger.ID)

	// Mutate and save (should PUT since ID is set).
	logger.Name = "Updated Logger"
	err = logger.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Updated Logger", logger.Name)
}

// --- Logger local mutation tests ---

func TestLogger_SetLevel(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	logger := client.Logging().Management().New("test-logger")

	assert.Nil(t, logger.Level)
	logger.SetLevel(smplkit.LogLevelDebug)
	require.NotNil(t, logger.Level)
	assert.Equal(t, smplkit.LogLevelDebug, *logger.Level)
}

func TestLogger_ClearLevel(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	logger := client.Logging().Management().New("test-logger")

	logger.SetLevel(smplkit.LogLevelWarn)
	require.NotNil(t, logger.Level)

	logger.ClearLevel()
	assert.Nil(t, logger.Level)
}

func TestLogger_SetEnvironmentLevel(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	logger := client.Logging().Management().New("test-logger")

	logger.SetEnvironmentLevel("production", smplkit.LogLevelError)

	envData, ok := logger.Environments["production"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "ERROR", envData["level"])
}

func TestLogger_ClearEnvironmentLevel(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	logger := client.Logging().Management().New("test-logger")

	logger.SetEnvironmentLevel("staging", smplkit.LogLevelDebug)
	require.Contains(t, logger.Environments, "staging")

	logger.ClearEnvironmentLevel("staging")
	assert.NotContains(t, logger.Environments, "staging")
}

func TestLogger_ClearEnvironmentLevel_NilEnvironments(t *testing.T) {
	logger := &smplkit.Logger{}
	// Should not panic when Environments is nil.
	logger.ClearEnvironmentLevel("staging")
}

func TestLogger_ClearEnvironmentLevel_NonMapEntry(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	logger := client.Logging().Management().New("test-logger")
	// Set a non-map entry to exercise the type assertion branch.
	logger.Environments["staging"] = "not-a-map"
	logger.ClearEnvironmentLevel("staging")
	// Should be unchanged since it was not a map.
	assert.Equal(t, "not-a-map", logger.Environments["staging"])
}

func TestLogger_ClearEnvironmentLevel_PreservesOtherKeys(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	logger := client.Logging().Management().New("test-logger")

	logger.Environments["staging"] = map[string]interface{}{
		"level": "DEBUG",
		"other": "keep",
	}

	logger.ClearEnvironmentLevel("staging")
	envData := logger.Environments["staging"].(map[string]interface{})
	assert.NotContains(t, envData, "level")
	assert.Equal(t, "keep", envData["other"])
}

func TestLogger_ClearAllEnvironmentLevels(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	logger := client.Logging().Management().New("test-logger")

	logger.SetEnvironmentLevel("production", smplkit.LogLevelError)
	logger.SetEnvironmentLevel("staging", smplkit.LogLevelDebug)
	require.Len(t, logger.Environments, 2)

	logger.ClearAllEnvironmentLevels()
	assert.Empty(t, logger.Environments)
}

// --- LogGroup Save tests ---

func TestLogGroup_Save_Create(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			assert.Equal(t, "/api/v1/log_groups", r.URL.Path)

			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			data := body["data"].(map[string]interface{})
			assert.Equal(t, "log_group", data["type"])
			assert.Equal(t, "infra", data["id"])

			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(sampleLogGroupJSON("infra", "Infra", "WARN")))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))

	group := client.Logging().Management().NewGroup("infra", smplkit.WithLogGroupName("Infra"))
	err := group.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "infra", group.ID)
}

func TestLogGroup_Save_CreatePath_EmptyID(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/log_groups", r.URL.Path)

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleLogGroupJSON("server-assigned", "Infra", "WARN")))
	}))

	group := client.Logging().Management().NewGroup("temp-id", smplkit.WithLogGroupName("Infra"))
	group.ID = "" // Clear ID to trigger create (POST) path
	err := group.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "server-assigned", group.ID)
	assert.Equal(t, "Infra", group.Name)
}

func TestLogGroup_Save_Update(t *testing.T) {
	mux := http.NewServeMux()

	// GET /api/v1/log_groups — returns list with one group (for GetGroup).
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	// PUT /api/v1/log_groups/{id}
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleLogGroupJSON("infra", "Updated Infra", "ERROR")))
	})

	client := newLoggingTestClient(t, mux)

	group, err := client.Logging().Management().GetGroup(context.Background(), "infra")
	require.NoError(t, err)
	assert.Equal(t, "infra", group.ID)

	group.Name = "Updated Infra"
	err = group.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Updated Infra", group.Name)
}

// --- LogGroup local mutation tests ---

func TestLogGroup_SetLevel(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	group := client.Logging().Management().NewGroup("infra")

	group.SetLevel(smplkit.LogLevelWarn)
	require.NotNil(t, group.Level)
	assert.Equal(t, smplkit.LogLevelWarn, *group.Level)
}

func TestLogGroup_ClearLevel(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	group := client.Logging().Management().NewGroup("infra")

	group.SetLevel(smplkit.LogLevelError)
	group.ClearLevel()
	assert.Nil(t, group.Level)
}

func TestLogGroup_SetEnvironmentLevel(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	group := client.Logging().Management().NewGroup("infra")

	group.SetEnvironmentLevel("production", smplkit.LogLevelError)

	envData, ok := group.Environments["production"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "ERROR", envData["level"])
}

func TestLogGroup_ClearEnvironmentLevel(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	group := client.Logging().Management().NewGroup("infra")

	group.SetEnvironmentLevel("staging", smplkit.LogLevelDebug)
	group.ClearEnvironmentLevel("staging")
	assert.NotContains(t, group.Environments, "staging")
}

func TestLogGroup_ClearEnvironmentLevel_NilEnvironments(t *testing.T) {
	group := &smplkit.LogGroup{}
	// Should not panic when Environments is nil.
	group.ClearEnvironmentLevel("staging")
}

func TestLogGroup_ClearEnvironmentLevel_NonMapEntry(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	group := client.Logging().Management().NewGroup("infra")
	group.Environments["staging"] = "not-a-map"
	group.ClearEnvironmentLevel("staging")
	assert.Equal(t, "not-a-map", group.Environments["staging"])
}

func TestLogGroup_ClearEnvironmentLevel_PreservesOtherKeys(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	group := client.Logging().Management().NewGroup("infra")

	group.Environments["staging"] = map[string]interface{}{
		"level": "DEBUG",
		"other": "keep",
	}

	group.ClearEnvironmentLevel("staging")
	envData := group.Environments["staging"].(map[string]interface{})
	assert.NotContains(t, envData, "level")
	assert.Equal(t, "keep", envData["other"])
}

func TestLogGroup_ClearAllEnvironmentLevels(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	group := client.Logging().Management().NewGroup("infra")

	group.SetEnvironmentLevel("production", smplkit.LogLevelError)
	group.SetEnvironmentLevel("staging", smplkit.LogLevelDebug)
	require.Len(t, group.Environments, 2)

	group.ClearAllEnvironmentLevels()
	assert.Empty(t, group.Environments)
}

// --- LoggingClient.Get tests ---

func TestLoggingClient_Get(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/loggers/my.logger", r.URL.Path)
		assert.Equal(t, "Bearer sk_test_key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Accept"))
		assert.True(t, strings.HasPrefix(r.Header.Get("User-Agent"), "smplkit-go-sdk/"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "My Logger", "INFO", true)))
	}))

	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)
	assert.Equal(t, "my.logger", logger.ID)
	assert.Equal(t, "My Logger", logger.Name)
	require.NotNil(t, logger.Level)
	assert.Equal(t, smplkit.LogLevelInfo, *logger.Level)
	assert.True(t, logger.Managed)
}

func TestLoggingClient_Get_NotFound(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))

	_, err := client.Logging().Management().Get(context.Background(), "nonexistent")
	require.Error(t, err)

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestLoggingClient_Get_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	_, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.Error(t, err)

	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Equal(t, 500, smplErr.StatusCode)
}

func TestLoggingClient_Get_NetworkError(t *testing.T) {
	transport := &failTransport{}
	httpClient := &http.Client{Transport: transport}

	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	_, err = client.Logging().Management().Get(context.Background(), "some-key")
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLoggingClient_Get_ReadBodyError(t *testing.T) {
	transport := &loggingBrokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	_, err = client.Logging().Management().Get(context.Background(), "some-key")
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestLoggingClient_Get_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid}`))
	}))

	_, err := client.Logging().Management().Get(context.Background(), "some-key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

// --- LoggingClient.List tests ---

func TestLoggingClient_List(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/loggers", r.URL.Path)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "a.logger", "type": "logger", "attributes": {"id": "a.logger", "name": "A", "managed": true, "environments": {}}},
				{"id": "b.logger", "type": "logger", "attributes": {"id": "b.logger", "name": "B", "managed": true, "environments": {}}}
			]
		}`))
	}))

	loggers, err := client.Logging().Management().List(context.Background())
	require.NoError(t, err)
	require.Len(t, loggers, 2)
	assert.Equal(t, "A", loggers[0].Name)
	assert.Equal(t, "B", loggers[1].Name)
}

func TestLoggingClient_List_Empty(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	}))

	loggers, err := client.Logging().Management().List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, loggers)
}

func TestLoggingClient_List_NetworkError(t *testing.T) {
	transport := &failTransport{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	_, err = client.Logging().Management().List(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLoggingClient_List_ReadBodyError(t *testing.T) {
	transport := &loggingBrokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	_, err = client.Logging().Management().List(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLoggingClient_List_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid}`))
	}))

	_, err := client.Logging().Management().List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLoggingClient_List_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	_, err := client.Logging().Management().List(context.Background())
	require.Error(t, err)

	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Equal(t, 500, smplErr.StatusCode)
}

// --- LoggingClient.Delete tests ---

func TestLoggingClient_Delete(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/v1/loggers/my.logger", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))

	err := client.Logging().Management().Delete(context.Background(), "my.logger")
	require.NoError(t, err)
}

func TestLoggingClient_Delete_NotFound(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))

	err := client.Logging().Management().Delete(context.Background(), "nonexistent")
	require.Error(t, err)

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestLoggingClient_Delete_NetworkError(t *testing.T) {
	transport := &failTransport{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	err = client.Logging().Management().Delete(context.Background(), "some-id")
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

// --- LoggingClient.GetGroup tests ---

func TestLoggingClient_GetGroup(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/log_groups", r.URL.Path)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	}))

	group, err := client.Logging().Management().GetGroup(context.Background(), "infra")
	require.NoError(t, err)
	assert.Equal(t, "infra", group.ID)
	assert.Equal(t, "Infra", group.Name)
}

func TestLoggingClient_GetGroup_NotFound(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	}))

	_, err := client.Logging().Management().GetGroup(context.Background(), "nonexistent")
	require.Error(t, err)

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
	assert.Contains(t, notFound.Error(), "nonexistent")
}

func TestLoggingClient_GetGroup_NetworkError(t *testing.T) {
	transport := &failTransport{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	_, err = client.Logging().Management().GetGroup(context.Background(), "infra")
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

// --- LoggingClient.ListGroups tests ---

func TestLoggingClient_ListGroups(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/log_groups", r.URL.Path)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "infra", "type": "log_group", "attributes": {"id": "infra", "name": "Infra", "environments": {}}},
				{"id": "app", "type": "log_group", "attributes": {"id": "app", "name": "App", "environments": {}}}
			]
		}`))
	}))

	groups, err := client.Logging().Management().ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, groups, 2)
	assert.Equal(t, "Infra", groups[0].Name)
	assert.Equal(t, "App", groups[1].Name)
}

func TestLoggingClient_ListGroups_ReadBodyError(t *testing.T) {
	transport := &loggingBrokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	_, err = client.Logging().Management().ListGroups(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLoggingClient_ListGroups_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid}`))
	}))

	_, err := client.Logging().Management().ListGroups(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLoggingClient_ListGroups_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	_, err := client.Logging().Management().ListGroups(context.Background())
	require.Error(t, err)

	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
}

// --- LoggingClient.DeleteGroup tests ---

func TestLoggingClient_DeleteGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	})
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	client := newLoggingTestClient(t, mux)

	err := client.Logging().Management().DeleteGroup(context.Background(), "infra")
	require.NoError(t, err)
}

func TestLoggingClient_DeleteGroup_NotFound(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))

	err := client.Logging().Management().DeleteGroup(context.Background(), "nonexistent")
	require.Error(t, err)

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
}

// --- Logger Save error paths ---

func TestLogger_Save_Create_NetworkError(t *testing.T) {
	transport := &failTransport{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	logger := client.Logging().Management().New("test-logger")
	err = logger.Save(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogger_Save_Create_ReadBodyError(t *testing.T) {
	transport := &loggingBrokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	logger := client.Logging().Management().New("test-logger")
	err = logger.Save(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogger_Save_Create_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	}))

	logger := client.Logging().Management().New("test-logger")
	err := logger.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLogger_Save_Create_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"validation error"}]}`))
	}))

	logger := client.Logging().Management().New("test-logger")
	err := logger.Save(context.Background())
	require.Error(t, err)

	var valErr *smplkit.SmplValidationError
	require.True(t, errors.As(err, &valErr))
}

// --- createLogger (POST) error paths — ID cleared to trigger create branch ---

func TestLogger_Save_CreatePath_NetworkError(t *testing.T) {
	transport := &failTransport{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	logger := client.Logging().Management().New("temp")
	logger.ID = ""
	err = logger.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogger_Save_CreatePath_ReadBodyError(t *testing.T) {
	transport := &loggingBrokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	logger := client.Logging().Management().New("temp")
	logger.ID = ""
	err = logger.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogger_Save_CreatePath_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"validation error"}]}`))
	}))

	logger := client.Logging().Management().New("temp")
	logger.ID = ""
	err := logger.Save(context.Background())
	require.Error(t, err)
	var valErr *smplkit.SmplValidationError
	require.True(t, errors.As(err, &valErr))
}

func TestLogger_Save_CreatePath_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	}))

	logger := client.Logging().Management().New("temp")
	logger.ID = ""
	err := logger.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLogger_Save_Update_NetworkError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/my.logger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "My Logger", "INFO", true)))
			return
		}
		// Close connection to trigger network error on PUT.
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	})

	client := newLoggingTestClient(t, mux)

	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)

	err = logger.Save(context.Background())
	require.Error(t, err)
}

func TestLogger_Save_Update_ReadBodyError(t *testing.T) {
	transport := &loggingMethodAwareRoundTripper{
		getHandler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(sampleLoggerJSON("my.logger", "My Logger", "INFO", true))),
				Header:     http.Header{"Content-Type": {"application/vnd.api+json"}},
			}, nil
		},
		putHandler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(&loggingErrReader{err: fmt.Errorf("simulated read error")}),
				Header:     make(http.Header),
			}, nil
		},
	}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)

	err = logger.Save(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogger_Save_Update_MalformedJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/my.logger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "My Logger", "INFO", true)))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid`))
	})

	client := newLoggingTestClient(t, mux)
	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)

	err = logger.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLogger_Save_Update_HTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/my.logger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "My Logger", "INFO", true)))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})

	client := newLoggingTestClient(t, mux)
	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)

	err = logger.Save(context.Background())
	require.Error(t, err)

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
}

// --- LogGroup Save error paths ---

func TestLogGroup_Save_Create_NetworkError(t *testing.T) {
	transport := &failTransport{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	group := client.Logging().Management().NewGroup("test-group")
	err = group.Save(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroup_Save_Create_ReadBodyError(t *testing.T) {
	transport := &loggingBrokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	group := client.Logging().Management().NewGroup("test-group")
	err = group.Save(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroup_Save_Create_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	}))

	group := client.Logging().Management().NewGroup("test-group")
	err := group.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLogGroup_Save_Create_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"validation error"}]}`))
	}))

	group := client.Logging().Management().NewGroup("test-group")
	err := group.Save(context.Background())
	require.Error(t, err)

	var valErr *smplkit.SmplValidationError
	require.True(t, errors.As(err, &valErr))
}

// --- createGroup (POST) error paths — ID cleared to trigger create branch ---

func TestLogGroup_Save_CreatePath_NetworkError(t *testing.T) {
	transport := &failTransport{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	group := client.Logging().Management().NewGroup("temp")
	group.ID = ""
	err = group.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroup_Save_CreatePath_ReadBodyError(t *testing.T) {
	transport := &loggingBrokenBodyRoundTripper{}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	group := client.Logging().Management().NewGroup("temp")
	group.ID = ""
	err = group.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroup_Save_CreatePath_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"validation error"}]}`))
	}))

	group := client.Logging().Management().NewGroup("temp")
	group.ID = ""
	err := group.Save(context.Background())
	require.Error(t, err)
	var valErr *smplkit.SmplValidationError
	require.True(t, errors.As(err, &valErr))
}

func TestLogGroup_Save_CreatePath_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	}))

	group := client.Logging().Management().NewGroup("temp")
	group.ID = ""
	err := group.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLogGroup_Save_Update_NetworkError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	})
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	})

	client := newLoggingTestClient(t, mux)
	group, err := client.Logging().Management().GetGroup(context.Background(), "infra")
	require.NoError(t, err)

	err = group.Save(context.Background())
	require.Error(t, err)
}

func TestLogGroup_Save_Update_ReadBodyError(t *testing.T) {
	transport := &loggingMethodAwareRoundTripper{
		getHandler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(sampleLogGroupListJSON("infra", "Infra", "WARN"))),
				Header:     http.Header{"Content-Type": {"application/vnd.api+json"}},
			}, nil
		},
		putHandler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(&loggingErrReader{err: fmt.Errorf("simulated read error")}),
				Header:     make(http.Header),
			}, nil
		},
	}
	httpClient := &http.Client{Transport: transport}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(httpClient), smplkit.DisableTelemetry())
	require.NoError(t, err)

	group, err := client.Logging().Management().GetGroup(context.Background(), "infra")
	require.NoError(t, err)

	err = group.Save(context.Background())
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroup_Save_Update_MalformedJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	})
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid`))
	})

	client := newLoggingTestClient(t, mux)
	group, err := client.Logging().Management().GetGroup(context.Background(), "infra")
	require.NoError(t, err)

	err = group.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLogGroup_Save_Update_HTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	})
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})

	client := newLoggingTestClient(t, mux)
	group, err := client.Logging().Management().GetGroup(context.Background(), "infra")
	require.NoError(t, err)

	err = group.Save(context.Background())
	require.Error(t, err)

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
}

// --- Delete error paths ---

func TestLoggingClient_DeleteLogger_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	err := client.Logging().Management().Delete(context.Background(), "my.logger")
	require.Error(t, err)
}

func TestLoggingClient_DeleteGroup_HTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	})
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	})

	client := newLoggingTestClient(t, mux)
	err := client.Logging().Management().DeleteGroup(context.Background(), "infra")
	require.Error(t, err)
}

// --- RegisterLogger tests ---

func TestLoggingClient_RegisterLogger(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)

	// RegisterLogger should not panic and should add to the buffer.
	client.Logging().RegisterLogger("MyApp/DB:Queries", smplkit.LogLevelDebug)

	// Register the same name again — should be deduplicated.
	client.Logging().RegisterLogger("myapp.db.queries", smplkit.LogLevelInfo)

	// No assertion on internal state, but should not panic.
}

// --- OnChange / OnChangeKey tests ---

func TestLoggingClient_OnChange(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)

	var called bool
	client.Logging().OnChange(func(evt *smplkit.LoggerChangeEvent) {
		called = true
	})

	// Verify listener was registered (indirectly — no panic, state accepted).
	assert.False(t, called) // Not called yet, just registered.
}

func TestLoggingClient_OnChangeKey(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)

	var called bool
	client.Logging().OnChangeKey("my.logger", func(evt *smplkit.LoggerChangeEvent) {
		called = true
	})

	assert.False(t, called)
}

// --- Logger response with environment data ---

func TestLoggingClient_Get_WithEnvironments(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": {
			"id": "my.logger",
			"type": "logger",
			"attributes": {
				"id": "my.logger",
				"name": "My Logger",
				"level": "INFO",
				"managed": true,
				"environments": {"production": {"level": "ERROR"}},
				"sources": [{"service": "test-service"}],
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}}`))
	}))

	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)
	assert.Contains(t, logger.Environments, "production")
	require.Len(t, logger.Sources, 1)
}

// --- Logger response with nil level ---

func TestLoggingClient_Get_NilLevel(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": {
			"id": "my.logger",
			"type": "logger",
			"attributes": {
				"id": "my.logger",
				"name": "My Logger",
				"managed": true,
				"environments": {}
			}
		}}`))
	}))

	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)
	assert.Nil(t, logger.Level)
}

// --- Logger response with empty level string ---

func TestLoggingClient_Get_EmptyLevel(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": {
			"id": "my.logger",
			"type": "logger",
			"attributes": {
				"id": "my.logger",
				"name": "My Logger",
				"level": "",
				"managed": true,
				"environments": {}
			}
		}}`))
	}))

	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)
	assert.Nil(t, logger.Level)
}

// --- Logger response with managed=false ---

func TestLoggingClient_Get_ManagedFalse(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "My Logger", "INFO", false)))
	}))

	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)
	assert.False(t, logger.Managed)
}

// --- LogGroup response with group parent ---

func TestLoggingClient_GetGroup_WithParent(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": [{
			"id": "database",
			"type": "log_group",
			"attributes": {
				"id": "database",
				"name": "Database",
				"level": "WARN",
				"group": "infra",
				"environments": {},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}]}`))
	}))

	group, err := client.Logging().Management().GetGroup(context.Background(), "database")
	require.NoError(t, err)
	require.NotNil(t, group.Group)
	assert.Equal(t, "infra", *group.Group)
	assert.NotNil(t, group.CreatedAt)
	assert.NotNil(t, group.UpdatedAt)
}

// --- Logger response with group ---

func TestLoggingClient_Get_WithGroup(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": {
			"id": "my.logger",
			"type": "logger",
			"attributes": {
				"id": "my.logger",
				"name": "My Logger",
				"level": "INFO",
				"group": "infra",
				"managed": true,
				"environments": {}
			}
		}}`))
	}))

	logger, err := client.Logging().Management().Get(context.Background(), "my.logger")
	require.NoError(t, err)
	require.NotNil(t, logger.Group)
	assert.Equal(t, "infra", *logger.Group)
}

// --- Adapter integration tests ---

// testAdapter is a mock LoggingAdapter for testing.
type testAdapter struct {
	name          string
	discovered    []smplkit.TestDiscoveredLogger
	appliedLevels []testAppliedLevel
	hookInstalled bool
	hookCallback  func(string, string)
	hookUninstall bool
}

type testAppliedLevel struct {
	loggerName string
	level      string
}

func (a *testAdapter) Name() string { return a.name }

func (a *testAdapter) Discover() []smplkit.TestDiscoveredLogger {
	return a.discovered
}

func (a *testAdapter) ApplyLevel(loggerName string, level string) {
	a.appliedLevels = append(a.appliedLevels, testAppliedLevel{loggerName, level})
}

func (a *testAdapter) InstallHook(onNewLogger func(name string, level string)) {
	a.hookInstalled = true
	a.hookCallback = onNewLogger
}

func (a *testAdapter) UninstallHook() {
	a.hookUninstall = true
	a.hookCallback = nil
}

func TestStartWithNoAdaptersWarns(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := newLoggingTestClient(t, mux)

	// Start with no adapters — should succeed with warning.
	err := client.Logging().Start(context.Background())
	require.NoError(t, err)

	// Management methods still work.
	loggers, err := client.Logging().Management().List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, loggers)
}

func TestRegisterAdapterBeforeStart(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := newLoggingTestClient(t, mux)

	adapter := &testAdapter{
		name: "test",
		discovered: []smplkit.TestDiscoveredLogger{
			{Name: "com.acme.app", Level: "INFO"},
		},
	}

	client.Logging().RegisterAdapter(adapter)
	err := client.Logging().Start(context.Background())
	require.NoError(t, err)

	// Verify Discover was called (adapter should have been queried).
	assert.True(t, adapter.hookInstalled, "InstallHook should have been called")
}

func TestRegisterAdapterAfterStartPanics(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := newLoggingTestClient(t, mux)

	err := client.Logging().Start(context.Background())
	require.NoError(t, err)

	adapter := &testAdapter{name: "test"}
	assert.Panics(t, func() {
		client.Logging().RegisterAdapter(adapter)
	})
}

func TestMultipleAdapters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := newLoggingTestClient(t, mux)

	adapter1 := &testAdapter{
		name: "adapter1",
		discovered: []smplkit.TestDiscoveredLogger{
			{Name: "com.acme.slog", Level: "INFO"},
		},
	}
	adapter2 := &testAdapter{
		name: "adapter2",
		discovered: []smplkit.TestDiscoveredLogger{
			{Name: "com.acme.zap", Level: "DEBUG"},
		},
	}

	client.Logging().RegisterAdapter(adapter1)
	client.Logging().RegisterAdapter(adapter2)
	err := client.Logging().Start(context.Background())
	require.NoError(t, err)

	// Both adapters should have hooks installed.
	assert.True(t, adapter1.hookInstalled)
	assert.True(t, adapter2.hookInstalled)
}

func TestStartWithAdapterDiscoverSkipsEmptyNames(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := newLoggingTestClient(t, mux)

	// Adapter discovers a logger with empty name (e.g., root handler).
	adapter := &testAdapter{
		name: "test",
		discovered: []smplkit.TestDiscoveredLogger{
			{Name: "", Level: "INFO"},
			{Name: "com.acme.app", Level: "DEBUG"},
		},
	}

	client.Logging().RegisterAdapter(adapter)
	err := client.Logging().Start(context.Background())
	require.NoError(t, err)
}

func TestOnNewLoggerAfterStart(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := newLoggingTestClient(t, mux)

	adapter := &testAdapter{
		name:       "test",
		discovered: []smplkit.TestDiscoveredLogger{},
	}

	client.Logging().RegisterAdapter(adapter)
	err := client.Logging().Start(context.Background())
	require.NoError(t, err)

	// Simulate a new logger being created in the framework after Start.
	require.NotNil(t, adapter.hookCallback)
	adapter.hookCallback("com.acme.new", "INFO")

	// The adapter should have had ApplyLevel called.
	require.NotEmpty(t, adapter.appliedLevels)
	assert.Equal(t, "com.acme.new", adapter.appliedLevels[0].loggerName)
	assert.Equal(t, "INFO", adapter.appliedLevels[0].level) // resolves to INFO (fallback)
}

func TestOnNewLoggerEmptyNameIgnored(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := newLoggingTestClient(t, mux)

	adapter := &testAdapter{
		name:       "test",
		discovered: []smplkit.TestDiscoveredLogger{},
	}

	client.Logging().RegisterAdapter(adapter)
	err := client.Logging().Start(context.Background())
	require.NoError(t, err)

	// Simulate a new logger with empty name — should be ignored.
	require.NotNil(t, adapter.hookCallback)
	adapter.hookCallback("", "INFO")

	// No ApplyLevel calls should have been made.
	assert.Empty(t, adapter.appliedLevels)
}

func TestApplyLevelsDelegatesToAdapters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": [{
			"id": "com.acme.app",
			"type": "logger",
			"attributes": {
				"id": "com.acme.app",
				"name": "App",
				"level": "WARN",
				"managed": true,
				"environments": {}
			}
		}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := newLoggingTestClient(t, mux)

	adapter := &testAdapter{
		name: "test",
		discovered: []smplkit.TestDiscoveredLogger{
			{Name: "com.acme.app", Level: "INFO"},
		},
	}

	client.Logging().RegisterAdapter(adapter)
	err := client.Logging().Start(context.Background())
	require.NoError(t, err)

	// After Start, applyLevels should have resolved com.acme.app to WARN
	// (from the server response) and called ApplyLevel.
	require.NotEmpty(t, adapter.appliedLevels)
	found := false
	for _, al := range adapter.appliedLevels {
		if al.loggerName == "com.acme.app" && al.level == "WARN" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected ApplyLevel(com.acme.app, WARN) to be called, got %v", adapter.appliedLevels)
}

func TestCloseCallsUninstallHook(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)

	adapter := &testAdapter{name: "test"}
	client.Logging().RegisterAdapter(adapter)

	err = client.Close()
	require.NoError(t, err)

	assert.True(t, adapter.hookUninstall, "UninstallHook should have been called on Close()")
}

// --- Client.Close cleans up logging ---

func TestClient_Close_LoggingCleanup(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)

	// Should not panic.
	err = client.Close()
	require.NoError(t, err)
}

// --- Test helper types ---

// loggingBrokenBodyRoundTripper returns a 200 response whose body fails on Read.
type loggingBrokenBodyRoundTripper struct{}

func (t *loggingBrokenBodyRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(&loggingErrReader{err: fmt.Errorf("simulated read error")}),
		Header:     make(http.Header),
	}, nil
}

type loggingErrReader struct{ err error }

func (r *loggingErrReader) Read(_ []byte) (int, error) { return 0, r.err }

// loggingMethodAwareRoundTripper dispatches to different handlers based on HTTP method.
type loggingMethodAwareRoundTripper struct {
	getHandler func(req *http.Request) (*http.Response, error)
	putHandler func(req *http.Request) (*http.Response, error)
}

func (t *loggingMethodAwareRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
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

// --- flushBuffer error logging ---

func TestFlushBuffer_LogsWarningOnHTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"Invalid level value"}]}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	var logBuf strings.Builder
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	client := newLoggingTestClient(t, mux)
	adapter := &testAdapter{
		name: "test",
		discovered: []smplkit.TestDiscoveredLogger{
			{Name: "com.acme.app", Level: "INFO"},
		},
	}
	client.Logging().RegisterAdapter(adapter)
	_ = client.Logging().Start(context.Background())

	logOutput := logBuf.String()
	assert.Contains(t, logOutput, "smplkit: bulk logger registration failed")
	assert.Contains(t, logOutput, "400")
	assert.Contains(t, logOutput, "Invalid level value")
}

func TestFlushBuffer_LogsWarningOnNetworkError(t *testing.T) {
	// Use a server that's immediately closed to trigger network errors on bulk endpoint.
	mux := http.NewServeMux()
	bulkCalled := false
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		bulkCalled = true
		// Close the connection abruptly by hijacking.
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	var logBuf strings.Builder
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	client := newLoggingTestClient(t, mux)
	adapter := &testAdapter{
		name: "test",
		discovered: []smplkit.TestDiscoveredLogger{
			{Name: "com.acme.network", Level: "DEBUG"},
		},
	}
	client.Logging().RegisterAdapter(adapter)
	_ = client.Logging().Start(context.Background())

	// The bulk endpoint was called (even if it errored).
	assert.True(t, bulkCalled, "bulk endpoint should have been called")
	assert.Contains(t, logBuf.String(), "smplkit: bulk logger registration failed")
}
