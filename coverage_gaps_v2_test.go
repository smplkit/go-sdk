package smplkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Active-record Delete on the new models ──────────────────────────────

func TestConfigEntry_Delete_NoClient(t *testing.T) {
	cfg := &ConfigEntry{ID: "x"}
	err := cfg.Delete(context.Background())
	require.Error(t, err)
}

func TestConfigEntry_Delete_NoID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(server.Close)
	c, err := NewClient(Config{APIKey: "k", Environment: "e", Service: "s"}, WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg := &ConfigEntry{client: c.config.Management()}
	err = cfg.Delete(context.Background())
	require.Error(t, err)
}

func TestConfigEntry_Delete_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	c, err := NewClient(Config{APIKey: "k", Environment: "e", Service: "s"}, WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg := &ConfigEntry{ID: "showcase-x", client: c.config.Management()}
	require.NoError(t, cfg.Delete(context.Background()))
}

func TestFlag_Delete_NoClient(t *testing.T) {
	f := &Flag{ID: "x"}
	require.Error(t, f.Delete(context.Background()))
}

func TestFlag_Delete_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	c, err := NewClient(Config{APIKey: "k", Environment: "e", Service: "s"}, WithBaseURL(server.URL))
	require.NoError(t, err)
	f := &Flag{ID: "showcase-flag", client: c.flags.Management()}
	require.NoError(t, f.Delete(context.Background()))
}

func TestLogger_Delete_NoClient(t *testing.T) {
	l := &Logger{ID: "x"}
	require.Error(t, l.Delete(context.Background()))
}

func TestLogger_Delete_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	c, err := NewClient(Config{APIKey: "k", Environment: "e", Service: "s"}, WithBaseURL(server.URL))
	require.NoError(t, err)
	l := &Logger{ID: "showcase.logger", client: c.logging.Management()}
	require.NoError(t, l.Delete(context.Background()))
}

// ── LiveConfig listener sugar ───────────────────────────────────────────

func TestLiveConfig_OnChange_GlobalAndKey_Registration(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{
			"app": {"host": "localhost"},
		},
	}
	lc := &LiveConfig{client: cc, id: "app"}

	// Registering both forms must not panic and must funnel through the
	// underlying ConfigClient.OnChange (with WithConfigID + WithItemKey).
	lc.OnChange(func(*ConfigChangeEvent) {})
	lc.OnChangeKey("host", func(*ConfigChangeEvent) {})

	// 2 listeners on the underlying client.
	cc.listenersMu.Lock()
	defer cc.listenersMu.Unlock()
	assert.Len(t, cc.listeners, 2)
}

// ── LoggingClient.Install vs Start (deprecated) ─────────────────────────

func TestLoggingClient_Install_RoutesThroughStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reflect every request as a 200/empty so Install can complete.
		switch r.Method {
		case "GET":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(server.Close)
	c, err := NewClient(
		Config{APIKey: "k", Environment: "e", Service: "s", DisableTelemetry: true},
		WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	// Install should be safe to call without any registered adapters.
	require.NoError(t, c.Logging().Install(context.Background()))

	// Calling again is a no-op (Install is idempotent via sync.Once).
	require.NoError(t, c.Logging().Install(context.Background()))
}

// ── Deprecated Logger base-level helpers (kept for migration) ────────────

func TestLogger_SetBaseLevel_ClearBaseLevel(t *testing.T) {
	l := &Logger{}
	l.SetBaseLevel(LogLevelInfo)
	require.NotNil(t, l.Level)
	assert.Equal(t, LogLevelInfo, *l.Level)
	l.ClearBaseLevel()
	assert.Nil(t, l.Level)
}

// ── per_env_verbs.fmtEqual edge cases (both-nil branch) ─────────────────

func TestFlag_RemoveValue_BothNilBranch(t *testing.T) {
	f := &Flag{}
	f.AddValue("Nil", nil).AddValue("Other", "x")
	f.RemoveValue(nil)
	assert.Len(t, *f.Values, 1)
	assert.Equal(t, "x", (*f.Values)[0].Value)
}

// ── EnableRules edge case: env data isn't a map ─────────────────────────

func TestFlag_EnableRules_HandlesUntypedEnvEntry(t *testing.T) {
	// A defensively-set env entry with a non-map value should be ignored
	// without panicking. This exercises the fallthrough branch of the
	// loop in EnableRules.
	f := &Flag{
		Environments: map[string]interface{}{
			"production": "not-a-map",
			"staging":    map[string]interface{}{"enabled": false},
		},
	}
	f.EnableRules("")
	staging := f.Environments["staging"].(map[string]interface{})
	assert.Equal(t, true, staging["enabled"])
	// production was left as-is because it wasn't a map
	assert.Equal(t, "not-a-map", f.Environments["production"])
}

// EnableRules with a specific environment exercises the non-empty branch
// (delegates to SetEnvironmentEnabled).
func TestFlag_EnableRules_OneEnv(t *testing.T) {
	f := &Flag{Environments: map[string]interface{}{
		"production": map[string]interface{}{"enabled": false},
	}}
	f.EnableRules("production")
	prod := f.Environments["production"].(map[string]interface{})
	assert.Equal(t, true, prod["enabled"])
}

// Save() with a nil client must surface a clear error rather than
// nil-deref'ing — covers the explicit guard in each model.
func TestSave_NoClientGuards(t *testing.T) {
	cfg := &ConfigEntry{ID: "x"}
	require.Error(t, cfg.Save(context.Background()))

	f := &Flag{ID: "x"}
	require.Error(t, f.Save(context.Background()))

	l := &Logger{ID: "x"}
	require.Error(t, l.Save(context.Background()))
}

func TestContextEntity_SaveDelete_NoClientGuards(t *testing.T) {
	ce := &ContextEntity{ContextType: "user", Key: "u-1"}
	require.Error(t, ce.Save(context.Background()))
	require.Error(t, ce.Delete(context.Background()))
}

func TestConfigClient_Snapshot_MissingID(t *testing.T) {
	cc := &ConfigClient{
		client:      &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{},
	}
	cc.initOnce.Do(func() {})
	v, err := cc.Snapshot(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, v)
}

func TestConfigClient_Snapshot_RecordsMetricsWhenEnabled(t *testing.T) {
	r := newMetricsReporter(&http.Client{}, "http://example.test", "test", "svc", 0)
	defer r.Close()
	cc := &ConfigClient{
		client: &Client{environment: "test", metrics: r},
		configCache: map[string]map[string]interface{}{
			"app": {"host": "localhost"},
		},
	}
	cc.initOnce.Do(func() {})
	v, err := cc.Snapshot(context.Background(), "app")
	require.NoError(t, err)
	assert.Equal(t, "localhost", v["host"])
}

// Hits saveEntity's network-error path so the classifyError + ReadAll
// branches both fire.
func TestContextEntity_Save_NetworkError(t *testing.T) {
	c, err := NewClient(
		Config{APIKey: "k", Environment: "e", Service: "s"},
		WithBaseURL("http://127.0.0.1:1"), // closed port — connect error
	)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	ce := &ContextEntity{
		ContextType: "user",
		Key:         "u-1",
		Attributes:  map[string]interface{}{},
		client:      c.Manage().Contexts(),
	}
	require.Error(t, ce.Save(context.Background()))
}

// Exercises the non-map-rule fall-through in TypedEnvironments — a rule
// entry that isn't a map[string]interface{} must be skipped without
// panicking.
func TestFlag_TypedEnvironments_NonMapRule(t *testing.T) {
	f := &Flag{
		Environments: map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"rules":   []interface{}{"not-a-map", map[string]interface{}{"value": "x"}},
			},
		},
	}
	typed := f.TypedEnvironments()
	prod := typed["production"]
	rules := prod.Rules()
	require.Len(t, rules, 1)
	assert.Equal(t, "x", rules[0].Value())
}

func TestContextEntity_Save_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	c, err := NewClient(Config{APIKey: "k", Environment: "e", Service: "s"}, WithBaseURL(server.URL))
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	name := "Alice"
	ce := &ContextEntity{
		ContextType: "user",
		Key:         "u-1",
		Name:        &name,
		Attributes:  map[string]interface{}{"plan": "ent"},
		client:      c.Manage().Contexts(),
	}
	require.NoError(t, ce.Save(context.Background()))
	require.NoError(t, ce.Delete(context.Background()))
}

func TestContextsManagement_Get_WiresClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"user:u-1","type":"context","attributes":{"name":"Alice","attributes":{"plan":"ent"}}}}`))
	}))
	t.Cleanup(server.Close)
	c, err := NewClient(Config{APIKey: "k", Environment: "e", Service: "s"}, WithBaseURL(server.URL))
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	ce, err := c.Manage().Contexts().Get(context.Background(), "user", "u-1")
	require.NoError(t, err)
	assert.Equal(t, "user", ce.ContextType)
	assert.Equal(t, "u-1", ce.Key)
}

// ── Manage Close (standalone branch) ────────────────────────────────────

func TestManagementClient_Close_Standalone(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_test_xxxxxxxx")
	mgmt, err := NewManagementClient(ManagementConfig{})
	require.NoError(t, err)
	mgmt.standalone = true
	require.NoError(t, mgmt.Close())
}

// ── Manage NewManagementClient: explicit-config branches ─────────────────

func TestNewManagementClient_AllExplicitFields(t *testing.T) {
	mgmt, err := NewManagementClient(ManagementConfig{
		APIKey:     "sk_test_xxxxxxxx",
		Profile:    "default",
		BaseDomain: "smplkit.com",
		Scheme:     "https",
		Debug:      true,
	})
	require.NoError(t, err)
	assert.NotNil(t, mgmt.Contexts())
	require.NoError(t, mgmt.Close())
}

func TestNewManagementClient_BadConfigSurfacesError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	_, err := NewManagementClient(ManagementConfig{Profile: "does-not-exist"})
	require.Error(t, err)
}

// Exercise the "explicit HTTP client with nil Transport" branch in
// NewManagementClient — without it, the auth transport wrap path would
// never see a fresh DefaultTransport substitution.
func TestNewManagementClient_WithExplicitHTTPClient(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_test_xx")
	hc := &http.Client{} // Transport is nil
	mgmt, err := NewManagementClient(ManagementConfig{}, WithHTTPClient(hc))
	require.NoError(t, err)
	require.NotNil(t, mgmt)
	defer func() { _ = mgmt.Close() }()
}

// Exercise the headerEditor closure in NewManagementClient by making an
// actual list call against an httptest server. This covers the closure
// body that only runs when the generated client actually hits the wire.
func TestNewManagementClient_HeaderEditorExercised(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Accept"))
		assert.NotEmpty(t, r.Header.Get("User-Agent"))
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(server.Close)

	mgmt, err := NewManagementClient(ManagementConfig{APIKey: "sk_test"}, WithBaseURL(server.URL))
	require.NoError(t, err)
	defer func() { _ = mgmt.Close() }()

	// Hits the app service via the wrapped transport, exercising headerEditor.
	envs, err := mgmt.Environments().List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, envs)
}

// --- extraHeaders editor closure coverage ---

// TestNewClient_ExtraHeadersEditorsCovered exercises all five per-service
// extra-header editor closures introduced in the ExtraHeaders feature. When
// ExtraHeaders is nil the loop body never runs; this test provides a non-empty
// map and triggers each client so every closure body is hit.
func TestNewClient_ExtraHeadersEditorsCovered(t *testing.T) {
	var mu sync.Mutex
	var seenHeaders []http.Header

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenHeaders = append(seenHeaders, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c, err := NewClient(
		Config{
			APIKey:           "sk_test_key",
			Environment:      "test",
			Service:          "test-svc",
			DisableTelemetry: true,
			ExtraHeaders:     map[string]string{"X-Extra": "1"},
		},
		WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	// app editor
	c.registerServiceContext(context.Background())
	// flags editor
	_, _ = c.flags.Management().List(context.Background())
	// logging editor
	_, _ = c.logging.Management().List(context.Background())
	// audit editor
	_, _ = c.audit.Events().List(context.Background(), ListEventsInput{})

	mu.Lock()
	defer mu.Unlock()
	require.Greater(t, len(seenHeaders), 0, "no requests were made")
	for _, h := range seenHeaders {
		assert.Equal(t, "1", h.Get("X-Extra"), "extra header missing on request to %v", h)
	}
}
