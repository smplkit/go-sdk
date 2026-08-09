package smplkit

// The stateless modes: Config.DisableEventBuffering (audit) and
// Config.DisableStreaming (config / flags / logging runtime surfaces).
//
// - DisableEventBuffering: no buffer worker goroutine ever starts; every
//   Events().Record performs one synchronous POST and returns the SDK's
//   typed errors on failure. Flush / Close are no-ops for the buffer.
// - DisableStreaming: the first live call still fetches / resolves / applies
//   once synchronously, but no sharedEventStream, no tickers or periodic
//   goroutines, and no threshold `go ...` calls are created — threshold
//   flushes run inline instead. Refresh re-fetches on demand and still
//   fires change handlers from deltas.
//
// Both switches are honored on the standalone constructors AND on the
// top-level NewClient (sub-clients inherit them).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smplkit/go-sdk/v3/logging/adapters"
)

// ---------------------------------------------------------------------------
// Config resolution
// ---------------------------------------------------------------------------

func TestResolveConfig_StatelessSwitchesCarry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SMPLKIT_PROFILE", "")

	rc, err := resolveConfig(Config{
		APIKey:                "sk_test",
		DisableEventBuffering: true,
		DisableStreaming:      true,
	})
	require.NoError(t, err)
	assert.True(t, rc.disableEventBuffering)
	assert.True(t, rc.disableStreaming)
}

func TestResolveConfig_StatelessSwitchesDefaultOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SMPLKIT_PROFILE", "")

	rc, err := resolveConfig(Config{APIKey: "sk_test"})
	require.NoError(t, err)
	assert.False(t, rc.disableEventBuffering)
	assert.False(t, rc.disableStreaming)
}

// ---------------------------------------------------------------------------
// Audit: DisableEventBuffering
// ---------------------------------------------------------------------------

func TestNewAuditClient_DisableEventBuffering_RecordSynchronous(t *testing.T) {
	var calls atomic.Int32
	var lastIdemp atomic.Value
	var lastBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		lastIdemp.Store(r.Header.Get("Idempotency-Key"))
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		lastBody.Store(string(buf))
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"event_type":"user.created","resource_type":"user","resource_id":"u-1"}}}`))
	}))
	defer srv.Close()

	ac, err := NewAuditClient(Config{
		APIKey:                "sk_test",
		Environment:           "production",
		DisableTelemetry:      true,
		DisableEventBuffering: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)
	require.Nil(t, ac.Events().buffer, "stateless mode must not construct a buffer")

	// One synchronous POST per Record — durable before Record returns.
	// input.Flush is meaningless in stateless mode and ignored.
	err = ac.Events().Record(CreateEventInput{
		EventType:      "user.created",
		ResourceType:   "user",
		ResourceID:     "u-1",
		IdempotencyKey: "idem-1",
		Flush:          true,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, "idem-1", lastIdemp.Load())
	// The configured environment is stamped onto the body (ADR-055).
	assert.Contains(t, lastBody.Load(), `"environment":"production"`)

	// No idempotency key: the header is simply absent.
	require.NoError(t, ac.Events().Record(CreateEventInput{
		EventType:    "user.updated",
		ResourceType: "user",
		ResourceID:   "u-1",
	}))
	assert.Equal(t, int32(2), calls.Load())
	assert.Equal(t, "", lastIdemp.Load())

	// Flush and Close are no-ops with no buffer.
	ac.Events().Flush(time.Second)
	require.NoError(t, ac.Close())
	assert.Equal(t, int32(2), calls.Load())
}

func TestAuditEvents_RecordSync_HTTPErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"event_type is invalid"}]}`))
	}))
	defer srv.Close()

	ac, err := NewAuditClient(Config{
		APIKey:                "sk_test",
		DisableTelemetry:      true,
		DisableEventBuffering: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)

	err = ac.Events().Record(CreateEventInput{
		EventType:    "bad type",
		ResourceType: "user",
		ResourceID:   "u-1",
	})
	require.Error(t, err)
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Contains(t, ve.Error(), "event_type is invalid")
}

func TestAuditEvents_RecordSync_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Close() // dead endpoint: every POST fails at the transport layer

	ac, err := NewAuditClient(Config{
		APIKey:                "sk_test",
		DisableTelemetry:      true,
		DisableEventBuffering: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)

	err = ac.Events().Record(CreateEventInput{
		EventType:    "user.created",
		ResourceType: "user",
		ResourceID:   "u-1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit Record:")
}

func TestNewClient_DisableEventBuffering_WiredAuditIsStateless(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/events") {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"00000000-0000-0000-0000-000000000001","type":"event","attributes":{"event_type":"user.created","resource_type":"user","resource_id":"u-1"}}}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{
		APIKey:                "sk_test",
		Environment:           "test",
		Service:               "svc",
		DisableTelemetry:      true,
		DisableEventBuffering: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	require.Nil(t, c.Audit().Events().buffer, "wired audit surface must honor DisableEventBuffering")
	require.NoError(t, c.Audit().Events().Record(CreateEventInput{
		EventType:    "user.created",
		ResourceType: "user",
		ResourceID:   "u-1",
	}))
	assert.Equal(t, int32(1), calls.Load())
}

func TestNewClient_DefaultKeepsAuditBuffered(t *testing.T) {
	c, err := NewClient(Config{
		APIKey:           "sk_test",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
	})
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	assert.NotNil(t, c.Audit().Events().buffer, "zero-value Config must keep the buffered write path")
}

// ---------------------------------------------------------------------------
// Flags: DisableStreaming
// ---------------------------------------------------------------------------

// statelessFlagsServer serves the endpoints the flags live surface touches;
// the flag's default flips with the `flipped` switch so Refresh has a delta
// to re-fetch.
func statelessFlagsServer(t *testing.T, flipped *atomic.Bool, ctxBulk, flagBulk *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/contexts/bulk"):
			if ctxBulk != nil {
				ctxBulk.Add(1)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/flags/bulk"):
			if flagBulk != nil {
				flagBulk.Add(1)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			def := "true"
			if flipped != nil && flipped.Load() {
				def = "false"
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"feature","type":"flag","attributes":{"id":"feature","name":"Feature","type":"BOOLEAN","default":` + def + `,"environments":{}}}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewFlagsClient_DisableStreaming_StatelessLive(t *testing.T) {
	srv := statelessFlagsServer(t, nil, nil, nil)

	fc, err := NewFlagsClient(Config{
		APIKey:           "sk_test",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)
	assert.True(t, fc.disableStreaming)

	// The first live call fetches once synchronously and evaluates locally.
	handle := fc.BooleanFlag("feature", false)
	assert.True(t, handle.Get(context.Background()))

	// No stream, no event handlers, no periodic flush goroutine.
	assert.Nil(t, fc.ownStream)
	assert.Nil(t, fc.runtime.streamManager)
	assert.Nil(t, fc.runtime.flagFlushDone)
	assert.Equal(t, "disconnected", fc.ConnectionStatus())
}

func TestNewFlagsClient_DisableStreaming_RefreshFiresChangeHandlers(t *testing.T) {
	var flipped atomic.Bool
	srv := statelessFlagsServer(t, &flipped, nil, nil)

	fc, err := NewFlagsClient(Config{
		APIKey:           "sk_test",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)

	handle := fc.BooleanFlag("feature", false)
	require.True(t, handle.Get(context.Background()))

	var events []*FlagChangeEvent
	fc.OnChange(func(e *FlagChangeEvent) { events = append(events, e) })

	// Refresh re-fetches on demand and fires change handlers.
	flipped.Store(true)
	require.NoError(t, fc.Refresh(context.Background()))
	require.NotEmpty(t, events)
	assert.Equal(t, "manual", events[0].Source)
	assert.False(t, handle.Get(context.Background()))
}

func TestFlagsClient_DisableStreaming_ThresholdFlushInline(t *testing.T) {
	var flagBulk atomic.Int32
	srv := statelessFlagsServer(t, nil, nil, &flagBulk)

	fc, err := NewFlagsClient(Config{
		APIKey:           "sk_test",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)

	// Crossing the batch threshold flushes inline — the POST completes
	// before the crossing RegisterFlag call returns; no goroutine to wait on.
	for i := 0; i < flagRegistrationThreshold; i++ {
		fc.RegisterFlag(fmt.Sprintf("flag-%d", i), "BOOLEAN", true)
	}
	assert.Equal(t, int32(1), flagBulk.Load())
	assert.Equal(t, 0, fc.PendingCount())
}

func TestFlagsRuntime_DisableStreaming_ContextFlushInline(t *testing.T) {
	var ctxBulk atomic.Int32
	srv := statelessFlagsServer(t, nil, &ctxBulk, nil)

	fc, err := NewFlagsClient(Config{
		APIKey:           "sk_test",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)

	contexts := make([]Context, contextBatchFlushSize)
	for i := range contexts {
		contexts[i] = Context{Type: "user", Key: fmt.Sprintf("u-%d", i)}
	}
	fc.SetContextProvider(func(_ context.Context) []Context { return contexts })

	handle := fc.BooleanFlag("feature", false)
	assert.True(t, handle.Get(context.Background()))

	// The provider pushed the context buffer past its threshold; the flush
	// ran inline during Get — observable synchronously, no goroutine.
	assert.Equal(t, int32(1), ctxBulk.Load())
	assert.Equal(t, 0, fc.runtime.contextBuffer.pendingCount())
}

// ---------------------------------------------------------------------------
// Config: DisableStreaming
// ---------------------------------------------------------------------------

// statelessConfigServer serves the endpoints the config live surface touches;
// the db config's host value flips with the `flipped` switch so Refresh has a
// delta to diff.
func statelessConfigServer(t *testing.T, flipped *atomic.Bool, bulk *atomic.Int32) *httptest.Server {
	t.Helper()
	configJSON := func() string {
		host := "localhost"
		if flipped != nil && flipped.Load() {
			host = "remote"
		}
		return `{"id":"db","type":"config","attributes":{"name":"DB","items":{"host":{"value":"` + host + `","type":"STRING"}},"environments":{},"parent":null}}`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/bulk", func(w http.ResponseWriter, _ *http.Request) {
		if bulk != nil {
			bulk.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/configs/db", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":` + configJSON() + `}`))
	})
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[` + configJSON() + `]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestNewConfigClient_DisableStreaming_StatelessLive(t *testing.T) {
	srv := statelessConfigServer(t, nil, nil)

	cc, err := NewConfigClient(Config{
		APIKey:           "sk_test",
		Environment:      "prod",
		Service:          "billing",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)
	assert.True(t, cc.disableStreaming)

	// The first live call fetches and resolves once synchronously.
	proxy, err := cc.Subscribe(context.Background(), "db")
	require.NoError(t, err)
	assert.Equal(t, "localhost", proxy.Value()["host"])

	// No stream, no event handlers.
	assert.Nil(t, cc.ownStream)
	assert.Nil(t, cc.streamManager)
}

func TestNewConfigClient_DisableStreaming_RefreshFiresChangeHandlers(t *testing.T) {
	var flipped atomic.Bool
	srv := statelessConfigServer(t, &flipped, nil)

	cc, err := NewConfigClient(Config{
		APIKey:           "sk_test",
		Environment:      "prod",
		Service:          "billing",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)

	proxy, err := cc.Subscribe(context.Background(), "db")
	require.NoError(t, err)
	require.Equal(t, "localhost", proxy.Value()["host"])

	var events []*ConfigChangeEvent
	cc.OnChange(func(e *ConfigChangeEvent) { events = append(events, e) })

	// Refresh re-fetches on demand and fires change handlers from the delta.
	flipped.Store(true)
	require.NoError(t, cc.Refresh(context.Background()))
	require.Len(t, events, 1)
	assert.Equal(t, "db", events[0].ConfigID)
	assert.Equal(t, "host", events[0].ItemKey)
	assert.Equal(t, "localhost", events[0].OldValue)
	assert.Equal(t, "remote", events[0].NewValue)
	assert.Equal(t, "manual", events[0].Source)
	assert.Equal(t, "remote", proxy.Value()["host"])
}

func TestConfigClient_DisableStreaming_ThresholdFlushInline(t *testing.T) {
	var bulk atomic.Int32
	srv := statelessConfigServer(t, nil, &bulk)

	cc, err := NewConfigClient(Config{
		APIKey:           "sk_test",
		Environment:      "prod",
		Service:          "billing",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)

	// Crossing the batch threshold flushes inline — observable synchronously.
	for i := 0; i < configRegistrationFlushSize; i++ {
		cc.RegisterConfig(fmt.Sprintf("cfg-%d", i), "billing", "prod", "", "", "")
	}
	assert.Equal(t, int32(1), bulk.Load())
	assert.Equal(t, 0, cc.PendingCount())
}

// ---------------------------------------------------------------------------
// Logging: DisableStreaming
// ---------------------------------------------------------------------------

func TestNewLoggingClient_DisableStreaming_InstallStateless(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"app","level":"WARN","managed":true,"environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	lc, err := NewLoggingClient(Config{
		APIKey:           "sk_test",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)
	assert.True(t, lc.disableStreaming)
	assert.True(t, lc.loggers.disableStreaming)

	adapter := &captureAdapter{discovered: []adapters.DiscoveredLogger{{Name: "app", Level: "DEBUG"}}}
	lc.RegisterAdapter(adapter)

	// Install still discovers, fetches, and applies once — synchronously.
	require.NoError(t, lc.Install(context.Background()))
	assert.True(t, lc.connected)
	require.NotEmpty(t, adapter.applied)
	assert.Equal(t, "WARN", adapter.applied[0].level)

	// No stream, no event handlers, no periodic flush goroutine.
	assert.Nil(t, lc.ownStream)
	assert.Nil(t, lc.streamManager)
	assert.Nil(t, lc.flushDone)

	// The not-installed gate is satisfied; Refresh re-fetches on demand.
	require.NoError(t, lc.Refresh(context.Background()))
	lc.close()
}

func TestLoggersClient_DisableStreaming_ThresholdFlushInline(t *testing.T) {
	var bulk atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/bulk", func(w http.ResponseWriter, _ *http.Request) {
		bulk.Add(1)
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	lc, err := NewLoggingClient(Config{
		APIKey:           "sk_test",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)

	level := LogLevelInfo
	sources := make([]LoggerSource, loggerBatchFlushSize)
	for i := range sources {
		sources[i] = LoggerSource{ID: fmt.Sprintf("logger-%d", i), ResolvedLevel: &level}
	}
	// flush=false, but crossing the batch threshold flushes inline —
	// observable synchronously, no goroutine.
	require.NoError(t, lc.Loggers().Register(context.Background(), sources, false))
	assert.Equal(t, int32(1), bulk.Load())
	assert.Equal(t, 0, lc.Loggers().PendingCount())
}

// ---------------------------------------------------------------------------
// SmplClient: DisableStreaming wiring + deferred machinery
// ---------------------------------------------------------------------------

func TestSmplClient_DisableStreaming_NoBackgroundMachinery(t *testing.T) {
	var ctxBulk atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/contexts/bulk") {
			ctxBulk.Add(1)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(Config{
		APIKey:           "sk_test",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	// Every runtime sub-client inherits the switch.
	assert.True(t, c.disableStreaming)
	assert.True(t, c.flags.disableStreaming)
	assert.True(t, c.config.disableStreaming)
	assert.True(t, c.logging.disableStreaming)
	assert.True(t, c.logging.loggers.disableStreaming)

	// The deferred machinery start arms no periodic flush timer and runs the
	// service-context registration inline (observable synchronously).
	c.ensureStarted()
	assert.Equal(t, int32(1), ctxBulk.Load())
	c.startMu.Lock()
	timer := c.flushTimer
	started := c.started
	c.startMu.Unlock()
	assert.Nil(t, timer, "stateless mode must not arm the periodic flush timer")
	assert.True(t, started)
}

func TestSmplClient_DisableStreaming_WaitUntilReadySkipsSocket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(Config{
		APIKey:           "sk_test",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
		DisableStreaming: true,
	}, withBaseURLOverride(srv.URL))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	// Pre-warms the flag and config caches, then returns — no socket.
	require.NoError(t, c.WaitUntilReady(context.Background(), time.Second))
	c.streamMu.Lock()
	stream := c.stream
	c.streamMu.Unlock()
	assert.Nil(t, stream, "stateless mode must not open the shared event stream")
}
