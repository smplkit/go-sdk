package smplkit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
)

// newManagementTestClient creates a test client routed to the given handler.
func newManagementTestClient(t *testing.T, handler http.Handler) *smplkit.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// ── Management() accessor ─────────────────────────────────────────────────────

func TestClient_ManagementReturnsSubClient(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test", Environment: "test", Service: "svc", DisableTelemetry: true})
	require.NoError(t, err)
	m := client.Management()
	require.NotNil(t, m)
	assert.Same(t, m, client.Management()) // same pointer
}

// ── EnvironmentClassification ─────────────────────────────────────────────────

func TestEnvironmentClassification_Values(t *testing.T) {
	assert.Equal(t, smplkit.EnvironmentClassification("STANDARD"), smplkit.EnvironmentClassificationStandard)
	assert.Equal(t, smplkit.EnvironmentClassification("AD_HOC"), smplkit.EnvironmentClassificationAdHoc)
}

// ── EnvironmentsManagement ────────────────────────────────────────────────────

func TestEnvironmentsManagement_New(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	e := client.Management().Environments().New("staging", "Staging")
	assert.Equal(t, "staging", e.ID)
	assert.Equal(t, "Staging", e.Name)
	assert.Equal(t, smplkit.EnvironmentClassificationStandard, e.Classification)
	assert.Nil(t, e.Color)
	assert.Nil(t, e.CreatedAt)
}

func TestEnvironmentsManagement_New_WithOptions(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	e := client.Management().Environments().New("preview",
		"Preview Branch",
		smplkit.WithEnvironmentColor("#8b5cf6"),
		smplkit.WithEnvironmentClassification(smplkit.EnvironmentClassificationAdHoc),
	)
	assert.Equal(t, "preview", e.ID)
	assert.Equal(t, smplkit.EnvironmentClassificationAdHoc, e.Classification)
	require.NotNil(t, e.Color)
	assert.Equal(t, "#8b5cf6", *e.Color)
}

func TestEnvironmentsManagement_List(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/environments" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[
				{"id":"production","type":"environment","attributes":{"name":"Production","classification":"STANDARD"}},
				{"id":"preview-acme","type":"environment","attributes":{"name":"Preview Acme","classification":"AD_HOC","color":"#8b5cf6"}}
			]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	envs, err := client.Management().Environments().List(context.Background())
	require.NoError(t, err)
	assert.Len(t, envs, 2)
	assert.Equal(t, "production", envs[0].ID)
	assert.Equal(t, smplkit.EnvironmentClassificationStandard, envs[0].Classification)
	assert.Equal(t, "preview-acme", envs[1].ID)
	assert.Equal(t, smplkit.EnvironmentClassificationAdHoc, envs[1].Classification)
	require.NotNil(t, envs[1].Color)
	assert.Equal(t, "#8b5cf6", *envs[1].Color)
}

func TestEnvironmentsManagement_Get(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/environments/production" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"production","type":"environment","attributes":{"name":"Production","classification":"STANDARD"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	e, err := client.Management().Environments().Get(context.Background(), "production")
	require.NoError(t, err)
	assert.Equal(t, "production", e.ID)
	assert.Equal(t, "Production", e.Name)
}

func TestEnvironmentsManagement_Get_NotFound(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))
	_, err := client.Management().Environments().Get(context.Background(), "nonexistent")
	require.Error(t, err)
	var nfe *smplkit.SmplNotFoundError
	assert.ErrorAs(t, err, &nfe)
}

func TestEnvironmentsManagement_Delete(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/environments/preview" {
			called = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	err := client.Management().Environments().Delete(context.Background(), "preview")
	require.NoError(t, err)
	assert.True(t, called)
}

func TestEnvironment_Save_Create(t *testing.T) {
	var receivedBody map[string]interface{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/environments" {
			if err := json.NewDecoder(r.Body).Decode(&receivedBody); err == nil {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"data":{"id":"staging","type":"environment","attributes":{"name":"Staging","classification":"STANDARD","created_at":"2024-01-01T00:00:00Z"}}}`))
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	e := client.Management().Environments().New("staging", "Staging")
	err := e.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "staging", e.ID)
	assert.NotNil(t, e.CreatedAt)
}

func TestEnvironment_Save_Update_NoClient(t *testing.T) {
	now := time.Now()
	env := smplkit.Environment{
		ID:        "production",
		Name:      "Production",
		CreatedAt: &now,
	}
	// No client set — Save must return an error regardless of create vs update path.
	err := env.Save(context.Background())
	require.Error(t, err)
}

func TestEnvironment_Save_NoClient(t *testing.T) {
	e := &smplkit.Environment{ID: "test", Name: "Test"}
	err := e.Save(context.Background())
	require.Error(t, err)
}

// ── ContextTypesManagement ────────────────────────────────────────────────────

func TestContextTypesManagement_New(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	ct := client.Management().ContextTypes().New("user", smplkit.WithContextTypeName("User"))
	assert.Equal(t, "user", ct.ID)
	assert.Equal(t, "User", ct.Name)
	assert.NotNil(t, ct.Attributes)
	assert.Empty(t, ct.Attributes)
}

func TestContextTypesManagement_New_DefaultName(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	ct := client.Management().ContextTypes().New("account")
	assert.Equal(t, "account", ct.Name) // defaults to id
}

func TestContextType_AddRemoveUpdateAttribute(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	ct := client.Management().ContextTypes().New("user")

	ct.AddAttribute("plan")
	ct.AddAttribute("region", map[string]interface{}{"type": "string"})
	assert.Len(t, ct.Attributes, 2)
	assert.Equal(t, map[string]interface{}{}, ct.Attributes["plan"])
	assert.Equal(t, map[string]interface{}{"type": "string"}, ct.Attributes["region"])

	ct.UpdateAttribute("plan", map[string]interface{}{"type": "enum"})
	assert.Equal(t, map[string]interface{}{"type": "enum"}, ct.Attributes["plan"])

	ct.RemoveAttribute("region")
	assert.Len(t, ct.Attributes, 1)
	assert.NotContains(t, ct.Attributes, "region")
}

func TestContextType_Save_NoClient(t *testing.T) {
	ct := &smplkit.ContextType{ID: "user", Name: "User"}
	err := ct.Save(context.Background())
	require.Error(t, err)
}

func TestContextTypesManagement_List(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/context_types" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[
				{"id":"user","attributes":{"id":"user","name":"User","attributes":{"plan":{},"region":{}}}},
				{"id":"account","attributes":{"id":"account","name":"Account","attributes":{}}}
			]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	types, err := client.Management().ContextTypes().List(context.Background())
	require.NoError(t, err)
	assert.Len(t, types, 2)
	assert.Equal(t, "user", types[0].ID)
	assert.Len(t, types[0].Attributes, 2)
}

func TestContextTypesManagement_Get(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/context_types/user" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"user","attributes":{"id":"user","name":"User","attributes":{"plan":{}}}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	ct, err := client.Management().ContextTypes().Get(context.Background(), "user")
	require.NoError(t, err)
	assert.Equal(t, "user", ct.ID)
	assert.Len(t, ct.Attributes, 1)
}

func TestContextTypesManagement_Delete(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/context_types/user" {
			called = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	err := client.Management().ContextTypes().Delete(context.Background(), "user")
	require.NoError(t, err)
	assert.True(t, called)
}

func TestContextType_Save_Create(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/context_types" {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"user","attributes":{"id":"user","name":"User","attributes":{"plan":{}},"created_at":"2024-01-01T00:00:00Z"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	ct := client.Management().ContextTypes().New("user", smplkit.WithContextTypeName("User"))
	ct.AddAttribute("plan")
	err := ct.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "user", ct.ID)
	assert.NotNil(t, ct.CreatedAt)
}

// ── ContextsManagement ────────────────────────────────────────────────────────

func TestContextsManagement_Register_NoFlush(t *testing.T) {
	flushed := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/contexts/bulk_register" {
			flushed = true
		}
		w.WriteHeader(http.StatusOK)
	})
	client := newManagementTestClient(t, handler)

	err := client.Management().Contexts().Register(context.Background(), []smplkit.Context{
		smplkit.NewContext("user", "u1", nil),
	})
	require.NoError(t, err)
	assert.False(t, flushed, "without WithContextFlush, no HTTP call expected")
}

func TestContextsManagement_Register_WithFlush(t *testing.T) {
	var received map[string]interface{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/contexts/bulk" {
			_ = json.NewDecoder(r.Body).Decode(&received)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	err := client.Management().Contexts().Register(context.Background(),
		[]smplkit.Context{smplkit.NewContext("user", "u1", map[string]interface{}{"plan": "free"})},
		smplkit.WithContextFlush(),
	)
	require.NoError(t, err)
	require.NotNil(t, received)
}

func TestContextsManagement_Flush_Empty(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	// No contexts registered — flush should be a no-op.
	err := client.Management().Contexts().Flush(context.Background())
	require.NoError(t, err)
}

func TestContextsManagement_List(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/contexts" && r.URL.Query().Get("filter[context_type]") == "user" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[
				{"id":"user:u1","type":"context","attributes":{"name":"Alice","attributes":{"plan":"free"}}},
				{"id":"user:u2","type":"context","attributes":{"attributes":{}}}
			]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	entities, err := client.Management().Contexts().List(context.Background(), "user")
	require.NoError(t, err)
	require.Len(t, entities, 2)
	assert.Equal(t, "user", entities[0].ContextType)
	assert.Equal(t, "u1", entities[0].Key)
	assert.Equal(t, "user:u1", entities[0].CompositeID())
	require.NotNil(t, entities[0].Name)
	assert.Equal(t, "Alice", *entities[0].Name)
}

func TestContextsManagement_Get_CompositeID(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/contexts/user:u1" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"user:u1","attributes":{"attributes":{}}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	ce, err := client.Management().Contexts().Get(context.Background(), "user:u1")
	require.NoError(t, err)
	assert.Equal(t, "user", ce.ContextType)
	assert.Equal(t, "u1", ce.Key)
}

func TestContextsManagement_Get_TypeAndKey(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/contexts/user:u1" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"user:u1","attributes":{"attributes":{}}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	ce, err := client.Management().Contexts().Get(context.Background(), "user", "u1")
	require.NoError(t, err)
	assert.Equal(t, "user", ce.ContextType)
	assert.Equal(t, "u1", ce.Key)
}

func TestContextsManagement_Get_InvalidID(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	_, err := client.Management().Contexts().Get(context.Background(), "invalid-no-colon")
	require.Error(t, err)
}

func TestContextsManagement_Get_TooManyArgs(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	_, err := client.Management().Contexts().Get(context.Background(), "a", "b", "c")
	require.Error(t, err)
}

func TestContextsManagement_Delete_CompositeID(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/contexts/user:u1" {
			called = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	err := client.Management().Contexts().Delete(context.Background(), "user:u1")
	require.NoError(t, err)
	assert.True(t, called)
}

func TestContextsManagement_Delete_TypeAndKey(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/contexts/account:acme" {
			called = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	err := client.Management().Contexts().Delete(context.Background(), "account", "acme")
	require.NoError(t, err)
	assert.True(t, called)
}

// ── AccountSettings ───────────────────────────────────────────────────────────

func TestAccountSettingsManagement_Get(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/accounts/current/settings" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"environment_order":["production","staging","development"]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	s, err := client.Management().AccountSettings().Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"production", "staging", "development"}, s.EnvironmentOrder())
}

func TestAccountSettingsManagement_Get_Empty(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	client := newManagementTestClient(t, handler)

	s, err := client.Management().AccountSettings().Get(context.Background())
	require.NoError(t, err)
	assert.Nil(t, s.EnvironmentOrder())
}

func TestAccountSettings_SetEnvironmentOrder(t *testing.T) {
	s := &smplkit.AccountSettings{Raw: map[string]interface{}{}}
	s.SetEnvironmentOrder([]string{"production", "staging"})
	assert.Equal(t, []string{"production", "staging"}, s.EnvironmentOrder())
}

func TestAccountSettings_Save_NoClient(t *testing.T) {
	s := &smplkit.AccountSettings{Raw: map[string]interface{}{}}
	err := s.Save(context.Background())
	require.Error(t, err)
}

func TestAccountSettingsManagement_Save(t *testing.T) {
	var putBody map[string]interface{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/accounts/current/settings":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"environment_order":["production"]}`))
		case r.Method == "PUT" && r.URL.Path == "/api/v1/accounts/current/settings":
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"environment_order":["production","staging","development"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := newManagementTestClient(t, handler)

	s, err := client.Management().AccountSettings().Get(context.Background())
	require.NoError(t, err)

	s.SetEnvironmentOrder([]string{"production", "staging", "development"})
	err = s.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"production", "staging", "development"}, s.EnvironmentOrder())
	require.NotNil(t, putBody)
}

// ── ContextEntity ─────────────────────────────────────────────────────────────

func TestContextEntity_CompositeID(t *testing.T) {
	ce := &smplkit.ContextEntity{ContextType: "user", Key: "u1"}
	assert.Equal(t, "user:u1", ce.CompositeID())
}

// ── Shared buffer: flags + management share observations ─────────────────────

func TestSharedBuffer_FlagsAndManagementShareObservations(t *testing.T) {
	flushed := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/contexts/bulk" {
			flushed++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	// Register via management contexts — should flush only when asked.
	err := client.Management().Contexts().Register(context.Background(),
		[]smplkit.Context{smplkit.NewContext("user", "u1", nil)},
	)
	require.NoError(t, err)
	assert.Equal(t, 0, flushed)

	err = client.Management().Contexts().Flush(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
}

// ── LoggerSource ──────────────────────────────────────────────────────────────

func TestNewLoggerSource(t *testing.T) {
	s := smplkit.NewLoggerSource("sqlalchemy.engine",
		smplkit.WithLoggerSourceService("user-service"),
		smplkit.WithLoggerSourceEnvironment("production"),
		smplkit.WithLoggerSourceResolvedLevel(smplkit.LogLevelWarn),
	)
	assert.Equal(t, "sqlalchemy.engine", s.ID)
	require.NotNil(t, s.Service)
	assert.Equal(t, "user-service", *s.Service)
	require.NotNil(t, s.Environment)
	assert.Equal(t, "production", *s.Environment)
	require.NotNil(t, s.ResolvedLevel)
	assert.Equal(t, smplkit.LogLevelWarn, *s.ResolvedLevel)
}

func TestLoggingManagement_RegisterSources(t *testing.T) {
	var received map[string]interface{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/loggers/bulk" {
			_ = json.NewDecoder(r.Body).Decode(&received)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":3}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	err := client.Logging().Management().RegisterSources(context.Background(), []smplkit.LoggerSource{
		smplkit.NewLoggerSource("sqlalchemy.engine",
			smplkit.WithLoggerSourceService("user-service"),
			smplkit.WithLoggerSourceEnvironment("production"),
			smplkit.WithLoggerSourceResolvedLevel(smplkit.LogLevelWarn),
		),
		smplkit.NewLoggerSource("httpx",
			smplkit.WithLoggerSourceService("checkout-service"),
			smplkit.WithLoggerSourceEnvironment("staging"),
		),
	})
	require.NoError(t, err)
	require.NotNil(t, received)

	loggers, ok := received["loggers"].([]interface{})
	require.True(t, ok)
	assert.Len(t, loggers, 2)
}

func TestLoggingManagement_RegisterSources_Empty(t *testing.T) {
	called := false
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	err := client.Logging().Management().RegisterSources(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, called, "empty sources should not make an HTTP call")
}

func TestLoggingManagement_RegisterSources_Error(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"invalid"}]}`))
	}))
	err := client.Logging().Management().RegisterSources(context.Background(), []smplkit.LoggerSource{
		smplkit.NewLoggerSource("my.logger"),
	})
	require.Error(t, err)
}
