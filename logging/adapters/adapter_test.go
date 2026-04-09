package adapters_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/smplkit/go-sdk/logging/adapters"
)

// mockAdapter is a test double that satisfies LoggingAdapter.
type mockAdapter struct {
	name          string
	discovered    []adapters.DiscoveredLogger
	appliedLevels []appliedLevel
	hookInstalled bool
	hookUninstall bool
	onNewLoggerFn func(name string, level string)
}

type appliedLevel struct {
	loggerName string
	level      string
}

func (m *mockAdapter) Name() string { return m.name }

func (m *mockAdapter) Discover() []adapters.DiscoveredLogger { return m.discovered }

func (m *mockAdapter) ApplyLevel(loggerName string, level string) {
	m.appliedLevels = append(m.appliedLevels, appliedLevel{loggerName, level})
}

func (m *mockAdapter) InstallHook(onNewLogger func(name string, level string)) {
	m.hookInstalled = true
	m.onNewLoggerFn = onNewLogger
}

func (m *mockAdapter) UninstallHook() {
	m.hookUninstall = true
	m.onNewLoggerFn = nil
}

func TestMockAdapter_SatisfiesInterface(t *testing.T) {
	var adapter adapters.LoggingAdapter = &mockAdapter{name: "mock"}
	assert.Equal(t, "mock", adapter.Name())
}

func TestDiscoveredLogger_Fields(t *testing.T) {
	dl := adapters.DiscoveredLogger{Name: "com.acme.app", Level: "INFO"}
	assert.Equal(t, "com.acme.app", dl.Name)
	assert.Equal(t, "INFO", dl.Level)
}

func TestMockAdapter_Discover(t *testing.T) {
	adapter := &mockAdapter{
		name: "mock",
		discovered: []adapters.DiscoveredLogger{
			{Name: "app", Level: "DEBUG"},
			{Name: "db", Level: "WARN"},
		},
	}
	result := adapter.Discover()
	assert.Len(t, result, 2)
	assert.Equal(t, "app", result[0].Name)
	assert.Equal(t, "DEBUG", result[0].Level)
}

func TestMockAdapter_ApplyLevel(t *testing.T) {
	adapter := &mockAdapter{name: "mock"}
	adapter.ApplyLevel("com.acme.app", "ERROR")
	assert.Len(t, adapter.appliedLevels, 1)
	assert.Equal(t, "com.acme.app", adapter.appliedLevels[0].loggerName)
	assert.Equal(t, "ERROR", adapter.appliedLevels[0].level)
}

func TestMockAdapter_InstallAndUninstallHook(t *testing.T) {
	adapter := &mockAdapter{name: "mock"}
	assert.False(t, adapter.hookInstalled)

	var called bool
	adapter.InstallHook(func(name string, level string) {
		called = true
	})
	assert.True(t, adapter.hookInstalled)
	assert.NotNil(t, adapter.onNewLoggerFn)

	// Fire the hook.
	adapter.onNewLoggerFn("test", "INFO")
	assert.True(t, called)

	adapter.UninstallHook()
	assert.True(t, adapter.hookUninstall)
	assert.Nil(t, adapter.onNewLoggerFn)
}
