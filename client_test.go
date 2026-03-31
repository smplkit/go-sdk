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
	client, err := smplkit.NewClient("sk_test_key")
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.Config())
}

func TestNewClient_WithBaseURL(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL("https://custom.example.com"))
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_WithTimeout(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key", smplkit.WithTimeout(5*time.Second))
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_WithHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 10 * time.Second}
	client, err := smplkit.NewClient("sk_test_key", smplkit.WithHTTPClient(custom))
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_MultipleOptions(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key",
		smplkit.WithBaseURL("https://custom.example.com"),
		smplkit.WithTimeout(10*time.Second),
	)
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestClient_ConfigReturnsSubClient(t *testing.T) {
	client, err := smplkit.NewClient("sk_test_key")
	require.NoError(t, err)
	cfg := client.Config()
	require.NotNil(t, cfg)
	// Calling Config() multiple times returns the same sub-client.
	assert.Same(t, cfg, client.Config())
}

func TestNewClient_EnvVar(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_api_env")
	client, err := smplkit.NewClient("")
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

	client, err := smplkit.NewClient("")
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewClient_ErrorWhenNoKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir())

	client, err := smplkit.NewClient("")
	require.Error(t, err)
	require.Nil(t, client)

	var smplErr *smplkit.SmplError
	require.True(t, errors.As(err, &smplErr))
	assert.Contains(t, smplErr.Message, "No API key provided")
	assert.Contains(t, smplErr.Message, "SMPLKIT_API_KEY")
	assert.Contains(t, smplErr.Message, "~/.smplkit")
}

func TestNewClient_ExplicitTakesPrecedence(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_api_env")
	client, err := smplkit.NewClient("sk_api_explicit")
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

	client, err := smplkit.NewClient("")
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

	client, err := smplkit.NewClient("")
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

	_, err = smplkit.NewClient("")
	require.Error(t, err)
}

func TestNewClient_ConfigFileNoApiKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\nother_key = value\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	_, err = smplkit.NewClient("")
	require.Error(t, err)
}

func TestNewClient_CommentsIgnored(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("# comment\n[default]\n# another comment\napi_key = sk_api_comment\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	client, err := smplkit.NewClient("")
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

	_, err = smplkit.NewClient("")
	require.Error(t, err)
}

func TestNewClient_DefaultSectionWithoutApiKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".smplkit")
	err := os.WriteFile(configPath, []byte("[default]\nsome_other = value\n"), 0o600)
	require.NoError(t, err)
	t.Setenv("HOME", dir)

	_, err = smplkit.NewClient("")
	require.Error(t, err)
}
