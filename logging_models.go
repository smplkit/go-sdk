package smplkit

import (
	"context"
	"time"
)

// LogLevel represents a smplkit canonical log level.
type LogLevel string

// Supported LogLevel values, alphabetical by wire constant.
// Severity order (least to most severe) is: TRACE, DEBUG, INFO, WARN,
// ERROR, FATAL, SILENT — but the constants are declared alphabetically
// to match the cross-SDK ordering convention.
const (
	// LogLevelDebug emits diagnostic detail useful while debugging.
	LogLevelDebug LogLevel = "DEBUG"
	// LogLevelError signals a failure that the caller should handle.
	LogLevelError LogLevel = "ERROR"
	// LogLevelFatal signals a non-recoverable failure.
	LogLevelFatal LogLevel = "FATAL"
	// LogLevelInfo is the default level for routine informational messages.
	LogLevelInfo LogLevel = "INFO"
	// LogLevelSilent suppresses all output.
	LogLevelSilent LogLevel = "SILENT"
	// LogLevelTrace is the most verbose level; finer-grained than DEBUG.
	LogLevelTrace LogLevel = "TRACE"
	// LogLevelWarn signals a non-fatal condition that warrants attention.
	LogLevelWarn LogLevel = "WARN"
)

// Logger represents a logger resource from the smplkit platform.
type Logger struct {
	// ID is the logger identifier.
	ID string
	// Name is the display name for the logger.
	Name string
	// Level is the base log level (nil = inherit).
	Level *LogLevel
	// Group is the group ID (nil = no group).
	Group *string
	// Managed indicates whether smplkit controls this logger's level.
	Managed bool
	// Sources holds source metadata.
	Sources []map[string]interface{}
	// Environments maps environment names to their configuration.
	Environments map[string]interface{}
	// CreatedAt is the creation timestamp.
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	client *LoggingManagement
}

// LoggerOption configures an unsaved Logger returned by LoggingClient.New.
type LoggerOption func(*Logger)

// WithLoggerName sets the display name for a logger.
func WithLoggerName(name string) LoggerOption {
	return func(l *Logger) { l.Name = name }
}

// WithLoggerManaged sets whether smplkit controls this logger's level.
func WithLoggerManaged(managed bool) LoggerOption {
	return func(l *Logger) { l.Managed = managed }
}

// Save persists the logger to the server.
// PUT has upsert semantics: the server creates the logger if it does not exist.
// The Logger instance is updated with the server response.
func (l *Logger) Save(ctx context.Context) error {
	if l.client == nil {
		return &Error{Message: "logger was constructed without a client; cannot save"}
	}
	return l.client.updateLogger(ctx, l)
}

// Delete removes the logger from the server. Equivalent to
// mgmt.Loggers().Delete(ctx, l.ID).
func (l *Logger) Delete(ctx context.Context) error {
	if l.client == nil || l.ID == "" {
		return &Error{Message: "logger was constructed without a client or id; cannot delete"}
	}
	return l.client.Delete(ctx, l.ID)
}

// SetBaseLevel sets the logger's base level (no environment scoping).
//
// Deprecated: Use SetLevel(level, "") for the unified per-env verb that
// mirrors the Python SDK's set_level(level, *, environment=None).
func (l *Logger) SetBaseLevel(level LogLevel) {
	l.Level = &level
}

// ClearBaseLevel clears the logger's base level.
//
// Deprecated: Use ClearLevel("") for the unified per-env verb.
func (l *Logger) ClearBaseLevel() {
	l.Level = nil
}

// SetEnvironmentLevel sets the log level for a specific environment.
// Call Save to persist.
func (l *Logger) SetEnvironmentLevel(env string, level LogLevel) {
	if l.Environments == nil {
		l.Environments = make(map[string]interface{})
	}
	envData, ok := l.Environments[env].(map[string]interface{})
	if !ok {
		envData = make(map[string]interface{})
	}
	envData["level"] = string(level)
	l.Environments[env] = envData
}

// ClearEnvironmentLevel clears the log level for a specific environment.
// Call Save to persist.
func (l *Logger) ClearEnvironmentLevel(env string) {
	if l.Environments == nil {
		return
	}
	envData, ok := l.Environments[env].(map[string]interface{})
	if !ok {
		return
	}
	delete(envData, "level")
	if len(envData) == 0 {
		delete(l.Environments, env)
	} else {
		l.Environments[env] = envData
	}
}

// ClearAllEnvironmentLevels clears all environment-specific levels.
// Call Save to persist.
func (l *Logger) ClearAllEnvironmentLevels() {
	l.Environments = make(map[string]interface{})
}

func (l *Logger) apply(other *Logger) {
	l.ID = other.ID
	l.Name = other.Name
	l.Level = other.Level
	l.Group = other.Group
	l.Managed = other.Managed
	l.Sources = other.Sources
	l.Environments = other.Environments
	l.CreatedAt = other.CreatedAt
	l.UpdatedAt = other.UpdatedAt
}

// LogGroup represents a log group resource from the smplkit platform.
type LogGroup struct {
	// ID is the log group identifier.
	ID string
	// Name is the display name for the log group.
	Name string
	// Level is the base log level (nil = inherit).
	Level *LogLevel
	// Group is the parent group ID (nil = no parent).
	Group *string
	// Environments maps environment names to their configuration.
	Environments map[string]interface{}
	// CreatedAt is the creation timestamp.
	CreatedAt *time.Time
	// UpdatedAt is the last-modified timestamp.
	UpdatedAt *time.Time

	client *LoggingManagement
}

// LogGroupOption configures an unsaved LogGroup returned by LoggingClient.NewGroup.
type LogGroupOption func(*LogGroup)

// WithLogGroupName sets the display name for a log group.
func WithLogGroupName(name string) LogGroupOption {
	return func(g *LogGroup) { g.Name = name }
}

// WithLogGroupParent sets the parent group UUID.
func WithLogGroupParent(groupID string) LogGroupOption {
	return func(g *LogGroup) { g.Group = &groupID }
}

// Save persists the log group to the server.
// The LogGroup instance is updated with the server response.
func (g *LogGroup) Save(ctx context.Context) error {
	if g.CreatedAt == nil {
		return g.client.createGroup(ctx, g)
	}
	return g.client.updateGroup(ctx, g)
}

// SetLevel sets the base log level. Call Save to persist.
func (g *LogGroup) SetLevel(level LogLevel) {
	g.Level = &level
}

// ClearLevel clears the base log level. Call Save to persist.
func (g *LogGroup) ClearLevel() {
	g.Level = nil
}

// SetEnvironmentLevel sets the log level for a specific environment.
// Call Save to persist.
func (g *LogGroup) SetEnvironmentLevel(env string, level LogLevel) {
	if g.Environments == nil {
		g.Environments = make(map[string]interface{})
	}
	envData, ok := g.Environments[env].(map[string]interface{})
	if !ok {
		envData = make(map[string]interface{})
	}
	envData["level"] = string(level)
	g.Environments[env] = envData
}

// ClearEnvironmentLevel clears the log level for a specific environment.
// Call Save to persist.
func (g *LogGroup) ClearEnvironmentLevel(env string) {
	if g.Environments == nil {
		return
	}
	envData, ok := g.Environments[env].(map[string]interface{})
	if !ok {
		return
	}
	delete(envData, "level")
	if len(envData) == 0 {
		delete(g.Environments, env)
	} else {
		g.Environments[env] = envData
	}
}

// ClearAllEnvironmentLevels clears all environment-specific levels.
// Call Save to persist.
func (g *LogGroup) ClearAllEnvironmentLevels() {
	g.Environments = make(map[string]interface{})
}

func (g *LogGroup) apply(other *LogGroup) {
	g.ID = other.ID
	g.Name = other.Name
	g.Level = other.Level
	g.Group = other.Group
	g.Environments = other.Environments
	g.CreatedAt = other.CreatedAt
	g.UpdatedAt = other.UpdatedAt
}

// LoggerSource describes a logger observed in a remote service process.
// Used with LoggingManagement.RegisterSources to seed source discovery data
// (e.g. for sample-data loading or cross-service migration) without running
// the actual service.
type LoggerSource struct {
	// ID is the normalized logger name (e.g. "sqlalchemy.engine").
	ID string
	// Service overrides the client's own service name.
	// Nil means use the client's service.
	Service *string
	// Environment overrides the client's own environment.
	// Nil means use the client's environment.
	Environment *string
	// ResolvedLevel is the effective log level observed in the process.
	ResolvedLevel *LogLevel
}

// LoggerSourceOption configures a LoggerSource.
type LoggerSourceOption func(*LoggerSource)

// WithLoggerSourceService sets an explicit service name for the source.
func WithLoggerSourceService(service string) LoggerSourceOption {
	return func(s *LoggerSource) { s.Service = &service }
}

// WithLoggerSourceEnvironment sets an explicit environment for the source.
func WithLoggerSourceEnvironment(env string) LoggerSourceOption {
	return func(s *LoggerSource) { s.Environment = &env }
}

// WithLoggerSourceResolvedLevel sets the effective log level for the source.
func WithLoggerSourceResolvedLevel(level LogLevel) LoggerSourceOption {
	return func(s *LoggerSource) { s.ResolvedLevel = &level }
}

// NewLoggerSource creates a LoggerSource with the given logger ID and options.
func NewLoggerSource(id string, opts ...LoggerSourceOption) LoggerSource {
	s := LoggerSource{ID: id}
	for _, opt := range opts {
		opt(&s)
	}
	return s
}

// LoggerChangeEvent describes a logger whose effective level was just
// re-applied. The SDK fires one event per logger per trigger — never a
// batch / summary event and never a deletion-flavored event. If a
// trigger removes a logger from server-side tracking, the deleted key
// itself fires nothing; dependents whose effective level moves fire
// normal change events through this same payload.
type LoggerChangeEvent struct {
	// ID is the normalized logger id whose effective level just moved.
	ID string
	// Level is the newly-applied effective level.
	Level *LogLevel
	// Source identifies the trigger ("websocket", "manual").
	Source string
}
