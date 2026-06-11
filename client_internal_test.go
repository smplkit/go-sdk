package smplkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listBody is a JSON:API list payload that every per-service generated client
// parses cleanly (empty data + a pagination meta block), so a single handler
// can satisfy config/flags/app/logging/audit/jobs list calls.
const listBody = `{"data":[],"meta":{"pagination":{"page":1,"size":1000}}}`

// TestNewClient_ExtraHeadersInjectedPerService drives one list call through
// every backend service so each service's extra-header request editor runs,
// asserting the caller-supplied header reaches the wire on all of them.
func TestNewClient_ExtraHeadersInjectedPerService(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{} // request path -> X-Custom header value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get("X-Custom")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(listBody))
	}))
	defer server.Close()

	c, err := NewClient(
		Config{
			APIKey:           "sk_test",
			Environment:      "test",
			Service:          "svc",
			DisableTelemetry: true,
			ExtraHeaders:     map[string]string{"X-Custom": "yes"},
		},
		withBaseURLOverride(server.URL),
	)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	require.NotNil(t, c.Account())

	ctx := context.Background()
	_, _ = c.Config().List(ctx)                                          // config service
	_, _ = c.Flags().List(ctx)                                           // flags service
	_, _ = c.Platform().Environments().List(ctx)                         // app service
	_, _ = c.Logging().Loggers().List(ctx)                               // logging service
	_, _ = c.Audit().ResourceTypes().List(ctx, ListResourceTypesInput{}) // audit service
	_, _ = c.Jobs().List(ctx, ListJobsInput{})                           // jobs service

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, seen, "expected at least one recorded request")
	for path, v := range seen {
		assert.Equalf(t, "yes", v, "extra header missing on %s", path)
	}
}

// TestSmplClient_PeriodicFlushLifecycle exercises the deferred periodic-flush
// machinery: a live tick (drains empty buffers and reschedules) and the
// post-Close no-op guards on both periodicFlush and schedulePeriodicFlush.
func TestSmplClient_PeriodicFlushLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(listBody))
	}))
	defer server.Close()

	c, err := NewClient(
		Config{APIKey: "sk_test", Environment: "test", Service: "svc", DisableTelemetry: true},
		withBaseURLOverride(server.URL),
	)
	require.NoError(t, err)

	// Live tick: drains the (empty) registration buffers and reschedules.
	c.periodicFlush()
	assert.False(t, c.isClosed())

	// After Close, both entry points are no-ops.
	_ = c.Close()
	assert.True(t, c.isClosed())
	c.periodicFlush()         // isClosed -> early return
	c.schedulePeriodicFlush() // closed -> early return
	assert.Nil(t, c.flushTimer)
}
