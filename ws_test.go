package smplkit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsTestServer stands up a local WebSocket server that upgrades the request
// and hands the connection to handler. It lets the dedicated WebSocket test
// exercise the real dial/connect/receive machinery against a mocked network
// (no real backend, no genuine reconnect leak).
func wsTestServer(t *testing.T, handler func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		handler(conn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestWS_SuiteWideDialNeutralized is the test-hygiene regression guard: it
// fails if the suite-wide WebSocket-dial neutralization (TestMain's wsLaunch
// no-op) is ever removed. With the seam in place, start() on a socket pointed
// at the real backend must not transition the socket out of "disconnected" —
// i.e. it must not dial — and stop() must still complete.
func TestWS_SuiteWideDialNeutralized(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "sk_should_never_dial", nil)
	ws.on("noop", func(map[string]interface{}) {}) // listener registration still works
	ws.start()
	// Give a real dialer time to change status if the seam regressed.
	time.Sleep(25 * time.Millisecond)
	require.Equal(t, "disconnected", ws.connectionStatus(),
		"WebSocket dial is not neutralized suite-wide — start() reached the network")
	ws.stop() // must not block: the no-op launcher closed wsDone
	assert.Equal(t, "disconnected", ws.connectionStatus())
}

// withRealWSLaunch restores the real run()-launching wsLaunch for the duration
// of a sub-test that needs the genuine reconnect loop, then resets the no-op.
func withRealWSLaunch(t *testing.T) {
	t.Helper()
	prev := wsLaunch
	wsLaunch = func(ws *sharedWebSocket) { go ws.run() }
	t.Cleanup(func() { wsLaunch = prev })
}

func TestWS_Connect_DispatchesEventsAndPongs(t *testing.T) {
	gotEvent := make(chan map[string]interface{}, 1)
	gotPong := make(chan string, 1)
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]string{"type": "connected"})
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":"flag.changed","id":"f1"}`))
		// Non-JSON and no-event messages are ignored by the receive loop.
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`not json`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"noevent":true}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte("ping"))
		_, msg, err := conn.ReadMessage()
		if err == nil {
			gotPong <- string(msg)
		}
	})

	ws := newSharedWebSocket(srv.URL, "key", makeReporter(t))
	ws.on("flag.changed", func(d map[string]interface{}) { gotEvent <- d })

	connDone := make(chan bool, 1)
	go func() { connDone <- ws.connect() }()

	select {
	case d := <-gotEvent:
		assert.Equal(t, "f1", d["id"])
	case <-time.After(2 * time.Second):
		t.Fatal("event was not dispatched to the listener")
	}
	assert.Equal(t, "connected", ws.connectionStatus())
	require.NoError(t, ws.waitConnected(context.Background(), time.Second))

	select {
	case p := <-gotPong:
		assert.Equal(t, "pong", p)
	case <-time.After(2 * time.Second):
		t.Fatal("client did not reply to ping with pong")
	}
	select {
	case <-connDone:
	case <-time.After(2 * time.Second):
		t.Fatal("connect did not return after the server closed")
	}
}

func TestWS_Connect_ListenerPanicRecovered(t *testing.T) {
	done := make(chan struct{})
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]string{"type": "connected"})
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":"boom","id":"x"}`))
		// Keep the connection open briefly so dispatch runs, then close.
		time.Sleep(50 * time.Millisecond)
	})
	ws := newSharedWebSocket(srv.URL, "key", nil)
	ws.on("boom", func(map[string]interface{}) { close(done); panic("listener blew up") })
	go ws.connect()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("listener was not invoked")
	}
	// A panicking listener must not crash the receive loop.
	assert.Equal(t, "connected", ws.connectionStatus())
}

func TestWS_Connect_ServerErrorConfirmation(t *testing.T) {
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]interface{}{"type": "error", "message": "bad key"})
	})
	ws := newSharedWebSocket(srv.URL, "key", nil)
	closed := ws.connect()
	assert.False(t, closed)
	// The error confirmation arrives before "connected" is set.
	assert.NotEqual(t, "connected", ws.connectionStatus())
}

func TestWS_Connect_ConfirmationReadError(t *testing.T) {
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		// Close immediately without sending the {"type":"connected"} frame.
	})
	ws := newSharedWebSocket(srv.URL, "key", nil)
	closed := ws.connect()
	assert.False(t, closed)
}

func TestWS_Connect_DialError(t *testing.T) {
	ws := newSharedWebSocket("http://127.0.0.1:1", "key", nil)
	closed := ws.connect()
	assert.False(t, closed)
	assert.Equal(t, "connecting", ws.connectionStatus())
}

func TestWS_Run_ReconnectsThenStops(t *testing.T) {
	withRealWSLaunch(t)
	connectedC := make(chan struct{}, 8)
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]string{"type": "connected"})
		connectedC <- struct{}{}
		// Close right away so run() loops and reconnects.
	})
	ws := newSharedWebSocket(srv.URL, "key", nil)
	ws.initBackoff = time.Millisecond
	ws.maxBackoff = 5 * time.Millisecond
	ws.start() // real launcher (restored) → go run()

	// Wait for at least two connect cycles to prove the reconnect loop runs.
	for i := 0; i < 2; i++ {
		select {
		case <-connectedC:
		case <-time.After(3 * time.Second):
			t.Fatalf("did not observe reconnect cycle %d", i+1)
		}
	}
	ws.stop() // closes closeCh, waits on wsDone
	assert.Equal(t, "disconnected", ws.connectionStatus())
}

func TestWS_Stop_WhileConnected(t *testing.T) {
	withRealWSLaunch(t)
	connected := make(chan struct{})
	release := make(chan struct{})
	srv := wsTestServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(map[string]string{"type": "connected"})
		close(connected)
		<-release // hold the connection open until the test tears down
	})
	t.Cleanup(func() { close(release) })

	ws := newSharedWebSocket(srv.URL, "key", makeReporter(t))
	ws.start() // real launcher (restored) → go run()

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("server never accepted the connection")
	}
	require.NoError(t, ws.waitConnected(context.Background(), time.Second))
	assert.Equal(t, "connected", ws.connectionStatus())

	// stop() closes closeCh; the stopWatcher goroutine closes the live conn,
	// the receive loop errors, observes closeCh closed, and run() exits.
	ws.stop()
	assert.Equal(t, "disconnected", ws.connectionStatus())
}

func TestWS_WaitConnected_TimeoutAndCancel(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "key", nil)

	err := ws.waitConnected(context.Background(), 10*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	// The timeout path must yield a typed *TimeoutError so callers (and
	// SmplClient.WaitUntilReady) can match it via errors.As, matching the
	// canonical Python hierarchy.
	var timeoutErr *TimeoutError
	require.True(t, errors.As(err, &timeoutErr), "expected *TimeoutError, got %T: %v", err, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = ws.waitConnected(ctx, time.Second)
	require.ErrorIs(t, err, context.Canceled)

	// Once connected, waitConnected returns immediately even with zero timeout.
	ws.setStatus("connected")
	require.NoError(t, ws.waitConnected(context.Background(), 0))
}

func TestWS_BuildWSURL(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://app.smplkit.com", "wss://app.smplkit.com/api/ws/v1/events?api_key=k"},
		{"http://127.0.0.1:8000/", "ws://127.0.0.1:8000/api/ws/v1/events?api_key=k"},
		{"app.smplkit.com", "wss://app.smplkit.com/api/ws/v1/events?api_key=k"},
	}
	for _, tc := range cases {
		ws := newSharedWebSocket(tc.base, "k", nil)
		assert.Equal(t, tc.want, ws.buildWSURL())
	}
}

func TestWS_OffUnregistersListener(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "k", nil)
	calls := 0
	fn := func(map[string]interface{}) { calls++ }
	ws.on("evt", fn)
	ws.off("evt", fn)
	ws.dispatch("evt", map[string]interface{}{}) // no registered listener now
	assert.Equal(t, 0, calls)

	// dispatch with no handlers is a no-op (covers the empty-listener branch).
	ws.dispatch("never-registered", map[string]interface{}{})
}
