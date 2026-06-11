package smplkit_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// This file exercises the renamed management sub-clients — LoggersClient and
// LogGroupsClient — primarily through the standalone NewLoggingClient
// constructor, which builds and owns its own generated logging transport. The
// wired-parent surface (client.Logging().Loggers()/.LogGroups()) is covered in
// logging_client_test.go.

func newStandaloneLogging(t *testing.T, handler http.Handler) *smplkit.LoggingClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	lc, err := smplkit.NewLoggingClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	return lc
}

const standaloneLoggerJSON = `{
	"data": {
		"id": "showcase",
		"type": "logger",
		"attributes": {"id":"showcase","name":"showcase","level":"INFO","managed":true,"environments":{},"sources":[]}
	}
}`

const standaloneLoggerListJSON = `{
	"data": [{
		"id": "showcase",
		"type": "logger",
		"attributes": {"id":"showcase","name":"showcase","level":"INFO","managed":true,"environments":{},"sources":[]}
	}]
}`

const standaloneLogGroupJSON = `{
	"data": {
		"id": "infra",
		"type": "log_group",
		"attributes": {"id":"infra","name":"Infra","level":"WARN","environments":{}}
	}
}`

const standaloneLogGroupListJSON = `{
	"data": [{
		"id": "infra",
		"type": "log_group",
		"attributes": {"id":"infra","name":"Infra","level":"WARN","environments":{}}
	}]
}`

// ── LoggersClient via standalone client ─────────────────────────────────────

func TestStandalone_Loggers_Get_List_Delete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/showcase", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			assert.Equal(t, "Bearer sk_test_key", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(standaloneLoggerJSON))
		case "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		_, _ = w.Write([]byte(standaloneLoggerListJSON))
	})

	lc := newStandaloneLogging(t, mux)
	loggers := lc.Loggers()

	got, err := loggers.Get(context.Background(), "showcase")
	require.NoError(t, err)
	assert.Equal(t, "showcase", got.ID)

	list, err := loggers.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "showcase", list[0].ID)

	require.NoError(t, loggers.Delete(context.Background(), "showcase"))
}

func TestStandalone_Loggers_New_Save(t *testing.T) {
	var saved bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/showcase", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		saved = true
		_, _ = w.Write([]byte(standaloneLoggerJSON))
	})
	lc := newStandaloneLogging(t, mux)

	logger := lc.Loggers().New("showcase")
	require.NoError(t, logger.Save(context.Background()))
	assert.True(t, saved)
}

func TestStandalone_Loggers_Register_Flush_PendingCount(t *testing.T) {
	var (
		bulkCalls int32
		bulkOnce  sync.Once
		bulkBody  []byte
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bulkCalls, 1)
		bulkOnce.Do(func() { bulkBody, _ = io.ReadAll(r.Body) })
		_, _ = w.Write([]byte(`{"registered":1}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	lc := newStandaloneLogging(t, mux)
	loggers := lc.Loggers()

	require.NoError(t, loggers.Register(context.Background(), []smplkit.LoggerSource{
		smplkit.NewLoggerSource("acme.app", smplkit.WithLoggerSourceResolvedLevel(smplkit.LogLevelInfo)),
	}, false))
	assert.Equal(t, 1, loggers.PendingCount())

	require.NoError(t, loggers.Flush(context.Background()))
	assert.Equal(t, int32(1), atomic.LoadInt32(&bulkCalls))
	assert.Equal(t, 0, loggers.PendingCount())
	assert.Contains(t, string(bulkBody), "acme.app")
}

func TestStandalone_Loggers_FlushSync_Empty(t *testing.T) {
	var bulkCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&bulkCalls, 1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	lc := newStandaloneLogging(t, mux)
	require.NoError(t, lc.Loggers().FlushSync(context.Background()))
	assert.Equal(t, int32(0), atomic.LoadInt32(&bulkCalls))
}

func TestStandalone_RegisterLogger_DrainsThroughLoggersBuffer(t *testing.T) {
	var bulkBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		bulkBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"registered":1}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	lc := newStandaloneLogging(t, mux)
	// RegisterLogger on the fused client buffers into the same queue the
	// Loggers sub-client drains.
	lc.RegisterLogger("acme.app", smplkit.LogLevelInfo)
	assert.Equal(t, 1, lc.Loggers().PendingCount())
	require.NoError(t, lc.Loggers().Flush(context.Background()))
	assert.Contains(t, string(bulkBody), "acme.app")
}

// ── LogGroupsClient via standalone client ───────────────────────────────────

func TestStandalone_LogGroups_New_Get_List_Delete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		_, _ = w.Write([]byte(standaloneLogGroupListJSON))
	})

	lc := newStandaloneLogging(t, mux)
	groups := lc.LogGroups()

	g := groups.New("infra")
	assert.Equal(t, "infra", g.ID)

	got, err := groups.Get(context.Background(), "infra")
	require.NoError(t, err)
	assert.Equal(t, "infra", got.ID)

	list, err := groups.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, groups.Delete(context.Background(), "infra"))
}

func TestStandalone_LogGroups_Create(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(standaloneLogGroupJSON))
	})
	lc := newStandaloneLogging(t, mux)
	g := lc.LogGroups().New("infra")
	require.NoError(t, g.Save(context.Background()))
	assert.Equal(t, "infra", g.ID)
}
