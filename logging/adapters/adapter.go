package adapters

// LoggingAdapter is the interface for logging framework integration.
//
// Implement this interface to add smplkit level control for a logging framework
// (e.g., slog, zap).
type LoggingAdapter interface {
	// Name returns a human-readable adapter name for diagnostics (e.g., "slog").
	Name() string

	// Discover scans the runtime for existing loggers.
	// Returns a list of discovered loggers with their names and levels.
	Discover() []DiscoveredLogger

	// ApplyLevel sets the level on a specific logger.
	// loggerName is the original (non-normalized) name.
	// level is a smplkit LogLevel string (e.g., "DEBUG", "INFO", "WARN").
	ApplyLevel(loggerName string, level string)

	// InstallHook installs a continuous discovery hook.
	// The callback receives (original_name, smplkit_level_string) whenever
	// a new logger is created in the framework.
	// May be a no-op if the framework doesn't support creation interception.
	InstallHook(onNewLogger func(name string, level string))

	// UninstallHook removes the hook installed by InstallHook.
	// Called on client Close().
	UninstallHook()
}

// DiscoveredLogger represents a logger found during discovery.
type DiscoveredLogger struct {
	Name  string
	Level string // smplkit level string
}
