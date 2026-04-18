package smplkit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// parseINIFile
// ---------------------------------------------------------------------------

func TestParseINIFile_Empty(t *testing.T) {
	result := parseINIFile("")
	assert.Empty(t, result)
}

func TestParseINIFile_SingleSection(t *testing.T) {
	result := parseINIFile("[default]\napi_key = sk_123\nscheme = https\n")
	require.Contains(t, result, "default")
	assert.Equal(t, "sk_123", result["default"]["api_key"])
	assert.Equal(t, "https", result["default"]["scheme"])
}

func TestParseINIFile_MultipleSections(t *testing.T) {
	content := "[common]\nbase_domain = custom.com\n\n[production]\napi_key = sk_prod\n\n[staging]\napi_key = sk_staging\n"
	result := parseINIFile(content)
	require.Len(t, result, 3)
	assert.Equal(t, "custom.com", result["common"]["base_domain"])
	assert.Equal(t, "sk_prod", result["production"]["api_key"])
	assert.Equal(t, "sk_staging", result["staging"]["api_key"])
}

func TestParseINIFile_CommentsIgnored(t *testing.T) {
	content := "# top comment\n[default]\n# inline comment\n; semicolon comment\napi_key = sk_123\n"
	result := parseINIFile(content)
	require.Contains(t, result, "default")
	assert.Equal(t, "sk_123", result["default"]["api_key"])
}

func TestParseINIFile_SpacesAroundEquals(t *testing.T) {
	content := "[default]\n  api_key  =  sk_spaced  \n"
	result := parseINIFile(content)
	assert.Equal(t, "sk_spaced", result["default"]["api_key"])
}

func TestParseINIFile_LinesBeforeSectionIgnored(t *testing.T) {
	content := "orphan_key = value\n[default]\napi_key = sk_123\n"
	result := parseINIFile(content)
	// orphan line should be ignored since it's not in a section
	require.Contains(t, result, "default")
	assert.Equal(t, "sk_123", result["default"]["api_key"])
}

func TestParseINIFile_CaseSensitive(t *testing.T) {
	content := "[Default]\nApi_Key = sk_123\n"
	result := parseINIFile(content)
	require.Contains(t, result, "Default")
	assert.Equal(t, "sk_123", result["Default"]["Api_Key"])
	assert.NotContains(t, result, "default")
}

// ---------------------------------------------------------------------------
// parseBool
// ---------------------------------------------------------------------------

func TestParseBool_TrueValues(t *testing.T) {
	for _, v := range []string{"true", "True", "TRUE", "1", "yes", "Yes", "YES"} {
		result, err := parseBool(v, "test")
		require.NoError(t, err, "value: %s", v)
		assert.True(t, result, "value: %s", v)
	}
}

func TestParseBool_FalseValues(t *testing.T) {
	for _, v := range []string{"false", "False", "FALSE", "0", "no", "No", "NO"} {
		result, err := parseBool(v, "test")
		require.NoError(t, err, "value: %s", v)
		assert.False(t, result, "value: %s", v)
	}
}

func TestParseBool_Invalid(t *testing.T) {
	_, err := parseBool("maybe", "debug")
	require.Error(t, err)
	var smplErr *SmplError
	require.ErrorAs(t, err, &smplErr)
	assert.Contains(t, smplErr.Message, "maybe")
	assert.Contains(t, smplErr.Message, "debug")
}

// ---------------------------------------------------------------------------
// resolveConfig — defaults
// ---------------------------------------------------------------------------

func TestResolveConfig_Defaults(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")
	t.Setenv("HOME", t.TempDir())

	rc, err := resolveConfig(Config{
		APIKey:      "sk_test",
		Environment: "test",
		Service:     "my-svc",
	})
	require.NoError(t, err)
	assert.Equal(t, "https", rc.scheme)
	assert.Equal(t, "smplkit.com", rc.baseDomain)
	assert.Equal(t, "default", rc.profile)
	assert.False(t, rc.debug)
	assert.False(t, rc.disableTelemetry)
}

// ---------------------------------------------------------------------------
// resolveConfig — file layer
// ---------------------------------------------------------------------------

func TestResolveConfig_FileDefaultProfile(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, dir, "[default]\napi_key = sk_file\nenvironment = staging\nservice = file-svc\n")

	rc, err := resolveConfig(Config{})
	require.NoError(t, err)
	assert.Equal(t, "sk_file", rc.apiKey)
	assert.Equal(t, "staging", rc.environment)
	assert.Equal(t, "file-svc", rc.service)
}

func TestResolveConfig_CommonInheritance(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, dir, "[common]\nbase_domain = custom.io\nscheme = http\n\n[default]\napi_key = sk_common\nenvironment = dev\nservice = common-svc\n")

	rc, err := resolveConfig(Config{})
	require.NoError(t, err)
	assert.Equal(t, "custom.io", rc.baseDomain)
	assert.Equal(t, "http", rc.scheme)
	assert.Equal(t, "sk_common", rc.apiKey)
}

func TestResolveConfig_CommonOverriddenByProfile(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, dir, "[common]\nbase_domain = common.io\n\n[myprofile]\napi_key = sk_prof\nenvironment = prod\nservice = prof-svc\nbase_domain = profile.io\n")

	rc, err := resolveConfig(Config{Profile: "myprofile"})
	require.NoError(t, err)
	assert.Equal(t, "profile.io", rc.baseDomain)
	assert.Equal(t, "sk_prof", rc.apiKey)
}

func TestResolveConfig_ProfileFromEnvVar(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "envprofile")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, dir, "[envprofile]\napi_key = sk_envprof\nenvironment = test\nservice = env-svc\n")

	rc, err := resolveConfig(Config{})
	require.NoError(t, err)
	assert.Equal(t, "envprofile", rc.profile)
	assert.Equal(t, "sk_envprof", rc.apiKey)
}

func TestResolveConfig_MissingProfileError(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, dir, "[staging]\napi_key = sk_staging\n")

	_, err := resolveConfig(Config{Profile: "nonexistent", Environment: "test", Service: "svc"})
	require.Error(t, err)
	var smplErr *SmplError
	require.ErrorAs(t, err, &smplErr)
	assert.Contains(t, smplErr.Message, "Profile [nonexistent] not found")
}

func TestResolveConfig_MissingFileSilentlySkipped(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")
	t.Setenv("HOME", t.TempDir()) // No .smplkit file.

	rc, err := resolveConfig(Config{
		APIKey:      "sk_test",
		Environment: "test",
		Service:     "svc",
	})
	require.NoError(t, err)
	assert.Equal(t, "sk_test", rc.apiKey)
}

func TestResolveConfig_EmptyValueTreatedAsUnset(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// api_key has empty value in file — should not override.
	writeConfig(t, dir, "[default]\napi_key =\nenvironment = test\nservice = svc\n")

	_, err := resolveConfig(Config{})
	require.Error(t, err) // No API key found.
	var smplErr *SmplError
	require.ErrorAs(t, err, &smplErr)
	assert.Contains(t, smplErr.Message, "No API key provided")
}

// ---------------------------------------------------------------------------
// resolveConfig — env var layer
// ---------------------------------------------------------------------------

func TestResolveConfig_EnvVarsOverrideFile(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_env")
	t.Setenv("SMPLKIT_ENVIRONMENT", "env-env")
	t.Setenv("SMPLKIT_SERVICE", "env-svc")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "env.io")
	t.Setenv("SMPLKIT_SCHEME", "http")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "true")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "1")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, dir, "[default]\napi_key = sk_file\nenvironment = file-env\nservice = file-svc\nbase_domain = file.io\nscheme = https\n")

	rc, err := resolveConfig(Config{})
	require.NoError(t, err)
	assert.Equal(t, "sk_env", rc.apiKey)
	assert.Equal(t, "env-env", rc.environment)
	assert.Equal(t, "env-svc", rc.service)
	assert.Equal(t, "env.io", rc.baseDomain)
	assert.Equal(t, "http", rc.scheme)
	assert.True(t, rc.debug)
	assert.True(t, rc.disableTelemetry)
}

// ---------------------------------------------------------------------------
// resolveConfig — Config struct layer
// ---------------------------------------------------------------------------

func TestResolveConfig_ConfigStructOverridesEnv(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_env")
	t.Setenv("SMPLKIT_ENVIRONMENT", "env-env")
	t.Setenv("SMPLKIT_SERVICE", "env-svc")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "env.io")
	t.Setenv("SMPLKIT_SCHEME", "http")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")
	t.Setenv("HOME", t.TempDir())

	rc, err := resolveConfig(Config{
		APIKey:      "sk_struct",
		Environment: "struct-env",
		Service:     "struct-svc",
		BaseDomain:  "struct.io",
		Scheme:      "https",
	})
	require.NoError(t, err)
	assert.Equal(t, "sk_struct", rc.apiKey)
	assert.Equal(t, "struct-env", rc.environment)
	assert.Equal(t, "struct-svc", rc.service)
	assert.Equal(t, "struct.io", rc.baseDomain)
	assert.Equal(t, "https", rc.scheme)
}

// ---------------------------------------------------------------------------
// resolveConfig — required field errors
// ---------------------------------------------------------------------------

func TestResolveConfig_MissingAPIKeyError(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("HOME", t.TempDir())

	_, err := resolveConfig(Config{Environment: "test", Service: "svc"})
	require.Error(t, err)
	var smplErr *SmplError
	require.ErrorAs(t, err, &smplErr)
	assert.Contains(t, smplErr.Message, "No API key provided")
	assert.Contains(t, smplErr.Message, "SMPLKIT_API_KEY")
	assert.Contains(t, smplErr.Message, "[default]")
}

func TestResolveConfig_MissingEnvironmentError(t *testing.T) {
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("HOME", t.TempDir())

	_, err := resolveConfig(Config{APIKey: "sk_test", Service: "svc"})
	require.Error(t, err)
	var smplErr *SmplError
	require.ErrorAs(t, err, &smplErr)
	assert.Contains(t, smplErr.Message, "No environment provided")
}

func TestResolveConfig_MissingServiceError(t *testing.T) {
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("HOME", t.TempDir())

	_, err := resolveConfig(Config{APIKey: "sk_test", Environment: "test"})
	require.Error(t, err)
	var smplErr *SmplError
	require.ErrorAs(t, err, &smplErr)
	assert.Contains(t, smplErr.Message, "No service provided")
}

// ---------------------------------------------------------------------------
// resolveConfig — boolean parsing from file
// ---------------------------------------------------------------------------

func TestResolveConfig_BoolsFromFile(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, dir, "[default]\napi_key = sk_test\nenvironment = test\nservice = svc\ndebug = yes\ndisable_telemetry = true\n")

	rc, err := resolveConfig(Config{})
	require.NoError(t, err)
	assert.True(t, rc.debug)
	assert.True(t, rc.disableTelemetry)
}

func TestResolveConfig_BoolsFalseInFileDoesNotOverride(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, dir, "[default]\napi_key = sk_test\nenvironment = test\nservice = svc\ndebug = false\n")

	rc, err := resolveConfig(Config{})
	require.NoError(t, err)
	assert.False(t, rc.debug) // false in file keeps the default
}

func TestResolveConfig_InvalidBoolInFileSilentlySkipped(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, dir, "[default]\napi_key = sk_test\nenvironment = test\nservice = svc\ndebug = maybe\n")

	rc, err := resolveConfig(Config{})
	require.NoError(t, err)
	assert.False(t, rc.debug) // invalid bool is silently skipped
}

// ---------------------------------------------------------------------------
// resolveConfig — default profile missing is OK when no other sections
// ---------------------------------------------------------------------------

func TestResolveConfig_DefaultProfileMissingNoOtherSections(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_BASE_DOMAIN", "")
	t.Setenv("SMPLKIT_SCHEME", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// File has [common] only, no [default] — should not error on missing default.
	writeConfig(t, dir, "[common]\nbase_domain = custom.io\n")

	rc, err := resolveConfig(Config{APIKey: "sk_test", Environment: "test", Service: "svc"})
	require.NoError(t, err)
	assert.Equal(t, "custom.io", rc.baseDomain)
}

// ---------------------------------------------------------------------------
// resolveConfig — Config.Profile struct field takes precedence over env var
// ---------------------------------------------------------------------------

func TestResolveConfig_ProfileStructOverridesEnv(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_ENVIRONMENT", "")
	t.Setenv("SMPLKIT_SERVICE", "")
	t.Setenv("SMPLKIT_PROFILE", "envprofile")
	t.Setenv("SMPLKIT_DEBUG", "")
	t.Setenv("SMPLKIT_DISABLE_TELEMETRY", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeConfig(t, dir, "[structprofile]\napi_key = sk_struct\nenvironment = test\nservice = svc\n\n[envprofile]\napi_key = sk_env\nenvironment = test\nservice = svc\n")

	rc, err := resolveConfig(Config{Profile: "structprofile"})
	require.NoError(t, err)
	assert.Equal(t, "structprofile", rc.profile)
	assert.Equal(t, "sk_struct", rc.apiKey)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, ".smplkit"), []byte(content), 0o600)
	require.NoError(t, err)
}
