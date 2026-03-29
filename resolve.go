package smplkit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var apiKeyRegexp = regexp.MustCompile(`\[default\]\s*[\s\S]*?api_key\s*=\s*"([^"]+)"`)

// resolveAPIKey resolves an API key from an explicit value, the SMPLKIT_API_KEY
// environment variable, or the ~/.smplkit config file. It returns an error if
// none of these provide a key.
func resolveAPIKey(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	if envVal := os.Getenv("SMPLKIT_API_KEY"); envVal != "" {
		return envVal, nil
	}

	home, err := os.UserHomeDir()
	if err == nil {
		configPath := filepath.Join(home, ".smplkit")
		data, err := os.ReadFile(configPath)
		if err == nil {
			if apiKey := parseAPIKeyFromConfig(string(data)); apiKey != "" {
				return apiKey, nil
			}
		}
	}

	return "", &SmplError{
		Message: "No API key provided. Set one of:\n" +
			"  1. Pass apiKey to NewClient()\n" +
			"  2. Set the SMPLKIT_API_KEY environment variable\n" +
			"  3. Add api_key to [default] in ~/.smplkit",
	}
}

// parseAPIKeyFromConfig extracts api_key from a TOML-like config file content.
func parseAPIKeyFromConfig(content string) string {
	// Find [default] section first
	idx := strings.Index(content, "[default]")
	if idx == -1 {
		return ""
	}
	matches := apiKeyRegexp.FindStringSubmatch(content)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}
