package smplkit

import (
	"context"
	"fmt"
	"net/http"

	"github.com/smplkit/go-sdk/internal/debug"
	genapp "github.com/smplkit/go-sdk/internal/generated/app"
	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
	genflags "github.com/smplkit/go-sdk/internal/generated/flags"
	genlogging "github.com/smplkit/go-sdk/internal/generated/logging"
)

// ManagementConfig holds the construction-time configuration for a
// management-only client. Mirrors smplkit.Config but drops the runtime
// fields (Environment, Service) that don't apply to management work.
//
// All fields are optional. When omitted, the SDK resolves them from
// SMPLKIT_* environment variables or the ~/.smplkit profile.
type ManagementConfig struct {
	// Profile selects an INI section in ~/.smplkit (defaults to "default").
	Profile string
	// APIKey authenticates with the smplkit platform.
	APIKey string
	// BaseDomain is the base domain for API requests (default "smplkit.com").
	BaseDomain string
	// Scheme is the URL scheme (default "https").
	Scheme string
	// Debug enables debug output in the SDK.
	Debug bool
}

// NewManagementClient creates a new management-only smplkit client.
//
// Construction has zero side effects: no service registration, no metrics
// thread, no websocket, no logger discovery. Use this client for setup
// scripts, CI/CD jobs, admin tools, and anywhere else the goal is CRUD
// against the platform — not runtime instrumentation.
//
// Mirrors Python's SmplManagementClient (rule 1 of the cross-SDK overhaul).
func NewManagementClient(cfg ManagementConfig, opts ...ClientOption) (*ManagementClient, error) {
	// Project to a runtime Config (Environment / Service unused for management
	// but the resolver requires them; pass placeholders that the management
	// surface will never consult).
	rc, err := resolveManagementConfig(cfg)
	if err != nil {
		return nil, err
	}
	if rc.debug {
		debug.Enable()
	}

	optCfg := defaultConfig()
	for _, opt := range opts {
		opt(&optCfg)
	}

	httpClient := optCfg.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: optCfg.timeout}
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: rc.apiKey, base: base}

	configURL := serviceURL(optCfg, "config", rc)
	flagsURL := serviceURL(optCfg, "flags", rc)
	appURL := serviceURL(optCfg, "app", rc)
	logURL := serviceURL(optCfg, "logging", rc)

	headerEditor := func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	}
	genConfigClient, _ := genconfig.NewClient(configURL,
		genconfig.WithHTTPClient(httpClient),
		genconfig.WithRequestEditorFn(headerEditor),
	)
	genFlagsClient, _ := genflags.NewClient(flagsURL,
		genflags.WithHTTPClient(httpClient),
		genflags.WithRequestEditorFn(headerEditor),
	)
	genAppClient, _ := genapp.NewClient(appURL,
		genapp.WithHTTPClient(httpClient),
		genapp.WithRequestEditorFn(headerEditor),
	)
	genLoggingClient, _ := genlogging.NewClient(logURL,
		genlogging.WithHTTPClient(httpClient),
		genlogging.WithRequestEditorFn(headerEditor),
	)

	// Build the slim runtime-less skeleton needed to wire up sub-clients.
	skeleton := &Client{
		apiKey:       rc.apiKey,
		appURL:       appURL,
		httpClient:   httpClient,
		appGenerated: genAppClient,
		contextBuf:   newContextRegistrationBuffer(),
	}
	skeleton.config = &ConfigClient{client: skeleton, generated: genConfigClient}
	skeleton.flags = &FlagsClient{client: skeleton, generated: genFlagsClient, appGenerated: genAppClient}
	skeleton.logging = newLoggingClient(skeleton, genLoggingClient)

	mgmt := &ManagementClient{
		client:     skeleton,
		appClient:  genAppClient,
		contextBuf: skeleton.contextBuf,
	}
	skeleton.management = mgmt

	return mgmt, nil
}

// Manage returns the management-plane sub-client. Mirrors Python's
// SmplClient.manage attribute (rule 1).
//
// The returned ManagementClient exposes eight flat namespaces:
//
//	client.Manage().Contexts()
//	client.Manage().ContextTypes()
//	client.Manage().Environments()
//	client.Manage().AccountSettings()
//	client.Manage().Config()
//	client.Manage().Flags()
//	client.Manage().Loggers()
//	client.Manage().LogGroups()
//
// The runtime sub-clients (client.Config, client.Flags, client.Logging)
// no longer expose a .Management() accessor — runtime and management are
// strictly separated.
func (c *Client) Manage() *ManagementClient { return c.management }

// Config returns the sub-client for config CRUD (mgmt.config).
// Mirrors Python's mgmt.config namespace.
func (m *ManagementClient) Config() *ConfigManagement {
	return m.client.Config().Management()
}

// Flags returns the sub-client for flag CRUD (mgmt.flags).
// Mirrors Python's mgmt.flags namespace.
func (m *ManagementClient) Flags() *FlagsManagement {
	return m.client.Flags().Management()
}

// Loggers returns the sub-client for logger CRUD (mgmt.loggers).
// Mirrors Python's mgmt.loggers namespace — split from the older
// LoggingManagement to keep loggers and log groups as separate verbs.
func (m *ManagementClient) Loggers() *LoggersManagement {
	if m.loggers == nil {
		m.loggers = &LoggersManagement{logging: m.client.Logging().Management()}
	}
	return m.loggers
}

// LogGroups returns the sub-client for log-group CRUD (mgmt.log_groups).
// Mirrors Python's mgmt.log_groups namespace.
func (m *ManagementClient) LogGroups() *LogGroupsManagement {
	if m.logGroups == nil {
		m.logGroups = &LogGroupsManagement{logging: m.client.Logging().Management()}
	}
	return m.logGroups
}

// Close releases HTTP resources held by this management client.
// No-op for the management client returned from a runtime Client — close
// the runtime Client instead.
func (m *ManagementClient) Close() error {
	if m.standalone {
		return nil
	}
	return nil
}

// resolveManagementConfig projects a ManagementConfig to the same internal
// resolved-config shape used by the runtime client, supplying placeholder
// values for the runtime-only fields.
func resolveManagementConfig(cfg ManagementConfig) (*resolvedConfig, error) {
	rc, err := resolveConfig(Config{
		Profile:    cfg.Profile,
		APIKey:     cfg.APIKey,
		BaseDomain: cfg.BaseDomain,
		Scheme:     cfg.Scheme,
		// management doesn't need environment/service, but resolveConfig
		// rejects empty Service when no env-var or profile fallback exists.
		// Use harmless placeholders so resolution succeeds; nothing in the
		// management surface reads these.
		Environment:      "_mgmt_",
		Service:          "_mgmt_",
		Debug:            cfg.Debug,
		DisableTelemetry: true,
	})
	if err != nil {
		// Allow the placeholders to bypass missing-environment/missing-service
		// errors when the customer truly only set api_key/base_domain/scheme.
		return nil, fmt.Errorf("smplkit: management client config error: %w", err)
	}
	return rc, nil
}
