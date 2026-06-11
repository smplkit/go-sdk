package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genlogging "github.com/smplkit/go-sdk/v3/internal/generated/logging"
	"github.com/smplkit/go-sdk/v3/logging/adapters"
)

// ── Shared internal test harness ────────────────────────────────────────────

// newTestLoggingClient builds a LoggingClient wired to a parent SmplClient
// whose generated logging transport points at an httptest server serving the
// supplied handler. The returned client has connected=true so the
// Install-gated live surface (OnChange / OnChangeKey / Refresh) works without
// driving a full Install — the WS-handler and resolution tests below poke the
// cache and handlers directly. Dedicated gate tests (pre-install path) live in
// the external logging_client_test.go.
func newTestLoggingClient(t *testing.T, handler http.Handler) *LoggingClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	httpClient := &http.Client{}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: "sk_test", base: base}

	headerEditor := genlogging.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genLoggingClient, _ := genlogging.NewClient(server.URL,
		genlogging.WithHTTPClient(httpClient),
		headerEditor,
	)

	c := &SmplClient{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		appURL:      server.URL,
		httpClient:  httpClient,
	}
	lc := newLoggingClient(c, genLoggingClient, nil)
	lc.connected = true
	return lc
}

// loggingFailRoundTripper returns a network error for every request.
type loggingFailRoundTripper struct{}

func (t *loggingFailRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("simulated network error")
}

// loggingBrokenBodyRoundTripper returns a status response whose body fails on Read.
type loggingBrokenBodyRoundTripper struct{ statusCode int }

func (t *loggingBrokenBodyRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.statusCode,
		Body:       io.NopCloser(&loggingErrReader{err: fmt.Errorf("simulated read error")}),
		Header:     make(http.Header),
	}, nil
}

type loggingErrReader struct{ err error }

func (r *loggingErrReader) Read(_ []byte) (int, error) { return 0, r.err }

// newDetachedLoggingClient builds a wired LoggingClient against a raw URL with a
// custom transport (no httptest server). Used for the broken-body / network
// error paths that need a hand-rolled RoundTripper.
func newDetachedLoggingClient(transport http.RoundTripper, url string) *LoggingClient {
	httpClient := &http.Client{Transport: transport}
	genLoggingClient, _ := genlogging.NewClient(url, genlogging.WithHTTPClient(httpClient))
	c := &SmplClient{environment: "test", service: "test-service", httpClient: httpClient}
	lc := newLoggingClient(c, genLoggingClient, nil)
	lc.connected = true
	return lc
}

// ── NormalizeLoggerName ─────────────────────────────────────────────────────

func TestNormalizeLoggerName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"slash and colon replaced with dot", "myapp/db:queries", "myapp.db.queries"},
		{"already normal lowercased", "Already.Normal", "already.normal"},
		{"all lowercase passthrough", "simple.logger", "simple.logger"},
		{"mixed separators", "App/Module:Sub/Deep:Leaf", "app.module.sub.deep.leaf"},
		{"empty string", "", ""},
		{"uppercase only", "UPPERCASE", "uppercase"},
		{"no separators uppercase", "MyLogger", "mylogger"},
		{"multiple consecutive slashes", "a//b", "a..b"},
		{"multiple consecutive colons", "a::b", "a..b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, NormalizeLoggerName(tt.input))
		})
	}
}

// ── keyToDisplayName (used by LogGroupsClient.New) ──────────────────────────

func TestKeyToDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"kebab-case", "checkout-v2", "Checkout V2"},
		{"snake_case", "user_service", "User Service"},
		{"single word", "infra", "Infra"},
		{"multiple hyphens", "my-cool-app", "My Cool App"},
		{"mixed separators", "my-service_name", "My Service Name"},
		{"already title case", "Already", "Already"},
		{"empty string", "", ""},
		{"all uppercase", "API-GATEWAY", "API GATEWAY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, keyToDisplayName(tt.input))
		})
	}
}

// ── resolveLoggerLevel resolution chain ─────────────────────────────────────

func TestResolveLoggerLevel_DirectLevel(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {"level": "WARN", "environments": map[string]interface{}{}},
	}
	level := resolveLoggerLevel("my.logger", "production", loggers, nil)
	assert.Equal(t, LogLevelWarn, level)
}

func TestResolveLoggerLevel_EnvironmentLevel(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"level": "WARN",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{"level": "ERROR"},
			},
		},
	}
	level := resolveLoggerLevel("my.logger", "production", loggers, nil)
	assert.Equal(t, LogLevelError, level)
}

func TestResolveLoggerLevel_EnvironmentOverridesBase(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"level": "DEBUG",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{"level": "ERROR"},
			},
		},
	}
	assert.Equal(t, LogLevelError, resolveLoggerLevel("my.logger", "production", loggers, nil))
	// Different environment falls through to base.
	assert.Equal(t, LogLevelDebug, resolveLoggerLevel("my.logger", "staging", loggers, nil))
}

func TestResolveLoggerLevel_GroupLevel(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {"group": "group-1", "environments": map[string]interface{}{}},
	}
	groups := map[string]map[string]interface{}{
		"group-1": {"level": "ERROR", "environments": map[string]interface{}{}},
	}
	assert.Equal(t, LogLevelError, resolveLoggerLevel("my.logger", "production", loggers, groups))
}

func TestResolveLoggerLevel_GroupEnvironmentLevel(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {"group": "group-1", "environments": map[string]interface{}{}},
	}
	groups := map[string]map[string]interface{}{
		"group-1": {
			"level": "WARN",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{"level": "FATAL"},
			},
		},
	}
	assert.Equal(t, LogLevelFatal, resolveLoggerLevel("my.logger", "production", loggers, groups))
}

func TestResolveLoggerLevel_GroupChain(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {"group": "child-group", "environments": map[string]interface{}{}},
	}
	groups := map[string]map[string]interface{}{
		"child-group":  {"group": "parent-group", "environments": map[string]interface{}{}},
		"parent-group": {"level": "TRACE", "environments": map[string]interface{}{}},
	}
	assert.Equal(t, LogLevelTrace, resolveLoggerLevel("my.logger", "production", loggers, groups))
}

func TestResolveLoggerLevel_GroupCycleDetection(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {"group": "group-a", "environments": map[string]interface{}{}},
	}
	groups := map[string]map[string]interface{}{
		"group-a": {"group": "group-b", "environments": map[string]interface{}{}},
		"group-b": {"group": "group-a", "environments": map[string]interface{}{}},
	}
	// Must not infinite loop; falls through to dot-ancestry then INFO.
	assert.Equal(t, LogLevelInfo, resolveLoggerLevel("my.logger", "production", loggers, groups))
}

func TestResolveLoggerLevel_DotNotationAncestry(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"com.acme.payments": {"environments": map[string]interface{}{}},
		"com.acme":          {"level": "DEBUG", "environments": map[string]interface{}{}},
	}
	assert.Equal(t, LogLevelDebug, resolveLoggerLevel("com.acme.payments", "production", loggers, nil))
}

func TestResolveLoggerLevel_DotNotationAncestry_DeepChain(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"com.acme.payments.stripe": {"environments": map[string]interface{}{}},
		"com":                      {"level": "WARN", "environments": map[string]interface{}{}},
	}
	assert.Equal(t, LogLevelWarn, resolveLoggerLevel("com.acme.payments.stripe", "production", loggers, nil))
}

func TestResolveLoggerLevel_Fallback(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {"environments": map[string]interface{}{}},
	}
	assert.Equal(t, LogLevelInfo, resolveLoggerLevel("my.logger", "production", loggers, nil))
}

func TestResolveLoggerLevel_UnknownLogger(t *testing.T) {
	assert.Equal(t, LogLevelInfo, resolveLoggerLevel("nonexistent", "production", nil, nil))
}

func TestResolveLoggerLevel_EmptyEnvironment(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"level": "DEBUG",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{"level": "ERROR"},
			},
		},
	}
	// Empty environment string skips the env-level check.
	assert.Equal(t, LogLevelDebug, resolveLoggerLevel("my.logger", "", loggers, nil))
}

func TestResolveLoggerLevel_GroupNotFound(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {"group": "nonexistent-group", "environments": map[string]interface{}{}},
	}
	assert.Equal(t, LogLevelInfo, resolveLoggerLevel("my.logger", "production", loggers, nil))
}

func TestResolveLoggerLevel_EnvironmentLevelEmptyString(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"level": "WARN",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{"level": ""},
			},
		},
	}
	// Empty string env level skipped; fall through to base.
	assert.Equal(t, LogLevelWarn, resolveLoggerLevel("my.logger", "production", loggers, nil))
}

func TestResolveLoggerLevel_GroupEnvironmentEmptyString(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {"group": "group-1", "environments": map[string]interface{}{}},
	}
	groups := map[string]map[string]interface{}{
		"group-1": {
			"level": "WARN",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{"level": ""},
			},
		},
	}
	assert.Equal(t, LogLevelWarn, resolveLoggerLevel("my.logger", "production", loggers, groups))
}

func TestResolveLoggerLevel_GroupBaseEmptyString(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {"group": "group-1", "environments": map[string]interface{}{}},
	}
	groups := map[string]map[string]interface{}{
		"group-1": {"level": "", "environments": map[string]interface{}{}},
	}
	assert.Equal(t, LogLevelInfo, resolveLoggerLevel("my.logger", "production", loggers, groups))
}

func TestResolveLoggerLevel_GroupParentEmptyString(t *testing.T) {
	// Group with an empty-string parent id must not recurse; falls through.
	loggers := map[string]map[string]interface{}{
		"my.logger": {"group": "group-1", "environments": map[string]interface{}{}},
	}
	groups := map[string]map[string]interface{}{
		"group-1": {"group": "", "environments": map[string]interface{}{}},
	}
	assert.Equal(t, LogLevelInfo, resolveLoggerLevel("my.logger", "production", loggers, groups))
}

func TestResolveLoggerLevel_LoggerEnvironmentsNotMap(t *testing.T) {
	// environments present but not a map → env branch skipped, base used.
	loggers := map[string]map[string]interface{}{
		"my.logger": {"level": "WARN", "environments": "not-a-map"},
	}
	assert.Equal(t, LogLevelWarn, resolveLoggerLevel("my.logger", "production", loggers, nil))
}

func TestResolveLoggerLevel_GroupEnvironmentsNotMap(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {"group": "group-1", "environments": map[string]interface{}{}},
	}
	groups := map[string]map[string]interface{}{
		"group-1": {"level": "ERROR", "environments": "not-a-map"},
	}
	assert.Equal(t, LogLevelError, resolveLoggerLevel("my.logger", "production", loggers, groups))
}

// ── Logger.apply / LogGroup.apply ───────────────────────────────────────────

func TestLogger_Apply(t *testing.T) {
	logger := &Logger{ID: "old-id", Name: "Old Name"}
	level := LogLevelDebug
	groupID := "group-123"
	now := time.Now()
	other := &Logger{
		ID: "new-id", Name: "New Name", Level: &level, Group: &groupID,
		Managed:      false,
		Sources:      []map[string]interface{}{{"service": "svc"}},
		Environments: map[string]interface{}{"prod": "data"},
		CreatedAt:    &now, UpdatedAt: &now,
	}
	logger.apply(other)
	assert.Equal(t, "new-id", logger.ID)
	assert.Equal(t, "New Name", logger.Name)
	require.NotNil(t, logger.Level)
	assert.Equal(t, LogLevelDebug, *logger.Level)
	require.NotNil(t, logger.Group)
	assert.Equal(t, "group-123", *logger.Group)
	assert.False(t, logger.Managed)
	require.Len(t, logger.Sources, 1)
	assert.NotNil(t, logger.CreatedAt)
	assert.NotNil(t, logger.UpdatedAt)
}

func TestLogGroup_Apply(t *testing.T) {
	group := &LogGroup{ID: "old-id", Name: "Old Name"}
	level := LogLevelError
	parentID := "parent-123"
	now := time.Now()
	other := &LogGroup{
		ID: "new-id", Name: "New Name", Level: &level, Group: &parentID,
		Environments: map[string]interface{}{"staging": "data"},
		CreatedAt:    &now, UpdatedAt: &now,
	}
	group.apply(other)
	assert.Equal(t, "new-id", group.ID)
	assert.Equal(t, "New Name", group.Name)
	require.NotNil(t, group.Level)
	assert.Equal(t, LogLevelError, *group.Level)
	require.NotNil(t, group.Group)
	assert.Equal(t, "parent-123", *group.Group)
	assert.NotNil(t, group.CreatedAt)
}

// ── Logger / LogGroup local mutation (models) ───────────────────────────────

func TestLogger_SetClearBaseLevel(t *testing.T) {
	l := &Logger{}
	l.SetBaseLevel(LogLevelDebug)
	require.NotNil(t, l.Level)
	assert.Equal(t, LogLevelDebug, *l.Level)
	l.ClearBaseLevel()
	assert.Nil(t, l.Level)
}

func TestLogger_SetEnvironmentLevel_NilEnvironments(t *testing.T) {
	l := &Logger{Environments: nil}
	l.SetEnvironmentLevel("production", LogLevelError)
	require.NotNil(t, l.Environments)
	envData := l.Environments["production"].(map[string]interface{})
	assert.Equal(t, "ERROR", envData["level"])
}

func TestLogger_SetEnvironmentLevel_NonMapEntry(t *testing.T) {
	l := &Logger{Environments: map[string]interface{}{"production": "not-a-map"}}
	l.SetEnvironmentLevel("production", LogLevelWarn)
	envData := l.Environments["production"].(map[string]interface{})
	assert.Equal(t, "WARN", envData["level"])
}

func TestLogger_ClearEnvironmentLevel_NilEnvironments(t *testing.T) {
	l := &Logger{}
	l.ClearEnvironmentLevel("staging") // must not panic
	assert.Nil(t, l.Environments)
}

func TestLogger_ClearEnvironmentLevel_NonMapEntry(t *testing.T) {
	l := &Logger{Environments: map[string]interface{}{"staging": "not-a-map"}}
	l.ClearEnvironmentLevel("staging")
	assert.Equal(t, "not-a-map", l.Environments["staging"])
}

func TestLogger_ClearEnvironmentLevel_RemovesEmptyAndPreservesOthers(t *testing.T) {
	l := &Logger{Environments: map[string]interface{}{
		"only":  map[string]interface{}{"level": "DEBUG"},
		"multi": map[string]interface{}{"level": "DEBUG", "other": "keep"},
	}}
	l.ClearEnvironmentLevel("only")
	assert.NotContains(t, l.Environments, "only")
	l.ClearEnvironmentLevel("multi")
	envData := l.Environments["multi"].(map[string]interface{})
	assert.NotContains(t, envData, "level")
	assert.Equal(t, "keep", envData["other"])
}

func TestLogger_ClearAllEnvironmentLevels(t *testing.T) {
	l := &Logger{}
	l.SetEnvironmentLevel("a", LogLevelError)
	l.SetEnvironmentLevel("b", LogLevelDebug)
	require.Len(t, l.Environments, 2)
	l.ClearAllEnvironmentLevels()
	assert.Empty(t, l.Environments)
}

func TestLogGroup_SetClearLevel(t *testing.T) {
	g := &LogGroup{}
	g.SetLevel(LogLevelWarn)
	require.NotNil(t, g.Level)
	assert.Equal(t, LogLevelWarn, *g.Level)
	g.ClearLevel()
	assert.Nil(t, g.Level)
}

func TestLogGroup_SetEnvironmentLevel_NilEnvironments(t *testing.T) {
	g := &LogGroup{Environments: nil}
	g.SetEnvironmentLevel("production", LogLevelWarn)
	require.NotNil(t, g.Environments)
	envData := g.Environments["production"].(map[string]interface{})
	assert.Equal(t, "WARN", envData["level"])
}

func TestLogGroup_SetEnvironmentLevel_NonMapEntry(t *testing.T) {
	g := &LogGroup{Environments: map[string]interface{}{"production": "not-a-map"}}
	g.SetEnvironmentLevel("production", LogLevelError)
	envData := g.Environments["production"].(map[string]interface{})
	assert.Equal(t, "ERROR", envData["level"])
}

func TestLogGroup_ClearEnvironmentLevel_NilEnvironments(t *testing.T) {
	g := &LogGroup{}
	g.ClearEnvironmentLevel("staging") // must not panic
	assert.Nil(t, g.Environments)
}

func TestLogGroup_ClearEnvironmentLevel_NonMapEntry(t *testing.T) {
	g := &LogGroup{Environments: map[string]interface{}{"staging": "not-a-map"}}
	g.ClearEnvironmentLevel("staging")
	assert.Equal(t, "not-a-map", g.Environments["staging"])
}

func TestLogGroup_ClearEnvironmentLevel_RemovesEmptyAndPreservesOthers(t *testing.T) {
	g := &LogGroup{Environments: map[string]interface{}{
		"only":  map[string]interface{}{"level": "DEBUG"},
		"multi": map[string]interface{}{"level": "DEBUG", "other": "keep"},
	}}
	g.ClearEnvironmentLevel("only")
	assert.NotContains(t, g.Environments, "only")
	g.ClearEnvironmentLevel("multi")
	envData := g.Environments["multi"].(map[string]interface{})
	assert.NotContains(t, envData, "level")
	assert.Equal(t, "keep", envData["other"])
}

func TestLogGroup_ClearAllEnvironmentLevels(t *testing.T) {
	g := &LogGroup{}
	g.SetEnvironmentLevel("a", LogLevelError)
	g.SetEnvironmentLevel("b", LogLevelDebug)
	require.Len(t, g.Environments, 2)
	g.ClearAllEnvironmentLevels()
	assert.Empty(t, g.Environments)
}

func TestLogger_Save_NilClient(t *testing.T) {
	l := &Logger{ID: "x"}
	err := l.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot save")
}

func TestLogger_Delete_NilClientOrID(t *testing.T) {
	err := (&Logger{ID: "x"}).Delete(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete")

	lc := newTestLoggingClient(t, nil)
	err = (&Logger{client: lc.loggers}).Delete(context.Background()) // empty ID
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete")
}

func TestLogGroup_Save_NilClient(t *testing.T) {
	g := &LogGroup{ID: "x"}
	err := g.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot save")
}

func TestLogGroup_Delete_NilClientOrID(t *testing.T) {
	err := (&LogGroup{ID: "x"}).Delete(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete")

	lc := newTestLoggingClient(t, nil)
	err = (&LogGroup{client: lc.logGroups}).Delete(context.Background()) // empty ID
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete")
}

// ── LoggerSource constructor + options ──────────────────────────────────────

func TestNewLoggerSource_Options(t *testing.T) {
	s := NewLoggerSource("sqlalchemy.engine",
		WithLoggerSourceService("svc"),
		WithLoggerSourceEnvironment("prod"),
		WithLoggerSourceResolvedLevel(LogLevelDebug),
	)
	assert.Equal(t, "sqlalchemy.engine", s.ID)
	require.NotNil(t, s.Service)
	assert.Equal(t, "svc", *s.Service)
	require.NotNil(t, s.Environment)
	assert.Equal(t, "prod", *s.Environment)
	require.NotNil(t, s.ResolvedLevel)
	assert.Equal(t, LogLevelDebug, *s.ResolvedLevel)
}

// ── buildLoggerAttributes / buildLogGroupAttributes ─────────────────────────

func TestBuildLoggerAttributes_WithLevel(t *testing.T) {
	level := LogLevelDebug
	l := &Logger{
		ID: "test", Name: "Test", Level: &level, Managed: true,
		Environments: map[string]interface{}{"prod": "data"},
		Sources:      []map[string]interface{}{{"service": "svc"}},
	}
	attrs := buildLoggerAttributes(l)
	require.NotNil(t, attrs.Level)
	assert.Equal(t, genlogging.LogLevel("DEBUG"), *attrs.Level)
	require.NotNil(t, attrs.Managed)
	assert.True(t, *attrs.Managed)
	require.NotNil(t, attrs.Environments)
	require.NotNil(t, attrs.Sources)
}

func TestBuildLoggerAttributes_Nils(t *testing.T) {
	attrs := buildLoggerAttributes(&Logger{ID: "test", Name: "Test", Managed: true})
	assert.Nil(t, attrs.Level)
	assert.Nil(t, attrs.Environments)
	assert.Nil(t, attrs.Sources)
}

func TestBuildLogGroupAttributes_WithLevel(t *testing.T) {
	level := LogLevelWarn
	parentID := "parent-id"
	g := &LogGroup{
		ID: "infra", Name: "Infra", Level: &level, Group: &parentID,
		Environments: map[string]interface{}{"prod": "data"},
	}
	attrs := buildLogGroupAttributes(g)
	require.NotNil(t, attrs.Level)
	assert.Equal(t, genlogging.LogLevel("WARN"), *attrs.Level)
	require.NotNil(t, attrs.ParentId)
	assert.Equal(t, "parent-id", *attrs.ParentId)
	require.NotNil(t, attrs.Environments)
}

func TestBuildLogGroupAttributes_Nils(t *testing.T) {
	attrs := buildLogGroupAttributes(&LogGroup{ID: "infra", Name: "Infra"})
	assert.Nil(t, attrs.Level)
	assert.Nil(t, attrs.Environments)
}

// ── resourceToLogger / resourceToLogGroup ───────────────────────────────────

func TestResourceToLogger_NilOptionalFields(t *testing.T) {
	r := genlogging.LoggerResource{Attributes: genlogging.Logger{Name: "Test Logger"}}
	logger := resourceToLogger(r, nil)
	assert.Equal(t, "", logger.ID)
	assert.Nil(t, logger.Level)
	assert.True(t, logger.Managed) // default true when Managed nil
	assert.Nil(t, logger.Sources)
	assert.NotNil(t, logger.Environments)
}

func TestResourceToLogger_AllFields(t *testing.T) {
	id := "my.logger"
	level := genlogging.LogLevel("WARN")
	group := "grp"
	managed := false
	sources := []map[string]interface{}{{"service": "svc"}}
	envs := map[string]interface{}{"prod": map[string]interface{}{"level": "ERROR"}}
	r := genlogging.LoggerResource{
		Id: &id,
		Attributes: genlogging.Logger{
			Name: "My Logger", Level: &level, Group: &group, Managed: &managed,
			Sources: &sources, Environments: &envs,
		},
	}
	logger := resourceToLogger(r, nil)
	assert.Equal(t, "my.logger", logger.ID)
	require.NotNil(t, logger.Level)
	assert.Equal(t, LogLevelWarn, *logger.Level)
	require.NotNil(t, logger.Group)
	assert.Equal(t, "grp", *logger.Group)
	assert.False(t, logger.Managed)
	require.Len(t, logger.Sources, 1)
	assert.Contains(t, logger.Environments, "prod")
}

func TestResourceToLogger_EmptyLevel(t *testing.T) {
	empty := genlogging.LogLevel("")
	r := genlogging.LoggerResource{Attributes: genlogging.Logger{Name: "Test", Level: &empty}}
	assert.Nil(t, resourceToLogger(r, nil).Level)
}

func TestResourceToLogGroup_NilOptionalFields(t *testing.T) {
	r := genlogging.LogGroupResource{Attributes: genlogging.LogGroup{Name: "Test Group"}}
	group := resourceToLogGroup(r, nil)
	assert.Equal(t, "", group.ID)
	assert.Nil(t, group.Level)
	assert.NotNil(t, group.Environments)
}

func TestResourceToLogGroup_AllFields(t *testing.T) {
	id := "infra"
	level := genlogging.LogLevel("WARN")
	parent := "root"
	envs := map[string]interface{}{"staging": map[string]interface{}{"level": "DEBUG"}}
	r := genlogging.LogGroupResource{
		Id: &id,
		Attributes: genlogging.LogGroup{
			Name: "Infra", Level: &level, ParentId: &parent, Environments: &envs,
		},
	}
	group := resourceToLogGroup(r, nil)
	assert.Equal(t, "infra", group.ID)
	require.NotNil(t, group.Level)
	assert.Equal(t, LogLevelWarn, *group.Level)
	require.NotNil(t, group.Group)
	assert.Equal(t, "root", *group.Group)
	assert.Contains(t, group.Environments, "staging")
}

func TestResourceToLogGroup_EmptyLevel(t *testing.T) {
	empty := genlogging.LogLevel("")
	r := genlogging.LogGroupResource{Attributes: genlogging.LogGroup{Name: "Test", Level: &empty}}
	assert.Nil(t, resourceToLogGroup(r, nil).Level)
}

// ── loggerRegistrationBuffer ────────────────────────────────────────────────

func TestLoggerRegistrationBuffer_AddDedupeDrain(t *testing.T) {
	buf := newLoggerRegistrationBuffer()
	buf.add("logger-a", "INFO", "INFO", "svc", "prod")
	buf.add("logger-b", "DEBUG", "DEBUG", "svc", "prod")
	buf.add("logger-a", "WARN", "WARN", "other", "stg") // dup ignored
	assert.Equal(t, 2, buf.pendingCount())

	batch := buf.drain()
	require.Len(t, batch, 2)
	assert.Equal(t, "logger-a", batch[0].key)
	assert.Equal(t, "INFO", batch[0].level)
	assert.Equal(t, "logger-b", batch[1].key)
	assert.Equal(t, 0, buf.pendingCount())

	assert.Empty(t, buf.drain())
}

func TestLoggerRegistrationBuffer_StoresResolvedLevel(t *testing.T) {
	buf := newLoggerRegistrationBuffer()
	buf.add("my-logger", "DEBUG", "INFO", "svc", "prod")
	batch := buf.drain()
	require.Len(t, batch, 1)
	assert.Equal(t, "DEBUG", batch[0].level)
	assert.Equal(t, "INFO", batch[0].resolvedLevel)
	assert.Equal(t, "svc", batch[0].service)
	assert.Equal(t, "prod", batch[0].environment)
}

// ── fetchAndCache ───────────────────────────────────────────────────────────

func TestFetchAndCache_PopulatesCaches(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"my.logger","type":"logger","attributes":{"id":"my.logger","name":"My Logger","level":"WARN","group":"group-id","managed":true,"environments":{"production":{"level":"ERROR"}},"sources":[{"service":"test-service"}]}}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"infra","type":"log_group","attributes":{"id":"infra","name":"Infra","level":"ERROR","parent_id":"root","environments":{"staging":{"level":"DEBUG"}}}}]}`))
	})
	lc := newTestLoggingClient(t, mux)

	require.NoError(t, lc.fetchAndCache(context.Background()))

	require.Contains(t, lc.loggersCache, "my.logger")
	assert.Equal(t, "WARN", lc.loggersCache["my.logger"]["level"])
	assert.Equal(t, "group-id", lc.loggersCache["my.logger"]["group"])
	assert.Equal(t, true, lc.loggersCache["my.logger"]["managed"])

	require.Contains(t, lc.groupsCache, "infra")
	assert.Equal(t, "ERROR", lc.groupsCache["infra"]["level"])
	assert.Equal(t, "root", lc.groupsCache["infra"]["group"])
}

func TestFetchAndCache_NilLevelAndGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"my.logger","type":"logger","attributes":{"id":"my.logger","name":"My Logger","managed":true,"environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"infra","type":"log_group","attributes":{"id":"infra","name":"Infra","environments":{}}}]}`))
	})
	lc := newTestLoggingClient(t, mux)
	require.NoError(t, lc.fetchAndCache(context.Background()))

	_, hasLevel := lc.loggersCache["my.logger"]["level"]
	_, hasGroup := lc.loggersCache["my.logger"]["group"]
	assert.False(t, hasLevel)
	assert.False(t, hasGroup)
	_, hasLevel = lc.groupsCache["infra"]["level"]
	_, hasGroup = lc.groupsCache["infra"]["group"]
	assert.False(t, hasLevel)
	assert.False(t, hasGroup)
}

func TestFetchAndCache_LoggerError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))
	require.Error(t, lc.fetchAndCache(context.Background()))
}

func TestFetchAndCache_GroupError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	})
	lc := newTestLoggingClient(t, mux)
	require.Error(t, lc.fetchAndCache(context.Background()))
}

// fetchAllLoggers / fetchAllLogGroups pagination: a full page forces a second
// fetch, then a short page terminates the loop.
func TestFetchAll_WalksPages(t *testing.T) {
	mkLoggers := func(n int) string {
		var b strings.Builder
		b.WriteString(`{"data":[`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":"l%d","type":"logger","attributes":{"id":"l%d","name":"l%d","managed":true,"environments":{}}}`, i, i, i)
		}
		b.WriteString(`]}`)
		return b.String()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page[number]") == "1" {
			_, _ = w.Write([]byte(mkLoggers(fetchAllPageSize)))
			return
		}
		// short page with a distinct id so it doesn't collide in the cache map
		_, _ = w.Write([]byte(`{"data":[{"id":"lLast","type":"logger","attributes":{"id":"lLast","name":"lLast","managed":true,"environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page[number]") == "1" {
			// one full page of groups, then short
			var b strings.Builder
			b.WriteString(`{"data":[`)
			for i := 0; i < fetchAllPageSize; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"id":"g%d","type":"log_group","attributes":{"id":"g%d","name":"g%d","environments":{}}}`, i, i, i)
			}
			b.WriteString(`]}`)
			_, _ = w.Write([]byte(b.String()))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gLast","type":"log_group","attributes":{"id":"gLast","name":"gLast","environments":{}}}]}`))
	})
	lc := newTestLoggingClient(t, mux)
	require.NoError(t, lc.fetchAndCache(context.Background()))
	assert.Len(t, lc.loggersCache, fetchAllPageSize+1)
	assert.Len(t, lc.groupsCache, fetchAllPageSize+1)
}

func TestFetchAllLoggers_ErrorPropagates(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	_, err := lc.fetchAllLoggers(context.Background())
	require.Error(t, err)
}

func TestFetchAllLogGroups_ErrorPropagates(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	_, err := lc.fetchAllLogGroups(context.Background())
	require.Error(t, err)
}

// ── fetchSingleLogger / fetchSingleGroup ────────────────────────────────────

func TestFetchSingleLogger_WithLevelAndGroup(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme.app","type":"logger","attributes":{"id":"com.acme.app","name":"app","level":"DEBUG","managed":true,"group":"my-group","environments":{}}}}`))
	}))
	entry, err := lc.fetchSingleLogger(context.Background(), "com.acme.app")
	require.NoError(t, err)
	assert.Equal(t, "DEBUG", entry["level"])
	assert.Equal(t, "my-group", entry["group"])
}

func TestFetchSingleLogger_NetworkError(t *testing.T) {
	lc := newDetachedLoggingClient(&loggingFailRoundTripper{}, "http://localhost")
	_, err := lc.fetchSingleLogger(context.Background(), "x")
	require.Error(t, err)
}

func TestFetchSingleLogger_ReadBodyError(t *testing.T) {
	lc := newDetachedLoggingClient(&loggingBrokenBodyRoundTripper{statusCode: 200}, "http://localhost")
	_, err := lc.fetchSingleLogger(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read response body")
}

func TestFetchSingleLogger_HTTPError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))
	_, err := lc.fetchSingleLogger(context.Background(), "x")
	require.Error(t, err)
}

func TestFetchSingleLogger_MalformedJSON(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not valid json`))
	}))
	_, err := lc.fetchSingleLogger(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestFetchSingleGroup_WithLevelAndParent(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"child","type":"log_group","attributes":{"id":"child","name":"Child","level":"INFO","parent_id":"parent","environments":{}}}}`))
	}))
	entry, err := lc.fetchSingleGroup(context.Background(), "child")
	require.NoError(t, err)
	assert.Equal(t, "INFO", entry["level"])
	assert.Equal(t, "parent", entry["group"])
}

func TestFetchSingleGroup_NetworkError(t *testing.T) {
	lc := newDetachedLoggingClient(&loggingFailRoundTripper{}, "http://localhost")
	_, err := lc.fetchSingleGroup(context.Background(), "x")
	require.Error(t, err)
}

func TestFetchSingleGroup_ReadBodyError(t *testing.T) {
	lc := newDetachedLoggingClient(&loggingBrokenBodyRoundTripper{statusCode: 200}, "http://localhost")
	_, err := lc.fetchSingleGroup(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read response body")
}

func TestFetchSingleGroup_HTTPError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	_, err := lc.fetchSingleGroup(context.Background(), "x")
	require.Error(t, err)
}

func TestFetchSingleGroup_MalformedJSON(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not valid json`))
	}))
	_, err := lc.fetchSingleGroup(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

// ── Flush (LoggingClient.Flush delegating to Loggers().Flush) ───────────────

func TestFlush_Empty(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("no HTTP expected for empty flush")
	}))
	require.NoError(t, lc.Flush(context.Background()))
}

func TestFlush_SendsLevelResolvedServiceEnvironment(t *testing.T) {
	var body map[string]interface{}
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	lc.buffer.add("app.logger", "WARN", "ERROR", "my-service", "production")
	require.NoError(t, lc.Flush(context.Background()))

	require.NotNil(t, body)
	loggers := body["loggers"].([]interface{})
	require.Len(t, loggers, 1)
	item := loggers[0].(map[string]interface{})
	assert.Equal(t, "WARN", item["level"])
	assert.Equal(t, "ERROR", item["resolved_level"])
	assert.Equal(t, "my-service", item["service"])
	assert.Equal(t, "production", item["environment"])
}

func TestFlush_OmitsEmptyOptionalFields(t *testing.T) {
	var body map[string]interface{}
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	// empty explicit level, empty service+environment → only id + resolved_level present.
	lc.buffer.add("inherited.logger", "", "INFO", "", "")
	require.NoError(t, lc.Flush(context.Background()))

	loggers := body["loggers"].([]interface{})
	item := loggers[0].(map[string]interface{})
	assert.Nil(t, item["level"])
	assert.Nil(t, item["service"])
	assert.Nil(t, item["environment"])
	assert.Equal(t, "INFO", item["resolved_level"])
}

func TestFlush_RecordsMetricsWhenSet(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	mr := newMetricsReporter(lc.client.httpClient, "http://metrics.invalid", "test", "svc", 0)
	t.Cleanup(mr.Close)
	lc.loggers.metrics = mr
	lc.buffer.add("metric.logger", "INFO", "INFO", "svc", "prod")
	require.NoError(t, lc.Flush(context.Background()))
}

func TestFlush_HTTPError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"Invalid level value"}]}`))
	}))
	lc.buffer.add("bad.logger", "INFO", "INFO", "svc", "prod")
	err := lc.Flush(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid level value")
}

func TestFlush_NetworkError(t *testing.T) {
	lc := newDetachedLoggingClient(&loggingFailRoundTripper{}, "http://localhost")
	lc.buffer.add("net.logger", "INFO", "INFO", "svc", "prod")
	err := lc.Flush(context.Background())
	require.Error(t, err)
	var connErr *ConnectionError
	require.ErrorAs(t, err, &connErr)
}

func TestFlush_ReadBodyError(t *testing.T) {
	lc := newDetachedLoggingClient(&loggingBrokenBodyRoundTripper{statusCode: 200}, "http://localhost")
	lc.buffer.add("body.read.fail", "INFO", "INFO", "svc", "env")
	err := lc.Flush(context.Background())
	require.Error(t, err)
	var connErr *ConnectionError
	require.ErrorAs(t, err, &connErr)
	assert.Contains(t, err.Error(), "failed to read response body")
}

func TestRegister_NoFlushUnderThreshold(t *testing.T) {
	var bulkCalls int32
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			atomic.AddInt32(&bulkCalls, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	require.NoError(t, lc.loggers.Register(context.Background(), []LoggerSource{
		NewLoggerSource("a.logger",
			WithLoggerSourceResolvedLevel(LogLevelInfo),
			WithLoggerSourceService("svc"),
			WithLoggerSourceEnvironment("prod"),
		),
	}, false))
	assert.Equal(t, 1, lc.loggers.PendingCount())
	assert.Equal(t, int32(0), atomic.LoadInt32(&bulkCalls), "below threshold → no flush")

	batch := lc.buffer.drain()
	require.Len(t, batch, 1)
	assert.Equal(t, "svc", batch[0].service)
	assert.Equal(t, "prod", batch[0].environment)
}

// Register with flush=false crossing loggerBatchFlushSize triggers a
// background thresholdFlush goroutine that drains the buffer.
func TestRegister_ThresholdTriggersBackgroundFlush(t *testing.T) {
	var bulkCalls int32
	flushed := make(chan struct{}, 1)
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			atomic.AddInt32(&bulkCalls, 1)
			_, _ = w.Write([]byte(`{"registered":1}`))
			select {
			case flushed <- struct{}{}:
			default:
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	sources := make([]LoggerSource, loggerBatchFlushSize)
	for i := range sources {
		sources[i] = NewLoggerSource(fmt.Sprintf("logger.%d", i), WithLoggerSourceResolvedLevel(LogLevelInfo))
	}
	require.NoError(t, lc.loggers.Register(context.Background(), sources, false))

	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("threshold flush goroutine did not POST to /loggers/bulk")
	}
	assert.GreaterOrEqual(t, atomic.LoadInt32(&bulkCalls), int32(1))
}

// thresholdFlush logs (does not return) an error from the background flush.
func TestThresholdFlush_LogsErrorOnFailure(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"rejected"}]}`))
	}))
	lc.buffer.add("threshold.err", "INFO", "INFO", "svc", "prod")

	var (
		mu  sync.Mutex
		buf strings.Builder
	)
	log.SetOutput(&lockedWriter{mu: &mu, b: &buf})
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	lc.loggers.thresholdFlush() // synchronous call exercises the log path

	mu.Lock()
	got := buf.String()
	mu.Unlock()
	assert.Contains(t, got, "smplkit: logger registration flush failed")
}

func TestFlushSync_Alias(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	lc.buffer.add("sync.logger", "INFO", "INFO", "svc", "prod")
	require.NoError(t, lc.loggers.FlushSync(context.Background()))
	assert.Equal(t, 0, lc.loggers.PendingCount())
}

// ── periodicFlush ───────────────────────────────────────────────────────────

func TestPeriodicFlush_Stops(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() { lc.periodicFlush(done); close(exited) }()
	close(done)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("periodicFlush did not exit after done closed")
	}
}

func TestPeriodicFlush_TickerFiresAndLogsError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test that waits for the 5s ticker")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server rejected batch"}]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	lc := newTestLoggingClient(t, mux)
	lc.buffer.add("periodic.error.logger", "INFO", "INFO", "my-service", "production")

	var (
		mu     sync.Mutex
		buf    strings.Builder
		writer = &lockedWriter{mu: &mu, b: &buf}
	)
	log.SetOutput(writer)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	done := make(chan struct{})
	exited := make(chan struct{})
	go func() { lc.periodicFlush(done); close(exited) }()
	time.Sleep(6 * time.Second)
	close(done)
	<-exited

	mu.Lock()
	got := buf.String()
	mu.Unlock()
	assert.Contains(t, got, "smplkit: bulk logger registration failed")
	assert.Contains(t, got, "server rejected batch")
}

// lockedWriter is a goroutine-safe wrapper around strings.Builder.
type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// ── close ───────────────────────────────────────────────────────────────────

func TestLoggingClient_Close_NilFields(t *testing.T) {
	c := &LoggingClient{
		loggersCache: map[string]map[string]interface{}{},
		groupsCache:  map[string]map[string]interface{}{},
		keyListeners: map[string][]func(*LoggerChangeEvent){},
		buffer:       newLoggerRegistrationBuffer(),
	}
	c.close() // flushDone, wsManager nil — must not panic
}

func TestLoggingClient_Close_WithFlushDoneAndAdapter(t *testing.T) {
	adapter := &captureAdapter{}
	c := &LoggingClient{
		loggersCache: map[string]map[string]interface{}{},
		groupsCache:  map[string]map[string]interface{}{},
		keyListeners: map[string][]func(*LoggerChangeEvent){},
		buffer:       newLoggerRegistrationBuffer(),
		flushDone:    make(chan struct{}),
		adapters:     []adapters.LoggingAdapter{adapter},
	}
	c.close()
	assert.Nil(t, c.flushDone)
	assert.True(t, adapter.uninstalled)
}

func TestLoggingClient_Close_StandaloneOwnWS(t *testing.T) {
	ws := &sharedWebSocket{
		listeners: make(map[string][]eventCallback),
		closeCh:   make(chan struct{}),
		wsDone:    make(chan struct{}),
	}
	close(ws.wsDone) // so stop() returns immediately
	c := &LoggingClient{
		client:       nil, // standalone
		ownWS:        ws,
		wsManager:    ws,
		loggersCache: map[string]map[string]interface{}{},
		groupsCache:  map[string]map[string]interface{}{},
		keyListeners: map[string][]func(*LoggerChangeEvent){},
		buffer:       newLoggerRegistrationBuffer(),
	}
	c.close()
	assert.Nil(t, c.ownWS)
	assert.Nil(t, c.wsManager)
}

// ── fireChangeListeners ─────────────────────────────────────────────────────

func newBareLoggingClient(cache map[string]map[string]interface{}) *LoggingClient {
	return &LoggingClient{
		client:       &SmplClient{environment: "test"},
		environment:  "test",
		loggersCache: cache,
		groupsCache:  map[string]map[string]interface{}{},
		keyListeners: map[string][]func(*LoggerChangeEvent){},
	}
}

func TestFireChangeListeners_EmptyKeyIsNoOp(t *testing.T) {
	c := newBareLoggingClient(map[string]map[string]interface{}{})
	var called bool
	c.globalListeners = append(c.globalListeners, func(*LoggerChangeEvent) { called = true })
	c.fireChangeListeners("", "websocket")
	assert.False(t, called)
}

func TestFireChangeListeners_GlobalAndKey(t *testing.T) {
	c := newBareLoggingClient(map[string]map[string]interface{}{
		"my.logger": {"level": "WARN", "environments": map[string]interface{}{}},
	})
	var globalEvent, keyEvent *LoggerChangeEvent
	c.globalListeners = append(c.globalListeners, func(e *LoggerChangeEvent) { globalEvent = e })
	c.keyListeners["my.logger"] = append(c.keyListeners["my.logger"], func(e *LoggerChangeEvent) { keyEvent = e })

	c.fireChangeListeners("my.logger", "websocket")

	require.NotNil(t, globalEvent)
	assert.Equal(t, "my.logger", globalEvent.ID)
	assert.Equal(t, "websocket", globalEvent.Source)
	require.NotNil(t, globalEvent.Level)
	assert.Equal(t, LogLevelWarn, *globalEvent.Level)
	require.NotNil(t, keyEvent)
	assert.Equal(t, "my.logger", keyEvent.ID)
}

func TestFireChangeListeners_LoggerNotInCacheNilLevel(t *testing.T) {
	c := newBareLoggingClient(map[string]map[string]interface{}{})
	var event *LoggerChangeEvent
	c.globalListeners = append(c.globalListeners, func(e *LoggerChangeEvent) { event = e })
	c.fireChangeListeners("unknown.logger", "websocket")
	require.NotNil(t, event)
	assert.Nil(t, event.Level)
}

func TestFireChangeListeners_GlobalPanicRecovery(t *testing.T) {
	c := newBareLoggingClient(map[string]map[string]interface{}{
		"my.logger": {"level": "INFO", "environments": map[string]interface{}{}},
	})
	var second bool
	c.globalListeners = append(c.globalListeners, func(*LoggerChangeEvent) { panic("bad") })
	c.globalListeners = append(c.globalListeners, func(*LoggerChangeEvent) { second = true })
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	c.fireChangeListeners("my.logger", "websocket")
	assert.True(t, second)
}

func TestFireChangeListeners_KeyPanicRecovery(t *testing.T) {
	c := newBareLoggingClient(map[string]map[string]interface{}{
		"my.logger": {"level": "INFO", "environments": map[string]interface{}{}},
	})
	var second bool
	c.keyListeners["my.logger"] = append(c.keyListeners["my.logger"], func(*LoggerChangeEvent) { panic("bad") })
	c.keyListeners["my.logger"] = append(c.keyListeners["my.logger"], func(*LoggerChangeEvent) { second = true })
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	c.fireChangeListeners("my.logger", "websocket")
	assert.True(t, second)
}

// ── requireInstalled gate (internal) ────────────────────────────────────────

func TestRequireInstalled(t *testing.T) {
	c := &LoggingClient{}
	err := c.requireInstalled()
	require.Error(t, err)
	var notInstalled *NotInstalledError
	require.ErrorAs(t, err, &notInstalled)

	c.connected = true
	require.NoError(t, c.requireInstalled())
}

func TestOnChange_GatedBeforeInstall(t *testing.T) {
	c := &LoggingClient{keyListeners: map[string][]func(*LoggerChangeEvent){}}
	err := c.OnChange(func(*LoggerChangeEvent) {})
	var notInstalled *NotInstalledError
	require.ErrorAs(t, err, &notInstalled)
	assert.Empty(t, c.globalListeners)

	c.connected = true
	require.NoError(t, c.OnChange(func(*LoggerChangeEvent) {}))
	assert.Len(t, c.globalListeners, 1)
}

func TestOnChangeKey_GatedBeforeInstall(t *testing.T) {
	c := &LoggingClient{keyListeners: map[string][]func(*LoggerChangeEvent){}}
	err := c.OnChangeKey("k", func(*LoggerChangeEvent) {})
	var notInstalled *NotInstalledError
	require.ErrorAs(t, err, &notInstalled)
	assert.Empty(t, c.keyListeners["k"])

	c.connected = true
	require.NoError(t, c.OnChangeKey("k", func(*LoggerChangeEvent) {}))
	assert.Len(t, c.keyListeners["k"], 1)
}

func TestRefresh_GatedBeforeInstall(t *testing.T) {
	c := &LoggingClient{}
	err := c.Refresh(context.Background())
	var notInstalled *NotInstalledError
	require.ErrorAs(t, err, &notInstalled)
}

// ── RegisterAdapter / RegisterLogger ────────────────────────────────────────

func TestRegisterAdapter_DisablesAutoLoad(t *testing.T) {
	c := &LoggingClient{}
	a := &captureAdapter{}
	c.RegisterAdapter(a)
	assert.True(t, c.explicitAdapters)
	require.Len(t, c.adapters, 1)
}

func TestRegisterAdapter_PanicsAfterInstall(t *testing.T) {
	c := &LoggingClient{connected: true}
	assert.Panics(t, func() { c.RegisterAdapter(&captureAdapter{}) })
}

func TestRegisterLogger_BuffersNormalized(t *testing.T) {
	c := &LoggingClient{buffer: newLoggerRegistrationBuffer(), service: "svc", environment: "prod"}
	c.RegisterLogger("MyApp/DB:Queries", LogLevelDebug)
	c.RegisterLogger("myapp.db.queries", LogLevelInfo) // dup of normalized form
	batch := c.buffer.drain()
	require.Len(t, batch, 1)
	assert.Equal(t, "myapp.db.queries", batch[0].key)
	assert.Equal(t, "DEBUG", batch[0].level)
}

// ── onNewLogger ─────────────────────────────────────────────────────────────

func TestOnNewLogger_EmptyNameIgnored(t *testing.T) {
	c := &LoggingClient{
		buffer:       newLoggerRegistrationBuffer(),
		nameMap:      map[string]string{},
		loggersCache: map[string]map[string]interface{}{},
		groupsCache:  map[string]map[string]interface{}{},
	}
	c.onNewLogger("", "INFO")
	assert.Equal(t, 0, c.buffer.pendingCount())
}

func TestOnNewLogger_NotConnectedBuffersOnly(t *testing.T) {
	a := &captureAdapter{}
	c := &LoggingClient{
		buffer:       newLoggerRegistrationBuffer(),
		nameMap:      map[string]string{},
		loggersCache: map[string]map[string]interface{}{},
		groupsCache:  map[string]map[string]interface{}{},
		adapters:     []adapters.LoggingAdapter{a},
		service:      "svc", environment: "prod",
	}
	c.onNewLogger("Com.Acme.New", "INFO")
	assert.Equal(t, 1, c.buffer.pendingCount())
	assert.Equal(t, "com.acme.new", c.nameMap["Com.Acme.New"])
	assert.Empty(t, a.applied, "no apply before connected")
}

func TestOnNewLogger_ConnectedAppliesAndRecordsMetric(t *testing.T) {
	a := &captureAdapter{}
	httpClient := &http.Client{}
	mr := newMetricsReporter(httpClient, "http://metrics.invalid", "test", "svc", 0)
	t.Cleanup(mr.Close)
	c := &LoggingClient{
		connected:    true,
		buffer:       newLoggerRegistrationBuffer(),
		nameMap:      map[string]string{},
		loggersCache: map[string]map[string]interface{}{},
		groupsCache:  map[string]map[string]interface{}{},
		adapters:     []adapters.LoggingAdapter{a},
		metrics:      mr,
		service:      "svc", environment: "prod",
	}
	c.onNewLogger("com.acme.new", "INFO")
	require.Len(t, a.applied, 1)
	assert.Equal(t, "com.acme.new", a.applied[0].name)
	assert.Equal(t, "INFO", a.applied[0].level) // INFO fallback
}

// ── applyLevels ─────────────────────────────────────────────────────────────

func TestApplyLevels_NoAdaptersIsNoOp(t *testing.T) {
	c := &LoggingClient{
		loggersCache: map[string]map[string]interface{}{},
		groupsCache:  map[string]map[string]interface{}{},
	}
	c.applyLevels() // must not panic with no adapters
}

func TestApplyLevels_ResolvesAndAppliesAndRecordsMetric(t *testing.T) {
	a := &captureAdapter{discovered: []adapters.DiscoveredLogger{
		{Name: "com.acme.db", Level: "DEBUG"},
		{Name: "", Level: "INFO"}, // empty name skipped
	}}
	httpClient := &http.Client{}
	mr := newMetricsReporter(httpClient, "http://metrics.invalid", "test", "svc", 0)
	t.Cleanup(mr.Close)
	c := &LoggingClient{
		environment: "test",
		adapters:    []adapters.LoggingAdapter{a},
		metrics:     mr,
		loggersCache: map[string]map[string]interface{}{
			"com.acme.db": {"level": "ERROR", "environments": map[string]interface{}{}},
		},
		groupsCache: map[string]map[string]interface{}{},
	}
	c.applyLevels()
	require.Len(t, a.applied, 1)
	assert.Equal(t, "com.acme.db", a.applied[0].name)
	assert.Equal(t, "ERROR", a.applied[0].level)
}

// ── Install (wired path, internal) ──────────────────────────────────────────

// installMux serves the endpoints Install touches: list loggers/groups, bulk,
// and a catch-all for the rest.
func installMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// installTestClient builds a wired (connected=false) LoggingClient whose parent
// has a pre-injected non-connecting WebSocket, so Install does not dial.
func installTestClient(t *testing.T, mux http.Handler) *LoggingClient {
	t.Helper()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	httpClient := &http.Client{}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: "sk_test", base: base}
	headerEditor := genlogging.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genLoggingClient, _ := genlogging.NewClient(server.URL,
		genlogging.WithHTTPClient(httpClient),
		headerEditor,
	)
	c := &SmplClient{apiKey: "sk_test", environment: "test", service: "test-service", appURL: server.URL, httpClient: httpClient}
	// Pre-inject a non-connecting WS so ensureWS()/ensureStarted() do not dial.
	c.ws = &sharedWebSocket{
		listeners: make(map[string][]eventCallback),
		closeCh:   make(chan struct{}),
		wsDone:    make(chan struct{}),
	}
	c.started = true // skip background goroutines
	lc := newLoggingClient(c, genLoggingClient, nil)
	return lc
}

func TestInstall_NoAdaptersWarnsAndConnects(t *testing.T) {
	var logBuf strings.Builder
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	lc := installTestClient(t, installMux())
	require.NoError(t, lc.Install(context.Background()))
	assert.True(t, lc.connected)
	assert.Contains(t, logBuf.String(), "no logging adapters registered")
	t.Cleanup(lc.close)
}

func TestInstall_RegistersWSHandlers(t *testing.T) {
	lc := installTestClient(t, installMux())
	require.NoError(t, lc.Install(context.Background()))

	ws := lc.client.ws
	ws.listenersMu.Lock()
	defer ws.listenersMu.Unlock()
	for _, ev := range []string{"logger_changed", "logger_deleted", "group_changed", "group_deleted", "loggers_changed"} {
		_, ok := ws.listeners[ev]
		assert.True(t, ok, "%s should be registered", ev)
	}
	t.Cleanup(lc.close)
}

func TestInstall_DiscoversAppliesAndInstallsHooks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":1}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"com.acme.db","type":"logger","attributes":{"id":"com.acme.db","name":"com.acme.db","managed":true,"group":"sql","environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"ERROR","environments":{}}}]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	lc := installTestClient(t, mux)
	a := &captureAdapter{discovered: []adapters.DiscoveredLogger{
		{Name: "com.acme.db", Level: "DEBUG"},
		{Name: "", Level: "INFO"}, // empty discovery skipped
	}}
	lc.RegisterAdapter(a)
	require.NoError(t, lc.Install(context.Background()))

	assert.True(t, a.hookInstalled, "InstallHook should fire")
	// Inherited level from group sql=ERROR must reach the adapter.
	var got string
	for _, al := range a.applied {
		if al.name == "com.acme.db" {
			got = al.level
		}
	}
	assert.Equal(t, "ERROR", got)
	t.Cleanup(lc.close)
}

func TestInstall_FetchErrorReturned(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"fail"}]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	lc := installTestClient(t, mux)
	err := lc.Install(context.Background())
	require.Error(t, err)
	assert.False(t, lc.connected)
}

func TestInstall_BulkFlushErrorLoggedNotFatal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"Invalid level value"}]}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	var logBuf strings.Builder
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	lc := installTestClient(t, mux)
	a := &captureAdapter{discovered: []adapters.DiscoveredLogger{{Name: "com.acme.app", Level: "INFO"}}}
	lc.RegisterAdapter(a)
	require.NoError(t, lc.Install(context.Background()))
	assert.True(t, lc.connected)
	assert.Contains(t, logBuf.String(), "smplkit: bulk logger registration failed")
	assert.Contains(t, logBuf.String(), "Invalid level value")
	t.Cleanup(lc.close)
}

func TestInstall_Idempotent(t *testing.T) {
	var listCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		listCalls.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	lc := installTestClient(t, mux)
	require.NoError(t, lc.Install(context.Background()))
	require.NoError(t, lc.Install(context.Background()))
	assert.Equal(t, int32(1), listCalls.Load(), "second Install is a no-op")
	t.Cleanup(lc.close)
}

func TestRefresh_PostInstall_FiresManualDeltas(t *testing.T) {
	var groupsLevel atomic.Value
	groupsLevel.Store("WARN")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"app.db","type":"logger","attributes":{"id":"app.db","name":"app.db","managed":true,"group":"sql","environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		body := strings.ReplaceAll(`{"data":[{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"$L","environments":{}}}]}`, "$L", groupsLevel.Load().(string))
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	lc := installTestClient(t, mux)
	require.NoError(t, lc.Install(context.Background()))
	t.Cleanup(lc.close)

	var events []*LoggerChangeEvent
	require.NoError(t, lc.OnChange(func(e *LoggerChangeEvent) { events = append(events, e) }))

	groupsLevel.Store("DEBUG")
	require.NoError(t, lc.Refresh(context.Background()))
	require.Len(t, events, 1)
	assert.Equal(t, "app.db", events[0].ID)
	assert.Equal(t, "manual", events[0].Source)
}

// ── NewLoggingClient (standalone) ───────────────────────────────────────────

func TestNewLoggingClient_Standalone_CRUD(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/showcase", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "Bearer sk_test_key", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"data":{"id":"showcase","type":"logger","attributes":{"id":"showcase","name":"showcase","level":"INFO","managed":true,"environments":{}}}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	lc, err := NewLoggingClient(
		Config{APIKey: "sk_test_key", Environment: "test", Service: "svc", DisableTelemetry: true},
		WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	assert.Equal(t, "test", lc.environment)
	assert.Equal(t, "svc", lc.service)
	assert.Nil(t, lc.client, "standalone has no parent")

	logger, err := lc.Loggers().Get(context.Background(), "showcase")
	require.NoError(t, err)
	assert.Equal(t, "showcase", logger.ID)
}

func TestNewLoggingClient_Standalone_ConfigError(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	_, err := NewLoggingClient(Config{}) // no API key resolvable
	require.Error(t, err)
}

func TestNewLoggingClient_Standalone_CustomHTTPClient(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/x", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"x","type":"logger","attributes":{"id":"x","name":"x","managed":true,"environments":{}}}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	hc := &http.Client{}
	lc, err := NewLoggingClient(
		Config{APIKey: "sk_test_key", Environment: "test", Service: "svc", DisableTelemetry: true},
		WithBaseURL(server.URL),
		WithHTTPClient(hc),
	)
	require.NoError(t, err)
	_, err = lc.Loggers().Get(context.Background(), "x")
	require.NoError(t, err)
}

func TestNewLoggingClient_Standalone_EnsureWSOwnsSocket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	lc, err := NewLoggingClient(
		Config{APIKey: "sk_test_key", Environment: "test", Service: "svc", DisableTelemetry: true},
		WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	ws := lc.ensureWS()
	require.NotNil(t, ws)
	assert.Same(t, ws, lc.ownWS, "standalone ensureWS builds and owns its own socket")
	assert.Same(t, ws, lc.ensureWS(), "ensureWS is idempotent")
	lc.ownWS.stop()
}

// ── shared capture adapter ──────────────────────────────────────────────────

type appliedLevel struct {
	name  string
	level string
}

// captureAdapter is a configurable adapters.LoggingAdapter test double.
type captureAdapter struct {
	discovered    []adapters.DiscoveredLogger
	applied       []appliedLevel
	hookInstalled bool
	hookCallback  func(string, string)
	uninstalled   bool
}

func (a *captureAdapter) Name() string { return "capture" }

func (a *captureAdapter) Discover() []adapters.DiscoveredLogger { return a.discovered }

func (a *captureAdapter) ApplyLevel(name, level string) {
	a.applied = append(a.applied, appliedLevel{name: name, level: level})
}

func (a *captureAdapter) InstallHook(cb func(string, string)) {
	a.hookInstalled = true
	a.hookCallback = cb
}

func (a *captureAdapter) UninstallHook() { a.uninstalled = true }
