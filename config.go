package smplkit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds user-facing configuration for the smplkit SDK.
// Fields use Go zero values (empty string, false) to represent "unset".
// Resolution order: defaults -> config file -> env vars -> struct fields.
type Config struct {
	// Profile selects the INI profile from ~/.smplkit.
	// Falls back to SMPLKIT_PROFILE env var, then "default".
	Profile string

	// APIKey is the Bearer token for API authentication.
	// Falls back to SMPLKIT_API_KEY env var, then the config file.
	APIKey string

	// BaseDomain overrides the base domain for service URLs.
	// Default: "smplkit.com". Falls back to SMPLKIT_BASE_DOMAIN env var.
	BaseDomain string

	// Scheme overrides the URL scheme. Default: "https".
	// Falls back to SMPLKIT_SCHEME env var.
	Scheme string

	// Environment is the target environment (e.g. "production", "staging").
	// Falls back to SMPLKIT_ENVIRONMENT env var, then the config file.
	Environment string

	// Service is the service identifier.
	// Falls back to SMPLKIT_SERVICE env var, then the config file.
	Service string

	// Debug enables verbose debug output.
	// Falls back to SMPLKIT_DEBUG env var, then the config file.
	Debug bool

	// DisableTelemetry disables anonymous SDK usage telemetry.
	// Falls back to SMPLKIT_DISABLE_TELEMETRY env var, then the config file.
	DisableTelemetry bool

	// ExtraHeaders are additional HTTP headers sent on every request.
	// SDK-owned headers (Authorization, Accept) take precedence over any
	// key supplied here — callers cannot override them. User-Agent is the
	// exception: the SDK sends smplkit-sdk-go/<version> by default, but a
	// User-Agent supplied here (any casing) replaces it, on HTTP requests
	// and the live event stream request alike.
	ExtraHeaders map[string]string

	// DisableEventBuffering makes audit event recording synchronous. It
	// applies to the audit event write path only.
	//
	// By default Events().Record enqueues onto an in-memory buffer and
	// returns immediately; a background worker delivers events with retry
	// on transient failures. When true, no background worker is ever
	// started: every Record call performs one synchronous POST and returns
	// the SDK's typed errors on failure, Flush is meaningless (each Record
	// is already durable on return), and Close has no buffer to drain.
	// This is the right shape for serverless and edge runtimes, whose
	// instances cannot host background delivery.
	DisableEventBuffering bool

	// DisableStreaming turns off live updates for the config, flags, and
	// logging runtime surfaces.
	//
	// By default the first live call opens a shared event stream and changes
	// stream in. When true, the first live call still fetches, resolves,
	// and applies definitions once synchronously, but no event stream is
	// opened and no background goroutines or timers are created — buffer
	// flushes that would normally run in the background run inline
	// instead, and Refresh re-fetches on demand (change handlers fire from
	// refresh deltas; there is no stream to drive them otherwise). This is
	// the right shape for serverless and edge runtimes, whose instances
	// cannot host socket-driven state.
	DisableStreaming bool
}

// resolvedConfig holds fully-resolved configuration with all layers merged.
type resolvedConfig struct {
	profile               string
	apiKey                string
	baseDomain            string
	scheme                string
	environment           string
	service               string
	debug                 bool
	disableTelemetry      bool
	disableEventBuffering bool
	disableStreaming      bool
	extraHeaders          map[string]string
}

// resolveConfig merges configuration from four layers and validates that an
// api key was resolved.
//
// Only the api key is required. Environment and service are optional: when
// absent the SDK sends no environment/service signal and the server derives
// the environment from the api key, so an audit/jobs-only customer needs
// neither. They are still resolved (and used) when supplied.
//  1. Defaults
//  2. Config file (~/.smplkit) [common] + selected profile
//  3. Environment variables
//  4. Explicit Config struct fields
func resolveConfig(cfg Config) (*resolvedConfig, error) {
	rc, err := resolveConfigLayers(cfg)
	if err != nil {
		return nil, err
	}
	if err := rc.requireAPIKey(); err != nil {
		return nil, err
	}
	return rc, nil
}

// requireAPIKey returns an error if no api key was resolved from any layer.
func (rc *resolvedConfig) requireAPIKey() error {
	if rc.apiKey == "" {
		return &Error{
			Message: "No API key provided. Set one of:\n" +
				"  1. Set Config.APIKey\n" +
				"  2. Set the SMPLKIT_API_KEY environment variable\n" +
				"  3. Add api_key to your ~/.smplkit profile:\n" +
				"     [" + rc.profile + "]\n" +
				"     api_key = your_key_here",
		}
	}
	return nil
}

// resolveConfigLayers merges configuration from four layers without
// validating any required field.
// 1. Defaults
// 2. Config file (~/.smplkit) [common] + selected profile
// 3. Environment variables
// 4. Explicit Config struct fields
func resolveConfigLayers(cfg Config) (*resolvedConfig, error) {
	// Layer 1: Defaults.
	rc := &resolvedConfig{
		scheme:     "https",
		baseDomain: "smplkit.com",
	}

	// Determine profile name.
	profile := cfg.Profile
	if profile == "" {
		profile = os.Getenv("SMPLKIT_PROFILE")
	}
	if profile == "" {
		profile = "default"
	}
	rc.profile = profile

	// Layer 2: Config file.
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		configPath := filepath.Join(home, ".smplkit")
		data, readErr := os.ReadFile(configPath)
		if readErr == nil {
			sections := parseINIFile(string(data))
			applyFileSection(rc, sections, "common")

			if profile != "common" {
				profileSection, profileExists := sections[profile]
				if !profileExists && profile != "default" {
					// Named profile is missing. Error if the file has other non-common sections.
					hasOtherSections := false
					for name := range sections {
						if name != "common" {
							hasOtherSections = true
							break
						}
					}
					if hasOtherSections {
						return nil, &Error{
							Message: fmt.Sprintf("Profile [%s] not found in ~/.smplkit", profile),
						}
					}
				}
				if profileExists {
					applyFileMap(rc, profileSection)
				}
			}
		}
	}

	// Layer 3: Environment variables.
	applyEnvVar(&rc.apiKey, "SMPLKIT_API_KEY")
	applyEnvVar(&rc.baseDomain, "SMPLKIT_BASE_DOMAIN")
	applyEnvVar(&rc.scheme, "SMPLKIT_SCHEME")
	applyEnvVar(&rc.environment, "SMPLKIT_ENVIRONMENT")
	applyEnvVar(&rc.service, "SMPLKIT_SERVICE")
	applyEnvBool(&rc.debug, "SMPLKIT_DEBUG")
	applyEnvBool(&rc.disableTelemetry, "SMPLKIT_DISABLE_TELEMETRY")

	// Layer 4: Explicit Config struct fields (non-zero values override).
	if cfg.APIKey != "" {
		rc.apiKey = cfg.APIKey
	}
	if cfg.BaseDomain != "" {
		rc.baseDomain = cfg.BaseDomain
	}
	if cfg.Scheme != "" {
		rc.scheme = cfg.Scheme
	}
	if cfg.Environment != "" {
		rc.environment = cfg.Environment
	}
	if cfg.Service != "" {
		rc.service = cfg.Service
	}
	if cfg.Debug {
		rc.debug = true
	}
	if cfg.DisableTelemetry {
		rc.disableTelemetry = true
	}
	// The stateless-mode switches are struct-field only (no env var / config
	// file layer): they describe the shape of the hosting runtime, which is a
	// property of the calling code rather than of the deployment environment.
	if cfg.DisableEventBuffering {
		rc.disableEventBuffering = true
	}
	if cfg.DisableStreaming {
		rc.disableStreaming = true
	}
	if len(cfg.ExtraHeaders) > 0 {
		rc.extraHeaders = cfg.ExtraHeaders
	}

	return rc, nil
}

// parseINIFile parses an INI-format string into section -> key -> value map.
// Supports # and ; comments. Section and key names are case-sensitive.
func parseINIFile(content string) map[string]map[string]string {
	sections := make(map[string]map[string]string)
	currentSection := ""

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			currentSection = trimmed[1 : len(trimmed)-1]
			if _, ok := sections[currentSection]; !ok {
				sections[currentSection] = make(map[string]string)
			}
			continue
		}
		if currentSection != "" {
			if eqIdx := strings.Index(trimmed, "="); eqIdx != -1 {
				key := strings.TrimSpace(trimmed[:eqIdx])
				value := strings.TrimSpace(trimmed[eqIdx+1:])
				sections[currentSection][key] = value
			}
		}
	}

	return sections
}

// parseBool parses a boolean value from a config file or environment variable.
// Accepts true/1/yes and false/0/no (case-insensitive).
func parseBool(value, key string) (bool, error) {
	switch strings.ToLower(value) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, &Error{
			Message: fmt.Sprintf("Invalid boolean value %q for %s (expected true/false/1/0/yes/no)", value, key),
		}
	}
}

// applyFileSection applies values from a named section to the resolved config.
func applyFileSection(rc *resolvedConfig, sections map[string]map[string]string, name string) {
	section, ok := sections[name]
	if !ok {
		return
	}
	applyFileMap(rc, section)
}

// applyFileMap applies a key-value map from a config file section to the resolved config.
func applyFileMap(rc *resolvedConfig, m map[string]string) {
	if v, ok := m["api_key"]; ok && v != "" {
		rc.apiKey = v
	}
	if v, ok := m["base_domain"]; ok && v != "" {
		rc.baseDomain = v
	}
	if v, ok := m["scheme"]; ok && v != "" {
		rc.scheme = v
	}
	if v, ok := m["environment"]; ok && v != "" {
		rc.environment = v
	}
	if v, ok := m["service"]; ok && v != "" {
		rc.service = v
	}
	if v, ok := m["debug"]; ok && v != "" {
		if b, err := parseBool(v, "debug"); err == nil && b {
			rc.debug = true
		}
	}
	if v, ok := m["disable_telemetry"]; ok && v != "" {
		if b, err := parseBool(v, "disable_telemetry"); err == nil && b {
			rc.disableTelemetry = true
		}
	}
}

// applyEnvVar applies an environment variable to a string field if non-empty.
func applyEnvVar(field *string, envName string) {
	if v := os.Getenv(envName); v != "" {
		*field = v
	}
}

// applyEnvBool applies a boolean environment variable to a bool field.
// Only sets to true (never resets to false from env).
func applyEnvBool(field *bool, envName string) {
	if v := os.Getenv(envName); v != "" {
		if b, err := parseBool(v, envName); err == nil && b {
			*field = true
		}
	}
}
