package smplkit

import "strings"

// NormalizeLoggerName normalizes a logger name by replacing "/" and ":" with "."
// and lowercasing the result.
// For example: "myapp/database:queries" → "myapp.database.queries".
func NormalizeLoggerName(name string) string {
	s := strings.NewReplacer("/", ".", ":", ".").Replace(name)
	return strings.ToLower(s)
}
