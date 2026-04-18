package smplkit_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
)

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

	var smplErr *smplkit.SmplError
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

	var smplErr *smplkit.SmplError
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
	var smplErr *smplkit.SmplError
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
	var smplErr *smplkit.SmplError
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

func TestNewClient_MissingEnvironment(t *testing.T) {
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	_, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", DisableTelemetry: true})
	require.Error(t, err)
	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No environment provided")
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

func TestNewClient_MissingService(t *testing.T) {
	t.Setenv("SMPLKIT_SERVICE", "")
	_, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", DisableTelemetry: true})
	require.Error(t, err)
	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No service provided")
	assert.Contains(t, smplErr.Message, "Config.Service")
	assert.Contains(t, smplErr.Message, "SMPLKIT_SERVICE")
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

func TestNewClient_ResolutionOrder_EnvironmentBeforeService(t *testing.T) {
	// If environment is missing, error should mention environment, not service.
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	_, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", DisableTelemetry: true})
	require.Error(t, err)
	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No environment provided")
}

func TestNewClient_ResolutionOrder_ServiceBeforeAPIKey(t *testing.T) {
	// If service is missing but environment is present, error should mention service.
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	_, err := smplkit.NewClient(smplkit.Config{Environment: "test", DisableTelemetry: true})
	require.Error(t, err)
	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No service provided")
}
