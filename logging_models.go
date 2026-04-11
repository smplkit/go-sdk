package smplkit

import (
	"context"
	"time"
)

// LogLevel represents a smplkit canonical log level.
type LogLevel string

const (
	LogLevelTrace  LogLevel = "TRACE"
	LogLevelDebug  LogLevel = "DEBUG"
	LogLevelInfo   LogLevel = "INFO"
	LogLevelWarn   LogLevel = "WARN"
	LogLevelError  LogLevel = "ERROR"
	LogLevelFatal  LogLevel = "FATAL"
	LogLevelSilent LogLevel = "SILENT"
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

	client *LoggingClient
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
// The Logger instance is updated with the server response.
func (l *Logger) Save(ctx context.Context) error {
	if l.CreatedAt == nil {
		return l.client.createLogger(ctx, l)
	}
	return l.client.updateLogger(ctx, l)
}

// SetLevel sets the base log level. Call Save to persist.
func (l *Logger) SetLevel(level LogLevel) {
	l.Level = &level
}

// ClearLevel clears the base log level. Call Save to persist.
func (l *Logger) ClearLevel() {
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

	client *LoggingClient
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

// LoggerChangeEvent describes a logger definition change.
type LoggerChangeEvent struct {
	// ID is the logger ID that changed.
	ID string
	// Level is the new resolved level (nil if deleted).
	Level *LogLevel
	// Source is "websocket" or "refresh".
	Source string
}
