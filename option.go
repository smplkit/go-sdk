package smplkit

import (
	"net/http"
	"time"
)

// clientConfig holds configuration for the Client.
type clientConfig struct {
	baseURL          string
	timeout          time.Duration
	httpClient       *http.Client
	disableTelemetry bool
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

// DisableTelemetry disables internal SDK usage telemetry. By default,
// the SDK reports anonymous usage metrics to the smplkit service.
func DisableTelemetry() ClientOption {
	return func(cfg *clientConfig) {
		cfg.disableTelemetry = true
	}
}
