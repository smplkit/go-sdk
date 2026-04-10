// Package slogadapter provides a smplkit logging adapter for Go's log/slog package.
//
// Wrap your slog.Handler with the adapter to enable smplkit-controlled log levels.
//
// Usage:
//
//	adapter := slogadapter.New()
//	handler := adapter.WrapHandler(slog.NewJSONHandler(os.Stdout, nil))
//	slog.SetDefault(slog.New(handler))
//
//	client.Logging().RegisterAdapter(adapter)
//	client.Logging().Start(ctx)
package slogadapter

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/smplkit/go-sdk/logging/adapters"
)

// smplkit level string → slog.Level mapping.
var levelMap = map[string]slog.Level{
	"TRACE":  slog.LevelDebug - 4,
	"DEBUG":  slog.LevelDebug,
	"INFO":   slog.LevelInfo,
	"WARN":   slog.LevelWarn,
	"ERROR":  slog.LevelError,
	"FATAL":  slog.LevelError + 4,
	"SILENT": slog.LevelError + 8,
}

// slog.Level → smplkit level string mapping (for Discover).
var reverseLevelMap = map[slog.Level]string{
	slog.LevelDebug - 4: "TRACE",
	slog.LevelDebug:     "DEBUG",
	slog.LevelInfo:      "INFO",
	slog.LevelWarn:      "WARN",
	slog.LevelError:     "ERROR",
	slog.LevelError + 4: "FATAL",
	slog.LevelError + 8: "SILENT",
}

// ToSlogLevel converts a smplkit level string to a slog.Level.
// Returns slog.LevelInfo for unknown levels.
func ToSlogLevel(level string) slog.Level {
	if l, ok := levelMap[strings.ToUpper(level)]; ok {
		return l
	}
	return slog.LevelInfo
}

// ToSmplkitLevel converts a slog.Level to a smplkit level string.
// Returns the closest matching level.
func ToSmplkitLevel(level slog.Level) string {
	if s, ok := reverseLevelMap[level]; ok {
		return s
	}
	// Find the closest level.
	switch {
	case level < slog.LevelDebug:
		return "TRACE"
	case level < slog.LevelInfo:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

// Adapter implements adapters.LoggingAdapter for log/slog.
type Adapter struct {
	mu       sync.RWMutex
	handlers map[*SmplHandler]struct{}
	hookFn   func(name string, level string)
}

// New creates a new slog adapter instance.
func New() *Adapter {
	return &Adapter{
		handlers: make(map[*SmplHandler]struct{}),
	}
}

// Name returns the adapter name for diagnostics.
func (a *Adapter) Name() string {
	return "slog"
}

// WrapHandler wraps a slog.Handler with smplkit level control.
func (a *Adapter) WrapHandler(h slog.Handler) *SmplHandler {
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelInfo) // Default to INFO

	sh := &SmplHandler{
		inner:    h,
		levelVar: levelVar,
		adapter:  a,
	}

	a.mu.Lock()
	a.handlers[sh] = struct{}{}
	a.mu.Unlock()

	return sh
}

// Discover returns all tracked handlers with their current levels.
func (a *Adapter) Discover() []adapters.DiscoveredLogger {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var result []adapters.DiscoveredLogger
	for h := range a.handlers {
		result = append(result, adapters.DiscoveredLogger{
			Name:  h.groupName(),
			Level: ToSmplkitLevel(h.levelVar.Level()),
		})
	}
	return result
}

// ApplyLevel sets the level on a handler identified by its group name.
func (a *Adapter) ApplyLevel(loggerName string, level string) {
	slogLevel := ToSlogLevel(level)

	a.mu.RLock()
	defer a.mu.RUnlock()

	for h := range a.handlers {
		if h.groupName() == loggerName {
			h.levelVar.Set(slogLevel)
		}
	}
}

// InstallHook registers a callback that fires when a new sub-logger is created.
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

// SmplHandler is a slog.Handler with smplkit-controlled log levels.
type SmplHandler struct {
	inner    slog.Handler
	levelVar *slog.LevelVar
	adapter  *Adapter
	groups   []string
}

// groupName returns the dot-separated group path for this handler.
func (h *SmplHandler) groupName() string {
	if len(h.groups) == 0 {
		return ""
	}
	return strings.Join(h.groups, ".")
}

// Enabled reports whether the handler handles records at the given level.
func (h *SmplHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.levelVar.Level()
}

// Handle processes a log record.
func (h *SmplHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.inner.Handle(ctx, r)
}

// WithAttrs returns a new handler with the given attributes.
func (h *SmplHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &SmplHandler{
		inner:    h.inner.WithAttrs(attrs),
		levelVar: h.levelVar,
		adapter:  h.adapter,
		groups:   h.groups,
	}
}

// WithGroup returns a new handler with the given group name.
func (h *SmplHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	newGroups := make([]string, len(h.groups), len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups = append(newGroups, name)

	child := &SmplHandler{
		inner:    h.inner.WithGroup(name),
		levelVar: new(slog.LevelVar),
		adapter:  h.adapter,
		groups:   newGroups,
	}
	// Inherit parent's level.
	child.levelVar.Set(h.levelVar.Level())

	h.adapter.mu.Lock()
	h.adapter.handlers[child] = struct{}{}
	h.adapter.mu.Unlock()

	h.adapter.fireHook(child.groupName(), ToSmplkitLevel(child.levelVar.Level()))

	return child
}

// Compile-time check that Adapter implements LoggingAdapter.
var _ adapters.LoggingAdapter = (*Adapter)(nil)

// Compile-time check that SmplHandler implements slog.Handler.
var _ slog.Handler = (*SmplHandler)(nil)
