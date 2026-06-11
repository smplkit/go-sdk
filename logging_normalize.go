package smplkit

import "strings"

// NormalizeLoggerName normalizes a logger name.
//
//   - Replace "/" with "."
//   - Replace ":" with "."
//   - Lowercase everything
func NormalizeLoggerName(name string) string {
	s := strings.NewReplacer("/", ".", ":", ".").Replace(name)
	return strings.ToLower(s)
}
