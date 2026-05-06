package smplkit

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/smplkit/go-sdk/v3/internal/debug"
	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
	genconfig "github.com/smplkit/go-sdk/v3/internal/generated/config"
	genflags "github.com/smplkit/go-sdk/v3/internal/generated/flags"
	genlogging "github.com/smplkit/go-sdk/v3/internal/generated/logging"
)

// Client is the top-level entry point for the smplkit SDK.
//
// Create one with NewClient and access sub-clients via accessor methods:
//
//	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_api_...", Environment: "production", Service: "my-service"})
//	cfgs, err := client.Config().Management().List(ctx)
type Client struct {
	apiKey       string
	environment  string
	service      string
	appURL       string
	httpClient   *http.Client
	appGenerated genapp.ClientInterface

	config     *ConfigClient
	flags      *FlagsClient
	logging    *LoggingClient
	audit      *AuditClient
	management *ManagementClient

	// Shared context registration buffer — used by both the flags runtime's
	// auto-registration path and client.Management().Contexts().Register().
	contextBuf *contextRegistrationBuffer

	metrics *metricsReporter

	wsMu sync.Mutex
	ws   *sharedWebSocket
}

// serviceURL builds a per-service URL from the client configuration.
// If baseURLOverride is set (test mode), all services share that URL.
// Otherwise the URL is {scheme}://{subdomain}.{baseDomain}.
func serviceURL(opts clientConfig, subdomain string, rc *resolvedConfig) string {
	if opts.baseURLOverride != "" {
		return opts.baseURLOverride
	}
	return fmt.Sprintf("%s://%s.%s", rc.scheme, subdomain, rc.baseDomain)
}

// NewClient creates a new smplkit API client.
//
// Configuration is resolved from four layers (later layers override earlier ones):
//  1. Defaults (scheme=https, baseDomain=smplkit.com)
//  2. Config file (~/.smplkit) with [common] and profile sections
//  3. Environment variables (SMPLKIT_API_KEY, SMPLKIT_ENVIRONMENT, etc.)
//  4. Explicit Config struct fields
//
// Use ClientOption functions to customize the timeout or HTTP client.
func NewClient(cfg Config, opts ...ClientOption) (*Client, error) {
	rc, err := resolveConfig(cfg)
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

	var httpClient *http.Client
	if optCfg.httpClient != nil {
		httpClient = optCfg.httpClient
	} else {
		httpClient = &http.Client{
			Timeout: optCfg.timeout,
		}
	}

	// Wrap the transport with auth.
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{
		token: rc.apiKey,
		base:  base,
	}

	configURL := serviceURL(optCfg, "config", rc)
	flagsURL := serviceURL(optCfg, "flags", rc)
	appURL := serviceURL(optCfg, "app", rc)
	logURL := serviceURL(optCfg, "logging", rc)
	auditURL := serviceURL(optCfg, "audit", rc)

	// Build the generated config client, passing the auth-wrapped httpClient
	// and a request editor that injects Accept + User-Agent headers.
	headerEditor := genconfig.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genConfigClient, _ := genconfig.NewClient(configURL,
		genconfig.WithHTTPClient(httpClient),
		headerEditor,
	)

	// Build the generated flags client with the same pattern.
	flagsHeaderEditor := genflags.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genFlagsClient, _ := genflags.NewClient(flagsURL,
		genflags.WithHTTPClient(httpClient),
		flagsHeaderEditor,
	)

	// Build the generated app client for context operations.
	appHeaderEditor := genapp.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genAppClient, _ := genapp.NewClient(appURL,
		genapp.WithHTTPClient(httpClient),
		appHeaderEditor,
	)

	// Build the generated logging client.
	loggingHeaderEditor := genlogging.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genLoggingClient, _ := genlogging.NewClient(logURL,
		genlogging.WithHTTPClient(httpClient),
		loggingHeaderEditor,
	)

	// Build the generated audit client.
	auditHeaderEditor := genaudit.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genAuditRaw, _ := genaudit.NewClient(auditURL,
		genaudit.WithHTTPClient(httpClient),
		auditHeaderEditor,
	)
	genAuditClient := &genaudit.ClientWithResponses{ClientInterface: genAuditRaw}

	ctxBuf := newContextRegistrationBuffer()

	c := &Client{
		apiKey:       rc.apiKey,
		environment:  rc.environment,
		service:      rc.service,
		appURL:       appURL,
		httpClient:   httpClient,
		appGenerated: genAppClient,
		contextBuf:   ctxBuf,
	}

	if !rc.disableTelemetry {
		c.metrics = newMetricsReporter(httpClient, appURL, rc.environment, rc.service, 0)
	}

	c.config = &ConfigClient{client: c, generated: genConfigClient}
	c.flags = &FlagsClient{client: c, generated: genFlagsClient, appGenerated: genAppClient}
	c.flags.runtime = newFlagsRuntime(c.flags, ctxBuf)
	c.logging = newLoggingClient(c, genLoggingClient)
	auditEvents := &AuditEvents{gen: genAuditClient, buffer: newAuditEventBuffer(genAuditClient)}
	c.audit = &AuditClient{client: c, gen: genAuditClient, events: auditEvents}

	// Build the management surface directly against the generated API
	// clients so the management plane doesn't depend on the runtime
	// sub-clients (rule 1 — no skeleton inversion). The runtime
	// sub-clients then attach themselves to the same management
	// instances so .Management() returns the same object.
	c.management = assembleManagementClient(false, optCfg, rc, httpClient, genAppClient, genConfigClient, genFlagsClient, genLoggingClient)
	c.management.client = c
	c.management.contextBuf = ctxBuf // share the runtime's buffer so context registration coalesces
	c.config.management = c.management.configMgmt
	c.config.management.attachRuntime(c.config)
	c.flags.management = c.management.flagsMgmt
	c.flags.management.attachRuntime(c.flags)
	c.logging.management = c.management.loggingMgmt
	c.logging.management.attachRuntime(c.logging)

	prefixLen := min(10, len(rc.apiKey))
	maskedKey := rc.apiKey[:prefixLen] + "..."
	debug.Debug("lifecycle", "Client created (api_key=%s, environment=%s, service=%s)", maskedKey, rc.environment, rc.service)

	return c, nil
}

// Environment returns the resolved environment name.
func (c *Client) Environment() string { return c.environment }

// Service returns the resolved service name.
func (c *Client) Service() string { return c.service }

// Config returns the sub-client for config management operations.
func (c *Client) Config() *ConfigClient {
	return c.config
}

// Flags returns the sub-client for flags management and runtime operations.
func (c *Client) Flags() *FlagsClient {
	return c.flags
}

// Logging returns the sub-client for logging management and runtime operations.
func (c *Client) Logging() *LoggingClient {
	return c.logging
}

// Audit returns the sub-client for the audit service (ADR-047).
//
// Use ``client.Audit().Events().Create(...)`` to record an event;
// the call is fire-and-forget — the SDK buffers the event in memory
// and a background goroutine flushes the buffer with retry on
// transient failures.

// Management returns the sub-client for app-service management operations
// (environments, context types, contexts, account settings).
func (c *Client) Management() *ManagementClient {
	return c.management
}

// Close releases all resources held by the client and its sub-clients.
func (c *Client) Close() error {
	debug.Debug("lifecycle", "Client.Close() called")
	if c.logging != nil {
		c.logging.close()
	}
	if c.audit != nil && c.audit.events != nil {
		c.audit.events.close()
	}
	c.stopWS()
	if c.metrics != nil {
		c.metrics.Close()
	}
	return nil
}

// registerServiceContext sends environment and service context registrations to the app service.
// Errors are logged but not returned.
func (c *Client) registerServiceContext(ctx context.Context) {
	svcAttrs := map[string]interface{}{"name": c.service}
	reqBody := genapp.ContextBulkRegister{
		Contexts: []genapp.ContextBulkItem{
			{
				Type: "environment",
				Key:  c.environment,
			},
			{
				Type:       "service",
				Key:        c.service,
				Attributes: &svcAttrs,
			},
		},
	}
	resp, err := c.appGenerated.BulkRegisterContextsWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		log.Printf("smplkit: failed to register service context: %s", err.Error())
		debug.Debug("api", "service context registration error details: %+v", err)
		return
	}
	resp.Body.Close()
}

// ensureWS returns the shared WebSocket connection.
func (c *Client) ensureWS() *sharedWebSocket {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.ws == nil {
		c.ws = newSharedWebSocket(c.appURL, c.apiKey, c.metrics)
		c.ws.start()
	}
	return c.ws
}

// WaitUntilReady eagerly opens the live-updates WebSocket and blocks
// until the server has accepted the upgrade, validated the API key,
// and registered the subscription. After this returns, on-change
// listeners are guaranteed to receive every server event from this
// point forward — including events triggered by writes the caller
// fires immediately afterward.
//
// Without this, code that constructs a Client and immediately calls a
// management write (Save / Delete / SetX+Save) can race the broadcast
// of the resulting change event and silently miss it: the SDK has not
// yet appeared in the server's subscriber registry when the broadcast
// runs, so the broadcast goes to zero subscribers.
//
// timeout=0 uses the SDK default (5s). Context cancellation also
// returns. Mirrors Python's client.wait_until_ready() and
// TypeScript's client.waitUntilReady().
func (c *Client) WaitUntilReady(ctx context.Context, timeout time.Duration) error {
	if timeout == 0 {
		timeout = wsConnectTimeout
	}
	return c.ensureWS().waitConnected(ctx, timeout)
}

// stopWS stops the shared WebSocket connection.
func (c *Client) stopWS() {
	c.wsMu.Lock()
	ws := c.ws
	c.wsMu.Unlock()
	if ws != nil {
		ws.stop()
	}
}
