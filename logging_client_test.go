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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// ── Shared external harness ─────────────────────────────────────────────────

func newLoggingTestClient(t *testing.T, handler http.Handler) *smplkit.SmplClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func sampleLoggerJSON(id, name, level string, managed bool) string {
	managedStr := "true"
	if !managed {
		managedStr = "false"
	}
	levelStr := "null"
	if level != "" {
		levelStr = `"` + level + `"`
	}
	return `{"data":{"id":"` + id + `","type":"logger","attributes":{"id":"` + id + `","name":"` + name + `","level":` + levelStr + `,"managed":` + managedStr + `,"environments":{},"sources":[],"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-06-15T12:00:00Z"}}}`
}

func sampleLogGroupJSON(id, name, level string) string {
	levelStr := "null"
	if level != "" {
		levelStr = `"` + level + `"`
	}
	return `{"data":{"id":"` + id + `","type":"log_group","attributes":{"id":"` + id + `","name":"` + name + `","level":` + levelStr + `,"environments":{},"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-06-15T12:00:00Z"}}}`
}

func sampleLogGroupListJSON(id, name, level string) string {
	levelStr := "null"
	if level != "" {
		levelStr = `"` + level + `"`
	}
	return `{"data":[{"id":"` + id + `","type":"log_group","attributes":{"id":"` + id + `","name":"` + name + `","level":` + levelStr + `,"environments":{},"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-06-15T12:00:00Z"}}]}`
}

// loggingFailTransport returns a network error for every request.
type loggingFailTransport struct{}

func (t *loggingFailTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

// loggingBrokenBodyTransport returns a 200 response whose body fails on Read.
type loggingBrokenBodyTransport struct{}

func (t *loggingBrokenBodyTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(&loggingBrokenReader{}),
		Header:     make(http.Header),
	}, nil
}

type loggingBrokenReader struct{}

func (r *loggingBrokenReader) Read(_ []byte) (int, error) {
	return 0, fmt.Errorf("simulated read error")
}

func newFailClient(t *testing.T, transport http.RoundTripper) *smplkit.SmplClient {
	t.Helper()
	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://example.com"),
		smplkit.WithHTTPClient(&http.Client{Transport: transport}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// ── Accessors ───────────────────────────────────────────────────────────────

func TestLoggingClient_Accessors(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	logging := client.Logging()
	require.NotNil(t, logging)
	assert.Same(t, logging, client.Logging())
	require.NotNil(t, logging.Loggers())
	assert.Same(t, logging.Loggers(), logging.Loggers())
	require.NotNil(t, logging.LogGroups())
	assert.Same(t, logging.LogGroups(), logging.LogGroups())
}

// ── Loggers().New / LogGroups().New ─────────────────────────────────────────

func TestLoggers_New(t *testing.T) {
	client := newLoggingTestClient(t, nil)
	logger := client.Logging().Loggers().New("my.logger")
	assert.Equal(t, "my.logger", logger.ID)
	assert.Equal(t, "my.logger", logger.Name) // defaults to id
	assert.True(t, logger.Managed)
	assert.NotNil(t, logger.Environments)
}

func TestLoggers_New_WithOptions(t *testing.T) {
	client := newLoggingTestClient(t, nil)
	logger := client.Logging().Loggers().New("checkout-v2",
		smplkit.WithLoggerName("Checkout Logger"),
		smplkit.WithLoggerManaged(false),
	)
	assert.Equal(t, "checkout-v2", logger.ID)
	assert.Equal(t, "Checkout Logger", logger.Name)
	assert.False(t, logger.Managed)
}

func TestLogGroups_New(t *testing.T) {
	client := newLoggingTestClient(t, nil)
	group := client.Logging().LogGroups().New("infra")
	assert.Equal(t, "infra", group.ID)
	assert.Equal(t, "Infra", group.Name) // keyToDisplayName
	assert.NotNil(t, group.Environments)
}

func TestLogGroups_New_WithOptions(t *testing.T) {
	client := newLoggingTestClient(t, nil)
	group := client.Logging().LogGroups().New("database",
		smplkit.WithLogGroupName("Database Group"),
		smplkit.WithLogGroupParent("infra"),
	)
	assert.Equal(t, "database", group.ID)
	assert.Equal(t, "Database Group", group.Name)
	require.NotNil(t, group.Group)
	assert.Equal(t, "infra", *group.Group)
}

// ── Loggers().Get / List / Delete ───────────────────────────────────────────

func TestLoggers_Get(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/loggers/my.logger", r.URL.Path)
		assert.Equal(t, "Bearer sk_test_key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Accept"))
		assert.True(t, strings.HasPrefix(r.Header.Get("User-Agent"), "smplkit-go-sdk/"))
		_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "My Logger", "INFO", true)))
	}))
	logger, err := client.Logging().Loggers().Get(context.Background(), "my.logger")
	require.NoError(t, err)
	assert.Equal(t, "my.logger", logger.ID)
	require.NotNil(t, logger.Level)
	assert.Equal(t, smplkit.LogLevelInfo, *logger.Level)
	assert.True(t, logger.Managed)
}

func TestLoggers_Get_NotFound(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))
	_, err := client.Logging().Loggers().Get(context.Background(), "nope")
	require.Error(t, err)
	var notFound *smplkit.NotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestLoggers_Get_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	_, err := client.Logging().Loggers().Get(context.Background(), "my.logger")
	require.Error(t, err)
	var base *smplkit.Error
	require.True(t, errors.As(err, &base))
	assert.Equal(t, 500, base.StatusCode)
}

func TestLoggers_Get_NetworkError(t *testing.T) {
	client := newFailClient(t, &loggingFailTransport{})
	_, err := client.Logging().Loggers().Get(context.Background(), "x")
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLoggers_Get_ReadBodyError(t *testing.T) {
	client := newFailClient(t, &loggingBrokenBodyTransport{})
	_, err := client.Logging().Loggers().Get(context.Background(), "x")
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Error(), "failed to read response body")
}

func TestLoggers_Get_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{not valid}`))
	}))
	_, err := client.Logging().Loggers().Get(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLoggers_Get_RichFields(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"my.logger","type":"logger","attributes":{"id":"my.logger","name":"My Logger","level":"INFO","group":"infra","managed":false,"environments":{"production":{"level":"ERROR"}},"sources":[{"service":"svc"}]}}}`))
	}))
	logger, err := client.Logging().Loggers().Get(context.Background(), "my.logger")
	require.NoError(t, err)
	require.NotNil(t, logger.Group)
	assert.Equal(t, "infra", *logger.Group)
	assert.False(t, logger.Managed)
	assert.Contains(t, logger.Environments, "production")
	require.Len(t, logger.Sources, 1)
}

func TestLoggers_List(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/loggers", r.URL.Path)
		_, _ = w.Write([]byte(`{"data":[
			{"id":"a.logger","type":"logger","attributes":{"id":"a.logger","name":"A","managed":true,"environments":{}}},
			{"id":"b.logger","type":"logger","attributes":{"id":"b.logger","name":"B","managed":true,"environments":{}}}
		]}`))
	}))
	loggers, err := client.Logging().Loggers().List(context.Background())
	require.NoError(t, err)
	require.Len(t, loggers, 2)
	assert.Equal(t, "A", loggers[0].Name)
}

func TestLoggers_List_Empty(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	loggers, err := client.Logging().Loggers().List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, loggers)
}

func TestLoggers_List_NetworkError(t *testing.T) {
	client := newFailClient(t, &loggingFailTransport{})
	_, err := client.Logging().Loggers().List(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLoggers_List_ReadBodyError(t *testing.T) {
	client := newFailClient(t, &loggingBrokenBodyTransport{})
	_, err := client.Logging().Loggers().List(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLoggers_List_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{invalid}`))
	}))
	_, err := client.Logging().Loggers().List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLoggers_List_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	_, err := client.Logging().Loggers().List(context.Background())
	require.Error(t, err)
}

func TestLoggers_Delete(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/v1/loggers/my.logger", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	require.NoError(t, client.Logging().Loggers().Delete(context.Background(), "my.logger"))
}

func TestLoggers_Delete_NotFound(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))
	err := client.Logging().Loggers().Delete(context.Background(), "nope")
	require.Error(t, err)
	var notFound *smplkit.NotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestLoggers_Delete_NetworkError(t *testing.T) {
	client := newFailClient(t, &loggingFailTransport{})
	err := client.Logging().Loggers().Delete(context.Background(), "x")
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLoggers_Delete_ReadBodyError(t *testing.T) {
	client := newFailClient(t, &loggingBrokenBodyTransport{})
	err := client.Logging().Loggers().Delete(context.Background(), "x")
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

// ── Logger.Save (upsert PUT) ────────────────────────────────────────────────

func TestLogger_Save_Upsert(t *testing.T) {
	var putLevel interface{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/my.logger", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		var body map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		data := body["data"].(map[string]interface{})
		assert.Equal(t, "logger", data["type"])
		assert.Equal(t, "my.logger", data["id"])
		putLevel = data["attributes"].(map[string]interface{})["level"]
		_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "My Logger", "INFO", true)))
	})
	client := newLoggingTestClient(t, mux)

	logger := client.Logging().Loggers().New("my.logger", smplkit.WithLoggerName("My Logger"))
	require.Nil(t, logger.Level)
	require.NoError(t, logger.Save(context.Background()))
	assert.Nil(t, putLevel, "null level when not explicitly set")
	assert.Equal(t, "My Logger", logger.Name)
}

func TestLogger_Save_NetworkError(t *testing.T) {
	client := newFailClient(t, &loggingFailTransport{})
	logger := client.Logging().Loggers().New("x")
	err := logger.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogger_Save_ReadBodyError(t *testing.T) {
	client := newFailClient(t, &loggingBrokenBodyTransport{})
	err := client.Logging().Loggers().New("x").Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogger_Save_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"validation error"}]}`))
	}))
	err := client.Logging().Loggers().New("x").Save(context.Background())
	require.Error(t, err)
	var valErr *smplkit.ValidationError
	require.True(t, errors.As(err, &valErr))
}

func TestLogger_Save_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{not valid}`))
	}))
	err := client.Logging().Loggers().New("x").Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

// ── Logger.Delete (model method) ────────────────────────────────────────────

func TestLogger_Delete_ViaModel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/my.logger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = w.Write([]byte(sampleLoggerJSON("my.logger", "My Logger", "INFO", true)))
			return
		}
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	})
	client := newLoggingTestClient(t, mux)
	logger, err := client.Logging().Loggers().Get(context.Background(), "my.logger")
	require.NoError(t, err)
	require.NoError(t, logger.Delete(context.Background()))
}

// ── LogGroups().Get / List / Delete ─────────────────────────────────────────

func TestLogGroups_Get(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/log_groups", r.URL.Path)
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	}))
	group, err := client.Logging().LogGroups().Get(context.Background(), "infra")
	require.NoError(t, err)
	assert.Equal(t, "infra", group.ID)
	assert.Equal(t, "Infra", group.Name)
}

func TestLogGroups_Get_NotFound(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	_, err := client.Logging().LogGroups().Get(context.Background(), "nope")
	require.Error(t, err)
	var notFound *smplkit.NotFoundError
	require.True(t, errors.As(err, &notFound))
	assert.Contains(t, notFound.Error(), "nope")
}

func TestLogGroups_Get_ListError(t *testing.T) {
	client := newFailClient(t, &loggingFailTransport{})
	_, err := client.Logging().LogGroups().Get(context.Background(), "infra")
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroups_Get_WithParent(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"database","type":"log_group","attributes":{"id":"database","name":"Database","level":"WARN","parent_id":"infra","environments":{},"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-06-15T12:00:00Z"}}]}`))
	}))
	group, err := client.Logging().LogGroups().Get(context.Background(), "database")
	require.NoError(t, err)
	require.NotNil(t, group.Group)
	assert.Equal(t, "infra", *group.Group)
	assert.NotNil(t, group.CreatedAt)
	assert.NotNil(t, group.UpdatedAt)
}

func TestLogGroups_List(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"infra","type":"log_group","attributes":{"id":"infra","name":"Infra","environments":{}}},
			{"id":"app","type":"log_group","attributes":{"id":"app","name":"App","environments":{}}}
		]}`))
	}))
	groups, err := client.Logging().LogGroups().List(context.Background())
	require.NoError(t, err)
	require.Len(t, groups, 2)
	assert.Equal(t, "Infra", groups[0].Name)
}

func TestLogGroups_List_NetworkError(t *testing.T) {
	client := newFailClient(t, &loggingFailTransport{})
	_, err := client.Logging().LogGroups().List(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroups_List_ReadBodyError(t *testing.T) {
	client := newFailClient(t, &loggingBrokenBodyTransport{})
	_, err := client.Logging().LogGroups().List(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroups_List_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{invalid}`))
	}))
	_, err := client.Logging().LogGroups().List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLogGroups_List_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	_, err := client.Logging().LogGroups().List(context.Background())
	require.Error(t, err)
}

func TestLogGroups_Delete(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/v1/log_groups/infra", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	require.NoError(t, client.Logging().LogGroups().Delete(context.Background(), "infra"))
}

func TestLogGroups_Delete_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	require.Error(t, client.Logging().LogGroups().Delete(context.Background(), "infra"))
}

func TestLogGroups_Delete_NetworkError(t *testing.T) {
	client := newFailClient(t, &loggingFailTransport{})
	err := client.Logging().LogGroups().Delete(context.Background(), "infra")
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroups_Delete_ReadBodyError(t *testing.T) {
	client := newFailClient(t, &loggingBrokenBodyTransport{})
	err := client.Logging().LogGroups().Delete(context.Background(), "infra")
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

// ── LogGroup.Save (create POST when CreatedAt nil; update PUT otherwise) ─────

func TestLogGroup_Save_Create(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/log_groups", r.URL.Path)
		var body map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		data := body["data"].(map[string]interface{})
		assert.Equal(t, "log_group", data["type"])
		assert.Equal(t, "infra", data["id"])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleLogGroupJSON("infra", "Infra", "WARN")))
	}))
	group := client.Logging().LogGroups().New("infra", smplkit.WithLogGroupName("Infra"))
	require.NoError(t, group.Save(context.Background()))
	assert.Equal(t, "infra", group.ID)
}

func TestLogGroup_Save_Create_NetworkError(t *testing.T) {
	client := newFailClient(t, &loggingFailTransport{})
	err := client.Logging().LogGroups().New("x").Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroup_Save_Create_ReadBodyError(t *testing.T) {
	client := newFailClient(t, &loggingBrokenBodyTransport{})
	err := client.Logging().LogGroups().New("x").Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLogGroup_Save_Create_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"validation error"}]}`))
	}))
	err := client.Logging().LogGroups().New("x").Save(context.Background())
	require.Error(t, err)
	var valErr *smplkit.ValidationError
	require.True(t, errors.As(err, &valErr))
}

func TestLogGroup_Save_Create_MalformedJSON(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	}))
	err := client.Logging().LogGroups().New("x").Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLogGroup_Save_Update(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	})
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		_, _ = w.Write([]byte(sampleLogGroupJSON("infra", "Updated Infra", "ERROR")))
	})
	client := newLoggingTestClient(t, mux)

	// Get populates CreatedAt, so Save takes the update (PUT) branch.
	group, err := client.Logging().LogGroups().Get(context.Background(), "infra")
	require.NoError(t, err)
	require.NotNil(t, group.CreatedAt)
	group.Name = "Updated Infra"
	require.NoError(t, group.Save(context.Background()))
	assert.Equal(t, "Updated Infra", group.Name)
}

func TestLogGroup_Save_Update_HTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	})
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})
	client := newLoggingTestClient(t, mux)
	group, err := client.Logging().LogGroups().Get(context.Background(), "infra")
	require.NoError(t, err)
	err = group.Save(context.Background())
	require.Error(t, err)
	var notFound *smplkit.NotFoundError
	require.True(t, errors.As(err, &notFound))
}

func TestLogGroup_Save_Update_MalformedJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	})
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{invalid`))
	})
	client := newLoggingTestClient(t, mux)
	group, err := client.Logging().LogGroups().Get(context.Background(), "infra")
	require.NoError(t, err)
	err = group.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestLogGroup_Save_Update_NetworkError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	})
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	})
	client := newLoggingTestClient(t, mux)
	group, err := client.Logging().LogGroups().Get(context.Background(), "infra")
	require.NoError(t, err)
	require.Error(t, group.Save(context.Background()))
}

func TestLogGroup_Save_Update_ReadBodyError(t *testing.T) {
	transport := &loggingMethodAwareTransport{
		getHandler: func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(sampleLogGroupListJSON("infra", "Infra", "WARN"))),
				Header:     http.Header{"Content-Type": {"application/vnd.api+json"}},
			}, nil
		},
		putHandler: func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(&loggingBrokenReader{}),
				Header:     make(http.Header),
			}, nil
		},
	}
	client := newFailClient(t, transport)
	group, err := client.Logging().LogGroups().Get(context.Background(), "infra")
	require.NoError(t, err)
	err = group.Save(context.Background())
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

// LogGroup.Save with CreatedAt nil but a server-assigned ID returned (create
// path returning a different ID) updates the in-memory group.
func TestLogGroup_Save_CreatePath_ServerAssignedID(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleLogGroupJSON("server-assigned", "Infra", "WARN")))
	}))
	group := client.Logging().LogGroups().New("temp", smplkit.WithLogGroupName("Infra"))
	require.NoError(t, group.Save(context.Background()))
	assert.Equal(t, "server-assigned", group.ID)
}

// ── LogGroup.Delete (model method) ──────────────────────────────────────────

func TestLogGroup_Delete_ViaModel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleLogGroupListJSON("infra", "Infra", "WARN")))
	})
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	})
	client := newLoggingTestClient(t, mux)
	group, err := client.Logging().LogGroups().Get(context.Background(), "infra")
	require.NoError(t, err)
	require.NoError(t, group.Delete(context.Background()))
}

// ── RegisterSources ─────────────────────────────────────────────────────────

func TestLoggers_RegisterSources(t *testing.T) {
	var body map[string]interface{}
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/loggers/bulk", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"registered":2}`))
	}))
	sources := []smplkit.LoggerSource{
		smplkit.NewLoggerSource("a.logger", smplkit.WithLoggerSourceService("svc"), smplkit.WithLoggerSourceResolvedLevel(smplkit.LogLevelDebug), smplkit.WithLoggerSourceLevel(smplkit.LogLevelWarn)),
		smplkit.NewLoggerSource("b.logger", smplkit.WithLoggerSourceEnvironment("prod")),
	}
	require.NoError(t, client.Logging().Loggers().RegisterSources(context.Background(), sources))
	loggers := body["loggers"].([]interface{})
	require.Len(t, loggers, 2)
	// The explicit (configured) level is sent distinct from resolved_level.
	first := loggers[0].(map[string]interface{})
	assert.Equal(t, string(smplkit.LogLevelWarn), first["level"])
	assert.Equal(t, string(smplkit.LogLevelDebug), first["resolved_level"])
}

func TestLoggers_RegisterSources_Empty(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("no HTTP expected for empty sources")
	}))
	require.NoError(t, client.Logging().Loggers().RegisterSources(context.Background(), nil))
}

func TestLoggers_RegisterSources_NetworkError(t *testing.T) {
	client := newFailClient(t, &loggingFailTransport{})
	err := client.Logging().Loggers().RegisterSources(context.Background(), []smplkit.LoggerSource{smplkit.NewLoggerSource("x")})
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLoggers_RegisterSources_ReadBodyError(t *testing.T) {
	client := newFailClient(t, &loggingBrokenBodyTransport{})
	err := client.Logging().Loggers().RegisterSources(context.Background(), []smplkit.LoggerSource{smplkit.NewLoggerSource("x")})
	require.Error(t, err)
	var connErr *smplkit.ConnectionError
	require.True(t, errors.As(err, &connErr))
}

func TestLoggers_RegisterSources_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"bad"}]}`))
	}))
	err := client.Logging().Loggers().RegisterSources(context.Background(), []smplkit.LoggerSource{smplkit.NewLoggerSource("x")})
	require.Error(t, err)
}

// ── Register (buffered) + Flush + PendingCount ──────────────────────────────

func TestLoggers_Register_BufferedThenFlush(t *testing.T) {
	var bulkBody []byte
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			bulkBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	loggers := client.Logging().Loggers()
	require.NoError(t, loggers.Register(context.Background(), []smplkit.LoggerSource{smplkit.NewLoggerSource("acme.app", smplkit.WithLoggerSourceResolvedLevel(smplkit.LogLevelInfo))}, false))
	assert.Equal(t, 1, loggers.PendingCount())
	require.NoError(t, loggers.Flush(context.Background()))
	assert.Equal(t, 0, loggers.PendingCount())
	assert.Contains(t, string(bulkBody), "acme.app")
}

func TestLoggers_Register_ExplicitLevel(t *testing.T) {
	var bulkBody []byte
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			bulkBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	loggers := client.Logging().Loggers()
	require.NoError(t, loggers.Register(context.Background(), []smplkit.LoggerSource{
		smplkit.NewLoggerSource("acme.app",
			smplkit.WithLoggerSourceResolvedLevel(smplkit.LogLevelInfo),
			smplkit.WithLoggerSourceLevel(smplkit.LogLevelWarn)),
	}, true))
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(bulkBody, &body))
	first := body["loggers"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, string(smplkit.LogLevelWarn), first["level"])
	assert.Equal(t, string(smplkit.LogLevelInfo), first["resolved_level"])
}

func TestLoggers_Register_FlushImmediately(t *testing.T) {
	var bulkCalls int32
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			atomic.AddInt32(&bulkCalls, 1)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	err := client.Logging().Loggers().Register(context.Background(), []smplkit.LoggerSource{smplkit.NewLoggerSource("now.logger")}, true)
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&bulkCalls))
}

func TestLoggers_Flush_EmptyNoHTTP(t *testing.T) {
	var bulkCalls int32
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			atomic.AddInt32(&bulkCalls, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	require.NoError(t, client.Logging().Loggers().Flush(context.Background()))
	assert.Equal(t, int32(0), atomic.LoadInt32(&bulkCalls))
}

func TestLoggers_Flush_HTTPError(t *testing.T) {
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"Invalid level value"}]}`))
	}))
	client.Logging().RegisterLogger("bad.logger", smplkit.LogLevelInfo)
	err := client.Logging().Loggers().Flush(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid level value")
}

// LoggingClient.Flush delegates to Loggers().Flush — exercise the top-level verb.
func TestLoggingClient_Flush_Delegates(t *testing.T) {
	var bulkCalls int32
	client := newLoggingTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			atomic.AddInt32(&bulkCalls, 1)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	client.Logging().RegisterLogger("acme.app", smplkit.LogLevelInfo)
	require.NoError(t, client.Logging().Flush(context.Background()))
	assert.Equal(t, int32(1), atomic.LoadInt32(&bulkCalls))
}

// ── NotInstalledError gate via public surface ───────────────────────────────

func TestLiveSurface_GatedBeforeInstall(t *testing.T) {
	client := newLoggingTestClient(t, nil)
	logging := client.Logging()

	err := logging.OnChange(func(*smplkit.LoggerChangeEvent) {})
	var ni *smplkit.NotInstalledError
	require.True(t, errors.As(err, &ni), "OnChange must be gated before Install")

	err = logging.OnChangeKey("k", func(*smplkit.LoggerChangeEvent) {})
	require.True(t, errors.As(err, &ni), "OnChangeKey must be gated before Install")

	err = logging.Refresh(context.Background())
	require.True(t, errors.As(err, &ni), "Refresh must be gated before Install")
}

func TestLiveSurface_WorksAfterInstall(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	client := newLoggingTestClient(t, mux)
	require.NoError(t, client.Logging().Install(context.Background()))

	require.NoError(t, client.Logging().OnChange(func(*smplkit.LoggerChangeEvent) {}))
	require.NoError(t, client.Logging().OnChangeKey("k", func(*smplkit.LoggerChangeEvent) {}))
	require.NoError(t, client.Logging().Refresh(context.Background()))
}

// ── RegisterAdapter / RegisterLogger via public surface ─────────────────────

func TestRegisterLogger_NoPanic(t *testing.T) {
	client := newLoggingTestClient(t, nil)
	client.Logging().RegisterLogger("MyApp/DB:Queries", smplkit.LogLevelDebug)
	assert.GreaterOrEqual(t, client.Logging().Loggers().PendingCount(), 1)
}

func TestRegisterAdapter_BeforeInstall(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	client := newLoggingTestClient(t, mux)
	adapter := &publicTestAdapter{discovered: []smplkit.TestDiscoveredLogger{{Name: "com.acme.app", Level: "INFO"}}}
	client.Logging().RegisterAdapter(adapter)
	require.NoError(t, client.Logging().Install(context.Background()))
	assert.True(t, adapter.hookInstalled)
}

func TestRegisterAdapter_AfterInstallPanics(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	client := newLoggingTestClient(t, mux)
	require.NoError(t, client.Logging().Install(context.Background()))
	assert.Panics(t, func() { client.Logging().RegisterAdapter(&publicTestAdapter{}) })
}

func TestInstall_NoAdaptersWarnsButManagementWorks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	var logBuf strings.Builder
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	client := newLoggingTestClient(t, mux)
	require.NoError(t, client.Logging().Install(context.Background()))
	assert.Contains(t, logBuf.String(), "no logging adapters registered")

	loggers, err := client.Logging().Loggers().List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, loggers)
}

func TestInstall_AppliesInheritedLevelViaGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":1}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"com.acme.db","type":"logger","attributes":{"id":"com.acme.db","name":"com.acme.db","managed":true,"group":"sql","environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"ERROR","environments":{}}}]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	client := newLoggingTestClient(t, mux)
	adapter := &publicTestAdapter{discovered: []smplkit.TestDiscoveredLogger{{Name: "com.acme.db", Level: "DEBUG"}}}
	client.Logging().RegisterAdapter(adapter)
	require.NoError(t, client.Logging().Install(context.Background()))

	var got string
	for _, al := range adapter.applied {
		if al.name == "com.acme.db" {
			got = al.level
		}
	}
	assert.Equal(t, "ERROR", got, "group's level must propagate to the dependent logger")
}

func TestInstall_NewLoggerHookAfterInstall(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	client := newLoggingTestClient(t, mux)
	adapter := &publicTestAdapter{}
	client.Logging().RegisterAdapter(adapter)
	require.NoError(t, client.Logging().Install(context.Background()))

	require.NotNil(t, adapter.hookCallback)
	adapter.hookCallback("com.acme.new", "INFO")
	require.NotEmpty(t, adapter.applied)
	assert.Equal(t, "com.acme.new", adapter.applied[0].name)
	assert.Equal(t, "INFO", adapter.applied[0].level)
}

// ── Close cleans up logging ─────────────────────────────────────────────────

func TestClose_CallsUninstallHook(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	adapter := &publicTestAdapter{}
	client.Logging().RegisterAdapter(adapter)
	require.NoError(t, client.Close())
	assert.True(t, adapter.uninstalled)
}

func TestClose_NoPanic(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NoError(t, client.Close())
}

// ── Test doubles ────────────────────────────────────────────────────────────

type publicAppliedLevel struct {
	name  string
	level string
}

type publicTestAdapter struct {
	discovered    []smplkit.TestDiscoveredLogger
	applied       []publicAppliedLevel
	hookInstalled bool
	hookCallback  func(string, string)
	uninstalled   bool
}

func (a *publicTestAdapter) Name() string                             { return "public-test" }
func (a *publicTestAdapter) Discover() []smplkit.TestDiscoveredLogger { return a.discovered }
func (a *publicTestAdapter) ApplyLevel(name string, level string) {
	a.applied = append(a.applied, publicAppliedLevel{name: name, level: level})
}
func (a *publicTestAdapter) InstallHook(cb func(name string, level string)) {
	a.hookInstalled = true
	a.hookCallback = cb
}
func (a *publicTestAdapter) UninstallHook() { a.uninstalled = true }

// loggingMethodAwareTransport dispatches to different handlers per HTTP method.
type loggingMethodAwareTransport struct {
	getHandler func(req *http.Request) (*http.Response, error)
	putHandler func(req *http.Request) (*http.Response, error)
}

func (t *loggingMethodAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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
