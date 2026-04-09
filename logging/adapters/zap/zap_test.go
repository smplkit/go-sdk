package zapadapter_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	zapadapter "github.com/smplkit/go-sdk/logging/adapters/zap"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func newTestCore() (zapcore.Core, *observer.ObservedLogs) {
	return observer.New(zapcore.DebugLevel)
}

func TestNew(t *testing.T) {
	adapter := zapadapter.New()
	assert.NotNil(t, adapter)
	assert.Equal(t, "zap", adapter.Name())
}

func TestWrapCore(t *testing.T) {
	adapter := zapadapter.New()
	inner, logs := newTestCore()
	core := adapter.WrapCore(inner)
	require.NotNil(t, core)

	// Apply DEBUG level so messages pass through.
	adapter.ApplyLevel("", "DEBUG")

	logger := zap.New(core)
	logger.Info("test message")
	assert.Equal(t, 1, logs.Len())
	assert.Equal(t, "test message", logs.All()[0].Message)
}

func TestWrapCore_EnabledRespectsSmplkitLevel(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	// Default level is INFO, so DEBUG should be filtered.
	assert.False(t, core.Enabled(zapcore.DebugLevel))
	assert.True(t, core.Enabled(zapcore.InfoLevel))
	assert.True(t, core.Enabled(zapcore.WarnLevel))
}

func TestDiscover(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	adapter.WrapCore(inner)

	discovered := adapter.Discover()
	require.Len(t, discovered, 1)
	assert.Equal(t, "", discovered[0].Name)
	assert.Equal(t, "INFO", discovered[0].Level)
}

func TestDiscover_WithNamed(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	// Create a named sub-core.
	_ = core.Named("database")

	discovered := adapter.Discover()
	assert.Len(t, discovered, 2)

	var names []string
	for _, d := range discovered {
		names = append(names, d.Name)
	}
	assert.Contains(t, names, "")
	assert.Contains(t, names, "database")
}

func TestApplyLevel(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	// Create a named sub-core.
	child := core.Named("payments")

	// Apply DEBUG level to the payments logger.
	adapter.ApplyLevel("payments", "DEBUG")

	// Verify the child is now DEBUG-enabled.
	assert.True(t, child.Enabled(zapcore.DebugLevel))
}

func TestApplyLevel_AllLevels(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	tests := []struct {
		smplkitLevel string
		enabledAt    zapcore.Level
		disabledAt   zapcore.Level
	}{
		{"TRACE", zapcore.DebugLevel, zapcore.DebugLevel - 1},
		{"DEBUG", zapcore.DebugLevel, zapcore.DebugLevel - 1},
		{"INFO", zapcore.InfoLevel, zapcore.DebugLevel},
		{"WARN", zapcore.WarnLevel, zapcore.InfoLevel},
		{"ERROR", zapcore.ErrorLevel, zapcore.WarnLevel},
		{"FATAL", zapcore.FatalLevel, zapcore.ErrorLevel},
		{"SILENT", zapcore.FatalLevel + 1, zapcore.FatalLevel},
	}

	for _, tt := range tests {
		t.Run(tt.smplkitLevel, func(t *testing.T) {
			adapter.ApplyLevel("", tt.smplkitLevel)
			assert.True(t, core.Enabled(tt.enabledAt),
				"expected level %v to be enabled for smplkit level %s", tt.enabledAt, tt.smplkitLevel)
			assert.False(t, core.Enabled(tt.disabledAt),
				"expected level %v to be disabled for smplkit level %s", tt.disabledAt, tt.smplkitLevel)
		})
	}
}

func TestInstallHook_DetectsNamed(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	var hookCalled bool
	var hookName, hookLevel string
	adapter.InstallHook(func(name string, level string) {
		hookCalled = true
		hookName = name
		hookLevel = level
	})

	// Named should fire the hook.
	_ = core.Named("cache")

	assert.True(t, hookCalled)
	assert.Equal(t, "cache", hookName)
	assert.Equal(t, "INFO", hookLevel)
}

func TestInstallHook_NestedNamed(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	var names []string
	adapter.InstallHook(func(name string, level string) {
		names = append(names, name)
	})

	// Nested Named calls.
	child := core.Named("com")
	_ = child.Named("acme")

	assert.Equal(t, []string{"com", "com.acme"}, names)
}

func TestUninstallHook(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	var called bool
	adapter.InstallHook(func(name string, level string) {
		called = true
	})

	adapter.UninstallHook()

	// Named after uninstall should NOT fire the hook.
	_ = core.Named("test")
	assert.False(t, called)
}

func TestLevelMapping_ToZapLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected zapcore.Level
	}{
		{"TRACE", zapcore.DebugLevel},
		{"DEBUG", zapcore.DebugLevel},
		{"INFO", zapcore.InfoLevel},
		{"WARN", zapcore.WarnLevel},
		{"ERROR", zapcore.ErrorLevel},
		{"FATAL", zapcore.FatalLevel},
		{"SILENT", zapcore.FatalLevel + 1},
		{"unknown", zapcore.InfoLevel},
		{"trace", zapcore.DebugLevel}, // case insensitive
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, zapadapter.ToZapLevel(tt.input))
		})
	}
}

func TestLevelMapping_ToSmplkitLevel(t *testing.T) {
	tests := []struct {
		input    zapcore.Level
		expected string
	}{
		{zapcore.DebugLevel, "DEBUG"},
		{zapcore.InfoLevel, "INFO"},
		{zapcore.WarnLevel, "WARN"},
		{zapcore.ErrorLevel, "ERROR"},
		{zapcore.DPanicLevel, "ERROR"},
		{zapcore.PanicLevel, "FATAL"},
		{zapcore.FatalLevel, "FATAL"},
		{zapcore.FatalLevel + 1, "SILENT"},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, zapadapter.ToSmplkitLevel(tt.input))
		})
	}
}

func TestLevelMapping_ToSmplkitLevel_NonStandard(t *testing.T) {
	// Levels between standard values.
	assert.Equal(t, "DEBUG", zapadapter.ToSmplkitLevel(zapcore.DebugLevel-1))
	assert.Equal(t, "ERROR", zapadapter.ToSmplkitLevel(zapcore.FatalLevel+2))
}

func TestWith_PreservesLevelControl(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	// With should return a core that still respects smplkit level.
	withFields := core.With([]zapcore.Field{zap.String("key", "val")})

	// Default level is INFO.
	assert.False(t, withFields.Enabled(zapcore.DebugLevel))
	assert.True(t, withFields.Enabled(zapcore.InfoLevel))
}

func TestNamed_EmptyName(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	// Empty name should return same core.
	same := core.Named("")
	assert.Equal(t, core, same)
}

func TestSync(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	err := core.Sync()
	assert.NoError(t, err)
}

func TestWrite(t *testing.T) {
	adapter := zapadapter.New()
	inner, logs := newTestCore()
	core := adapter.WrapCore(inner)

	// Apply DEBUG to allow writes.
	adapter.ApplyLevel("", "DEBUG")

	logger := zap.New(core)
	logger.Info("hello", zap.String("k", "v"))

	require.Equal(t, 1, logs.Len())
	assert.Equal(t, "hello", logs.All()[0].Message)
}

func TestCheck(t *testing.T) {
	adapter := zapadapter.New()
	inner, logs := newTestCore()
	core := adapter.WrapCore(inner)

	// Default is INFO, so debug should be filtered.
	entry := zapcore.Entry{Level: zapcore.DebugLevel, Message: "debug"}
	ce := core.Check(entry, nil)
	assert.Nil(t, ce)

	// Info should pass.
	entry = zapcore.Entry{Level: zapcore.InfoLevel, Message: "info"}
	ce = core.Check(entry, nil)
	require.NotNil(t, ce)
	ce.Write()
	assert.Equal(t, 1, logs.Len())
}

func TestConcurrentAccess(t *testing.T) {
	adapter := zapadapter.New()
	inner, _ := newTestCore()
	core := adapter.WrapCore(inner)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = core.Named("concurrent")
			adapter.Discover()
			adapter.ApplyLevel("", "DEBUG")
		}()
	}
	wg.Wait()
}
