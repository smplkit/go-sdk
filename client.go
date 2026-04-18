package smplkit

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/smplkit/go-sdk/internal/debug"
	genapp "github.com/smplkit/go-sdk/internal/generated/app"
	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
	genflags "github.com/smplkit/go-sdk/internal/generated/flags"
	genlogging "github.com/smplkit/go-sdk/internal/generated/logging"
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

	config  *ConfigClient
	flags   *FlagsClient
	logging *LoggingClient

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

	c := &Client{
		apiKey:       rc.apiKey,
		environment:  rc.environment,
		service:      rc.service,
		appURL:       appURL,
		httpClient:   httpClient,
		appGenerated: genAppClient,
	}

	if !rc.disableTelemetry {
		c.metrics = newMetricsReporter(httpClient, appURL, rc.environment, rc.service, 0)
	}

	c.config = &ConfigClient{client: c, generated: genConfigClient}
	c.flags = &FlagsClient{client: c, generated: genFlagsClient, appGenerated: genAppClient}
	c.flags.runtime = newFlagsRuntime(c.flags)
	c.logging = newLoggingClient(c, genLoggingClient)

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

// Close releases all resources held by the client and its sub-clients.
func (c *Client) Close() error {
	debug.Debug("lifecycle", "Client.Close() called")
	if c.logging != nil {
		c.logging.close()
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
		log.Printf("smplkit: failed to register service context: %v", err)
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

// stopWS stops the shared WebSocket connection.
func (c *Client) stopWS() {
	c.wsMu.Lock()
	ws := c.ws
	c.wsMu.Unlock()
	if ws != nil {
		ws.stop()
	}
}
