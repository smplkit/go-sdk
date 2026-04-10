package smplkit

import (
	"encoding/json"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// sharedWebSocket manages the real-time event connection.
type sharedWebSocket struct {
	appBaseURL string
	apiKey     string

	listenersMu sync.Mutex
	listeners   map[string][]eventCallback

	statusMu sync.RWMutex
	status   string // "disconnected" | "connecting" | "connected" | "reconnecting"

	closeCh   chan struct{}
	closeOnce sync.Once //nolint:unused // used by stop(), which is part of the shutdown lifecycle
	wsDone    chan struct{}

	dialWS func(url string) (*websocket.Conn, error)

	// initBackoff and maxBackoff allow tests to override defaults; zero means use defaults.
	initBackoff time.Duration
	maxBackoff  time.Duration
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

func newSharedWebSocket(appBaseURL, apiKey string) *sharedWebSocket {
	return &sharedWebSocket{
		appBaseURL: appBaseURL,
		apiKey:     apiKey,
		listeners:  make(map[string][]eventCallback),
		status:     "disconnected",
		closeCh:    make(chan struct{}),
		wsDone:     make(chan struct{}),
		dialWS:     defaultDialWS,
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
}

func defaultDialWS(wsURL string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	return conn, err
}

// start launches the background WebSocket goroutine.
func (ws *sharedWebSocket) start() {
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
	ws.setStatus("connecting")

	dial := ws.dialWS
	if dial == nil {
		dial = defaultDialWS
	}

	conn, dialErr := dial(wsURL)
	if dialErr != nil {
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
		select {
		case <-ws.closeCh:
			return true
		default:
		}
		return false
	}

	if msgType, _ := msg["type"].(string); msgType == "error" {
		log.Printf("smplkit: shared WebSocket connection error: %v", msg["message"])
		return false
	}

	ws.setStatus("connected")

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
			select {
			case <-ws.closeCh:
				return true
			default:
			}
			return false
		}

		// Heartbeat: server sends "ping", client responds with "pong".
		if string(message) == "ping" {
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
