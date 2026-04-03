package smplkit

import (
	"net/http"
	"time"
)

// clientConfig holds internal configuration for the Client, populated by
// functional options passed to NewClient.
type clientConfig struct {
	baseURL    string
	timeout    time.Duration
	httpClient *http.Client
	service    string
}

// defaultConfig returns sensible defaults for a new Client.
func defaultConfig() clientConfig {
	return clientConfig{
		baseURL: "https://config.smplkit.com",
		timeout: 30 * time.Second,
	}
}

// ClientOption configures the Client. Pass options to NewClient.
type ClientOption func(*clientConfig)

// WithBaseURL overrides the default API base URL.
func WithBaseURL(url string) ClientOption {
	return func(c *clientConfig) {
		c.baseURL = url
	}
}

// WithTimeout sets the HTTP request timeout. The default is 30 seconds.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) {
		c.timeout = d
	}
}

// WithHTTPClient replaces the default HTTP client entirely. When set, the
// WithTimeout option is ignored because the caller controls the client.
func WithHTTPClient(c *http.Client) ClientOption {
	return func(cfg *clientConfig) {
		cfg.httpClient = c
	}
}

// WithService sets the service name for automatic context injection.
// When set, the SDK registers the service as a context instance during
// Connect() and includes it in flag evaluation context.
func WithService(name string) ClientOption {
	return func(cfg *clientConfig) {
		cfg.service = name
	}
}
