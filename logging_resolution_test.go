package smplkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genlogging "github.com/smplkit/go-sdk/internal/generated/logging"
)

// --- NormalizeLoggerName tests ---

func TestNormalizeLoggerName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "slash and colon replaced with dot",
			input:    "myapp/db:queries",
			expected: "myapp.db.queries",
		},
		{
			name:     "already normal lowercased",
			input:    "Already.Normal",
			expected: "already.normal",
		},
		{
			name:     "all lowercase passthrough",
			input:    "simple.logger",
			expected: "simple.logger",
		},
		{
			name:     "mixed separators",
			input:    "App/Module:Sub/Deep:Leaf",
			expected: "app.module.sub.deep.leaf",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "uppercase only",
			input:    "UPPERCASE",
			expected: "uppercase",
		},
		{
			name:     "no separators uppercase",
			input:    "MyLogger",
			expected: "mylogger",
		},
		{
			name:     "multiple consecutive slashes",
			input:    "a//b",
			expected: "a..b",
		},
		{
			name:     "multiple consecutive colons",
			input:    "a::b",
			expected: "a..b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeLoggerName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// --- keyToDisplayName tests ---

func TestKeyToDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "kebab-case",
			input:    "checkout-v2",
			expected: "Checkout V2",
		},
		{
			name:     "snake_case",
			input:    "user_service",
			expected: "User Service",
		},
		{
			name:     "single word",
			input:    "infra",
			expected: "Infra",
		},
		{
			name:     "multiple hyphens",
			input:    "my-cool-app",
			expected: "My Cool App",
		},
		{
			name:     "mixed separators",
			input:    "my-service_name",
			expected: "My Service Name",
		},
		{
			name:     "already title case",
			input:    "Already",
			expected: "Already",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "all uppercase",
			input:    "API-GATEWAY",
			expected: "API GATEWAY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := keyToDisplayName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// --- resolveLoggerLevel tests ---

func TestResolveLoggerLevel_DirectLevel(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"level":        "WARN",
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{}

	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelWarn, level)
}

func TestResolveLoggerLevel_EnvironmentLevel(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"level": "WARN",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"level": "ERROR",
				},
			},
		},
	}
	groups := map[string]map[string]interface{}{}

	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelError, level)
}

func TestResolveLoggerLevel_EnvironmentOverridesBase(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"level": "DEBUG",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"level": "ERROR",
				},
			},
		},
	}
	groups := map[string]map[string]interface{}{}

	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelError, level)

	// Different environment falls through to base.
	level = resolveLoggerLevel("my.logger", "staging", loggers, groups)
	assert.Equal(t, LogLevelDebug, level)
}

func TestResolveLoggerLevel_GroupLevel(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"group":        "group-1",
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{
		"group-1": {
			"level":        "ERROR",
			"environments": map[string]interface{}{},
		},
	}

	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelError, level)
}

func TestResolveLoggerLevel_GroupEnvironmentLevel(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"group":        "group-1",
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{
		"group-1": {
			"level": "WARN",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"level": "FATAL",
				},
			},
		},
	}

	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelFatal, level)
}

func TestResolveLoggerLevel_GroupChain(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"group":        "child-group",
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{
		"child-group": {
			"group":        "parent-group",
			"environments": map[string]interface{}{},
		},
		"parent-group": {
			"level":        "TRACE",
			"environments": map[string]interface{}{},
		},
	}

	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelTrace, level)
}

func TestResolveLoggerLevel_GroupCycleDetection(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"group":        "group-a",
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{
		"group-a": {
			"group":        "group-b",
			"environments": map[string]interface{}{},
		},
		"group-b": {
			"group":        "group-a",
			"environments": map[string]interface{}{},
		},
	}

	// Should not infinite loop; falls through to dot-notation ancestry, then INFO.
	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelInfo, level)
}

func TestResolveLoggerLevel_DotNotationAncestry(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"com.acme.payments": {
			"environments": map[string]interface{}{},
		},
		"com.acme": {
			"level":        "DEBUG",
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{}

	level := resolveLoggerLevel("com.acme.payments", "production", loggers, groups)
	assert.Equal(t, LogLevelDebug, level)
}

func TestResolveLoggerLevel_DotNotationAncestry_DeepChain(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"com.acme.payments.stripe": {
			"environments": map[string]interface{}{},
		},
		"com": {
			"level":        "WARN",
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{}

	// "com.acme.payments.stripe" -> "com.acme.payments" (not found) -> "com.acme" (not found) -> "com" (has WARN)
	level := resolveLoggerLevel("com.acme.payments.stripe", "production", loggers, groups)
	assert.Equal(t, LogLevelWarn, level)
}

func TestResolveLoggerLevel_Fallback(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{}

	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelInfo, level)
}

func TestResolveLoggerLevel_UnknownLogger(t *testing.T) {
	loggers := map[string]map[string]interface{}{}
	groups := map[string]map[string]interface{}{}

	level := resolveLoggerLevel("nonexistent", "production", loggers, groups)
	assert.Equal(t, LogLevelInfo, level)
}

func TestResolveLoggerLevel_EmptyEnvironment(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"level": "DEBUG",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"level": "ERROR",
				},
			},
		},
	}
	groups := map[string]map[string]interface{}{}

	// Empty environment string should skip env-level check.
	level := resolveLoggerLevel("my.logger", "", loggers, groups)
	assert.Equal(t, LogLevelDebug, level)
}

func TestResolveLoggerLevel_GroupNotFound(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"group":        "nonexistent-group",
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{}

	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelInfo, level)
}

func TestResolveLoggerLevel_EnvironmentLevelEmptyString(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"level": "WARN",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"level": "",
				},
			},
		},
	}
	groups := map[string]map[string]interface{}{}

	// Empty string env level should be skipped, fall through to base level.
	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelWarn, level)
}

func TestResolveLoggerLevel_GroupEnvironmentEmptyString(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"group":        "group-1",
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{
		"group-1": {
			"level": "WARN",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"level": "",
				},
			},
		},
	}

	// Empty string group env level should be skipped, fall through to group base.
	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelWarn, level)
}

func TestResolveLoggerLevel_GroupBaseEmptyString(t *testing.T) {
	loggers := map[string]map[string]interface{}{
		"my.logger": {
			"group":        "group-1",
			"environments": map[string]interface{}{},
		},
	}
	groups := map[string]map[string]interface{}{
		"group-1": {
			"level":        "",
			"environments": map[string]interface{}{},
		},
	}

	// Empty string group base level should fall through to INFO.
	level := resolveLoggerLevel("my.logger", "production", loggers, groups)
	assert.Equal(t, LogLevelInfo, level)
}

// --- unflattenDotNotation tests ---

func TestUnflattenDotNotation(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name: "simple dotted key",
			input: map[string]interface{}{
				"database.host": "localhost",
			},
			expected: map[string]interface{}{
				"database": map[string]interface{}{
					"host": "localhost",
				},
			},
		},
		{
			name: "deeply nested key",
			input: map[string]interface{}{
				"a.b.c": "deep",
			},
			expected: map[string]interface{}{
				"a": map[string]interface{}{
					"b": map[string]interface{}{
						"c": "deep",
					},
				},
			},
		},
		{
			name: "no dots passthrough",
			input: map[string]interface{}{
				"simple": "value",
			},
			expected: map[string]interface{}{
				"simple": "value",
			},
		},
		{
			name:     "empty map",
			input:    map[string]interface{}{},
			expected: map[string]interface{}{},
		},
		{
			name: "multiple keys same prefix",
			input: map[string]interface{}{
				"database.host": "localhost",
				"database.port": 5432,
			},
			expected: map[string]interface{}{
				"database": map[string]interface{}{
					"host": "localhost",
					"port": 5432,
				},
			},
		},
		{
			name: "conflict: dotted key overwrites scalar",
			input: map[string]interface{}{
				"db.host": "localhost",
			},
			expected: map[string]interface{}{
				"db": map[string]interface{}{
					"host": "localhost",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := unflattenDotNotation(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// --- LoggingClient accessor test (internal) ---

func TestLoggingClient_Accessor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	httpClient := &http.Client{}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: "sk_test", base: base}

	genLoggingClient, _ := genlogging.NewClient(server.URL, genlogging.WithHTTPClient(httpClient))

	c := &Client{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		baseURL:     server.URL,
		httpClient:  httpClient,
	}
	c.logging = newLoggingClient(c, genLoggingClient)

	logging := c.Logging()
	require.NotNil(t, logging)
	assert.Same(t, logging, c.Logging())
}

// --- Logger.apply tests ---

func TestLogger_Apply(t *testing.T) {
	logger := &Logger{
		ID:   "old-id",
		Name: "Old Name",
	}
	level := LogLevelDebug
	groupID := "group-123"
	other := &Logger{
		ID:           "new-id",
		Name:         "New Name",
		Level:        &level,
		Group:        &groupID,
		Managed:      false,
		Environments: map[string]interface{}{"prod": "data"},
	}
	logger.apply(other)

	assert.Equal(t, "new-id", logger.ID)
	assert.Equal(t, "New Name", logger.Name)
	require.NotNil(t, logger.Level)
	assert.Equal(t, LogLevelDebug, *logger.Level)
	require.NotNil(t, logger.Group)
	assert.Equal(t, "group-123", *logger.Group)
	assert.False(t, logger.Managed)
}

// --- LogGroup.apply tests ---

func TestLogGroup_Apply(t *testing.T) {
	group := &LogGroup{
		ID:   "old-id",
		Name: "Old Name",
	}
	level := LogLevelError
	parentID := "parent-123"
	other := &LogGroup{
		ID:           "new-id",
		Name:         "New Name",
		Level:        &level,
		Group:        &parentID,
		Environments: map[string]interface{}{"staging": "data"},
	}
	group.apply(other)

	assert.Equal(t, "new-id", group.ID)
	assert.Equal(t, "New Name", group.Name)
	require.NotNil(t, group.Level)
	assert.Equal(t, LogLevelError, *group.Level)
	require.NotNil(t, group.Group)
	assert.Equal(t, "parent-123", *group.Group)
}

// --- loggerRegistrationBuffer tests ---

func TestLoggerRegistrationBuffer_AddAndDrain(t *testing.T) {
	buf := newLoggerRegistrationBuffer()

	buf.add("logger-a", "INFO", "my-service")
	buf.add("logger-b", "DEBUG", "my-service")
	// Duplicate should be ignored.
	buf.add("logger-a", "WARN", "other-service")

	batch := buf.drain()
	require.Len(t, batch, 2)
	assert.Equal(t, "logger-a", batch[0].key)
	assert.Equal(t, "INFO", batch[0].level)
	assert.Equal(t, "logger-b", batch[1].key)

	// Second drain should be empty.
	batch = buf.drain()
	assert.Empty(t, batch)
}

func TestLoggerRegistrationBuffer_DrainEmpty(t *testing.T) {
	buf := newLoggerRegistrationBuffer()
	batch := buf.drain()
	assert.Empty(t, batch)
}

// --- buildLoggerAttributes tests ---

func TestBuildLoggerAttributes_WithLevel(t *testing.T) {
	level := LogLevelDebug
	logger := &Logger{
		ID:           "test",
		Name:         "Test",
		Level:        &level,
		Managed:      true,
		Environments: map[string]interface{}{"prod": "data"},
		Sources:      []map[string]interface{}{{"service": "my-svc"}},
	}

	attrs := buildLoggerAttributes(logger)
	require.NotNil(t, attrs.Id)
	assert.Equal(t, "test", *attrs.Id)
	require.NotNil(t, attrs.Level)
	assert.Equal(t, "DEBUG", *attrs.Level)
	require.NotNil(t, attrs.Managed)
	assert.True(t, *attrs.Managed)
	require.NotNil(t, attrs.Environments)
	require.NotNil(t, attrs.Sources)
}

func TestBuildLoggerAttributes_NilLevel(t *testing.T) {
	logger := &Logger{
		ID:      "test",
		Name:    "Test",
		Managed: true,
	}

	attrs := buildLoggerAttributes(logger)
	assert.Nil(t, attrs.Level)
}

func TestBuildLoggerAttributes_NilEnvironments(t *testing.T) {
	logger := &Logger{
		ID:      "test",
		Name:    "Test",
		Managed: true,
	}

	attrs := buildLoggerAttributes(logger)
	assert.Nil(t, attrs.Environments)
}

func TestBuildLoggerAttributes_NilSources(t *testing.T) {
	logger := &Logger{
		ID:      "test",
		Name:    "Test",
		Managed: true,
	}

	attrs := buildLoggerAttributes(logger)
	assert.Nil(t, attrs.Sources)
}

// --- buildLogGroupAttributes tests ---

func TestBuildLogGroupAttributes_WithLevel(t *testing.T) {
	level := LogLevelWarn
	parentID := "parent-id"
	group := &LogGroup{
		ID:           "infra",
		Name:         "Infra",
		Level:        &level,
		Group:        &parentID,
		Environments: map[string]interface{}{"prod": "data"},
	}

	attrs := buildLogGroupAttributes(group)
	require.NotNil(t, attrs.Id)
	assert.Equal(t, "infra", *attrs.Id)
	require.NotNil(t, attrs.Level)
	assert.Equal(t, "WARN", *attrs.Level)
	require.NotNil(t, attrs.Group)
	assert.Equal(t, "parent-id", *attrs.Group)
	require.NotNil(t, attrs.Environments)
}

func TestBuildLogGroupAttributes_NilLevel(t *testing.T) {
	group := &LogGroup{
		ID:   "infra",
		Name: "Infra",
	}

	attrs := buildLogGroupAttributes(group)
	assert.Nil(t, attrs.Level)
}

func TestBuildLogGroupAttributes_NilEnvironments(t *testing.T) {
	group := &LogGroup{
		ID:   "infra",
		Name: "Infra",
	}

	attrs := buildLogGroupAttributes(group)
	assert.Nil(t, attrs.Environments)
}

// --- fireChangeListeners tests ---

func TestFireChangeListeners_EmptyKey(t *testing.T) {
	c := &LoggingClient{
		loggersCache: make(map[string]map[string]interface{}),
		groupsCache:  make(map[string]map[string]interface{}),
		keyListeners: make(map[string][]func(*LoggerChangeEvent)),
	}
	c.client = &Client{environment: "test"}

	var called bool
	c.globalListeners = append(c.globalListeners, func(evt *LoggerChangeEvent) {
		called = true
	})

	// Empty key should be a no-op.
	c.fireChangeListeners("", "websocket")
	assert.False(t, called)
}

func TestFireChangeListeners_GlobalAndKeyListeners(t *testing.T) {
	c := &LoggingClient{
		loggersCache: map[string]map[string]interface{}{
			"my.logger": {
				"level":        "WARN",
				"environments": map[string]interface{}{},
			},
		},
		groupsCache:  make(map[string]map[string]interface{}),
		keyListeners: make(map[string][]func(*LoggerChangeEvent)),
	}
	c.client = &Client{environment: "test"}

	var globalEvent *LoggerChangeEvent
	var keyEvent *LoggerChangeEvent
	c.globalListeners = append(c.globalListeners, func(evt *LoggerChangeEvent) {
		globalEvent = evt
	})
	c.keyListeners["my.logger"] = append(c.keyListeners["my.logger"], func(evt *LoggerChangeEvent) {
		keyEvent = evt
	})

	c.fireChangeListeners("my.logger", "websocket")

	require.NotNil(t, globalEvent)
	assert.Equal(t, "my.logger", globalEvent.ID)
	assert.Equal(t, "websocket", globalEvent.Source)
	require.NotNil(t, globalEvent.Level)
	assert.Equal(t, LogLevelWarn, *globalEvent.Level)

	require.NotNil(t, keyEvent)
	assert.Equal(t, "my.logger", keyEvent.ID)
}

func TestFireChangeListeners_PanicRecovery(t *testing.T) {
	c := &LoggingClient{
		loggersCache: map[string]map[string]interface{}{
			"my.logger": {
				"level":        "INFO",
				"environments": map[string]interface{}{},
			},
		},
		groupsCache:  make(map[string]map[string]interface{}),
		keyListeners: make(map[string][]func(*LoggerChangeEvent)),
	}
	c.client = &Client{environment: "test"}

	var secondCalled bool
	c.globalListeners = append(c.globalListeners, func(evt *LoggerChangeEvent) {
		panic("bad listener")
	})
	c.globalListeners = append(c.globalListeners, func(evt *LoggerChangeEvent) {
		secondCalled = true
	})

	// Should not panic.
	c.fireChangeListeners("my.logger", "websocket")
	assert.True(t, secondCalled)
}

func TestFireChangeListeners_KeyPanicRecovery(t *testing.T) {
	c := &LoggingClient{
		loggersCache: map[string]map[string]interface{}{
			"my.logger": {
				"level":        "INFO",
				"environments": map[string]interface{}{},
			},
		},
		groupsCache:  make(map[string]map[string]interface{}),
		keyListeners: make(map[string][]func(*LoggerChangeEvent)),
	}
	c.client = &Client{environment: "test"}

	var secondCalled bool
	c.keyListeners["my.logger"] = append(c.keyListeners["my.logger"], func(evt *LoggerChangeEvent) {
		panic("bad key listener")
	})
	c.keyListeners["my.logger"] = append(c.keyListeners["my.logger"], func(evt *LoggerChangeEvent) {
		secondCalled = true
	})

	c.fireChangeListeners("my.logger", "websocket")
	assert.True(t, secondCalled)
}

func TestFireChangeListeners_LoggerNotInCache(t *testing.T) {
	c := &LoggingClient{
		loggersCache: make(map[string]map[string]interface{}),
		groupsCache:  make(map[string]map[string]interface{}),
		keyListeners: make(map[string][]func(*LoggerChangeEvent)),
	}
	c.client = &Client{environment: "test"}

	var event *LoggerChangeEvent
	c.globalListeners = append(c.globalListeners, func(evt *LoggerChangeEvent) {
		event = evt
	})

	c.fireChangeListeners("unknown.logger", "websocket")

	// Should fire with nil level since logger is not in cache.
	require.NotNil(t, event)
	assert.Equal(t, "unknown.logger", event.ID)
	assert.Nil(t, event.Level)
}

// --- LoggingClient.close tests ---

func TestLoggingClient_Close_NilFlushDone(t *testing.T) {
	c := &LoggingClient{
		loggersCache: make(map[string]map[string]interface{}),
		groupsCache:  make(map[string]map[string]interface{}),
		keyListeners: make(map[string][]func(*LoggerChangeEvent)),
		buffer:       newLoggerRegistrationBuffer(),
	}
	// flushDone is nil — should not panic.
	c.close()
}

func TestLoggingClient_Close_WithFlushDone(t *testing.T) {
	c := &LoggingClient{
		loggersCache: make(map[string]map[string]interface{}),
		groupsCache:  make(map[string]map[string]interface{}),
		keyListeners: make(map[string][]func(*LoggerChangeEvent)),
		buffer:       newLoggerRegistrationBuffer(),
		flushDone:    make(chan struct{}),
	}
	c.close()
	assert.Nil(t, c.flushDone)
}

// --- fetchAndCache tests ---

func TestFetchAndCache(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": [{
			"id": "my.logger",
			"type": "logger",
			"attributes": {
				"id": "my.logger",
				"name": "My Logger",
				"level": "WARN",
				"group": "group-id",
				"managed": true,
				"environments": {"production": {"level": "ERROR"}},
				"sources": [{"service": "test-service"}]
			}
		}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": [{
			"id": "infra",
			"type": "log_group",
			"attributes": {
				"id": "infra",
				"name": "Infra",
				"level": "ERROR",
				"group": "parent-group-id",
				"environments": {"staging": {"level": "DEBUG"}}
			}
		}]}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

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

	c := &Client{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		httpClient:  httpClient,
	}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.fetchAndCache(context.Background())
	require.NoError(t, err)

	// Verify logger cache.
	require.Contains(t, lc.loggersCache, "my.logger")
	loggerEntry := lc.loggersCache["my.logger"]
	assert.Equal(t, "WARN", loggerEntry["level"])
	assert.Equal(t, "group-id", loggerEntry["group"])
	assert.Equal(t, true, loggerEntry["managed"])

	// Verify group cache.
	require.Contains(t, lc.groupsCache, "infra")
	groupEntry := lc.groupsCache["infra"]
	assert.Equal(t, "ERROR", groupEntry["level"])
	assert.Equal(t, "parent-group-id", groupEntry["group"])
}

func TestFetchAndCache_LoggerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	httpClient := &http.Client{}
	genLoggingClient, _ := genlogging.NewClient(server.URL, genlogging.WithHTTPClient(httpClient))

	c := &Client{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		httpClient:  httpClient,
	}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.fetchAndCache(context.Background())
	require.Error(t, err)
}

func TestFetchAndCache_GroupError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	httpClient := &http.Client{}
	genLoggingClient, _ := genlogging.NewClient(server.URL, genlogging.WithHTTPClient(httpClient))

	c := &Client{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		httpClient:  httpClient,
	}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.fetchAndCache(context.Background())
	require.Error(t, err)
}

// --- fetchAndCache with nil level and nil group ---

func TestFetchAndCache_NilLevelAndGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": [{
			"id": "my.logger",
			"type": "logger",
			"attributes": {
				"id": "my.logger",
				"name": "My Logger",
				"managed": true,
				"environments": {}
			}
		}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": [{
			"id": "infra",
			"type": "log_group",
			"attributes": {
				"id": "infra",
				"name": "Infra",
				"environments": {}
			}
		}]}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	httpClient := &http.Client{}
	genLoggingClient, _ := genlogging.NewClient(server.URL, genlogging.WithHTTPClient(httpClient))

	c := &Client{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		httpClient:  httpClient,
	}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.fetchAndCache(context.Background())
	require.NoError(t, err)

	// Logger cache should not have "level" or "group" keys.
	loggerEntry := lc.loggersCache["my.logger"]
	_, hasLevel := loggerEntry["level"]
	_, hasGroup := loggerEntry["group"]
	assert.False(t, hasLevel)
	assert.False(t, hasGroup)

	// Group cache should not have "level" or "group" keys.
	groupEntry := lc.groupsCache["infra"]
	_, hasLevel = groupEntry["level"]
	_, hasGroup = groupEntry["group"]
	assert.False(t, hasLevel)
	assert.False(t, hasGroup)
}

// --- newTestLoggingClient helper ---

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

	c := &Client{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		baseURL:     server.URL,
		httpClient:  httpClient,
	}
	lc := newLoggingClient(c, genLoggingClient)
	return lc
}

// --- deleteLoggerByID error paths ---

func TestDeleteLoggerByID_CheckStatusError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	err := lc.deleteLoggerByID(context.Background(), "my-logger")
	require.Error(t, err)
}

func TestDeleteLoggerByID_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	serverURL := server.URL
	server.Close()

	httpClient := &http.Client{}
	genLoggingClient, _ := genlogging.NewClient(serverURL, genlogging.WithHTTPClient(httpClient))
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.deleteLoggerByID(context.Background(), "my-logger")
	require.Error(t, err)
}

// brokenBodyTransportLogging wraps an HTTP transport and returns a broken response body.
type brokenBodyTransportLogging struct {
	statusCode int
}

func (t *brokenBodyTransportLogging) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.statusCode,
		Body:       io.NopCloser(&brokenReaderLogging{}),
		Header:     make(http.Header),
	}, nil
}

type brokenReaderLogging struct{}

func (b *brokenReaderLogging) Read(p []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestDeleteLoggerByID_BodyReadFailure(t *testing.T) {
	httpClient := &http.Client{
		Transport: &brokenBodyTransportLogging{statusCode: 204},
	}
	genLoggingClient, _ := genlogging.NewClient("http://localhost",
		genlogging.WithHTTPClient(httpClient),
	)
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.deleteLoggerByID(context.Background(), "my-logger")
	require.Error(t, err)
}

// --- deleteGroupByID error paths ---

func TestDeleteGroupByID_CheckStatusError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	err := lc.deleteGroupByID(context.Background(), "my-group")
	require.Error(t, err)
}

func TestDeleteGroupByID_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	serverURL := server.URL
	server.Close()

	httpClient := &http.Client{}
	genLoggingClient, _ := genlogging.NewClient(serverURL, genlogging.WithHTTPClient(httpClient))
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.deleteGroupByID(context.Background(), "my-group")
	require.Error(t, err)
}

func TestDeleteGroupByID_BodyReadFailure(t *testing.T) {
	httpClient := &http.Client{
		Transport: &brokenBodyTransportLogging{statusCode: 204},
	}
	genLoggingClient, _ := genlogging.NewClient("http://localhost",
		genlogging.WithHTTPClient(httpClient),
	)
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.deleteGroupByID(context.Background(), "my-group")
	require.Error(t, err)
}

// --- resourceToLogger nil optional fields ---

func TestResourceToLogger_NilOptionalFields(t *testing.T) {
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, nil)

	r := genlogging.LoggerResource{
		Attributes: genlogging.Logger{
			Name: "Test Logger",
			// Id, Level, Managed, Sources, Environments all nil
		},
	}

	logger := resourceToLogger(r, lc)
	assert.Equal(t, "", logger.ID)
	assert.Nil(t, logger.Level)
	assert.True(t, logger.Managed) // default true when Managed is nil
	assert.Nil(t, logger.Sources)
	assert.NotNil(t, logger.Environments) // defaults to empty map when nil
}

func TestResourceToLogger_EmptyLevel(t *testing.T) {
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, nil)

	emptyLevel := ""
	r := genlogging.LoggerResource{
		Attributes: genlogging.Logger{
			Name:  "Test Logger",
			Level: &emptyLevel,
		},
	}

	logger := resourceToLogger(r, lc)
	assert.Nil(t, logger.Level) // empty string level treated as nil
}

func TestResourceToLogger_ManagedFalse(t *testing.T) {
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, nil)

	managedFalse := false
	r := genlogging.LoggerResource{
		Attributes: genlogging.Logger{
			Name:    "Test Logger",
			Managed: &managedFalse,
		},
	}

	logger := resourceToLogger(r, lc)
	assert.False(t, logger.Managed)
}

// --- resourceToLogGroup nil optional fields ---

func TestResourceToLogGroup_NilOptionalFields(t *testing.T) {
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, nil)

	r := genlogging.LogGroupResource{
		Attributes: genlogging.LogGroup{
			Name: "Test Group",
			// Id, Level, Environments all nil
		},
	}

	group := resourceToLogGroup(r, lc)
	assert.Equal(t, "", group.ID)
	assert.Nil(t, group.Level)
	assert.NotNil(t, group.Environments) // defaults to empty map when nil
}

func TestResourceToLogGroup_EmptyLevel(t *testing.T) {
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, nil)

	emptyLevel := ""
	r := genlogging.LogGroupResource{
		Attributes: genlogging.LogGroup{
			Name:  "Test Group",
			Level: &emptyLevel,
		},
	}

	group := resourceToLogGroup(r, lc)
	assert.Nil(t, group.Level) // empty string level treated as nil
}

// --- flushBuffer ---

func TestFlushBuffer_Empty(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Should not make any requests when buffer is empty
	lc.flushBuffer(context.Background())
}

func TestFlushBuffer_WithEntries(t *testing.T) {
	var receivedBody map[string]interface{}

	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			b := make([]byte, 4096)
			n, _ := r.Body.Read(b)
			_ = json.Unmarshal(b[:n], &receivedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":2}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	lc.buffer.add("app.logger", "INFO", "my-service")
	lc.buffer.add("db.logger", "DEBUG", "")

	lc.flushBuffer(context.Background())

	require.NotNil(t, receivedBody)
	loggers := receivedBody["loggers"].([]interface{})
	assert.Len(t, loggers, 2)
}

func TestFlushBuffer_WithService(t *testing.T) {
	var receivedBody map[string]interface{}

	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			b := make([]byte, 4096)
			n, _ := r.Body.Read(b)
			_ = json.Unmarshal(b[:n], &receivedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	lc.buffer.add("app.logger", "INFO", "my-service")
	lc.flushBuffer(context.Background())

	require.NotNil(t, receivedBody)
	loggers := receivedBody["loggers"].([]interface{})
	first := loggers[0].(map[string]interface{})
	assert.Equal(t, "my-service", first["service"])
}

// --- periodicFlush ---

func TestPeriodicFlush_Stops(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	done := make(chan struct{})

	// Start and immediately stop
	go lc.periodicFlush(done)
	time.Sleep(10 * time.Millisecond)
	close(done)

	// Give goroutine time to exit
	time.Sleep(50 * time.Millisecond)
}

func TestPeriodicFlush_TickerFires(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test that waits for 5s ticker")
	}

	var flushCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		flushCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":1}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	lc := newTestLoggingClient(t, mux)

	// Add loggers to the buffer so the flush has something to send
	lc.buffer.add("ticker.logger", "INFO", "my-service")

	done := make(chan struct{})
	go lc.periodicFlush(done)

	// Wait for the 5-second ticker to fire at least once
	time.Sleep(6 * time.Second)
	close(done)

	assert.GreaterOrEqual(t, flushCount.Load(), int32(1), "periodic flush ticker should have fired at least once")
}

// --- handleLoggerChanged ---

func TestHandleLoggerChanged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"my.logger","type":"logger","attributes":{"id":"my.logger","name":"My Logger","level":"WARN","managed":true,"environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	lc := newTestLoggingClient(t, mux)

	var received *LoggerChangeEvent
	lc.OnChange(func(evt *LoggerChangeEvent) {
		received = evt
	})

	lc.handleLoggerChanged(map[string]interface{}{"id": "my.logger"})

	require.NotNil(t, received)
	assert.Equal(t, "my.logger", received.ID)
	assert.Equal(t, "websocket", received.Source)
}

func TestHandleLoggerChanged_FetchError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	var called bool
	lc.OnChange(func(evt *LoggerChangeEvent) {
		called = true
	})

	// Should not panic; error causes early return
	lc.handleLoggerChanged(map[string]interface{}{"id": "my.logger"})
	assert.False(t, called)
}

// --- Logger.SetEnvironmentLevel with nil Environments ---

func TestLoggerSetEnvironmentLevel_NilEnvironments(t *testing.T) {
	l := &Logger{
		Environments: nil,
	}

	l.SetEnvironmentLevel("production", LogLevelError)

	require.NotNil(t, l.Environments)
	envData := l.Environments["production"].(map[string]interface{})
	assert.Equal(t, "ERROR", envData["level"])
}

// --- LogGroup.SetEnvironmentLevel with nil Environments ---

func TestLogGroupSetEnvironmentLevel_NilEnvironments(t *testing.T) {
	g := &LogGroup{
		Environments: nil,
	}

	g.SetEnvironmentLevel("production", LogLevelWarn)

	require.NotNil(t, g.Environments)
	envData := g.Environments["production"].(map[string]interface{})
	assert.Equal(t, "WARN", envData["level"])
}

// --- Start ---

func TestStart_Basic(t *testing.T) {
	var bulkCalled atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			bulkCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":0}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"my.logger","type":"logger","attributes":{"id":"my.logger","name":"My Logger","level":"INFO","managed":true,"environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		bulkCalled.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/ws/v1/events", func(w http.ResponseWriter, r *http.Request) {
		// Return 200 OK; the real WS upgrade won't happen but Start handles this
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

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

	c := &Client{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		baseURL:     server.URL,
		httpClient:  httpClient,
	}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.Start(context.Background())
	require.NoError(t, err)
	assert.True(t, lc.started)

	// Clean up
	lc.close()
	c.stopWS()
}

func TestStart_FetchError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":0}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"fail"}]}`))
	})
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	httpClient := &http.Client{}
	genLoggingClient, _ := genlogging.NewClient(server.URL, genlogging.WithHTTPClient(httpClient))

	c := &Client{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		baseURL:     server.URL,
		httpClient:  httpClient,
	}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.Start(context.Background())
	require.Error(t, err)
	assert.False(t, lc.started)
}

func TestStart_Idempotent(t *testing.T) {
	var callCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loggers/bulk" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":0}`))
			return
		}
		callCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"my.logger","type":"logger","attributes":{"id":"my.logger","name":"My Logger","managed":true,"environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

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

	c := &Client{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		baseURL:     server.URL,
		httpClient:  httpClient,
	}
	lc := newLoggingClient(c, genLoggingClient)

	err := lc.Start(context.Background())
	require.NoError(t, err)

	// Second call should be no-op
	err = lc.Start(context.Background())
	require.NoError(t, err)

	// List loggers should have been called only once (during first Start)
	assert.Equal(t, int32(1), callCount.Load())

	lc.close()
	c.stopWS()
}
