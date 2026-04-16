package smplkit

import (
	"context"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/smplkit/go-sdk/internal/debug"
	genapp "github.com/smplkit/go-sdk/internal/generated/app"
	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
	genflags "github.com/smplkit/go-sdk/internal/generated/flags"
	genlogging "github.com/smplkit/go-sdk/internal/generated/logging"
)

const (
	appBaseURL     = "https://app.smplkit.com"
	loggingBaseURL = "https://logging.smplkit.com"
)

// Client is the top-level entry point for the smplkit SDK.
//
// Create one with NewClient and access sub-clients via accessor methods:
//
//	client, err := smplkit.NewClient("sk_api_...", "production", "my-service")
//	cfgs, err := client.Config().Management().List(ctx)
type Client struct {
	apiKey       string
	environment  string
	service      string
	baseURL      string
	httpClient   *http.Client
	appGenerated genapp.ClientInterface

	config  *ConfigClient
	flags   *FlagsClient
	logging *LoggingClient

	metrics *metricsReporter

	wsMu sync.Mutex
	ws   *sharedWebSocket
}

// NewClient creates a new smplkit API client.
//
// The apiKey is used for Bearer token authentication on every request.
// Pass an empty string to resolve the API key automatically from the
// SMPLKIT_API_KEY environment variable or the ~/.smplkit config file.
//
// The environment is required; pass an empty string to resolve from
// SMPLKIT_ENVIRONMENT.
//
// The service is required; pass an empty string to resolve from
// SMPLKIT_SERVICE.
//
// Use ClientOption functions to customize the base URL, timeout, or HTTP client.
func NewClient(apiKey string, environment string, service string, opts ...ClientOption) (*Client, error) {
	// 1. Resolve environment first.
	resolvedEnv := environment
	if resolvedEnv == "" {
		resolvedEnv = os.Getenv("SMPLKIT_ENVIRONMENT")
	}
	if resolvedEnv == "" {
		return nil, &SmplError{
			Message: "No environment provided. Set one of:\n" +
				"  1. Pass environment to NewClient\n" +
				"  2. Set the SMPLKIT_ENVIRONMENT environment variable",
		}
	}

	// 2. Resolve service second.
	resolvedService := service
	if resolvedService == "" {
		resolvedService = os.Getenv("SMPLKIT_SERVICE")
	}
	if resolvedService == "" {
		return nil, &SmplError{
			Message: "No service provided. Set one of:\n" +
				"  1. Pass service to NewClient\n" +
				"  2. Set the SMPLKIT_SERVICE environment variable",
		}
	}

	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	// 3. Resolve API key last (receives the already-resolved environment).
	resolved, err := resolveAPIKey(apiKey, resolvedEnv)
	if err != nil {
		return nil, err
	}

	var httpClient *http.Client
	if cfg.httpClient != nil {
		httpClient = cfg.httpClient
	} else {
		httpClient = &http.Client{
			Timeout: cfg.timeout,
		}
	}

	// Wrap the transport with auth.
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{
		token: resolved,
		base:  base,
	}

	// Build the generated config client, passing the auth-wrapped httpClient
	// and a request editor that injects Accept + User-Agent headers.
	headerEditor := genconfig.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genConfigClient, _ := genconfig.NewClient(cfg.baseURL,
		genconfig.WithHTTPClient(httpClient),
		headerEditor,
	)

	// Build the generated flags client with the same pattern.
	flagsHeaderEditor := genflags.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	flagsBaseURL := "https://flags.smplkit.com"
	if cfg.baseURL != "" && cfg.baseURL != defaultConfig().baseURL {
		flagsBaseURL = cfg.baseURL
	}
	genFlagsClient, _ := genflags.NewClient(flagsBaseURL,
		genflags.WithHTTPClient(httpClient),
		flagsHeaderEditor,
	)

	// Build the generated app client for context operations.
	appHeaderEditor := genapp.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	appURL := appBaseURL
	if cfg.baseURL != "" && cfg.baseURL != defaultConfig().baseURL {
		appURL = cfg.baseURL
	}
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
	logURL := loggingBaseURL
	if cfg.baseURL != "" && cfg.baseURL != defaultConfig().baseURL {
		logURL = cfg.baseURL
	}
	genLoggingClient, _ := genlogging.NewClient(logURL,
		genlogging.WithHTTPClient(httpClient),
		loggingHeaderEditor,
	)

	c := &Client{
		apiKey:       resolved,
		environment:  resolvedEnv,
		service:      resolvedService,
		baseURL:      cfg.baseURL,
		httpClient:   httpClient,
		appGenerated: genAppClient,
	}

	if !cfg.disableTelemetry {
		c.metrics = newMetricsReporter(httpClient, appURL, resolvedEnv, resolvedService, 0)
	}

	c.config = &ConfigClient{client: c, generated: genConfigClient}
	c.flags = &FlagsClient{client: c, generated: genFlagsClient, appGenerated: genAppClient}
	c.flags.runtime = newFlagsRuntime(c.flags)
	c.logging = newLoggingClient(c, genLoggingClient)

	var maskedKey string
	if len(resolved) > 14 {
		maskedKey = resolved[:10] + "..." + resolved[len(resolved)-4:]
	} else if len(resolved) > 4 {
		maskedKey = resolved[:4] + "..."
	} else {
		maskedKey = resolved + "..."
	}
	debug.Debug("lifecycle", "Client created (api_key=%s, environment=%s, service=%s)", maskedKey, resolvedEnv, resolvedService)

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
		c.ws = newSharedWebSocket(appBaseURL, c.apiKey, c.metrics)
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
