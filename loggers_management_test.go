package smplkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
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
