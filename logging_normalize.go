package smplkit

import "strings"

// NormalizeLoggerName normalizes a logger name to the canonical dot-separated,
// lowercase form. For example: "myapp/database:queries" becomes "myapp.database.queries".
func NormalizeLoggerName(name string) string {
	s := strings.NewReplacer("/", ".", ":", ".").Replace(name)
	return strings.ToLower(s)
}
