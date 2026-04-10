package smplkit

import "strings"

// resolveLoggerLevel resolves the effective log level for a logger key.
// Resolution checks the logger's own level, its group hierarchy, and
// ancestor loggers by name, falling back to INFO if no level is found.
func resolveLoggerLevel(
	loggerKey string,
	environment string,
	loggers map[string]map[string]interface{},
	groups map[string]map[string]interface{},
) LogLevel {
	if level := resolveLoggerLevelInternal(loggerKey, environment, loggers, groups); level != "" {
		return level
	}

	// Walk dot-notation ancestry.
	parts := strings.Split(loggerKey, ".")
	for i := len(parts) - 1; i > 0; i-- {
		ancestor := strings.Join(parts[:i], ".")
		if level := resolveLoggerLevelInternal(ancestor, environment, loggers, groups); level != "" {
			return level
		}
	}

	return LogLevelInfo
}

// resolveLoggerLevelInternal resolves the level for a single logger key
// (without walking dot-notation ancestors).
func resolveLoggerLevelInternal(
	loggerKey string,
	environment string,
	loggers map[string]map[string]interface{},
	groups map[string]map[string]interface{},
) LogLevel {
	logger, ok := loggers[loggerKey]
	if !ok {
		return ""
	}

	// 1. Logger's environment-specific level.
	if environment != "" {
		if envs, ok := logger["environments"].(map[string]interface{}); ok {
			if envData, ok := envs[environment].(map[string]interface{}); ok {
				if level, ok := envData["level"].(string); ok && level != "" {
					return LogLevel(level)
				}
			}
		}
	}

	// 2. Logger's own base level.
	if level, ok := logger["level"].(string); ok && level != "" {
		return LogLevel(level)
	}

	// 3. Group chain resolution.
	if groupID, ok := logger["group"].(string); ok && groupID != "" {
		if level := resolveGroupLevel(groupID, environment, groups, make(map[string]bool)); level != "" {
			return level
		}
	}

	return ""
}

// resolveGroupLevel walks the group hierarchy to find a level.
func resolveGroupLevel(
	groupID string,
	environment string,
	groups map[string]map[string]interface{},
	visited map[string]bool,
) LogLevel {
	if visited[groupID] {
		return "" // Cycle detection.
	}
	visited[groupID] = true

	group, ok := groups[groupID]
	if !ok {
		return ""
	}

	// Environment-specific level.
	if environment != "" {
		if envs, ok := group["environments"].(map[string]interface{}); ok {
			if envData, ok := envs[environment].(map[string]interface{}); ok {
				if level, ok := envData["level"].(string); ok && level != "" {
					return LogLevel(level)
				}
			}
		}
	}

	// Base level.
	if level, ok := group["level"].(string); ok && level != "" {
		return LogLevel(level)
	}

	// Parent group.
	if parentID, ok := group["group"].(string); ok && parentID != "" {
		return resolveGroupLevel(parentID, environment, groups, visited)
	}

	return ""
}
