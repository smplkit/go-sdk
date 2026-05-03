package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/smplkit/go-sdk/v3/internal/debug"
)

// wsConnectTimeout bounds how long runtime ensureInit calls block waiting
// for the WebSocket subscription to be registered server-side. The block
// closes a race where an immediate write after Get() triggers a broadcast
// before the subscription exists; if WS cannot connect within this window
// we proceed without a confirmed subscription rather than failing Get().
const wsConnectTimeout = 5 * time.Second

// sharedWebSocket manages the real-time event connection.
type sharedWebSocket struct {
	appBaseURL string
	apiKey     string

	listenersMu sync.Mutex
	listeners   map[string][]eventCallback

	statusMu sync.RWMutex
	status   string // "disconnected" | "connecting" | "connected" | "reconnecting"

	// firstConnectedCh is closed exactly once, the first time the WebSocket
	// reaches the "connected" state (i.e. server has accepted the upgrade,
	// validated the API key, registered the subscription, and sent the
	// {"type":"connected"} confirmation). Callers can wait on it via
	// waitConnected to avoid a race where the SDK fires writes that
	// trigger broadcasts before the WS subscription is registered
	// server-side and so silently miss the resulting events.
	firstConnectedCh   chan struct{}
	firstConnectedOnce sync.Once

	closeCh   chan struct{}
	closeOnce sync.Once //nolint:unused // used by stop(), which is part of the shutdown lifecycle
	wsDone    chan struct{}

	dialWS func(url string) (*websocket.Conn, error)

	// initBackoff and maxBackoff allow tests to override defaults; zero means use defaults.
	initBackoff time.Duration
	maxBackoff  time.Duration

	metrics *metricsReporter
}

type eventCallback struct {
	id uintptr
	fn func(map[string]interface{})
}

var callbackIDCounter uintptr
var callbackIDMu sync.Mutex

func nextCallbackID() uintptr {
	callbackIDMu.Lock()
	defer callbackIDMu.Unlock()
	callbackIDCounter++
	return callbackIDCounter
}

func newSharedWebSocket(appBaseURL, apiKey string, metrics *metricsReporter) *sharedWebSocket {
	return &sharedWebSocket{
		appBaseURL:       appBaseURL,
		apiKey:           apiKey,
		listeners:        make(map[string][]eventCallback),
		status:           "disconnected",
		firstConnectedCh: make(chan struct{}),
		closeCh:          make(chan struct{}),
		wsDone:           make(chan struct{}),
		dialWS:           defaultDialWS,
		metrics:          metrics,
	}
}

// on registers a listener for a specific event type.
func (ws *sharedWebSocket) on(eventName string, callback func(map[string]interface{})) {
	ws.listenersMu.Lock()
	defer ws.listenersMu.Unlock()
	id := nextCallbackID()
	ws.listeners[eventName] = append(ws.listeners[eventName], eventCallback{id: id, fn: callback})
}

// off unregisters a listener for a specific event type (by function pointer).
func (ws *sharedWebSocket) off(eventName string, _ func(map[string]interface{})) {
	ws.listenersMu.Lock()
	defer ws.listenersMu.Unlock()
	// Compare function pointers using the address of the function value.
	// This is a best-effort match.
	cbs := ws.listeners[eventName]
	// We can't reliably compare function values in Go, so we remove the last
	// registered callback for this event. Callers should unregister in reverse
	// order of registration. This matches the Python pattern where each module
	// registers its own handler.
	if len(cbs) > 0 {
		ws.listeners[eventName] = cbs[:len(cbs)-1]
	}
}

func (ws *sharedWebSocket) dispatch(eventName string, data map[string]interface{}) {
	ws.listenersMu.Lock()
	cbs := make([]eventCallback, len(ws.listeners[eventName]))
	copy(cbs, ws.listeners[eventName])
	ws.listenersMu.Unlock()

	if len(cbs) == 0 {
		debug.Debug("websocket", "no handler registered for event: %q", eventName)
		return
	}
	debug.Debug("websocket", "routing %q to %d handler(s)", eventName, len(cbs))

	for _, cb := range cbs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("smplkit: exception in event listener for %q: %v", eventName, r)
				}
			}()
			cb.fn(data)
		}()
	}
}

func (ws *sharedWebSocket) connectionStatus() string {
	ws.statusMu.RLock()
	defer ws.statusMu.RUnlock()
	return ws.status
}

func (ws *sharedWebSocket) setStatus(s string) {
	ws.statusMu.Lock()
	ws.status = s
	ws.statusMu.Unlock()
	if s == "connected" {
		ws.firstConnectedOnce.Do(func() { close(ws.firstConnectedCh) })
	}
}

// waitConnected blocks until the WebSocket reaches its first "connected"
// state, the context is canceled, or the timeout elapses. It returns
// nil on connect, ctx.Err() on cancellation, or a timeout error.
//
// Callers use this to avoid the race where they immediately trigger a
// write whose broadcast event arrives at the server before the WS
// subscription is registered. After the first successful connection,
// subsequent reconnects do not block (the channel stays closed).
func (ws *sharedWebSocket) waitConnected(ctx context.Context, timeout time.Duration) error {
	select {
	case <-ws.firstConnectedCh:
		return nil
	default:
	}
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timeoutCh = t.C
	}
	select {
	case <-ws.firstConnectedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timeoutCh:
		return fmt.Errorf("smplkit: timed out waiting for WebSocket connection after %s", timeout)
	}
}

func defaultDialWS(wsURL string) (*websocket.Conn, error) {
	// CloudFront's WAF blocks WebSocket upgrade requests that omit a
	// User-Agent header. gorilla/websocket doesn't set one by default
	// (browsers do), so we inject it explicitly to match the User-Agent
	// the HTTP transport sends. Without this, the upgrade request can
	// be rejected with HTTP 403 before reaching our backend.
	header := http.Header{"User-Agent": []string{userAgent}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	return conn, err
}

// start launches the background WebSocket goroutine.
func (ws *sharedWebSocket) start() {
	debug.Debug("websocket", "starting WebSocket connection")
	go ws.run()
}

// stop closes the WebSocket connection and waits for the goroutine to exit.
func (ws *sharedWebSocket) stop() { //nolint:unused // lifecycle method called by Client.stopWS
	ws.closeOnce.Do(func() {
		close(ws.closeCh)
	})
	<-ws.wsDone
	ws.setStatus("disconnected")
}

func (ws *sharedWebSocket) buildWSURL() string {
	u := ws.appBaseURL
	if strings.HasPrefix(u, "https://") {
		u = "wss://" + u[len("https://"):]
	} else if strings.HasPrefix(u, "http://") {
		u = "ws://" + u[len("http://"):]
	} else {
		u = "wss://" + u
	}
	u = strings.TrimRight(u, "/")
	return u + "/api/ws/v1/events?" + url.Values{"api_key": {ws.apiKey}}.Encode()
}

func (ws *sharedWebSocket) run() {
	defer func() {
		ws.setStatus("disconnected")
		close(ws.wsDone)
	}()

	backoff := ws.initBackoff
	if backoff == 0 {
		backoff = time.Second
	}
	maxBackoff := ws.maxBackoff
	if maxBackoff == 0 {
		maxBackoff = 60 * time.Second
	}

	for {
		select {
		case <-ws.closeCh:
			return
		default:
		}

		closed := ws.connect()
		if closed {
			return
		}

		// Back off then retry.
		debug.Debug("websocket", "reconnecting in %s", backoff)
		ws.setStatus("reconnecting")
		select {
		case <-ws.closeCh:
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func (ws *sharedWebSocket) connect() (closed bool) {
	wsURL := ws.buildWSURL()
	// Log a sanitized URL (strip api_key query param).
	sanitizedURL := wsURL
	if idx := strings.Index(wsURL, "?"); idx >= 0 {
		sanitizedURL = wsURL[:idx]
	}
	debug.Debug("websocket", "connecting to %s", sanitizedURL)
	ws.setStatus("connecting")

	dial := ws.dialWS
	if dial == nil {
		dial = defaultDialWS
	}

	conn, dialErr := dial(wsURL)
	if dialErr != nil {
		debug.Debug("websocket", "dial error: %v", dialErr)
		select {
		case <-ws.closeCh:
			return true
		default:
		}
		return false
	}
	defer conn.Close() //nolint:errcheck

	// Wait for {"type": "connected"} confirmation.
	var msg map[string]interface{}
	if err := conn.ReadJSON(&msg); err != nil {
		debug.Debug("websocket", "error reading connection confirmation: %v", err)
		select {
		case <-ws.closeCh:
			return true
		default:
		}
		return false
	}

	if msgType, _ := msg["type"].(string); msgType == "error" {
		log.Printf("smplkit: shared WebSocket connection error: %v", msg["message"])
		debug.Debug("websocket", "connection error details: %+v", msg)
		return false
	}

	debug.Debug("websocket", "connected")
	ws.setStatus("connected")
	if ws.metrics != nil {
		ws.metrics.RecordGauge("platform.websocket_connections", 1, "connections", nil)
	}

	// Close the WebSocket when closeCh fires.
	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-ws.closeCh:
			conn.Close() //nolint:errcheck
		case <-stopWatcher:
		}
	}()

	// Receive loop.
	for {
		_, message, readErr := conn.ReadMessage()
		if readErr != nil {
			if ws.metrics != nil {
				ws.metrics.RecordGauge("platform.websocket_connections", 0, "connections", nil)
			}
			select {
			case <-ws.closeCh:
				return true
			default:
			}
			return false
		}

		// Heartbeat: server sends "ping", client responds with "pong".
		if string(message) == "ping" {
			debug.Debug("websocket", "ping received, sending pong")
			_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal(message, &data); err != nil {
			continue
		}

		event, _ := data["event"].(string)
		if event != "" {
			ws.dispatch(event, data)
		}
	}
}
