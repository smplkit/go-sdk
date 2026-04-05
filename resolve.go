package smplkit

import (
	"os"
	"path/filepath"
	"strings"
)

// resolveAPIKey resolves an API key from an explicit value, the SMPLKIT_API_KEY
// environment variable, or the ~/.smplkit config file. The environment parameter
// is used to select the config file section: [{environment}] is tried first,
// then [default]. It returns an error if none of these provide a key.
func resolveAPIKey(explicit string, environment string) (string, error) {
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
			if apiKey := parseAPIKeyFromConfig(string(data), environment); apiKey != "" {
				return apiKey, nil
			}
		}
	}

	return "", &SmplError{
		Message: "No API key provided. Set one of:\n" +
			"  1. Pass apiKey to NewClient()\n" +
			"  2. Set the SMPLKIT_API_KEY environment variable\n" +
			"  3. Create a ~/.smplkit file with:\n" +
			"     [" + environment + "]\n" +
			"     api_key = your_key_here",
	}
}

// parseAPIKeyFromConfig extracts api_key from an INI-format config file content.
// It tries the [{environment}] section first, then falls back to [default].
func parseAPIKeyFromConfig(content string, environment string) string {
	// Try environment-specific section first, then default.
	if key := extractAPIKeyFromSection(content, "["+environment+"]"); key != "" {
		return key
	}
	return extractAPIKeyFromSection(content, "[default]")
}

// extractAPIKeyFromSection extracts api_key from a specific INI section.
func extractAPIKeyFromSection(content string, section string) string {
	inSection := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			inSection = strings.EqualFold(trimmed, section)
			continue
		}
		if inSection && strings.HasPrefix(trimmed, "api_key") {
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
