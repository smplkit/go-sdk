// Package zapadapter provides a smplkit logging adapter for go.uber.org/zap.
//
// The adapter uses a core wrapper pattern: customers wrap their zapcore.Core
// with the adapter, which intercepts Enabled() checks to apply smplkit-controlled
// levels dynamically.
//
// Usage:
//
//	adapter := zapadapter.New()
//	core := adapter.WrapCore(zapcore.NewCore(encoder, writer, zap.InfoLevel))
//	logger := zap.New(core)
//
//	client.Logging().RegisterAdapter(adapter)
//	client.Logging().Start(ctx)
package zapadapter

import (
	"strings"
	"sync"

	"github.com/smplkit/go-sdk/logging/adapters"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// smplkit level string → zapcore.Level mapping.
var levelMap = map[string]zapcore.Level{
	"TRACE":  zapcore.DebugLevel,
	"DEBUG":  zapcore.DebugLevel,
	"INFO":   zapcore.InfoLevel,
	"WARN":   zapcore.WarnLevel,
	"ERROR":  zapcore.ErrorLevel,
	"FATAL":  zapcore.FatalLevel,
	"SILENT": zapcore.FatalLevel + 1,
}

// zapcore.Level → smplkit level string mapping (for Discover).
var reverseLevelMap = map[zapcore.Level]string{
	zapcore.DebugLevel:     "DEBUG",
	zapcore.InfoLevel:      "INFO",
	zapcore.WarnLevel:      "WARN",
	zapcore.ErrorLevel:     "ERROR",
	zapcore.DPanicLevel:    "ERROR",
	zapcore.PanicLevel:     "FATAL",
	zapcore.FatalLevel:     "FATAL",
	zapcore.FatalLevel + 1: "SILENT",
}

// ToZapLevel converts a smplkit level string to a zapcore.Level.
// Returns zapcore.InfoLevel for unknown levels.
func ToZapLevel(level string) zapcore.Level {
	if l, ok := levelMap[strings.ToUpper(level)]; ok {
		return l
	}
	return zapcore.InfoLevel
}

// ToSmplkitLevel converts a zapcore.Level to a smplkit level string.
// Returns the closest matching level.
func ToSmplkitLevel(level zapcore.Level) string {
	if s, ok := reverseLevelMap[level]; ok {
		return s
	}
	if level < zapcore.InfoLevel {
		return "DEBUG"
	}
	return "ERROR"
}

// Adapter implements adapters.LoggingAdapter for go.uber.org/zap.
type Adapter struct {
	mu     sync.RWMutex
	cores  map[*SmplCore]struct{}
	hookFn func(name string, level string)
}

// New creates a new zap adapter instance.
func New() *Adapter {
	return &Adapter{
		cores: make(map[*SmplCore]struct{}),
	}
}

// Name returns the adapter name for diagnostics.
func (a *Adapter) Name() string {
	return "zap"
}

// WrapCore wraps a zapcore.Core with smplkit level control.
// The returned SmplCore intercepts Enabled() to check against the
// smplkit-controlled AtomicLevel.
func (a *Adapter) WrapCore(c zapcore.Core) *SmplCore {
	atomicLevel := zap.NewAtomicLevelAt(zapcore.InfoLevel)

	sc := &SmplCore{
		inner:   c,
		level:   atomicLevel,
		adapter: a,
	}

	a.mu.Lock()
	a.cores[sc] = struct{}{}
	a.mu.Unlock()

	return sc
}

// Discover returns all tracked cores with their current levels.
func (a *Adapter) Discover() []adapters.DiscoveredLogger {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var result []adapters.DiscoveredLogger
	for c := range a.cores {
		result = append(result, adapters.DiscoveredLogger{
			Name:  c.loggerName(),
			Level: ToSmplkitLevel(c.level.Level()),
		})
	}
	return result
}

// ApplyLevel sets the level on a core identified by its logger name.
func (a *Adapter) ApplyLevel(loggerName string, level string) {
	zapLevel := ToZapLevel(level)

	a.mu.RLock()
	defer a.mu.RUnlock()

	for c := range a.cores {
		if c.loggerName() == loggerName {
			c.level.SetLevel(zapLevel)
		}
	}
}

// InstallHook stores a callback that fires when Named() creates a new sub-logger.
func (a *Adapter) InstallHook(onNewLogger func(name string, level string)) {
	a.mu.Lock()
	a.hookFn = onNewLogger
	a.mu.Unlock()
}

// UninstallHook removes the installed hook.
func (a *Adapter) UninstallHook() {
	a.mu.Lock()
	a.hookFn = nil
	a.mu.Unlock()
}

func (a *Adapter) fireHook(name string, level string) {
	a.mu.RLock()
	fn := a.hookFn
	a.mu.RUnlock()

	if fn != nil {
		fn(name, level)
	}
}

// SmplCore wraps a zapcore.Core with smplkit-controlled level filtering.
type SmplCore struct {
	inner   zapcore.Core
	level   zap.AtomicLevel
	adapter *Adapter
	names   []string
}

// loggerName returns the dot-separated name path for this core.
func (c *SmplCore) loggerName() string {
	if len(c.names) == 0 {
		return ""
	}
	return strings.Join(c.names, ".")
}

// Enabled reports whether the core handles entries at the given level.
// Uses the smplkit-controlled level instead of the inner core's level.
func (c *SmplCore) Enabled(level zapcore.Level) bool {
	return c.level.Enabled(level)
}

// With adds structured context to the core.
func (c *SmplCore) With(fields []zapcore.Field) zapcore.Core {
	return &SmplCore{
		inner:   c.inner.With(fields),
		level:   c.level,
		adapter: c.adapter,
		names:   c.names,
	}
}

// Check determines whether the supplied entry should be logged.
func (c *SmplCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		ce = ce.AddCore(ent, c)
	}
	return ce
}

// Write serializes the entry and any fields to their destination.
func (c *SmplCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	return c.inner.Write(ent, fields)
}

// Sync flushes buffered logs.
func (c *SmplCore) Sync() error {
	return c.inner.Sync()
}

// Named adds a new path segment to the core's logger name.
// This creates a new tracked core and fires the hook if installed.
func (c *SmplCore) Named(name string) *SmplCore {
	if name == "" {
		return c
	}

	newNames := make([]string, len(c.names), len(c.names)+1)
	copy(newNames, c.names)
	newNames = append(newNames, name)

	child := &SmplCore{
		inner:   c.inner,
		level:   zap.NewAtomicLevelAt(c.level.Level()),
		adapter: c.adapter,
		names:   newNames,
	}

	c.adapter.mu.Lock()
	c.adapter.cores[child] = struct{}{}
	c.adapter.mu.Unlock()

	c.adapter.fireHook(child.loggerName(), ToSmplkitLevel(child.level.Level()))

	return child
}

// Compile-time check that Adapter implements LoggingAdapter.
var _ adapters.LoggingAdapter = (*Adapter)(nil)

// Compile-time check that SmplCore implements zapcore.Core.
var _ zapcore.Core = (*SmplCore)(nil)
