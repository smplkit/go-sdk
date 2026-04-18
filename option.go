package smplkit

import (
	"net/http"
	"time"
)

// clientConfig holds non-config options for the Client (timeout, HTTP client, test overrides).
type clientConfig struct {
	baseURLOverride string // test-only: if set, all service URLs use this value
	timeout         time.Duration
	httpClient      *http.Client
}

// defaultConfig returns sensible defaults for a new Client.
func defaultConfig() clientConfig {
	return clientConfig{
		timeout: 30 * time.Second,
	}
}

// ClientOption configures the Client. Pass options to NewClient.
type ClientOption func(*clientConfig)

// withBaseURLOverride is an unexported test helper that routes all four service
// clients to the same base URL. It exists solely to support test servers;
// production callers should use Config.BaseDomain and Config.Scheme instead.
func withBaseURLOverride(url string) ClientOption {
	return func(c *clientConfig) {
		c.baseURLOverride = url
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
