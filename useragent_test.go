package smplkit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// standaloneUADrivers issues one request from each standalone client shape so
// the User-Agent tests below exercise every hand-built transport.
var standaloneUADrivers = []struct {
	name string
	call func(t *testing.T, cfg smplkit.Config, srvURL string)
}{
	{"config", func(t *testing.T, cfg smplkit.Config, srvURL string) {
		c, err := smplkit.NewConfigClient(cfg, smplkit.WithBaseURL(srvURL))
		require.NoError(t, err)
		_, _ = c.List(context.Background())
	}},
	{"flags", func(t *testing.T, cfg smplkit.Config, srvURL string) {
		c, err := smplkit.NewFlagsClient(cfg, smplkit.WithBaseURL(srvURL))
		require.NoError(t, err)
		_, _ = c.List(context.Background())
	}},
	{"logging", func(t *testing.T, cfg smplkit.Config, srvURL string) {
		c, err := smplkit.NewLoggingClient(cfg, smplkit.WithBaseURL(srvURL))
		require.NoError(t, err)
		_, _ = c.Loggers().List(context.Background())
	}},
	{"platform", func(t *testing.T, cfg smplkit.Config, srvURL string) {
		c, err := smplkit.NewPlatformClient(cfg, smplkit.WithBaseURL(srvURL))
		require.NoError(t, err)
		_, _ = c.Environments().List(context.Background())
	}},
	{"jobs", func(t *testing.T, cfg smplkit.Config, srvURL string) {
		c, err := smplkit.NewJobsClient(cfg, smplkit.WithBaseURL(srvURL))
		require.NoError(t, err)
		_, _ = c.List(context.Background(), smplkit.ListJobsInput{})
	}},
	{"audit", func(t *testing.T, cfg smplkit.Config, srvURL string) {
		c, err := smplkit.NewAuditClient(cfg, smplkit.WithBaseURL(srvURL))
		require.NoError(t, err)
		defer func() { _ = c.Close() }()
		_, _ = c.ResourceTypes().List(context.Background(), smplkit.ListResourceTypesInput{})
	}},
}

// TestStandaloneClients_DefaultUserAgent verifies every standalone client
// sends the SDK-identifying default User-Agent — smplkit-sdk-go/<version>,
// version resolved from build metadata — when the caller supplies none.
func TestStandaloneClients_DefaultUserAgent(t *testing.T) {
	cfg := smplkit.Config{
		APIKey:           "sk_test_key",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
	}
	for _, d := range standaloneUADrivers {
		t.Run(d.name, func(t *testing.T) {
			srv, headers := newHeaderCaptureServer()
			defer srv.Close()
			d.call(t, cfg, srv.URL)

			seen := headers()
			require.NotNil(t, seen, "server should have received a request")
			assert.Regexp(t, `^smplkit-sdk-go/\S+$`, seen.Get("User-Agent"))
		})
	}
}

// TestStandaloneClients_CallerUserAgentWins verifies a caller-supplied
// User-Agent — provided through the extra-headers surface under a
// non-canonical casing — replaces the SDK default on every standalone client.
func TestStandaloneClients_CallerUserAgentWins(t *testing.T) {
	cfg := smplkit.Config{
		APIKey:           "sk_test_key",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
		ExtraHeaders:     map[string]string{"user-agent": "my-app/2.0"},
	}
	for _, d := range standaloneUADrivers {
		t.Run(d.name, func(t *testing.T) {
			srv, headers := newHeaderCaptureServer()
			defer srv.Close()
			d.call(t, cfg, srv.URL)

			seen := headers()
			require.NotNil(t, seen, "server should have received a request")
			assert.Equal(t, "my-app/2.0", seen.Get("User-Agent"))
			// The other SDK-owned headers remain SDK-owned.
			assert.Equal(t, "application/vnd.api+json", seen.Get("Accept"))
		})
	}
}
