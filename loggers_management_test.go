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

func newMgmtNamespaceTestClient(t *testing.T, handler http.Handler) *smplkit.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service"},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	return client
}

// loggerJSON is the minimum JSON:API single-resource response shape the SDK accepts.
const loggerJSON = `{
	"data": {
		"id": "showcase",
		"type": "logger",
		"attributes": {
			"id": "showcase",
			"name": "showcase",
			"level": "INFO",
			"managed": true,
			"environments": {},
			"sources": []
		}
	}
}`

const loggerListJSON = `{
	"data": [{
		"id": "showcase",
		"type": "logger",
		"attributes": {"id":"showcase","name":"showcase","level":"INFO","managed":true,"environments":{},"sources":[]}
	}]
}`

const logGroupJSON = `{
	"data": {
		"id": "infra",
		"type": "log_group",
		"attributes": {"id":"infra","name":"Infra","level":"WARN","environments":{}}
	}
}`

const logGroupListJSON = `{
	"data": [{
		"id": "infra",
		"type": "log_group",
		"attributes": {"id":"infra","name":"Infra","level":"WARN","environments":{}}
	}]
}`

func TestLoggersManagement_Get_List_Delete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/showcase", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			_, _ = w.Write([]byte(loggerJSON))
		case "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected method %s on list endpoint", r.Method)
		}
		_, _ = w.Write([]byte(loggerListJSON))
	})

	client := newMgmtNamespaceTestClient(t, mux)
	ns := client.Manage().Loggers()

	got, err := ns.Get(context.Background(), "showcase")
	require.NoError(t, err)
	assert.Equal(t, "showcase", got.ID)

	list, err := ns.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "showcase", list[0].ID)

	require.NoError(t, ns.Delete(context.Background(), "showcase"))
}

// --- LoggersManagement.Flush ---

func TestLoggersManagement_Flush_PostsBufferedDiscoveries(t *testing.T) {
	var (
		bulkCalls    int32
		bulkBodyOnce sync.Once
		bulkBody     []byte
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bulkCalls, 1)
		bulkBodyOnce.Do(func() {
			bulkBody, _ = io.ReadAll(r.Body)
		})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":1}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := newMgmtNamespaceTestClient(t, mux)
	client.Logging().RegisterLogger("acme.app", smplkit.LogLevelInfo)

	require.NoError(t, client.Manage().Loggers().Flush(context.Background()))
	assert.Equal(t, int32(1), atomic.LoadInt32(&bulkCalls))
	assert.Contains(t, string(bulkBody), `"acme.app"`)
}

func TestLoggersManagement_Flush_EmptyBuffer_NoHTTP(t *testing.T) {
	var bulkCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&bulkCalls, 1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := newMgmtNamespaceTestClient(t, mux)
	require.NoError(t, client.Manage().Loggers().Flush(context.Background()))
	assert.Equal(t, int32(0), atomic.LoadInt32(&bulkCalls))
}

func TestLoggersManagement_Flush_PropagatesHTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"Invalid level value"}]}`))
	})

	client := newMgmtNamespaceTestClient(t, mux)
	client.Logging().RegisterLogger("bad.logger", smplkit.LogLevelInfo)

	err := client.Manage().Loggers().Flush(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid level value")
}

func TestLoggersManagement_Flush_StandaloneNoOp(t *testing.T) {
	// A management-only client (NewManagementClient) has no runtime
	// LoggingClient and therefore no discovery buffer. Flush must be a
	// no-op rather than panicking.
	mgmt, err := smplkit.NewManagementClient(smplkit.ManagementConfig{
		APIKey: "sk_test_key",
	}, smplkit.WithBaseURL("http://127.0.0.1:1"))
	require.NoError(t, err)

	require.NoError(t, mgmt.Loggers().Flush(context.Background()))
}

func TestLogGroupsManagement_New_Get_List_Delete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups/infra", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			_, _ = w.Write([]byte(logGroupJSON))
		case "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(logGroupListJSON))
	})

	client := newMgmtNamespaceTestClient(t, mux)
	ns := client.Manage().LogGroups()

	// Construction: New returns an unsaved LogGroup (no HTTP).
	g := ns.New("infra")
	assert.Equal(t, "infra", g.ID)

	// Read paths.
	got, err := ns.Get(context.Background(), "infra")
	require.NoError(t, err)
	assert.Equal(t, "infra", got.ID)

	list, err := ns.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, ns.Delete(context.Background(), "infra"))
}
