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
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.Config())
}

func TestNewClient_WithBaseURL(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithBaseURL("https://custom.example.com"), smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_WithTimeout(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithTimeout(5*time.Second), smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_WithHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 10 * time.Second}
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.WithHTTPClient(custom), smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_MultipleOptions(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service",
		smplkit.WithBaseURL("https://custom.example.com"),
		smplkit.WithTimeout(10*time.Second), smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestClient_ConfigReturnsSubClient(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	cfg := client.Config()
	require.NotNil(t, cfg)
	// Calling Config() multiple times returns the same sub-client.
	assert.Same(t, cfg, client.Config())
}

func TestNewClient_EnvVar(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_api_env")
	client, err := smplkit.NewClient("", "test", "test-service", smplkit.DisableTelemetry())
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

	client, err := smplkit.NewClient("", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_ConfigFileEnvironmentSection(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[production]\napi_key = sk_api_prod\n[default]\napi_key = sk_api_default\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	client, err := smplkit.NewClient("", "production", "test-service", smplkit.DisableTelemetry())
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

	// Environment is "staging" but only [default] section exists — should fall back.
	client, err := smplkit.NewClient("", "staging", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_ConfigFileEnvironmentSectionTakesPrecedence(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	// Both sections present; environment section should win.
	err := os.WriteFile(configPath, []byte("[staging]\napi_key = sk_api_staging\n[default]\napi_key = sk_api_default\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	client, err := smplkit.NewClient("", "staging", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_ErrorWhenNoKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir())

	client, err := smplkit.NewClient("", "test", "test-service", smplkit.DisableTelemetry())
	require.Error(t, err)
	require.Nil(t, client)

	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No API key provided")
	assert.Contains(t, smplErr.Message, "SMPLKIT_API_KEY")
	assert.Contains(t, smplErr.Message, "~/.smplkit")
}

func TestNewClient_ErrorWhenNoKey_ShowsEnvironmentInSection(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir())

	_, err := smplkit.NewClient("", "production", "test-service", smplkit.DisableTelemetry())
	require.Error(t, err)

	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "[production]")
}

func TestNewClient_ExplicitTakesPrecedence(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_api_env")
	client, err := smplkit.NewClient("sk_api_explicit", "test", "test-service", smplkit.DisableTelemetry())
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

	client, err := smplkit.NewClient("", "test", "test-service", smplkit.DisableTelemetry())
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

	client, err := smplkit.NewClient("", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_MalformedConfigFile(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("this is not valid ini"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	_, err = smplkit.NewClient("", "test", "test-service", smplkit.DisableTelemetry())
	require.Error(t, err)
}

func TestNewClient_ConfigFileNoApiKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\nother_key = value\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	_, err = smplkit.NewClient("", "test", "test-service", smplkit.DisableTelemetry())
	require.Error(t, err)
}

func TestNewClient_CommentsIgnored(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("# comment\n[default]\n# another comment\napi_key = sk_api_comment\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	client, err := smplkit.NewClient("", "test", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_MissingDefaultSection(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[staging]\napi_key = sk_api_staging\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	// Environment is "test", file has [staging] only — no match and no [default].
	_, err = smplkit.NewClient("", "test", "test-service", smplkit.DisableTelemetry())
	require.Error(t, err)
}

func TestNewClient_DefaultSectionWithoutApiKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\nsome_other = value\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	_, err = smplkit.NewClient("", "test", "test-service", smplkit.DisableTelemetry())
	require.Error(t, err)
}

func TestNewClient_MissingEnvironment(t *testing.T) {
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	_, err := smplkit.NewClient("sk_test_key", "", "", smplkit.DisableTelemetry())
	require.Error(t, err)
	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No environment provided")
	assert.NotContains(t, smplErr.Message, "~/.smplkit")
}

func TestNewClient_EnvironmentFromEnvVar(t *testing.T) {
	t.Setenv("SMPLKIT_ENVIRONMENT", "staging")
	client, err := smplkit.NewClient("sk_test_key", "", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_ServiceParam(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "my-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestClient_Environment(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "staging", "test-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	assert.Equal(t, "staging", client.Environment())
}

func TestClient_Service(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", "test", "api-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	assert.Equal(t, "api-service", client.Service())
}

func TestNewClient_MissingService(t *testing.T) {
	t.Setenv("SMPLKIT_SERVICE", "")
	_, err := smplkit.NewClient("sk_test_key", "test", "", smplkit.DisableTelemetry())
	require.Error(t, err)
	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No service provided")
	assert.Contains(t, smplErr.Message, "Pass service to NewClient")
	assert.Contains(t, smplErr.Message, "SMPLKIT_SERVICE")
}

func TestClient_ServiceFromEnvVar(t *testing.T) {
	t.Setenv("SMPLKIT_SERVICE", "env-service")
	client, err := smplkit.NewClient("sk_test_key", "test", "", smplkit.DisableTelemetry())
	require.NoError(t, err)
	assert.Equal(t, "env-service", client.Service())
}

func TestNewClient_ServiceExplicitTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv("SMPLKIT_SERVICE", "env-service")
	client, err := smplkit.NewClient("sk_test_key", "test", "explicit-service", smplkit.DisableTelemetry())
	require.NoError(t, err)
	assert.Equal(t, "explicit-service", client.Service())
}

func TestNewClient_ResolutionOrder_EnvironmentBeforeService(t *testing.T) {
	// If environment is missing, error should mention environment, not service.
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	_, err := smplkit.NewClient("sk_test_key", "", "", smplkit.DisableTelemetry())
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
	_, err := smplkit.NewClient("", "test", "", smplkit.DisableTelemetry())
	require.Error(t, err)
	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No service provided")
}
