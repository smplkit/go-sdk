// Package debug provides the SMPLKIT_DEBUG diagnostic facility.
// It is an internal package and must not be imported by external packages.
//
// Set SMPLKIT_DEBUG=1 (or "true" / "yes") to enable verbose output to stderr.
// All other values (including unset) disable output. The environment variable
// is read once at package init time and cached; changes after startup are not
// observed.
package debug

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// debugEnabled is set once at package init time.
var debugEnabled bool

func init() {
	debugEnabled = parseDebugEnv(os.Getenv("SMPLKIT_DEBUG"))
}

// parseDebugEnv returns true for the accepted truthy values: "1", "true", "yes"
// (case-insensitive, surrounding whitespace ignored). Everything else is false.
func parseDebugEnv(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	return v == "1" || v == "true" || v == "yes"
}

// IsEnabled reports whether debug output is currently enabled.
func IsEnabled() bool { return debugEnabled }

// Debug writes a single diagnostic line to stderr when debug output is enabled.
// It is a no-op when disabled and is safe to call from any goroutine.
//
// Output format: [smplkit:{subsystem}] {RFC3339Nano timestamp} {message}\n
func Debug(subsystem, format string, args ...any) {
	if !debugEnabled {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	fmt.Fprintf(os.Stderr, "[smplkit:%s] %s %s\n", subsystem, ts, msg)
}
