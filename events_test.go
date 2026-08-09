package smplkit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sseHeaders writes the SSE response headers and flushes them so the client's
// connect() observes an established stream even while the handler stays open.
func sseHeaders(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fl := w.(http.Flusher)
	fl.Flush()
	return fl
}

// sseWrite writes one raw chunk of stream data and flushes it.
func sseWrite(w http.ResponseWriter, fl http.Flusher, chunk string) {
	_, _ = io.WriteString(w, chunk)
	fl.Flush()
}

// TestEvents_SuiteWideLaunchNeutralized is the test-hygiene regression guard:
// it fails if the suite-wide connect neutralization (TestMain's eventsLaunch
// no-op) is ever removed. With the seam in place, start() on a stream pointed
// at the real backend must not transition the stream out of "disconnected" —
// i.e. it must not connect — and stop() must still complete.
func TestEvents_SuiteWideLaunchNeutralized(t *testing.T) {
	s := newSharedEventStream("https://app.smplkit.com", "sk_should_never_connect", nil)
	s.on("noop", func(map[string]interface{}) {}) // listener registration still works
	s.start()
	// Give a real connector time to change status if the seam regressed.
	time.Sleep(25 * time.Millisecond)
	require.Equal(t, "disconnected", s.connectionStatus(),
		"event-stream connect is not neutralized suite-wide — start() reached the network")
	s.stop() // must not block: the no-op launcher closed streamDone
	assert.Equal(t, "disconnected", s.connectionStatus())
}

// withRealEventsLaunch restores the real run()-launching eventsLaunch for the
// duration of a sub-test that needs the genuine reconnect loop, then resets
// the no-op.
func withRealEventsLaunch(t *testing.T) {
	t.Helper()
	prev := eventsLaunch
	eventsLaunch = realEventsLaunch
	t.Cleanup(func() { eventsLaunch = prev })
}

func TestEvents_Connect_EstablishAndDispatch(t *testing.T) {
	gotEvent := make(chan map[string]interface{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := sseHeaders(w)
		sseWrite(w, fl, "retry: 1000\n\n")
		sseWrite(w, fl, "event: connected\ndata: {}\n\n")
		sseWrite(w, fl, "event: flag_changed\ndata: {\"id\": \"f1\"}\n\n")
		// Unknown event names are ignored silently.
		sseWrite(w, fl, "event: mystery_event\ndata: {}\n\n")
		// Handler returns → stream ends → connect returns.
	}))
	t.Cleanup(srv.Close)

	r := makeReporter(t)
	defer r.Close()
	s := newSharedEventStream(srv.URL, "key", r)
	s.on("flag_changed", func(d map[string]interface{}) { gotEvent <- d })

	established, closed := s.connect()
	assert.True(t, established)
	assert.False(t, closed)

	select {
	case d := <-gotEvent:
		assert.Equal(t, "f1", d["id"])
	default:
		t.Fatal("flag_changed was not dispatched to the listener")
	}
	assert.Equal(t, "connected", s.connectionStatus())
	require.NoError(t, s.waitConnected(context.Background(), time.Second))
	// The server's retry hint seeds the backoff base.
	assert.Equal(t, time.Second, s.retryHint)
	assert.Equal(t, time.Second, s.backoffBase())
}

// TestEvents_ConnectBarrier_SignaledOnEstablish locks the connect-barrier
// semantics: firstConnectedCh signals as soon as the stream is established
// (HTTP 200 with a text/event-stream Content-Type) — before any event
// arrives — because at that point the server has already registered the
// subscription, so an immediate write after Get() cannot miss its broadcast.
func TestEvents_ConnectBarrier_SignaledOnEstablish(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w) // NO events, not even `connected` — headers only
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	s := newSharedEventStream(srv.URL, "key", nil)
	connDone := make(chan struct{})
	go func() { s.connect(); close(connDone) }()

	require.NoError(t, s.waitConnected(context.Background(), 2*time.Second),
		"barrier must signal on 200 + text/event-stream, without waiting for any event")
	assert.Equal(t, "connected", s.connectionStatus())

	releaseOnce.Do(func() { close(release) })
	select {
	case <-connDone:
	case <-time.After(2 * time.Second):
		t.Fatal("connect did not return after the server closed the stream")
	}
}

// TestEvents_Connect_RequestHeaders asserts the stream request carries
// Accept: text/event-stream, the Bearer API key, a User-Agent (the SDK
// default, or the caller's own when supplied), and — per the wire contract —
// never a Last-Event-ID header (the SDK does not resume streams).
func TestEvents_Connect_RequestHeaders(t *testing.T) {
	type captured struct{ accept, auth, ua, lastEventID string }
	capCh := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capCh <- captured{
			accept:      r.Header.Get("Accept"),
			auth:        r.Header.Get("Authorization"),
			ua:          r.Header.Get("User-Agent"),
			lastEventID: r.Header.Get("Last-Event-ID"),
		}
		sseHeaders(w)
	}))
	t.Cleanup(srv.Close)

	t.Run("default UA", func(t *testing.T) {
		s := newSharedEventStream(srv.URL, "sk_api_test", nil)
		established, _ := s.connect()
		require.True(t, established)
		got := <-capCh
		assert.Equal(t, "text/event-stream", got.accept)
		assert.Equal(t, "Bearer sk_api_test", got.auth)
		assert.Regexp(t, `^smplkit-sdk-go/\S+$`, got.ua)
		assert.Empty(t, got.lastEventID, "Last-Event-ID must never be sent")
	})

	t.Run("caller UA wins", func(t *testing.T) {
		s := newSharedEventStream(srv.URL, "sk_api_test", nil)
		s.callerUA = "corp-agent/7"
		established, _ := s.connect()
		require.True(t, established)
		got := <-capCh
		assert.Equal(t, "corp-agent/7", got.ua)
	})
}

func TestEvents_Connect_AuthFailure401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	s := newSharedEventStream(srv.URL, "bad-key", nil)
	established, closed := s.connect()
	assert.False(t, established)
	assert.False(t, closed)
	assert.NotEqual(t, "connected", s.connectionStatus())
}

func TestEvents_Connect_WrongContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(srv.Close)

	s := newSharedEventStream(srv.URL, "key", nil)
	established, closed := s.connect()
	assert.False(t, established)
	assert.False(t, closed)
	assert.NotEqual(t, "connected", s.connectionStatus())
}

func TestEvents_Connect_RequestBuildError(t *testing.T) {
	// An invalid URL escape makes http.NewRequestWithContext fail.
	s := newSharedEventStream("http://%zz", "key", nil)
	established, closed := s.connect()
	assert.False(t, established)
	assert.False(t, closed)
}

func TestEvents_Connect_ConnectionRefused(t *testing.T) {
	s := newSharedEventStream("http://127.0.0.1:1", "key", nil)
	established, closed := s.connect()
	assert.False(t, established)
	assert.False(t, closed)
	assert.Equal(t, "connecting", s.connectionStatus())
}

func TestEvents_Connect_ListenerPanicRecovered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fl := sseHeaders(w)
		sseWrite(w, fl, "event: boom\ndata: {\"id\": \"x\"}\n\n")
	}))
	t.Cleanup(srv.Close)

	s := newSharedEventStream(srv.URL, "key", nil)
	invoked := false
	s.on("boom", func(map[string]interface{}) { invoked = true; panic("listener blew up") })
	established, _ := s.connect()
	require.True(t, established)
	assert.True(t, invoked, "listener should have been invoked")
	// A panicking listener must not crash the receive loop.
	assert.Equal(t, "connected", s.connectionStatus())
}

func TestEvents_HandleFrame(t *testing.T) {
	s := newSharedEventStream("https://app.smplkit.com", "key", nil)
	var got map[string]interface{}
	s.on("flag_changed", func(d map[string]interface{}) { got = d })

	s.handleFrame("flag_changed", "not json") // ignored
	assert.Nil(t, got)

	s.handleFrame("flag_changed", `{"id":"f1"}`)
	require.NotNil(t, got)
	assert.Equal(t, "f1", got["id"])
}

// TestEvents_Run_BackoffResetsAfterSuccessfulConnect locks the fix for the
// historical bug where the reconnect backoff was initialized once outside the
// loop and only ever doubled. The delay between attempts must double across
// consecutive failures and drop back to the base after a successful connect.
//
// Attempt script: 1 OK, 2 FAIL, 3 FAIL, 4 OK, 5 (observed only).
// Expected gaps (base 100ms, cap 300ms): g12≈100ms, g23≈200ms, g34≈300ms
// (doubled to 400ms, then capped), and — the reset under test — g45≈100ms
// again. The pre-fix behavior would keep doubling toward the cap, making g45
// at least as large as g34.
func TestEvents_Run_BackoffResetsAfterSuccessfulConnect(t *testing.T) {
	withRealEventsLaunch(t)

	var mu sync.Mutex
	var arrivals []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		n := len(arrivals)
		mu.Unlock()
		switch n {
		case 1, 4:
			sseHeaders(w) // established, then immediately closed by return
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	s := newSharedEventStream(srv.URL, "key", nil)
	s.initBackoff = 100 * time.Millisecond
	s.maxBackoff = 300 * time.Millisecond
	s.start()
	t.Cleanup(s.stop)

	waitForAttempts := func(n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			count := len(arrivals)
			mu.Unlock()
			if count >= n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d connection attempts", n)
	}
	waitForAttempts(5)
	s.stop()

	mu.Lock()
	g12 := arrivals[1].Sub(arrivals[0])
	g23 := arrivals[2].Sub(arrivals[1])
	g34 := arrivals[3].Sub(arrivals[2])
	g45 := arrivals[4].Sub(arrivals[3])
	mu.Unlock()

	// Doubling across the failure run (lower bounds are hard guarantees of
	// the timer; generous upper slack absorbs scheduler jitter).
	assert.GreaterOrEqual(t, g12, 90*time.Millisecond, "first retry waits the base delay")
	assert.GreaterOrEqual(t, g23, 190*time.Millisecond, "second consecutive failure doubles the delay")
	assert.GreaterOrEqual(t, g34, 290*time.Millisecond, "third consecutive failure doubles again (capped)")
	assert.Less(t, g34, 390*time.Millisecond, "the doubled delay must be capped at maxBackoff")
	// THE RESET: after the successful connect on attempt 4, the next delay
	// returns to the base instead of continuing to double.
	assert.Less(t, g45, 250*time.Millisecond,
		"backoff must reset to base after a successful connect (pre-fix behavior kept doubling)")
	assert.Less(t, g45, g34, "post-success delay must shrink, not grow")
}

// TestEvents_Run_RetryHintSeedsBackoff proves the server's `retry:` value
// replaces the default backoff base: with a 40ms hint received on the first
// (successful) connection, the reconnect happens long before the 300ms
// initBackoff that would otherwise apply.
func TestEvents_Run_RetryHintSeedsBackoff(t *testing.T) {
	withRealEventsLaunch(t)

	var mu sync.Mutex
	var arrivals []time.Time
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		n := len(arrivals)
		mu.Unlock()
		fl := sseHeaders(w)
		if n == 1 {
			sseWrite(w, fl, "retry: 40\n")
			return // close: next delay must derive from the 40ms hint
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	s := newSharedEventStream(srv.URL, "key", nil)
	s.initBackoff = 300 * time.Millisecond
	s.start()

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		count := len(arrivals)
		mu.Unlock()
		if count >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the reconnect attempt")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.stop()

	mu.Lock()
	g12 := arrivals[1].Sub(arrivals[0])
	mu.Unlock()
	assert.Equal(t, 40*time.Millisecond, s.retryHint, "retry hint must be parsed from the stream")
	assert.Less(t, g12, 250*time.Millisecond,
		"reconnect delay must derive from the server retry hint, not initBackoff")
	assert.GreaterOrEqual(t, g12, 35*time.Millisecond)
}

// TestEvents_Run_ReconnectTriggersRefetch locks the reconnect re-sync: a
// refetch callback registered via onReconnectRefetch must NOT run on the
// initial connect, and must run exactly once after the stream reconnects.
func TestEvents_Run_ReconnectTriggersRefetch(t *testing.T) {
	withRealEventsLaunch(t)

	var refetchCount atomic.Int32
	refetchRan := make(chan struct{}, 4)

	var attempt atomic.Int32
	var countAtSecondAttempt atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempt.Add(1)
		if n == 2 {
			// Snapshot BEFORE this (re)connect is established: proves the
			// initial connect did not refetch.
			countAtSecondAttempt.Store(refetchCount.Load())
		}
		sseHeaders(w)
		if n == 1 {
			return // drop the stream right after the initial connect
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	s := newSharedEventStream(srv.URL, "key", nil)
	s.initBackoff = 20 * time.Millisecond
	s.onReconnectRefetch(func() {
		refetchCount.Add(1)
		refetchRan <- struct{}{}
	})
	s.start()
	t.Cleanup(s.stop)

	select {
	case <-refetchRan:
	case <-time.After(5 * time.Second):
		t.Fatal("refetch callback did not run on reconnect")
	}
	assert.Equal(t, int32(0), countAtSecondAttempt.Load(),
		"refetch must NOT run on the initial connect")
	assert.Equal(t, int32(1), refetchCount.Load(),
		"refetch must run exactly once per reconnect")
}

func TestEvents_Run_StopDuringBackoffWait(t *testing.T) {
	withRealEventsLaunch(t)

	attemptCh := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attemptCh <- struct{}{}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	s := newSharedEventStream(srv.URL, "key", nil)
	s.initBackoff = 5 * time.Second // long enough that stop() must interrupt it
	s.start()

	select {
	case <-attemptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("server never saw a connection attempt")
	}

	done := make(chan struct{})
	go func() { s.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop() did not interrupt the backoff wait")
	}
	assert.Equal(t, "disconnected", s.connectionStatus())
}

func TestEvents_StopWhileConnected(t *testing.T) {
	withRealEventsLaunch(t)

	connected := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)
		connected <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	r := makeReporter(t)
	defer r.Close()
	s := newSharedEventStream(srv.URL, "key", r)
	s.start()

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("server never established the stream")
	}
	require.NoError(t, s.waitConnected(context.Background(), time.Second))
	assert.Equal(t, "connected", s.connectionStatus())

	// stop() closes closeCh; the per-connection watcher cancels the request
	// context, the blocked read errors, connect observes closeCh closed, and
	// run() exits.
	s.stop()
	assert.Equal(t, "disconnected", s.connectionStatus())
}

// TestEvents_Liveness_SilentStreamReconnects proves the read watchdog: a
// stream that goes silent past the read timeout is dropped and re-dialed
// even though the TCP connection is still open server-side.
func TestEvents_Liveness_SilentStreamReconnects(t *testing.T) {
	withRealEventsLaunch(t)

	var attempt atomic.Int32
	reconnected := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempt.Add(1)
		sseHeaders(w)
		if n == 2 {
			close(reconnected)
		}
		// Both attempts go silent; attempt 1's silence must trip the watchdog.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	s := newSharedEventStream(srv.URL, "key", nil)
	s.readTimeout = 120 * time.Millisecond
	s.initBackoff = 20 * time.Millisecond
	s.start()
	t.Cleanup(s.stop)

	select {
	case <-reconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not drop the silent stream and reconnect")
	}
}

// TestEvents_Liveness_CommentFramesCountAsLiveness proves keepalive comment
// frames re-arm the watchdog: a stream that only ever sends `: keepalive`
// comments (each within the read timeout) survives well past the timeout.
func TestEvents_Liveness_CommentFramesCountAsLiveness(t *testing.T) {
	withRealEventsLaunch(t)

	var mu sync.Mutex
	var arrivals []time.Time
	reconnected := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		n := len(arrivals)
		mu.Unlock()
		fl := sseHeaders(w)
		if n == 1 {
			// Keepalive comments for 300ms (well past the 150ms timeout),
			// then silence until the watchdog drops the connection.
			for i := 0; i < 6; i++ {
				time.Sleep(50 * time.Millisecond)
				sseWrite(w, fl, ": keepalive\n")
			}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		close(reconnected)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	s := newSharedEventStream(srv.URL, "key", nil)
	s.readTimeout = 150 * time.Millisecond
	s.initBackoff = 20 * time.Millisecond
	s.start()
	t.Cleanup(s.stop)

	select {
	case <-reconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("stream never reconnected after keepalives stopped")
	}
	mu.Lock()
	survived := arrivals[1].Sub(arrivals[0])
	mu.Unlock()
	assert.GreaterOrEqual(t, survived, 300*time.Millisecond,
		"comment frames must count as liveness — the stream died before the keepalives stopped")
}

func TestEvents_WaitConnected_TimeoutAndCancel(t *testing.T) {
	s := newSharedEventStream("https://app.smplkit.com", "key", nil)

	err := s.waitConnected(context.Background(), 10*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	// The timeout path must yield a typed *TimeoutError so callers (and
	// SmplClient.WaitUntilReady) can match it via errors.As, matching the
	// canonical Python hierarchy.
	var timeoutErr *TimeoutError
	require.True(t, errors.As(err, &timeoutErr), "expected *TimeoutError, got %T: %v", err, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = s.waitConnected(ctx, time.Second)
	require.ErrorIs(t, err, context.Canceled)

	// timeout=0 disables the timer entirely; a canceled context still returns.
	err = s.waitConnected(ctx, 0)
	require.ErrorIs(t, err, context.Canceled)

	// Once connected, waitConnected returns immediately even with zero timeout.
	s.setStatus("connected")
	require.NoError(t, s.waitConnected(context.Background(), 0))
}

func TestEvents_BuildEventsURL(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://app.smplkit.com", "https://app.smplkit.com/api/v1/events"},
		{"http://127.0.0.1:8000/", "http://127.0.0.1:8000/api/v1/events"},
		{"app.smplkit.com", "https://app.smplkit.com/api/v1/events"},
	}
	for _, tc := range cases {
		s := newSharedEventStream(tc.base, "k", nil)
		assert.Equal(t, tc.want, s.buildEventsURL())
	}
}

func TestEvents_Defaults(t *testing.T) {
	s := newSharedEventStream("https://app.smplkit.com", "k", nil)
	assert.Equal(t, time.Second, s.backoffBase())
	assert.Equal(t, 60*time.Second, s.maxBackoffOrDefault())
	assert.Equal(t, 45*time.Second, s.readTimeoutOrDefault())

	s.retryHint = 250 * time.Millisecond
	assert.Equal(t, 250*time.Millisecond, s.backoffBase(), "server retry hint wins")
	s.retryHint = 0
	s.initBackoff = 5 * time.Millisecond
	assert.Equal(t, 5*time.Millisecond, s.backoffBase(), "test override wins over default")
	s.maxBackoff = 2 * time.Second
	assert.Equal(t, 2*time.Second, s.maxBackoffOrDefault())
	s.readTimeout = 3 * time.Second
	assert.Equal(t, 3*time.Second, s.readTimeoutOrDefault())
}

func TestEvents_OffUnregistersListener(t *testing.T) {
	s := newSharedEventStream("https://app.smplkit.com", "k", nil)
	calls := 0
	fn := func(map[string]interface{}) { calls++ }
	s.on("evt", fn)
	s.off("evt", fn)
	s.dispatch("evt", map[string]interface{}{}) // no registered listener now
	assert.Equal(t, 0, calls)

	// dispatch with no handlers is a no-op (covers the empty-listener branch).
	s.dispatch("never-registered", map[string]interface{}{})
	// off on an event with no listeners is a no-op.
	s.off("never-registered", fn)
}

func TestEvents_RefetchRegistry(t *testing.T) {
	s := newSharedEventStream("https://app.smplkit.com", "k", nil)
	var order []string
	idA := s.onReconnectRefetch(func() { order = append(order, "a") })
	idB := s.onReconnectRefetch(func() { order = append(order, "b") })

	s.runRefetch()
	assert.Equal(t, []string{"a", "b"}, order)

	s.offReconnectRefetch(idA)
	order = nil
	s.runRefetch()
	assert.Equal(t, []string{"b"}, order)

	// Unknown ids are a no-op.
	s.offReconnectRefetch(9999999)
	s.offReconnectRefetch(idB)
	order = nil
	s.runRefetch()
	assert.Empty(t, order)
}

func TestEvents_RefetchPanicRecovered(t *testing.T) {
	s := newSharedEventStream("https://app.smplkit.com", "k", nil)
	ran := false
	s.onReconnectRefetch(func() { panic("refetch blew up") })
	s.onReconnectRefetch(func() { ran = true })
	s.runRefetch() // must not panic outward
	assert.True(t, ran, "a panicking refetch must not prevent later callbacks")
}

// ---------------------------------------------------------------------------
// SSE parser
// ---------------------------------------------------------------------------

type parsedFrame struct{ name, data string }

func collectParser() (*sseParser, *[]parsedFrame, *[]time.Duration) {
	frames := &[]parsedFrame{}
	retries := &[]time.Duration{}
	p := newSSEParser(
		func(name, data string) { *frames = append(*frames, parsedFrame{name, data}) },
		func(d time.Duration) { *retries = append(*retries, d) },
	)
	return p, frames, retries
}

func TestSSEParser_LineEndings(t *testing.T) {
	for _, tc := range []struct {
		label string
		nl    string
	}{
		{"LF", "\n"},
		{"CRLF", "\r\n"},
		{"CR", "\r"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			p, frames, _ := collectParser()
			stream := "event: flag_changed" + tc.nl + `data: {"id": "f1"}` + tc.nl + tc.nl
			p.feed([]byte(stream))
			require.Len(t, *frames, 1)
			assert.Equal(t, parsedFrame{"flag_changed", `{"id": "f1"}`}, (*frames)[0])
		})
	}
}

func TestSSEParser_SplitAcrossReads(t *testing.T) {
	// Field names, values, and CRLF pairs split across arbitrary read-buffer
	// boundaries must reassemble: feed the stream one byte at a time.
	p, frames, retries := collectParser()
	stream := "retry: 1500\r\nevent: config_changed\r\ndata: {\"id\": \"db\"}\r\n\r\n"
	for i := 0; i < len(stream); i++ {
		p.feed([]byte{stream[i]})
	}
	require.Len(t, *frames, 1)
	assert.Equal(t, parsedFrame{"config_changed", `{"id": "db"}`}, (*frames)[0])
	require.Len(t, *retries, 1)
	assert.Equal(t, 1500*time.Millisecond, (*retries)[0])
}

func TestSSEParser_MultipleDataLinesJoinWithNewline(t *testing.T) {
	p, frames, _ := collectParser()
	p.feed([]byte("data: {\ndata: \"id\": \"x\"\ndata: }\n\n"))
	require.Len(t, *frames, 1)
	assert.Equal(t, "message", (*frames)[0].name)
	assert.Equal(t, "{\n\"id\": \"x\"\n}", (*frames)[0].data)
}

func TestSSEParser_CommentsIgnored(t *testing.T) {
	p, frames, _ := collectParser()
	p.feed([]byte(": keepalive\n:another comment\nevent: e\ndata: {}\n: mid-frame comment\n\n"))
	require.Len(t, *frames, 1)
	assert.Equal(t, parsedFrame{"e", "{}"}, (*frames)[0])
}

func TestSSEParser_UnknownFieldsIgnored(t *testing.T) {
	p, frames, retries := collectParser()
	p.feed([]byte("id: 42\nfancy-new-field: hello\nevent: e\ndata: {}\n\n"))
	require.Len(t, *frames, 1)
	assert.Equal(t, parsedFrame{"e", "{}"}, (*frames)[0])
	assert.Empty(t, *retries)
}

func TestSSEParser_BOMStripped(t *testing.T) {
	t.Run("single feed", func(t *testing.T) {
		p, frames, _ := collectParser()
		p.feed([]byte("\ufeffevent: e\ndata: {}\n\n"))
		require.Len(t, *frames, 1)
		assert.Equal(t, parsedFrame{"e", "{}"}, (*frames)[0])
	})
	t.Run("split feed", func(t *testing.T) {
		// The BOM's three bytes arrive in separate reads.
		p, frames, _ := collectParser()
		stream := []byte("\ufeffevent: e\ndata: {}\n\n")
		for _, b := range stream {
			p.feed([]byte{b})
		}
		require.Len(t, *frames, 1)
		assert.Equal(t, parsedFrame{"e", "{}"}, (*frames)[0])
	})
	t.Run("BOM only strips on the first line", func(t *testing.T) {
		p, frames, _ := collectParser()
		p.feed([]byte("event: e\ndata: \ufeffx\n\n"))
		require.Len(t, *frames, 1)
		assert.Equal(t, "\ufeffx", (*frames)[0].data)
	})
}

func TestSSEParser_NoDataFrameNotDispatched(t *testing.T) {
	p, frames, _ := collectParser()
	p.feed([]byte("event: nothing_here\n\n"))
	assert.Empty(t, *frames)
	// The event-type buffer still resets: a following data-only frame
	// dispatches as the default "message", not as "nothing_here".
	p.feed([]byte("data: {}\n\n"))
	require.Len(t, *frames, 1)
	assert.Equal(t, "message", (*frames)[0].name)
}

func TestSSEParser_ValueEdgeCases(t *testing.T) {
	p, frames, _ := collectParser()
	// "data" with no colon contributes an empty data line; only ONE leading
	// space is stripped from a value; a colon in the value is preserved.
	p.feed([]byte("data\n\n"))
	require.Len(t, *frames, 1)
	assert.Equal(t, "", (*frames)[0].data)

	p.feed([]byte("data:  two spaces\n\n"))
	require.Len(t, *frames, 2)
	assert.Equal(t, " two spaces", (*frames)[1].data)

	p.feed([]byte("data: a:b\n\n"))
	require.Len(t, *frames, 3)
	assert.Equal(t, "a:b", (*frames)[2].data)
}

func TestSSEParser_RetryField(t *testing.T) {
	p, frames, retries := collectParser()
	p.feed([]byte("retry: 1000\n"))
	p.feed([]byte("retry:250\n"))  // no space after colon
	p.feed([]byte("retry: abc\n")) // non-numeric: ignored
	p.feed([]byte("retry: 12x\n")) // trailing garbage: ignored
	p.feed([]byte("retry: -5\n"))  // sign: ignored (digits only)
	p.feed([]byte("retry:\n"))     // empty: ignored
	p.feed([]byte("retry: 9f9\n")) // non-ASCII digit shape: ignored
	assert.Equal(t, []time.Duration{time.Second, 250 * time.Millisecond}, *retries)
	assert.Empty(t, *frames)
}

func TestSSEParser_CRLFSplitDoesNotDoubleTerminate(t *testing.T) {
	// A CR at the end of one chunk followed by LF at the start of the next is
	// ONE terminator — it must not synthesize an extra blank line (which
	// would dispatch a frame early).
	p, frames, _ := collectParser()
	p.feed([]byte("event: e\r"))
	p.feed([]byte("\ndata: {}\r"))
	assert.Empty(t, *frames, "no blank line yet — nothing may dispatch")
	p.feed([]byte("\n\r\n"))
	require.Len(t, *frames, 1)
	assert.Equal(t, parsedFrame{"e", "{}"}, (*frames)[0])
}
