package smplkit

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	cfg := &ConfigEntry{client: c.config}
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
	cfg := &ConfigEntry{ID: "showcase-x", client: c.config}
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
	f := &Flag{ID: "showcase-flag", client: c.flags}
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
	l := &Logger{ID: "showcase.logger", client: c.logging}
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
