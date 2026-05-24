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

	smplkit "github.com/smplkit/go-sdk/v3"
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

// ── ServicesManagement ────────────────────────────────────────────────────────

func TestServicesManagement_New(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	s := client.Management().Services().New("user_service", "User Service")
	assert.Equal(t, "user_service", s.ID)
	assert.Equal(t, "User Service", s.Name)
	assert.Nil(t, s.CreatedAt)
}

func TestServicesManagement_List(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/services" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[
				{"id":"user_service","type":"service","attributes":{"name":"User Service"}},
				{"id":"billing","type":"service","attributes":{"name":"Billing"}}
			],"meta":{"pagination":{"page":1,"size":1000}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	svcs, err := client.Management().Services().List(context.Background())
	require.NoError(t, err)
	assert.Len(t, svcs, 2)
	assert.Equal(t, "user_service", svcs[0].ID)
	assert.Equal(t, "User Service", svcs[0].Name)
	assert.Equal(t, "billing", svcs[1].ID)
}

func TestServicesManagement_List_WithPagination(t *testing.T) {
	var capturedQuery string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/services" {
			capturedQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"page":2,"size":50}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	_, err := client.Management().Services().List(
		context.Background(),
		smplkit.WithPageNumber(2),
		smplkit.WithPageSize(50),
	)
	require.NoError(t, err)
	assert.Contains(t, capturedQuery, "page%5Bnumber%5D=2")
	assert.Contains(t, capturedQuery, "page%5Bsize%5D=50")
}

func TestServicesManagement_Get(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/services/user_service" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"user_service","type":"service","attributes":{"name":"User Service"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	s, err := client.Management().Services().Get(context.Background(), "user_service")
	require.NoError(t, err)
	assert.Equal(t, "user_service", s.ID)
	assert.Equal(t, "User Service", s.Name)
}

func TestServicesManagement_Get_NotFound(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))
	_, err := client.Management().Services().Get(context.Background(), "nonexistent")
	require.Error(t, err)
	var nfe *smplkit.SmplNotFoundError
	assert.ErrorAs(t, err, &nfe)
}

func TestServicesManagement_Delete(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/services/user_service" {
			called = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	err := client.Management().Services().Delete(context.Background(), "user_service")
	require.NoError(t, err)
	assert.True(t, called)
}

func TestService_Save_Create(t *testing.T) {
	var receivedBody map[string]interface{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/services" {
			if err := json.NewDecoder(r.Body).Decode(&receivedBody); err == nil {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"data":{"id":"user_service","type":"service","attributes":{"name":"User Service","created_at":"2026-04-01T00:00:00Z"}}}`))
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	s := client.Management().Services().New("user_service", "User Service")
	err := s.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "user_service", s.ID)
	assert.NotNil(t, s.CreatedAt)

	require.NotNil(t, receivedBody)
	data, ok := receivedBody["data"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "service", data["type"])
	assert.Equal(t, "user_service", data["id"])
	attrs, ok := data["attributes"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "User Service", attrs["name"])
}

func TestService_Save_Update(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/v1/services/user_service" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"user_service","type":"service","attributes":{"name":"Renamed","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-02T00:00:00Z"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	now := time.Now()
	s := &smplkit.Service{ID: "user_service", Name: "Renamed", CreatedAt: &now}
	// Inject the client via roundtrip helper.
	saved := client.Management().Services().New("user_service", "Renamed")
	saved.CreatedAt = &now
	err := saved.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Renamed", saved.Name)

	// The raw struct without a client must still error.
	err = s.Save(context.Background())
	require.Error(t, err)
}

func TestService_Save_Conflict(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/services" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"errors":[{"status":"409","title":"Conflict","detail":"service exists"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	s := client.Management().Services().New("user_service", "User Service")
	err := s.Save(context.Background())
	require.Error(t, err)
	var ce *smplkit.SmplConflictError
	assert.ErrorAs(t, err, &ce)
}

func TestService_Save_NoClient(t *testing.T) {
	s := &smplkit.Service{ID: "user_service", Name: "User Service"}
	err := s.Save(context.Background())
	require.Error(t, err)
}

func TestService_Delete_NoClient(t *testing.T) {
	s := &smplkit.Service{ID: "user_service", Name: "User Service"}
	err := s.Delete(context.Background())
	require.Error(t, err)
}

func TestService_Delete_ViaModel(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/services/user_service" {
			called = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	s := client.Management().Services().New("user_service", "User Service")
	err := s.Delete(context.Background())
	require.NoError(t, err)
	assert.True(t, called)
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

func TestLoggingManagement_RegisterSources_NoBranches(t *testing.T) {
	// Source with no optional fields covers the nil-branch paths in RegisterSources.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/loggers/bulk" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	err := client.Logging().Management().RegisterSources(context.Background(), []smplkit.LoggerSource{
		smplkit.NewLoggerSource("bare.logger"), // no service, env, or level
	})
	require.NoError(t, err)
}

// ── Error path coverage ───────────────────────────────────────────────────────

func TestEnvironment_Save_Update(t *testing.T) {
	// Update path: env already has CreatedAt set → triggers PUT.
	updated := false
	now := time.Now()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/v1/environments/production" {
			updated = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"production","type":"environment","attributes":{"name":"Production Updated","classification":"STANDARD","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-06-01T00:00:00Z"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	// Construct an env that already has CreatedAt — Save should call update.
	e := client.Management().Environments().New("production", "Production")
	e.CreatedAt = &now
	err := e.Save(context.Background())
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, "Production Updated", e.Name)
	assert.NotNil(t, e.UpdatedAt)
}

func TestEnvironmentsManagement_List_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid json`))
	}))
	_, err := client.Management().Environments().List(context.Background())
	require.Error(t, err)
}

func TestEnvironmentsManagement_Get_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not json`))
	}))
	_, err := client.Management().Environments().Get(context.Background(), "production")
	require.Error(t, err)
}

func TestEnvironmentsManagement_Create_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not json`))
	}))
	e := client.Management().Environments().New("test", "Test")
	err := e.Save(context.Background())
	require.Error(t, err)
}

func TestEnvironmentsManagement_Update_Error(t *testing.T) {
	now := time.Now()
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	e := client.Management().Environments().New("production", "Production")
	e.CreatedAt = &now
	err := e.Save(context.Background())
	require.Error(t, err)
}

func TestEnvironmentsManagement_Update_BadJSON(t *testing.T) {
	now := time.Now()
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not json`))
	}))
	e := client.Management().Environments().New("production", "Production")
	e.CreatedAt = &now
	err := e.Save(context.Background())
	require.Error(t, err)
}

// ── ServicesManagement bad-JSON paths ─────────────────────────────────────────

func TestServicesManagement_List_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid json`))
	}))
	_, err := client.Management().Services().List(context.Background())
	require.Error(t, err)
}

func TestServicesManagement_Get_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not json`))
	}))
	_, err := client.Management().Services().Get(context.Background(), "user_service")
	require.Error(t, err)
}

func TestServicesManagement_Create_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not json`))
	}))
	s := client.Management().Services().New("user_service", "User Service")
	err := s.Save(context.Background())
	require.Error(t, err)
}

func TestServicesManagement_Update_BadJSON(t *testing.T) {
	now := time.Now()
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not json`))
	}))
	s := client.Management().Services().New("user_service", "User Service")
	s.CreatedAt = &now
	err := s.Save(context.Background())
	require.Error(t, err)
}

func TestServicesManagement_List_ServerError(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	_, err := client.Management().Services().List(context.Background())
	require.Error(t, err)
}

func TestServicesManagement_Update_ServerError(t *testing.T) {
	now := time.Now()
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	s := client.Management().Services().New("user_service", "User Service")
	s.CreatedAt = &now
	err := s.Save(context.Background())
	require.Error(t, err)
}

func TestContextType_Save_Update(t *testing.T) {
	// Update path: ct already has CreatedAt set → triggers PUT.
	now := time.Now()
	updated := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/v1/context_types/user" {
			updated = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"user","attributes":{"id":"user","name":"User","attributes":{"plan":{}},"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-06-01T00:00:00Z"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	ct := client.Management().ContextTypes().New("user", smplkit.WithContextTypeName("User"))
	ct.CreatedAt = &now
	ct.AddAttribute("plan")
	err := ct.Save(context.Background())
	require.NoError(t, err)
	assert.True(t, updated)
	assert.NotNil(t, ct.UpdatedAt)
}

func TestContextTypesManagement_List_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not json`))
	}))
	_, err := client.Management().ContextTypes().List(context.Background())
	require.Error(t, err)
}

func TestContextTypesManagement_Get_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not json`))
	}))
	_, err := client.Management().ContextTypes().Get(context.Background(), "user")
	require.Error(t, err)
}

func TestContextTypesManagement_Create_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not json`))
	}))
	ct := client.Management().ContextTypes().New("user")
	err := ct.Save(context.Background())
	require.Error(t, err)
}

func TestContextTypesManagement_Update_Error(t *testing.T) {
	now := time.Now()
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	ct := client.Management().ContextTypes().New("user")
	ct.CreatedAt = &now
	err := ct.Save(context.Background())
	require.Error(t, err)
}

func TestContextTypesManagement_Update_BadJSON(t *testing.T) {
	now := time.Now()
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not json`))
	}))
	ct := client.Management().ContextTypes().New("user")
	ct.CreatedAt = &now
	err := ct.Save(context.Background())
	require.Error(t, err)
}

func TestContextTypesManagement_List_NonDictAttribute(t *testing.T) {
	// When an attribute value is not a map, resourceToContextType should default to {}.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"user","attributes":{"id":"user","name":"User","attributes":{"plan":"string_value"}}}]}`))
	})
	client := newManagementTestClient(t, handler)

	types, err := client.Management().ContextTypes().List(context.Background())
	require.NoError(t, err)
	require.Len(t, types, 1)
	assert.Equal(t, map[string]interface{}{}, types[0].Attributes["plan"])
}

func TestContextsManagement_List_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not json`))
	}))
	_, err := client.Management().Contexts().List(context.Background(), "user")
	require.Error(t, err)
}

func TestContextsManagement_List_ParseEntityError(t *testing.T) {
	// Data item is not valid JSON for a context entity.
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":["not an object"]}`))
	}))
	_, err := client.Management().Contexts().List(context.Background(), "user")
	require.Error(t, err)
}

func TestContextsManagement_List_EntityNoColon(t *testing.T) {
	// Entity ID without colon separator — ContextType is the whole ID, Key is empty.
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"nocolon","attributes":{"attributes":{}}}]}`))
	}))
	entities, err := client.Management().Contexts().List(context.Background(), "user")
	require.NoError(t, err)
	require.Len(t, entities, 1)
	assert.Equal(t, "nocolon", entities[0].ContextType)
	assert.Equal(t, "", entities[0].Key)
}

func TestContextsManagement_Get_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not json`))
	}))
	_, err := client.Management().Contexts().Get(context.Background(), "user:u1")
	require.Error(t, err)
}

func TestContextsManagement_Get_ParseEntityError(t *testing.T) {
	// data field is a string, not an object.
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"not an object"}`))
	}))
	_, err := client.Management().Contexts().Get(context.Background(), "user:u1")
	require.Error(t, err)
}

func TestContextsManagement_Get_ZeroArgs(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	_, err := client.Management().Contexts().Get(context.Background())
	require.Error(t, err)
}

func TestContextsManagement_Get_EmptyCompositeID(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	_, err := client.Management().Contexts().Get(context.Background(), "")
	require.Error(t, err)
}

func TestContextsManagement_Get_EmptyKey(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	_, err := client.Management().Contexts().Get(context.Background(), "user", "")
	require.Error(t, err)
}

func TestContextsManagement_Delete_ZeroArgs(t *testing.T) {
	client := newManagementTestClient(t, http.NotFoundHandler())
	err := client.Management().Contexts().Delete(context.Background())
	require.Error(t, err)
}

func TestContextsManagement_Flush_Error(t *testing.T) {
	// Register then flush to a server returning 500 — should propagate error.
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"flush failed"}]}`))
	}))

	err := client.Management().Contexts().Register(context.Background(),
		[]smplkit.Context{smplkit.NewContext("user", "u1", nil)},
	)
	require.NoError(t, err)
	err = client.Management().Contexts().Flush(context.Background())
	require.Error(t, err)
}

func TestAccountSettings_Get_BadJSON(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not json`))
	}))
	_, err := client.Management().AccountSettings().Get(context.Background())
	require.Error(t, err)
}

func TestAccountSettings_Get_Error(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"unauthorized"}]}`))
	}))
	_, err := client.Management().AccountSettings().Get(context.Background())
	require.Error(t, err)
}

func TestAccountSettings_Save_Error(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"environment_order":["production"]}`))
		case r.Method == "PUT":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := newManagementTestClient(t, handler)

	s, err := client.Management().AccountSettings().Get(context.Background())
	require.NoError(t, err)
	err = s.Save(context.Background())
	require.Error(t, err)
}

func TestAccountSettings_Save_BadJSON(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"environment_order":["production"]}`))
		case r.Method == "PUT":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{not json`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := newManagementTestClient(t, handler)

	s, err := client.Management().AccountSettings().Get(context.Background())
	require.NoError(t, err)
	err = s.Save(context.Background())
	require.Error(t, err)
}

func TestAccountSettings_SetEnvironmentOrder_NilRaw(t *testing.T) {
	s := &smplkit.AccountSettings{} // Raw is nil
	s.SetEnvironmentOrder([]string{"production", "staging"})
	assert.Equal(t, []string{"production", "staging"}, s.EnvironmentOrder())
}

func TestAccountSettings_EnvironmentOrder_StringSlice(t *testing.T) {
	// SetEnvironmentOrder stores []interface{}; verify []string case also works
	// by directly constructing a Raw with a []string value.
	s := &smplkit.AccountSettings{
		Raw: map[string]interface{}{
			"environment_order": []string{"production", "staging"},
		},
	}
	assert.Equal(t, []string{"production", "staging"}, s.EnvironmentOrder())
}

func TestAccountSettings_EnvironmentOrder_UnknownType(t *testing.T) {
	s := &smplkit.AccountSettings{
		Raw: map[string]interface{}{
			"environment_order": 42, // unknown type
		},
	}
	assert.Nil(t, s.EnvironmentOrder())
}

func TestContextType_AddAttribute_NilMeta(t *testing.T) {
	ct := &smplkit.ContextType{}
	ct.AddAttribute("plan", nil) // explicit nil meta → else branch
	require.NotNil(t, ct.Attributes)
	assert.Equal(t, map[string]interface{}{}, ct.Attributes["plan"])
}

func TestContextType_UpdateAttribute_NilExistingAttrs(t *testing.T) {
	ct := &smplkit.ContextType{} // Attributes is nil
	ct.UpdateAttribute("plan", map[string]interface{}{"type": "string"})
	require.NotNil(t, ct.Attributes)
	assert.Equal(t, map[string]interface{}{"type": "string"}, ct.Attributes["plan"])
}

func TestContextType_Save_Update_HasUpdatedAt(t *testing.T) {
	// Response includes updated_at — verify parseContextTypeRaw parses it.
	now := time.Now()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"user","attributes":{"id":"user","name":"User","attributes":{},"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-06-15T12:00:00Z"}}}`))
	})
	client := newManagementTestClient(t, handler)

	ct := client.Management().ContextTypes().New("user")
	ct.CreatedAt = &now
	err := ct.Save(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, ct.UpdatedAt)
}

func TestFlagsListContextTypes_NonDictAttribute(t *testing.T) {
	// ListContextTypes on the flags client also has the non-dict attribute path.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/context_types" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"user","attributes":{"id":"user","name":"User","attributes":{"plan":"string_value"}}}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	types, err := client.Flags().Management().ListContextTypes(context.Background())
	require.NoError(t, err)
	require.Len(t, types, 1)
	assert.Equal(t, map[string]interface{}{}, types[0].Attributes["plan"])
}

func TestParseContextTypeRaw_UpdatedAt(t *testing.T) {
	// parseContextTypeRaw is called via Get — verify updated_at parsing branch.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/context_types/user" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"user","attributes":{"id":"user","name":"User","attributes":{},"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-06-15T12:00:00Z"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	ct, err := client.Management().ContextTypes().Get(context.Background(), "user")
	require.NoError(t, err)
	assert.NotNil(t, ct.UpdatedAt)
}

func TestContextsManagement_Register_WithFlush_Error(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"flush error"}]}`))
	}))

	err := client.Management().Contexts().Register(context.Background(),
		[]smplkit.Context{smplkit.NewContext("user", "u1", nil)},
		smplkit.WithContextFlush(),
	)
	require.Error(t, err)
}

// ── checkStatus error paths ───────────────────────────────────────────────────
// These tests cover the `if err := checkStatus(...); err != nil { return err }`
// branches by having the server return a non-2xx status for each method.

func TestEnvironmentsManagement_List_Error(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	_, err := client.Management().Environments().List(context.Background())
	require.Error(t, err)
}

func TestEnvironmentsManagement_Create_Error(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"invalid"}]}`))
	}))
	e := client.Management().Environments().New("bad", "Bad")
	err := e.Save(context.Background())
	require.Error(t, err)
}

func TestContextTypesManagement_List_Error(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	_, err := client.Management().ContextTypes().List(context.Background())
	require.Error(t, err)
}

func TestContextTypesManagement_Get_Error(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))
	_, err := client.Management().ContextTypes().Get(context.Background(), "nonexistent")
	require.Error(t, err)
}

func TestContextTypesManagement_Create_Error(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"invalid"}]}`))
	}))
	ct := client.Management().ContextTypes().New("bad")
	err := ct.Save(context.Background())
	require.Error(t, err)
}

func TestContextsManagement_List_Error(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	_, err := client.Management().Contexts().List(context.Background(), "user")
	require.Error(t, err)
}

func TestContextsManagement_Get_Error(t *testing.T) {
	client := newManagementTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))
	_, err := client.Management().Contexts().Get(context.Background(), "user:u1")
	require.Error(t, err)
}

// ── Flags ListContextTypes: dict-value attribute branch ───────────────────────

func TestFlagsManagement_ListContextTypes_DictAttribute(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/context_types" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"user","attributes":{"id":"user","name":"User","attributes":{"plan":{"type":"string"}}}}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newManagementTestClient(t, handler)

	types, err := client.Flags().Management().ListContextTypes(context.Background())
	require.NoError(t, err)
	require.Len(t, types, 1)
	assert.Equal(t, map[string]interface{}{"type": "string"}, types[0].Attributes["plan"])
}
