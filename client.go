package smplkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
	genflags "github.com/smplkit/go-sdk/internal/generated/flags"
)

const appBaseURL = "https://app.smplkit.com"

// Client is the top-level entry point for the smplkit SDK.
//
// Create one with NewClient and access sub-clients via accessor methods:
//
//	client, err := smplkit.NewClient("sk_api_...", "production")
//	err = client.Connect(ctx)
//	cfgs, err := client.Config().List(ctx)
type Client struct {
	apiKey      string
	environment string
	service     string
	baseURL     string
	httpClient  *http.Client

	config *ConfigClient
	flags  *FlagsClient

	wsMu      sync.Mutex
	ws        *sharedWebSocket
	connected bool
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
// Use ClientOption functions to customize the base URL, timeout, HTTP client,
// or service name.
func NewClient(apiKey string, environment string, opts ...ClientOption) (*Client, error) {
	resolved, err := resolveAPIKey(apiKey)
	if err != nil {
		return nil, err
	}

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

	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	resolvedService := cfg.service
	if resolvedService == "" {
		resolvedService = os.Getenv("SMPLKIT_SERVICE")
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

	c := &Client{
		apiKey:      resolved,
		environment: resolvedEnv,
		service:     resolvedService,
		baseURL:     cfg.baseURL,
		httpClient:  httpClient,
	}
	c.config = &ConfigClient{client: c, generated: genConfigClient}
	c.flags = &FlagsClient{client: c, generated: genFlagsClient}
	c.flags.runtime = newFlagsRuntime(c.flags)
	return c, nil
}

// Environment returns the resolved environment name.
func (c *Client) Environment() string { return c.environment }

// Service returns the resolved service name, or empty string if not set.
func (c *Client) Service() string { return c.service }

// Config returns the sub-client for config management operations.
func (c *Client) Config() *ConfigClient {
	return c.config
}

// Flags returns the sub-client for flags management and runtime operations.
func (c *Client) Flags() *FlagsClient {
	return c.flags
}

// Connect connects to the smplkit platform: fetches initial flag and config
// data, opens the shared WebSocket, and registers the service as a context
// instance (if provided).
//
// This method is idempotent — calling it multiple times is safe.
func (c *Client) Connect(ctx context.Context) error {
	if c.connected {
		return nil
	}

	// Register service context (fire-and-forget)
	if c.service != "" {
		c.registerServiceContext(ctx)
	}

	// Connect flags (fetch definitions, register WS listeners)
	if err := c.flags.connectInternal(ctx, c.environment); err != nil {
		return err
	}

	// Connect config (fetch all, resolve, cache)
	if err := c.config.connectInternal(ctx, c.environment); err != nil {
		return err
	}

	c.connected = true
	return nil
}

// registerServiceContext sends a service context registration to the app service.
// Errors are logged but not returned (fire-and-forget).
func (c *Client) registerServiceContext(ctx context.Context) {
	payload := map[string]interface{}{
		"contexts": []map[string]interface{}{
			{
				"type":       "service",
				"key":        c.service,
				"attributes": map[string]interface{}{"name": c.service},
			},
		},
	}
	// json.Marshal cannot fail here — the payload contains only string values.
	body, _ := json.Marshal(payload)

	appURL := appBaseURL
	if c.baseURL != "" && c.baseURL != defaultConfig().baseURL {
		appURL = c.baseURL
	}
	url := fmt.Sprintf("%s/api/v1/contexts/bulk", appURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("smplkit: failed to register service context: %v", err)
		return
	}
	resp.Body.Close()
}

// ensureWS returns the shared WebSocket, starting it if needed.
func (c *Client) ensureWS() *sharedWebSocket {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.ws == nil {
		c.ws = newSharedWebSocket(appBaseURL, c.apiKey)
		c.ws.start()
	}
	return c.ws
}

// stopWS stops the shared WebSocket if running.
func (c *Client) stopWS() { //nolint:unused // lifecycle method for future Close() implementation
	c.wsMu.Lock()
	ws := c.ws
	c.wsMu.Unlock()
	if ws != nil {
		ws.stop()
	}
}
