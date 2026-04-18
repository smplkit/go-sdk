package smplkit

import (
	"net/http"
	"time"
)

// clientConfig holds configuration for the Client.
type clientConfig struct {
	baseDomain       string
	scheme           string
	baseURLOverride  string // test-only: if set, all service URLs use this value
	timeout          time.Duration
	httpClient       *http.Client
	disableTelemetry bool
}

// defaultConfig returns sensible defaults for a new Client.
func defaultConfig() clientConfig {
	return clientConfig{
		baseDomain: "smplkit.com",
		scheme:     "https",
		timeout:    30 * time.Second,
	}
}

// ClientOption configures the Client. Pass options to NewClient.
type ClientOption func(*clientConfig)

// WithBaseDomain overrides the base domain used to compute per-service URLs.
// URLs are computed as {scheme}://{service}.{domain}, e.g. https://config.smplkit.com.
// The default domain is "smplkit.com".
func WithBaseDomain(domain string) ClientOption {
	return func(c *clientConfig) {
		c.baseDomain = domain
	}
}

// WithScheme overrides the URL scheme used to compute per-service URLs.
// The default scheme is "https".
func WithScheme(scheme string) ClientOption {
	return func(c *clientConfig) {
		c.scheme = scheme
	}
}

// withBaseURLOverride is an unexported test helper that routes all four service
// clients to the same base URL. It exists solely to support test servers;
// production callers should use WithBaseDomain and WithScheme instead.
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

// DisableTelemetry disables internal SDK usage telemetry. By default,
// the SDK reports anonymous usage metrics to the smplkit service.
func DisableTelemetry() ClientOption {
	return func(cfg *clientConfig) {
		cfg.disableTelemetry = true
	}
}
