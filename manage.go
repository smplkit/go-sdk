package smplkit

import (
	"context"
	"fmt"
	"net/http"

	"github.com/smplkit/go-sdk/v3/internal/debug"
	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
	genconfig "github.com/smplkit/go-sdk/v3/internal/generated/config"
	genflags "github.com/smplkit/go-sdk/v3/internal/generated/flags"
	genlogging "github.com/smplkit/go-sdk/v3/internal/generated/logging"
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
// thread, no websocket, no logger discovery, no synchronous outbound HTTP
// calls. Use this client for setup scripts, CI/CD jobs, admin tools, and
// anywhere else the goal is CRUD against the platform — not runtime
// instrumentation.
//
// Mirrors Python's SmplManagementClient (rule 1 of the cross-SDK
// overhaul). The Go implementation owns its sub-management surfaces
// directly — no runtime-Client skeleton is constructed.
func NewManagementClient(cfg ManagementConfig, opts ...ClientOption) (*ManagementClient, error) {
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

	httpClient, genApp, genCfg, genFlags, genLogging := buildGenClients(optCfg, rc)
	genAudit := buildAuditGenClient(optCfg, rc, httpClient)
	return assembleManagementClient(true, optCfg, rc, httpClient, genApp, genCfg, genFlags, genLogging, genAudit), nil
}

// buildGenClients constructs the four generated API clients and wires
// the auth + headers transport. Shared between NewClient (runtime) and
// NewManagementClient (standalone management).
func buildGenClients(optCfg clientConfig, rc *resolvedConfig) (
	*http.Client,
	genapp.ClientInterface,
	genconfig.ClientInterface,
	genflags.ClientInterface,
	genlogging.ClientInterface,
) {
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
	genApp, _ := genapp.NewClient(appURL,
		genapp.WithHTTPClient(httpClient),
		genapp.WithRequestEditorFn(headerEditor),
	)
	genCfg, _ := genconfig.NewClient(configURL,
		genconfig.WithHTTPClient(httpClient),
		genconfig.WithRequestEditorFn(headerEditor),
	)
	genFlags, _ := genflags.NewClient(flagsURL,
		genflags.WithHTTPClient(httpClient),
		genflags.WithRequestEditorFn(headerEditor),
	)
	genLogging, _ := genlogging.NewClient(logURL,
		genlogging.WithHTTPClient(httpClient),
		genlogging.WithRequestEditorFn(headerEditor),
	)
	return httpClient, genApp, genCfg, genFlags, genLogging
}

// buildAuditGenClient constructs the generated audit API client for the
// management plane. Unlike the runtime path, no extra environment/service
// headers are injected — management callers authenticate via the API key
// only (set by authTransport on httpClient).
func buildAuditGenClient(optCfg clientConfig, rc *resolvedConfig, httpClient *http.Client) *genaudit.ClientWithResponses {
	auditURL := serviceURL(optCfg, "audit", rc)
	headerEditor := genaudit.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	raw, _ := genaudit.NewClient(auditURL, genaudit.WithHTTPClient(httpClient), headerEditor)
	return &genaudit.ClientWithResponses{ClientInterface: raw}
}

// assembleManagementClient wires the management surfaces directly against
// the generated API clients — no runtime skeleton.
func assembleManagementClient(
	standalone bool,
	_ clientConfig,
	_ *resolvedConfig,
	_ *http.Client,
	genApp genapp.ClientInterface,
	genCfg genconfig.ClientInterface,
	genFlags genflags.ClientInterface,
	genLogging genlogging.ClientInterface,
	genAudit *genaudit.ClientWithResponses,
) *ManagementClient {
	mgmt := &ManagementClient{
		appClient:  genApp,
		contextBuf: newContextRegistrationBuffer(),
		standalone: standalone,
	}

	cfgMgmt := newConfigManagement(genCfg)
	flagsMgmt := newFlagsManagement(genFlags, genApp)
	loggingMgmt := newLoggingManagement(genLogging)

	mgmt.configMgmt = cfgMgmt
	mgmt.flagsMgmt = flagsMgmt
	mgmt.loggersMgmt = &LoggersManagement{logging: loggingMgmt}
	mgmt.logGroupsMgmt = &LogGroupsManagement{logging: loggingMgmt}
	mgmt.loggingMgmt = loggingMgmt
	mgmt.auditMgmt = &AuditManagement{
		forwarders: &AuditForwarders{gen: genAudit},
	}

	return mgmt
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
// The same *ManagementClient is shared by the runtime sub-clients —
// client.Config().Management() returns the same instance as
// client.Manage().Config().
func (c *Client) Manage() *ManagementClient { return c.management }

// Config returns the sub-client for config CRUD (mgmt.config).
// Mirrors Python's mgmt.config namespace.
func (m *ManagementClient) Config() *ConfigManagement {
	return m.configMgmt
}

// Flags returns the sub-client for flag CRUD (mgmt.flags).
func (m *ManagementClient) Flags() *FlagsManagement {
	return m.flagsMgmt
}

// Loggers returns the sub-client for logger CRUD (mgmt.loggers).
// Split from the older combined LoggingManagement so loggers and log
// groups live in distinct namespaces.
func (m *ManagementClient) Loggers() *LoggersManagement {
	return m.loggersMgmt
}

// LogGroups returns the sub-client for log-group CRUD (mgmt.log_groups).
func (m *ManagementClient) LogGroups() *LogGroupsManagement {
	return m.logGroupsMgmt
}

// Audit returns the sub-client for audit forwarder CRUD (mgmt.audit).
func (m *ManagementClient) Audit() *AuditManagement {
	return m.auditMgmt
}

// Close releases HTTP resources held by this management client.
// No-op for the management client returned from a runtime Client —
// close the runtime Client instead so its WebSocket / metrics / context
// flush all unwind in order. For a standalone management client built
// via NewManagementClient, Close has nothing to release (the underlying
// http.Client is owned by net/http and pooled there) but is provided
// for symmetry and to give customers a clean defer point.
func (m *ManagementClient) Close() error { return nil }

// resolveManagementConfig projects a ManagementConfig to the internal
// resolved-config shape, supplying placeholder values for the
// runtime-only fields.
func resolveManagementConfig(cfg ManagementConfig) (*resolvedConfig, error) {
	rc, err := resolveConfig(Config{
		Profile:    cfg.Profile,
		APIKey:     cfg.APIKey,
		BaseDomain: cfg.BaseDomain,
		Scheme:     cfg.Scheme,
		// management never reads environment/service, but resolveConfig
		// rejects empty values; pass harmless placeholders.
		Environment:      "_mgmt_",
		Service:          "_mgmt_",
		Debug:            cfg.Debug,
		DisableTelemetry: true,
	})
	if err != nil {
		return nil, fmt.Errorf("smplkit: management client config error: %w", err)
	}
	return rc, nil
}
