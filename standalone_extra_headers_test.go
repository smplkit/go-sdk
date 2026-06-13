package smplkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// newHeaderCaptureServer returns a test server that records the headers of the
// last request it received, plus a getter for them.
func newHeaderCaptureServer() (*httptest.Server, func() http.Header) {
	var mu sync.Mutex
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	return srv, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

// TestStandaloneClients_ExtraHeadersOnRequests verifies that ExtraHeaders are
// sent on requests from every standalone client (not just the top-level
// SmplClient), and that SDK-owned headers still win on collision.
func TestStandaloneClients_ExtraHeadersOnRequests(t *testing.T) {
	const apiKey = "sk_test_key"
	extra := map[string]string{
		"X-Custom":      "hello",
		"Accept":        "text/plain",  // collides with the SDK Accept header
		"Authorization": "Bearer nope", // collides with the auth transport
	}
	baseCfg := func(srvURL string) smplkit.Config {
		return smplkit.Config{
			APIKey:           apiKey,
			Environment:      "test",
			Service:          "svc",
			DisableTelemetry: true,
			ExtraHeaders:     extra,
		}
	}

	assertHeaders := func(t *testing.T, seen http.Header) {
		t.Helper()
		require.NotNil(t, seen, "server should have received a request")
		assert.Equal(t, "hello", seen.Get("X-Custom"), "caller extra header must be sent")
		// SDK-owned headers win on collision.
		assert.Equal(t, "application/vnd.api+json", seen.Get("Accept"))
		assert.Equal(t, "Bearer "+apiKey, seen.Get("Authorization"))
	}

	t.Run("config", func(t *testing.T) {
		srv, headers := newHeaderCaptureServer()
		defer srv.Close()
		c, err := smplkit.NewConfigClient(baseCfg(srv.URL), smplkit.WithBaseURL(srv.URL))
		require.NoError(t, err)
		_, _ = c.List(context.Background())
		assertHeaders(t, headers())
	})

	t.Run("flags", func(t *testing.T) {
		srv, headers := newHeaderCaptureServer()
		defer srv.Close()
		c, err := smplkit.NewFlagsClient(baseCfg(srv.URL), smplkit.WithBaseURL(srv.URL))
		require.NoError(t, err)
		_, _ = c.List(context.Background())
		assertHeaders(t, headers())
	})

	t.Run("logging", func(t *testing.T) {
		srv, headers := newHeaderCaptureServer()
		defer srv.Close()
		c, err := smplkit.NewLoggingClient(baseCfg(srv.URL), smplkit.WithBaseURL(srv.URL))
		require.NoError(t, err)
		_, _ = c.Loggers().List(context.Background())
		assertHeaders(t, headers())
	})

	// buildGenClients is shared by the standalone platform and account clients.
	t.Run("platform", func(t *testing.T) {
		srv, headers := newHeaderCaptureServer()
		defer srv.Close()
		c, err := smplkit.NewPlatformClient(baseCfg(srv.URL), smplkit.WithBaseURL(srv.URL))
		require.NoError(t, err)
		_, _ = c.Environments().List(context.Background())
		assertHeaders(t, headers())
	})

	// buildJobsGenClient is the standalone jobs transport.
	t.Run("jobs", func(t *testing.T) {
		srv, headers := newHeaderCaptureServer()
		defer srv.Close()
		c, err := smplkit.NewJobsClient(baseCfg(srv.URL), smplkit.WithBaseURL(srv.URL))
		require.NoError(t, err)
		_, _ = c.List(context.Background(), smplkit.ListJobsInput{})
		assertHeaders(t, headers())
	})
}
