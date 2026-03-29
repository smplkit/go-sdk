package smplkit

import (
	"context"
	"net/http"

	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
)

// Client is the top-level entry point for the smplkit SDK.
//
// Create one with NewClient and access sub-clients via accessor methods:
//
//	client, err := smplkit.NewClient("sk_api_...")
//	cfgs, err := client.Config().List(ctx)
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	config     *ConfigClient
}

// NewClient creates a new smplkit API client.
//
// The apiKey is used for Bearer token authentication on every request.
// Pass an empty string to resolve the API key automatically from the
// SMPLKIT_API_KEY environment variable or the ~/.smplkit config file.
// Use ClientOption functions to customize the base URL, timeout, or HTTP client.
func NewClient(apiKey string, opts ...ClientOption) (*Client, error) {
	resolved, err := resolveAPIKey(apiKey)
	if err != nil {
		return nil, err
	}

	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
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
	genClient, _ := genconfig.NewClient(cfg.baseURL,
		genconfig.WithHTTPClient(httpClient),
		headerEditor,
	)

	c := &Client{
		apiKey:     resolved,
		baseURL:    cfg.baseURL,
		httpClient: httpClient,
	}
	c.config = &ConfigClient{client: c, generated: genClient}
	return c, nil
}

// Config returns the sub-client for config management operations.
func (c *Client) Config() *ConfigClient {
	return c.config
}
