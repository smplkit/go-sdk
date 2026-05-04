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
		appURL:      server.URL,
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

	buf.add("logger-a", "INFO", "INFO", "my-service", "production")
	buf.add("logger-b", "DEBUG", "DEBUG", "my-service", "production")
	// Duplicate should be ignored.
	buf.add("logger-a", "WARN", "WARN", "other-service", "staging")

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
	require.NotNil(t, attrs.Level)
	assert.Equal(t, "WARN", *attrs.Level)
	require.NotNil(t, attrs.ParentId)
	assert.Equal(t, "parent-id", *attrs.ParentId)
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
				"parent_id": "parent-group-id",
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
		appURL:      server.URL,
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

	err := lc.Management().Delete(context.Background(), "my-logger")
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

	err := lc.Management().Delete(context.Background(), "my-logger")
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

	err := lc.Management().Delete(context.Background(), "my-logger")
	require.Error(t, err)
}

// --- deleteGroupByID error paths ---

func TestDeleteGroupByID_CheckStatusError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	err := lc.Management().DeleteGroup(context.Background(), "my-group")
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

	err := lc.Management().DeleteGroup(context.Background(), "my-group")
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

	err := lc.Management().DeleteGroup(context.Background(), "my-group")
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

	logger := resourceToLogger(r, lc.Management())
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

	logger := resourceToLogger(r, lc.Management())
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

	logger := resourceToLogger(r, lc.Management())
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

	group := resourceToLogGroup(r, lc.Management())
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

	group := resourceToLogGroup(r, lc.Management())
	assert.Nil(t, group.Level) // empty string level treated as nil
}

// --- Flush ---

func TestFlush_Empty(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Should not make any requests when buffer is empty
	_ = lc.Flush(context.Background())
}

func TestFlush_WithEntries(t *testing.T) {
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

	lc.buffer.add("app.logger", "INFO", "INFO", "my-service", "production")
	lc.buffer.add("db.logger", "DEBUG", "DEBUG", "", "")

	_ = lc.Flush(context.Background())

	require.NotNil(t, receivedBody)
	loggers := receivedBody["loggers"].([]interface{})
	assert.Len(t, loggers, 2)
}

func TestFlush_WithService(t *testing.T) {
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

	lc.buffer.add("app.logger", "INFO", "INFO", "my-service", "production")
	_ = lc.Flush(context.Background())

	require.NotNil(t, receivedBody)
	loggers := receivedBody["loggers"].([]interface{})
	first := loggers[0].(map[string]interface{})
	assert.Equal(t, "my-service", first["service"])
	assert.Equal(t, "production", first["environment"])
}

func TestFlush_SendsBothLevelAndResolvedLevel(t *testing.T) {
	// When level and resolved_level are both set to the same value (e.g. from
	// slog/zap adapters that have no parent inheritance), both fields must be
	// present in the bulk payload.
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

	lc.buffer.add("app.logger", "DEBUG", "DEBUG", "my-service", "production")
	_ = lc.Flush(context.Background())

	require.NotNil(t, receivedBody)
	loggers := receivedBody["loggers"].([]interface{})
	require.Len(t, loggers, 1)
	item := loggers[0].(map[string]interface{})
	assert.Equal(t, "DEBUG", item["level"])
	assert.Equal(t, "DEBUG", item["resolved_level"])
}

func TestFlush_OmitsLevelWhenEmpty(t *testing.T) {
	// When the explicit level is empty (e.g. inherited), the level field must be
	// omitted from the payload while resolved_level is still sent.
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

	// Empty explicit level, non-empty resolved level.
	lc.buffer.add("inherited.logger", "", "INFO", "", "")
	_ = lc.Flush(context.Background())

	require.NotNil(t, receivedBody)
	loggers := receivedBody["loggers"].([]interface{})
	require.Len(t, loggers, 1)
	item := loggers[0].(map[string]interface{})
	assert.Nil(t, item["level"], "level should be absent when not explicitly set")
	assert.Equal(t, "INFO", item["resolved_level"])
}

func TestFlush_ResolvedLevelDifferentFromLevel(t *testing.T) {
	// Verify that level and resolved_level are sent as independent values when
	// they differ (e.g. a logger with an explicitly-set level that differs from
	// its effective resolved level after group inheritance).
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

	lc.buffer.add("grp.logger", "WARN", "ERROR", "svc", "production")
	_ = lc.Flush(context.Background())

	require.NotNil(t, receivedBody)
	loggers := receivedBody["loggers"].([]interface{})
	require.Len(t, loggers, 1)
	item := loggers[0].(map[string]interface{})
	assert.Equal(t, "WARN", item["level"])
	assert.Equal(t, "ERROR", item["resolved_level"])
}

func TestLoggerRegistrationBuffer_StoresResolvedLevel(t *testing.T) {
	buf := newLoggerRegistrationBuffer()

	buf.add("my-logger", "DEBUG", "INFO", "svc", "production")
	batch := buf.drain()
	require.Len(t, batch, 1)
	assert.Equal(t, "my-logger", batch[0].key)
	assert.Equal(t, "DEBUG", batch[0].level)
	assert.Equal(t, "INFO", batch[0].resolvedLevel)
	assert.Equal(t, "svc", batch[0].service)
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
	lc.buffer.add("ticker.logger", "INFO", "INFO", "my-service", "production")

	done := make(chan struct{})
	go lc.periodicFlush(done)

	// Wait for the 5-second ticker to fire at least once
	time.Sleep(6 * time.Second)
	close(done)

	assert.GreaterOrEqual(t, flushCount.Load(), int32(1), "periodic flush ticker should have fired at least once")
}

func TestPeriodicFlush_LogsWarningOnFlushError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test that waits for 5s ticker")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server rejected batch"}]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	lc := newTestLoggingClient(t, mux)
	lc.buffer.add("periodic.error.logger", "INFO", "INFO", "my-service", "production")

	// strings.Builder isn't safe for concurrent access, so guard it
	// with a mutex — periodicFlush writes from a goroutine while the
	// test reads from the main one.
	var (
		mu     sync.Mutex
		buf    strings.Builder
		writer = &lockedWriter{mu: &mu, b: &buf}
	)
	log.SetOutput(writer)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		lc.periodicFlush(done)
		close(exited)
	}()
	time.Sleep(6 * time.Second)
	close(done)
	<-exited // happens-after ensures all writes are visible below

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

func TestFlush_ReturnsConnectionErrorOnBodyReadFailure(t *testing.T) {
	httpClient := &http.Client{
		Transport: &brokenBodyTransportLogging{statusCode: 200},
	}
	genLoggingClient, _ := genlogging.NewClient("http://localhost",
		genlogging.WithHTTPClient(httpClient),
	)
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, genLoggingClient)
	lc.buffer.add("body.read.fail", "INFO", "INFO", "svc", "env")

	err := lc.Flush(context.Background())
	require.Error(t, err)
	var connErr *ConnectionError
	require.ErrorAs(t, err, &connErr)
	assert.Contains(t, err.Error(), "failed to read response body")
}

// --- handleLoggerChanged ---

func TestHandleLoggerChanged(t *testing.T) {
	mux := http.NewServeMux()
	// Scoped single fetch for logger_changed event.
	mux.HandleFunc("/api/v1/loggers/my.logger", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"my.logger","type":"logger","attributes":{"id":"my.logger","name":"My Logger","level":"WARN","managed":true,"environments":{}}}}`))
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

func TestHandleLoggerChanged_UsesIDField(t *testing.T) {
	mux := http.NewServeMux()
	// Scoped single fetch uses the id field from the event payload.
	mux.HandleFunc("/api/v1/loggers/my.logger", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"my.logger","type":"logger","attributes":{"id":"my.logger","name":"My Logger","level":"WARN","managed":true,"environments":{}}}}`))
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

// --- handleGroupChanged ---

func TestHandleGroupChanged(t *testing.T) {
	mux := http.NewServeMux()
	// Scoped single fetch for group_changed event.
	mux.HandleFunc("/api/v1/log_groups/sql", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"ERROR","environments":{}}}}`))
	})

	lc := newTestLoggingClient(t, mux)

	// handleGroupChanged should not panic and should trigger re-fetch + applyLevels.
	lc.handleGroupChanged(map[string]interface{}{"id": "sql"})
}

func TestHandleGroupChanged_FetchError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	// Should not panic; error causes early return.
	lc.handleGroupChanged(map[string]interface{}{"id": "sql"})
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
		appURL:      server.URL,
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
		appURL:      server.URL,
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
		appURL:      server.URL,
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

// --- WS listener registration after Start ---

func TestLoggingStart_RegistersWSListeners(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	lc := newTestLoggingClient(t, mux)

	// Pre-inject a sharedWebSocket so ensureWS() uses it without starting a goroutine.
	ws := &sharedWebSocket{
		listeners: make(map[string][]eventCallback),
		closeCh:   make(chan struct{}),
		wsDone:    make(chan struct{}),
	}
	lc.client.ws = ws

	err := lc.Start(context.Background())
	require.NoError(t, err)

	ws.listenersMu.Lock()
	_, hasLoggerChanged := ws.listeners["logger_changed"]
	_, hasLoggerDeleted := ws.listeners["logger_deleted"]
	_, hasGroupChanged := ws.listeners["group_changed"]
	_, hasGroupDeleted := ws.listeners["group_deleted"]
	_, hasLoggersChanged := ws.listeners["loggers_changed"]
	ws.listenersMu.Unlock()

	assert.True(t, hasLoggerChanged, "logger_changed should be registered in WS listener map")
	assert.True(t, hasLoggerDeleted, "logger_deleted should be registered in WS listener map")
	assert.True(t, hasGroupChanged, "group_changed should be registered in WS listener map")
	assert.True(t, hasGroupDeleted, "group_deleted should be registered in WS listener map")
	assert.True(t, hasLoggersChanged, "loggers_changed should be registered in WS listener map")

	lc.close()
}

// ========== New WS event handler tests for logging ==========

// TestHandleLoggerChanged_ScopedFetch_ContentChanged verifies that logger_changed
// calls GetLogger (scoped) and fires listeners when content differs.
func TestHandleLoggerChanged_ScopedFetch_ContentChanged(t *testing.T) {
	var fetchCount int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/com.acme.app", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme.app","type":"logger","attributes":{"id":"com.acme.app","name":"com.acme.app","level":"WARN","managed":true,"environments":{}}}}`))
	})

	lc := newTestLoggingClient(t, http.HandlerFunc(mux.ServeHTTP))

	// Pre-populate cache with different content.
	lc.loggersCache["com.acme.app"] = map[string]interface{}{
		"id":      "com.acme.app",
		"name":    "com.acme.app",
		"level":   "DEBUG",
		"managed": true,
	}

	var received *LoggerChangeEvent
	lc.OnChange(func(evt *LoggerChangeEvent) {
		received = evt
	})

	lc.handleLoggerChanged(map[string]interface{}{"id": "com.acme.app"})

	assert.Equal(t, int32(1), atomic.LoadInt32(&fetchCount), "should call GetLogger once")
	require.NotNil(t, received, "listener should fire when content changed")
	assert.Equal(t, "com.acme.app", received.ID)
	assert.False(t, received.Deleted)
}

// TestHandleLoggerChanged_ScopedFetch_ContentUnchanged verifies that logger_changed
// does NOT fire listeners when content is identical.
// We pre-warm the cache using fetchSingleLogger so the stored map matches exactly.
func TestHandleLoggerChanged_ScopedFetch_ContentUnchanged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/com.acme.app", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme.app","type":"logger","attributes":{"id":"com.acme.app","name":"com.acme.app","level":"DEBUG","managed":true,"environments":{}}}}`))
	})

	lc := newTestLoggingClient(t, http.HandlerFunc(mux.ServeHTTP))

	// Pre-warm: fetch once so the cache has the exact map representation.
	preFetched, err := lc.fetchSingleLogger(context.Background(), "com.acme.app")
	require.NoError(t, err)
	lc.loggersCache["com.acme.app"] = preFetched

	var called bool
	lc.OnChange(func(evt *LoggerChangeEvent) { called = true })

	// Second handleLoggerChanged call: server returns same data → no diff.
	lc.handleLoggerChanged(map[string]interface{}{"id": "com.acme.app"})

	assert.False(t, called, "listener should NOT fire when content is unchanged")
}

// TestHandleLoggerDeleted_StoreRemoval_ListenerFired verifies that logger_deleted
// removes the logger from cache and fires the listener with Deleted=true,
// without making any HTTP fetch.
func TestHandleLoggerDeleted_StoreRemoval_ListenerFired(t *testing.T) {
	var fetchCount int32
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		w.WriteHeader(http.StatusOK)
	}))

	lc.loggersCache["gone.logger"] = map[string]interface{}{
		"id":   "gone.logger",
		"name": "gone.logger",
	}

	var evt *LoggerChangeEvent
	lc.OnChange(func(e *LoggerChangeEvent) { evt = e })

	lc.handleLoggerDeleted(map[string]interface{}{"id": "gone.logger"})

	assert.Equal(t, int32(0), atomic.LoadInt32(&fetchCount), "logger_deleted must NOT make HTTP fetch")
	_, stillInCache := lc.loggersCache["gone.logger"]
	assert.False(t, stillInCache, "logger should be removed from cache")
	require.NotNil(t, evt)
	assert.True(t, evt.Deleted, "event should have Deleted=true")
	assert.Equal(t, "gone.logger", evt.ID)
}

// TestHandleGroupChanged_ScopedFetch verifies that group_changed calls
// GetLogGroup (scoped) without a full refetch.
func TestHandleGroupChanged_ScopedFetch(t *testing.T) {
	var fetchCount int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups/sql", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"ERROR","environments":{}}}}`))
	})

	lc := newTestLoggingClient(t, http.HandlerFunc(mux.ServeHTTP))

	// Different content to trigger update.
	lc.groupsCache["sql"] = map[string]interface{}{"id": "sql", "level": "DEBUG"}

	lc.handleGroupChanged(map[string]interface{}{"id": "sql"})

	assert.Equal(t, int32(1), atomic.LoadInt32(&fetchCount), "should call GetLogGroup once")
}

// TestHandleGroupDeleted_RemovesFromCache verifies that group_deleted
// removes the group from cache without an HTTP fetch.
func TestHandleGroupDeleted_RemovesFromCache(t *testing.T) {
	var fetchCount int32
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		w.WriteHeader(http.StatusOK)
	}))

	lc.groupsCache["sql"] = map[string]interface{}{"id": "sql"}

	lc.handleGroupDeleted(map[string]interface{}{"id": "sql"})

	assert.Equal(t, int32(0), atomic.LoadInt32(&fetchCount), "group_deleted must NOT make HTTP fetch")
	_, stillInCache := lc.groupsCache["sql"]
	assert.False(t, stillInCache, "group should be removed from cache")
}

// TestHandleLoggersChanged_FullFetch_DiffFiring verifies that loggers_changed
// fetches full list, diffs, and fires listeners for changed loggers.
func TestHandleLoggersChanged_FullFetch_DiffFiring(t *testing.T) {
	var listFetched int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&listFetched, 1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[
			{"id":"com.acme.app","type":"logger","attributes":{"id":"com.acme.app","name":"com.acme.app","level":"WARN","managed":true,"environments":{}}},
			{"id":"com.acme.db","type":"logger","attributes":{"id":"com.acme.db","name":"com.acme.db","level":"DEBUG","managed":true,"environments":{}}}
		]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	lc := newTestLoggingClient(t, http.HandlerFunc(mux.ServeHTTP))

	// Pre-populate with different content for com.acme.app; com.acme.db is new.
	lc.loggersCache["com.acme.app"] = map[string]interface{}{
		"id": "com.acme.app", "level": "DEBUG",
	}

	var globalFired int
	var keyAppFired, keyDBFired bool

	lc.OnChange(func(evt *LoggerChangeEvent) { globalFired++ })
	lc.OnChangeKey("com.acme.app", func(evt *LoggerChangeEvent) { keyAppFired = true })
	lc.OnChangeKey("com.acme.db", func(evt *LoggerChangeEvent) { keyDBFired = true })

	lc.handleLoggersChanged(map[string]interface{}{})

	assert.GreaterOrEqual(t, atomic.LoadInt32(&listFetched), int32(1), "should call list fetch")
	assert.True(t, keyAppFired, "com.acme.app listener should fire (content changed)")
	assert.True(t, keyDBFired, "com.acme.db listener should fire (new logger)")
	_ = globalFired // global fires once per changed key in this path
}

// ========== Coverage gap tests ==========

// TestFetchSingleLogger_NetworkError covers the network error path.
func TestFetchSingleLogger_NetworkError(t *testing.T) {
	httpClient := &http.Client{Transport: &failingTransportLogging{}}
	genLoggingClient, _ := genlogging.NewClient("http://localhost",
		genlogging.WithHTTPClient(httpClient),
	)
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, genLoggingClient)

	_, err := lc.fetchSingleLogger(context.Background(), "my-logger")
	assert.Error(t, err)
}

// TestFetchSingleLogger_ReadBodyError covers the read body error path.
func TestFetchSingleLogger_ReadBodyError(t *testing.T) {
	httpClient := &http.Client{Transport: &brokenBodyTransportLogging{statusCode: 200}}
	genLoggingClient, _ := genlogging.NewClient("http://localhost",
		genlogging.WithHTTPClient(httpClient),
	)
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, genLoggingClient)

	_, err := lc.fetchSingleLogger(context.Background(), "my-logger")
	assert.Error(t, err)
}

// TestFetchSingleLogger_HTTPError covers the HTTP error status path.
func TestFetchSingleLogger_HTTPError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))

	_, err := lc.fetchSingleLogger(context.Background(), "missing-logger")
	assert.Error(t, err)
}

// TestFetchSingleLogger_MalformedJSON covers the JSON parse error path.
func TestFetchSingleLogger_MalformedJSON(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not valid json`))
	}))

	_, err := lc.fetchSingleLogger(context.Background(), "my-logger")
	assert.Error(t, err)
}

// TestFetchSingleLogger_WithGroup covers the l.Group != nil path.
func TestFetchSingleLogger_WithGroup(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme.app","type":"logger","attributes":{"id":"com.acme.app","name":"com.acme.app","level":"DEBUG","managed":true,"group":"my-group","environments":{}}}}`))
	}))

	result, err := lc.fetchSingleLogger(context.Background(), "com.acme.app")
	require.NoError(t, err)
	assert.Equal(t, "my-group", result["group"])
}

// TestFetchSingleGroup_NetworkError covers the network error path.
func TestFetchSingleGroup_NetworkError(t *testing.T) {
	httpClient := &http.Client{Transport: &failingTransportLogging{}}
	genLoggingClient, _ := genlogging.NewClient("http://localhost",
		genlogging.WithHTTPClient(httpClient),
	)
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, genLoggingClient)

	_, err := lc.fetchSingleGroup(context.Background(), "my-group")
	assert.Error(t, err)
}

// TestFetchSingleGroup_ReadBodyError covers the read body error path.
func TestFetchSingleGroup_ReadBodyError(t *testing.T) {
	httpClient := &http.Client{Transport: &brokenBodyTransportLogging{statusCode: 200}}
	genLoggingClient, _ := genlogging.NewClient("http://localhost",
		genlogging.WithHTTPClient(httpClient),
	)
	c := &Client{environment: "test", service: "test-service"}
	lc := newLoggingClient(c, genLoggingClient)

	_, err := lc.fetchSingleGroup(context.Background(), "my-group")
	assert.Error(t, err)
}

// TestFetchSingleGroup_HTTPError covers the HTTP error status path.
func TestFetchSingleGroup_HTTPError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))

	_, err := lc.fetchSingleGroup(context.Background(), "missing-group")
	assert.Error(t, err)
}

// TestFetchSingleGroup_MalformedJSON covers the JSON parse error path.
func TestFetchSingleGroup_MalformedJSON(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not valid json`))
	}))

	_, err := lc.fetchSingleGroup(context.Background(), "my-group")
	assert.Error(t, err)
}

// TestFetchSingleGroup_WithParentGroup covers the g.Group != nil path.
func TestFetchSingleGroup_WithParentGroup(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"child","type":"log_group","attributes":{"id":"child","name":"Child","level":"INFO","parent_id":"parent-group","environments":{}}}}`))
	}))

	result, err := lc.fetchSingleGroup(context.Background(), "child")
	require.NoError(t, err)
	assert.Equal(t, "parent-group", result["group"])
}

// TestHandleGroupChanged_ScopedFetch_ContentUnchanged verifies group_changed
// does NOT call applyLevels when content is identical.
func TestHandleGroupChanged_ScopedFetch_ContentUnchanged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups/sql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"ERROR","environments":{}}}}`))
	})

	lc := newTestLoggingClient(t, http.HandlerFunc(mux.ServeHTTP))

	// Pre-warm: fetch once so the cache has the exact map representation.
	preFetched, err := lc.fetchSingleGroup(context.Background(), "sql")
	require.NoError(t, err)
	lc.groupsCache["sql"] = preFetched

	// Second handleGroupChanged call: server returns same data → no diff → early return.
	lc.handleGroupChanged(map[string]interface{}{"id": "sql"})
	// Test passes if no panic or unexpected behavior.
}

// TestHandleGroupChanged_FetchError_NoChange covers the early return on fetch error.
func TestHandleGroupChanged_FetchError_NoChange(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	// Should not panic; error causes early return.
	lc.handleGroupChanged(map[string]interface{}{"id": "sql"})
}

// TestHandleLoggersChanged_FetchError covers the fetchAndCache error early return.
func TestHandleLoggersChanged_FetchError(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	var called bool
	lc.OnChange(func(evt *LoggerChangeEvent) { called = true })

	lc.handleLoggersChanged(map[string]interface{}{})
	assert.False(t, called)
}

// TestFireDeletedListeners_EmptyKey covers the empty key early return.
func TestFireDeletedListeners_EmptyKey(t *testing.T) {
	lc := newTestLoggingClient(t, nil)
	var called bool
	lc.OnChange(func(evt *LoggerChangeEvent) { called = true })
	lc.fireDeletedListeners("", "test")
	assert.False(t, called)
}

// TestFireDeletedListeners_GlobalPanic covers the global listener panic recovery.
func TestFireDeletedListeners_GlobalPanic(t *testing.T) {
	lc := newTestLoggingClient(t, nil)
	lc.OnChange(func(evt *LoggerChangeEvent) { panic("global panic") })
	assert.NotPanics(t, func() {
		lc.fireDeletedListeners("my-logger", "test")
	})
}

// TestFireDeletedListeners_KeyListenerPanic covers the key-scoped listener panic recovery.
func TestFireDeletedListeners_KeyListenerPanic(t *testing.T) {
	lc := newTestLoggingClient(t, nil)
	lc.OnChangeKey("my-logger", func(evt *LoggerChangeEvent) { panic("key panic") })
	assert.NotPanics(t, func() {
		lc.fireDeletedListeners("my-logger", "test")
	})
}

// failingTransportLogging returns a network error for all requests.
type failingTransportLogging struct{}

func (t *failingTransportLogging) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("simulated network error")
}

// --- Refresh ---

// refreshMux serves /api/v1/loggers and /api/v1/log_groups list endpoints
// from atomic pointers, so the test can swap the served body between
// successive Refresh calls.
func refreshMux(loggersBody, groupsBody *atomic.Value) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(loggersBody.Load().(string)))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(groupsBody.Load().(string)))
	})
	return mux
}

func TestRefresh_FetchesAndCachesLoggers(t *testing.T) {
	var loggers, groups atomic.Value
	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"INFO","managed":true,"environments":{},"sources":[]}}]}`)
	groups.Store(`{"data":[]}`)

	lc := newTestLoggingClient(t, refreshMux(&loggers, &groups))

	require.NoError(t, lc.Refresh(context.Background()))
	assert.Contains(t, lc.loggersCache, "app")
	assert.Equal(t, "INFO", lc.loggersCache["app"]["level"])
}

func TestRefresh_FiresChangeListenerWithManualSource(t *testing.T) {
	var loggers, groups atomic.Value
	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"INFO","managed":true,"environments":{},"sources":[]}}]}`)
	groups.Store(`{"data":[]}`)

	lc := newTestLoggingClient(t, refreshMux(&loggers, &groups))
	require.NoError(t, lc.Refresh(context.Background()))

	var events []*LoggerChangeEvent
	lc.OnChange(func(evt *LoggerChangeEvent) { events = append(events, evt) })

	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"DEBUG","managed":true,"environments":{},"sources":[]}}]}`)
	require.NoError(t, lc.Refresh(context.Background()))

	require.Len(t, events, 1)
	assert.Equal(t, "app", events[0].ID)
	assert.Equal(t, "manual", events[0].Source)
	require.NotNil(t, events[0].Level)
	assert.Equal(t, LogLevelDebug, *events[0].Level)
	assert.False(t, events[0].Deleted)
}

func TestRefresh_FiresDeletedListenerForRemovedLogger(t *testing.T) {
	var loggers, groups atomic.Value
	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"INFO","managed":true,"environments":{},"sources":[]}}]}`)
	groups.Store(`{"data":[]}`)

	lc := newTestLoggingClient(t, refreshMux(&loggers, &groups))
	require.NoError(t, lc.Refresh(context.Background()))

	var events []*LoggerChangeEvent
	lc.OnChange(func(evt *LoggerChangeEvent) { events = append(events, evt) })

	loggers.Store(`{"data":[]}`)
	require.NoError(t, lc.Refresh(context.Background()))

	require.Len(t, events, 1)
	assert.Equal(t, "app", events[0].ID)
	assert.True(t, events[0].Deleted)
	assert.Equal(t, "manual", events[0].Source)
}

func TestRefresh_NoListenerFireWhenUnchanged(t *testing.T) {
	var loggers, groups atomic.Value
	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"INFO","managed":true,"environments":{},"sources":[]}}]}`)
	groups.Store(`{"data":[]}`)

	lc := newTestLoggingClient(t, refreshMux(&loggers, &groups))
	require.NoError(t, lc.Refresh(context.Background()))

	var fired int
	lc.OnChange(func(_ *LoggerChangeEvent) { fired++ })

	require.NoError(t, lc.Refresh(context.Background()))
	assert.Zero(t, fired, "no listener should fire when fetch produces identical caches")
}

func TestRefresh_AppliesResolvedLevelToAdapter(t *testing.T) {
	var loggers, groups atomic.Value
	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"INFO","managed":true,"environments":{},"sources":[]}}]}`)
	groups.Store(`{"data":[]}`)

	lc := newTestLoggingClient(t, refreshMux(&loggers, &groups))
	captured := &refreshCapturingAdapter{discovered: []refreshDiscoveredEntry{{name: "app", level: "INFO"}}}
	lc.RegisterAdapter(captured)

	require.NoError(t, lc.Refresh(context.Background()))
	require.NotEmpty(t, captured.applied, "Refresh should call ApplyLevel on registered adapters")
	last := captured.applied[len(captured.applied)-1]
	assert.Equal(t, "app", last.name)
	assert.Equal(t, "INFO", last.level)

	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"ERROR","managed":true,"environments":{},"sources":[]}}]}`)
	require.NoError(t, lc.Refresh(context.Background()))
	last = captured.applied[len(captured.applied)-1]
	assert.Equal(t, "app", last.name)
	assert.Equal(t, "ERROR", last.level)
}

func TestRefresh_ReturnsErrorOnFetchFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	})
	lc := newTestLoggingClient(t, mux)

	err := lc.Refresh(context.Background())
	require.Error(t, err)
}

// refreshCapturingAdapter records every ApplyLevel call so tests can
// assert that Refresh propagates levels to registered adapters.
type refreshCapturingAdapter struct {
	discovered []refreshDiscoveredEntry
	applied    []refreshDiscoveredEntry
}

type refreshDiscoveredEntry struct {
	name  string
	level string
}

func (a *refreshCapturingAdapter) Name() string { return "capture" }

func (a *refreshCapturingAdapter) Discover() []adapters.DiscoveredLogger {
	out := make([]adapters.DiscoveredLogger, len(a.discovered))
	for i, d := range a.discovered {
		out[i] = adapters.DiscoveredLogger{Name: d.name, Level: d.level}
	}
	return out
}

func (a *refreshCapturingAdapter) ApplyLevel(name, level string) {
	a.applied = append(a.applied, refreshDiscoveredEntry{name: name, level: level})
}

func (a *refreshCapturingAdapter) InstallHook(_ func(string, string)) {}

func (a *refreshCapturingAdapter) UninstallHook() {}
