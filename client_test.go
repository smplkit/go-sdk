package smplkit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// waitReadyServer serves empty config/flag lists so WaitUntilReady's eager
// config+flags connect succeeds, leaving only the WebSocket handshake (which a
// plain httptest server rejects) to drive the readiness wait.
func waitReadyServer(t *testing.T) *httptest.Server {
	t.Helper()
	h := http.NewServeMux()
	empty := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}
	h.HandleFunc("/api/v1/configs", empty)
	h.HandleFunc("/api/v1/flags", empty)
	h.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_WaitUntilReady_TimeoutWhenWSNeverConnects(t *testing.T) {
	// config + flags connect successfully (empty lists), so the only thing
	// left is the WebSocket handshake — which a plain httptest server rejects,
	// so the WS dial loop never reaches "connected" and WaitUntilReady must
	// return a timeout error rather than blocking forever.
	srv := waitReadyServer(t)
	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL(srv.URL),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	err = client.WaitUntilReady(context.Background(), 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestClient_WaitUntilReady_EagerConnectError(t *testing.T) {
	// Point at a closed port: WaitUntilReady eagerly connects flags + config
	// before waiting on the socket (matching Python's wait_until_ready), so the
	// connection failure surfaces rather than blocking forever.
	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://127.0.0.1:1"),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	err = client.WaitUntilReady(context.Background(), 50*time.Millisecond)
	require.Error(t, err)
}

func TestClient_WaitUntilReady_ZeroTimeoutUsesDefault(t *testing.T) {
	// timeout=0 falls back to the SDK default; the eager flags/config connect
	// against an unreachable host returns the failure without waiting it out.
	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL("http://127.0.0.1:1"),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	err = client.WaitUntilReady(context.Background(), 0)
	require.Error(t, err)
}

func TestClient_WaitUntilReady_ConfigConnectError(t *testing.T) {
	// flags connects (empty list) but config returns 500 — WaitUntilReady
	// surfaces the config connect failure (it eagerly connects flags first,
	// then config, before the WebSocket wait).
	h := http.NewServeMux()
	h.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	h.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"status":"500","detail":"config down"}]}`))
	})
	h.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL(srv.URL),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	err = client.WaitUntilReady(context.Background(), time.Second)
	require.Error(t, err)
}

func TestNewClient_Defaults(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.Config())
}

func TestNewClient_WithBaseDomain(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", BaseDomain: "custom.example.com", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_WithScheme(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", Scheme: "http", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_WithTimeout(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true}, smplkit.WithTimeout(5*time.Second))
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_WithHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 10 * time.Second}
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true}, smplkit.WithHTTPClient(custom))
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_MultipleOptions(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", BaseDomain: "custom.example.com", Scheme: "https", DisableTelemetry: true},
		smplkit.WithTimeout(10*time.Second))
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestClient_ConfigReturnsSubClient(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	cfg := client.Config()
	require.NotNil(t, cfg)
	// Calling Config() multiple times returns the same sub-client.
	assert.Same(t, cfg, client.Config())
}

func TestNewClient_EnvVar(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_api_env")
	client, err := smplkit.NewClient(smplkit.Config{Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_ConfigFile(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\napi_key = sk_api_file\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	client, err := smplkit.NewClient(smplkit.Config{Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_ConfigFileProfileSection(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[myprofile]\napi_key = sk_api_prof\n[default]\napi_key = sk_api_default\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	client, err := smplkit.NewClient(smplkit.Config{Profile: "myprofile", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_ConfigFileFallsBackToDefault(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\napi_key = sk_api_default\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	// No matching profile section — should fall back to [default].
	client, err := smplkit.NewClient(smplkit.Config{Environment: "staging", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_ErrorWhenNoKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir())

	client, err := smplkit.NewClient(smplkit.Config{Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.Error(t, err)
	require.Nil(t, client)

	var smplErr *smplkit.Error
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No API key provided")
	assert.Contains(t, smplErr.Message, "SMPLKIT_API_KEY")
	assert.Contains(t, smplErr.Message, "~/.smplkit")
}

func TestNewClient_ErrorWhenNoKey_ShowsProfileInSection(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir())

	_, err := smplkit.NewClient(smplkit.Config{Environment: "production", Service: "test-service", DisableTelemetry: true})
	require.Error(t, err)

	var smplErr *smplkit.Error
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "[default]")
}

func TestNewClient_ExplicitTakesPrecedence(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_api_env")
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_api_explicit", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_EnvTakesPrecedenceOverFile(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_api_env")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\napi_key = sk_api_file\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	client, err := smplkit.NewClient(smplkit.Config{Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_EmptyEnvTreatedAsUnset(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\napi_key = sk_api_file\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	client, err := smplkit.NewClient(smplkit.Config{Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_MissingFileSkipped(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir()) // No .smplkit file in temp dir.

	_, err := smplkit.NewClient(smplkit.Config{Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.Error(t, err)
	// Should fail with "no API key" since the file doesn't exist.
	var smplErr *smplkit.Error
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No API key provided")
}

func TestNewClient_ConfigFileNoApiKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\nother_key = value\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	_, err = smplkit.NewClient(smplkit.Config{Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.Error(t, err)
}

func TestNewClient_CommentsIgnored(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("# comment\n[default]\n# another comment\n; semicolon comment\napi_key = sk_api_comment\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	client, err := smplkit.NewClient(smplkit.Config{Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_MissingProfile(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[staging]\napi_key = sk_api_staging\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	// Named profile "myprofile" is missing and file has other non-common sections.
	_, err = smplkit.NewClient(smplkit.Config{Profile: "myprofile", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.Error(t, err)
	var smplErr *smplkit.Error
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "Profile [myprofile] not found")
}

func TestNewClient_DefaultSectionWithoutApiKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\nsome_other = value\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	_, err = smplkit.NewClient(smplkit.Config{Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.Error(t, err)
}

func TestNewClient_EnvironmentOptional(t *testing.T) {
	// Environment is optional — the top-level client constructs with just an
	// api key and the server derives the environment from it.
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("HOME", t.TempDir())
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "", client.Environment())
}

func TestNewClient_EnvironmentFromEnvVar(t *testing.T) {
	t.Setenv("SMPLKIT_ENVIRONMENT", "staging")
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_ServiceParam(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "my-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestClient_Environment(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "staging", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	assert.Equal(t, "staging", client.Environment())
}

func TestClient_Service(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "api-service", DisableTelemetry: true})
	require.NoError(t, err)
	assert.Equal(t, "api-service", client.Service())
}

func TestNewClient_ServiceOptional(t *testing.T) {
	// Service is optional — an audit/jobs-only customer supplies neither
	// environment nor service.
	t.Setenv("SMPLKIT_SERVICE", "")
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "", client.Service())
}

func TestClient_ServiceFromEnvVar(t *testing.T) {
	t.Setenv("SMPLKIT_SERVICE", "env-service")
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", DisableTelemetry: true})
	require.NoError(t, err)
	assert.Equal(t, "env-service", client.Service())
}

func TestNewClient_ServiceExplicitTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv("SMPLKIT_SERVICE", "env-service")
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "explicit-service", DisableTelemetry: true})
	require.NoError(t, err)
	assert.Equal(t, "explicit-service", client.Service())
}

func TestNewClient_EnvAndServiceBothOptional(t *testing.T) {
	// With only an api key, the top-level client constructs and both
	// environment and service resolve empty (derived server-side).
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("HOME", t.TempDir())
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "", client.Environment())
	assert.Equal(t, "", client.Service())
}

func TestNewClient_MissingAPIKeyError(t *testing.T) {
	// The api key remains the only required field; clear every source of one.
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("HOME", t.TempDir())
	_, err := smplkit.NewClient(smplkit.Config{Environment: "test", DisableTelemetry: true})
	require.Error(t, err)
	var smplErr *smplkit.Error
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No API key provided")
}

func TestNewClient_DebugFieldEnablesDebugOutput(t *testing.T) {
	// Ensure SMPLKIT_DEBUG is unset so we can observe the Config.Debug field alone.
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Cleanup(func() { smplkit.SetDebugEnabled(false) })

	client, err := smplkit.NewClient(smplkit.Config{
		APIKey:           "sk_test_key",
		Environment:      "test",
		Service:          "test-service",
		Debug:            true,
		DisableTelemetry: true,
	})
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.True(t, smplkit.IsDebugEnabled(), "debug should be enabled when Config.Debug=true")
}

func TestNewClient_DebugFromConfigFile(t *testing.T) {
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Cleanup(func() { smplkit.SetDebugEnabled(false) })
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\napi_key = sk_api_file\ndebug = true\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	client, err := smplkit.NewClient(smplkit.Config{Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.True(t, smplkit.IsDebugEnabled(), "debug should be enabled when debug=true is set in config file")
}

func TestNewClient_ExtraHeaders_PresentOnRequests(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client, err := smplkit.NewClient(
		smplkit.Config{
			APIKey:           "sk_test_key",
			Environment:      "test",
			Service:          "test-service",
			DisableTelemetry: true,
			ExtraHeaders:     map[string]string{"X-Custom": "hello"},
		},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, _ = client.Config().List(context.Background())

	require.NotNil(t, seen)
	assert.Equal(t, "hello", seen.Get("X-Custom"))
	assert.Equal(t, "Bearer sk_test_key", seen.Get("Authorization"))
	assert.Equal(t, "application/vnd.api+json", seen.Get("Accept"))
}

func TestNewClient_ExtraHeaders_SDKHeadersWinOnCollision(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client, err := smplkit.NewClient(
		smplkit.Config{
			APIKey:           "sk_test_key",
			Environment:      "test",
			Service:          "test-service",
			DisableTelemetry: true,
			ExtraHeaders: map[string]string{
				"Authorization": "Bearer overridden",
				"Accept":        "text/plain",
			},
		},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, _ = client.Config().List(context.Background())

	require.NotNil(t, seen)
	assert.Equal(t, "Bearer sk_test_key", seen.Get("Authorization"))
	assert.Equal(t, "application/vnd.api+json", seen.Get("Accept"))
}
