package slogadapter_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	slogadapter "github.com/smplkit/go-sdk/logging/adapters/slog"
)

func TestNew(t *testing.T) {
	adapter := slogadapter.New()
	assert.NotNil(t, adapter)
	assert.Equal(t, "slog", adapter.Name())
}

func TestWrapHandler(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := adapter.WrapHandler(inner)
	require.NotNil(t, handler)

	// Create a logger and write a message.
	logger := slog.New(handler)
	logger.Info("test message")
	assert.Contains(t, buf.String(), "test message")
}

func TestWrapHandler_EnabledRespectsSmplkitLevel(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := adapter.WrapHandler(inner)

	// Default level is INFO, so DEBUG should be filtered.
	assert.False(t, handler.Enabled(context.Background(), slog.LevelDebug))
	assert.True(t, handler.Enabled(context.Background(), slog.LevelInfo))
	assert.True(t, handler.Enabled(context.Background(), slog.LevelWarn))
}

func TestDiscover(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	adapter.WrapHandler(inner)

	discovered := adapter.Discover()
	require.Len(t, discovered, 1)
	assert.Equal(t, "", discovered[0].Name)
	assert.Equal(t, "INFO", discovered[0].Level)
}

func TestDiscover_WithGroup(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := adapter.WrapHandler(inner)

	// Create a sub-logger via WithGroup.
	_ = handler.WithGroup("database")

	discovered := adapter.Discover()
	// Should have 2: root handler + database group.
	assert.Len(t, discovered, 2)

	var names []string
	for _, d := range discovered {
		names = append(names, d.Name)
	}
	assert.Contains(t, names, "")
	assert.Contains(t, names, "database")
}

func TestApplyLevel(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := adapter.WrapHandler(inner)

	// Create a named sub-logger.
	child := handler.WithGroup("payments")

	// Apply DEBUG level to the payments logger.
	adapter.ApplyLevel("payments", "DEBUG")

	// Verify the child is now DEBUG-enabled.
	assert.True(t, child.Enabled(context.Background(), slog.LevelDebug))
}

func TestApplyLevel_AllLevels(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug - 4})
	handler := adapter.WrapHandler(inner)

	tests := []struct {
		smplkitLevel string
		enabledAt    slog.Level
		disabledAt   slog.Level
	}{
		{"TRACE", slog.LevelDebug - 4, slog.LevelDebug - 5},
		{"DEBUG", slog.LevelDebug, slog.LevelDebug - 1},
		{"INFO", slog.LevelInfo, slog.LevelDebug},
		{"WARN", slog.LevelWarn, slog.LevelInfo},
		{"ERROR", slog.LevelError, slog.LevelWarn},
		{"FATAL", slog.LevelError + 4, slog.LevelError},
		{"SILENT", slog.LevelError + 8, slog.LevelError + 4},
	}

	for _, tt := range tests {
		t.Run(tt.smplkitLevel, func(t *testing.T) {
			adapter.ApplyLevel("", tt.smplkitLevel)
			assert.True(t, handler.Enabled(context.Background(), tt.enabledAt),
				"expected level %v to be enabled for smplkit level %s", tt.enabledAt, tt.smplkitLevel)
			assert.False(t, handler.Enabled(context.Background(), tt.disabledAt),
				"expected level %v to be disabled for smplkit level %s", tt.disabledAt, tt.smplkitLevel)
		})
	}
}

func TestInstallHook_DetectsWithGroup(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := adapter.WrapHandler(inner)

	var hookCalled bool
	var hookName, hookLevel string
	adapter.InstallHook(func(name string, level string) {
		hookCalled = true
		hookName = name
		hookLevel = level
	})

	// WithGroup should fire the hook.
	_ = handler.WithGroup("cache")

	assert.True(t, hookCalled)
	assert.Equal(t, "cache", hookName)
	assert.Equal(t, "INFO", hookLevel)
}

func TestInstallHook_NestedGroups(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := adapter.WrapHandler(inner)

	var names []string
	adapter.InstallHook(func(name string, level string) {
		names = append(names, name)
	})

	// Nested WithGroup calls.
	child := handler.WithGroup("com")
	_ = child.WithGroup("acme")

	assert.Equal(t, []string{"com", "com.acme"}, names)
}

func TestUninstallHook(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := adapter.WrapHandler(inner)

	var called bool
	adapter.InstallHook(func(name string, level string) {
		called = true
	})

	adapter.UninstallHook()

	// WithGroup after uninstall should NOT fire the hook.
	_ = handler.WithGroup("test")
	assert.False(t, called)
}

func TestLevelMapping_ToSlogLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
	}{
		{"TRACE", slog.LevelDebug - 4},
		{"DEBUG", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"WARN", slog.LevelWarn},
		{"ERROR", slog.LevelError},
		{"FATAL", slog.LevelError + 4},
		{"SILENT", slog.LevelError + 8},
		{"unknown", slog.LevelInfo},
		{"trace", slog.LevelDebug - 4}, // case insensitive
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, slogadapter.ToSlogLevel(tt.input))
		})
	}
}

func TestLevelMapping_ToSmplkitLevel(t *testing.T) {
	tests := []struct {
		input    slog.Level
		expected string
	}{
		{slog.LevelDebug - 4, "TRACE"},
		{slog.LevelDebug, "DEBUG"},
		{slog.LevelInfo, "INFO"},
		{slog.LevelWarn, "WARN"},
		{slog.LevelError, "ERROR"},
		{slog.LevelError + 4, "FATAL"},
		{slog.LevelError + 8, "SILENT"},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, slogadapter.ToSmplkitLevel(tt.input))
		})
	}
}

func TestLevelMapping_ToSmplkitLevel_NonStandard(t *testing.T) {
	// Levels between standard values.
	tests := []struct {
		input    slog.Level
		expected string
	}{
		{slog.LevelDebug - 5, "TRACE"},
		{slog.LevelDebug + 1, "DEBUG"},
		{slog.LevelInfo + 1, "INFO"},
		{slog.LevelWarn + 1, "WARN"},
		{slog.LevelError + 1, "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, slogadapter.ToSmplkitLevel(tt.input))
		})
	}
}

func TestWithAttrs_PreservesLevelControl(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := adapter.WrapHandler(inner)

	// WithAttrs should return a handler that still respects smplkit level.
	withAttrs := handler.WithAttrs([]slog.Attr{slog.String("key", "val")})

	// Default level is INFO.
	assert.False(t, withAttrs.Enabled(context.Background(), slog.LevelDebug))
	assert.True(t, withAttrs.Enabled(context.Background(), slog.LevelInfo))
}

func TestWithGroup_EmptyName(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := adapter.WrapHandler(inner)

	// Empty group name should return same handler.
	same := handler.WithGroup("")
	assert.Equal(t, handler, same)
}

func TestHandle(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := adapter.WrapHandler(inner)

	// Apply DEBUG level so debug messages pass.
	adapter.ApplyLevel("", "DEBUG")

	logger := slog.New(handler)
	logger.Debug("debug msg")
	assert.Contains(t, buf.String(), "debug msg")
}

func TestConcurrentAccess(t *testing.T) {
	adapter := slogadapter.New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := adapter.WrapHandler(inner)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = handler.WithGroup("concurrent")
			adapter.Discover()
			adapter.ApplyLevel("", "DEBUG")
		}()
	}
	wg.Wait()
}
