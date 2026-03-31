package smplkit

import (
	"os"
	"path/filepath"
	"strings"
)

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
			"  3. Create a ~/.smplkit file with:\n" +
			"     [default]\n" +
			"     api_key = your_key_here",
	}
}

// parseAPIKeyFromConfig extracts api_key from an INI-format config file content.
func parseAPIKeyFromConfig(content string) string {
	inDefault := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			inDefault = strings.EqualFold(trimmed, "[default]")
			continue
		}
		if inDefault && strings.HasPrefix(trimmed, "api_key") {
			if eqIdx := strings.Index(trimmed, "="); eqIdx != -1 {
				value := strings.TrimSpace(trimmed[eqIdx+1:])
				if value != "" {
					return value
				}
			}
		}
	}
	return ""
}
