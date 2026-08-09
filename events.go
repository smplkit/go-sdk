package smplkit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/smplkit/go-sdk/v3/internal/debug"
)

// eventsConnectTimeout bounds how long runtime ensureInit calls block waiting
// for the live event stream to be established server-side. The block closes a
// race where an immediate write after Get() triggers a broadcast before the
// subscription exists; if the stream cannot connect within this window we
// proceed without a confirmed subscription rather than failing Get().
const eventsConnectTimeout = 5 * time.Second

// eventsReadTimeout is the liveness deadline on the event stream. The server
// emits a keepalive comment every 30 seconds when idle, so 45 seconds of
// silence (one and a half missed keepalives) means the stream is dead: the
// client drops the connection and re-enters the reconnect loop. Any bytes
// received — including comment frames — count as liveness.
const eventsReadTimeout = 45 * time.Second

// sharedEventStream manages the real-time event connection: a long-lived
// GET /api/v1/events request whose response body is a Server-Sent Events
// (text/event-stream) stream.
type sharedEventStream struct {
	appBaseURL string
	apiKey     string

	// callerUA is the caller-supplied User-Agent (from Config.ExtraHeaders,
	// any casing) to send on the stream request; "" means send the SDK default.
	callerUA string

	listenersMu sync.Mutex
	listeners   map[string][]eventCallback

	// refetch holds callbacks registered by product modules via
	// onReconnectRefetch. On every successful REconnect (not the initial
	// connect) the stream invokes them so each loaded module re-syncs the
	// state it may have missed while disconnected, via its own bulk-refresh
	// path (which fires change listeners only for keys whose resolved state
	// actually changed).
	refetchMu sync.Mutex
	refetch   []refetchCallback

	statusMu sync.RWMutex
	status   string // "disconnected" | "connecting" | "connected" | "reconnecting"

	// firstConnectedCh is closed exactly once, the first time the event
	// stream is established (see connect for the definition). Callers can
	// wait on it via waitConnected to avoid a race where the SDK fires
	// writes that trigger broadcasts before the subscription is registered
	// server-side and so silently miss the resulting events.
	firstConnectedCh   chan struct{}
	firstConnectedOnce sync.Once

	closeCh    chan struct{}
	closeOnce  sync.Once //nolint:unused // used by stop(), which is part of the shutdown lifecycle
	streamDone chan struct{}

	// httpClient performs the streaming GET. It deliberately has no overall
	// request timeout — the stream is long-lived; liveness is enforced by
	// the read watchdog instead. Set by the constructor; a test seam.
	httpClient *http.Client

	// initBackoff, maxBackoff, and readTimeout allow tests to override
	// defaults; zero means use defaults.
	initBackoff time.Duration
	maxBackoff  time.Duration
	readTimeout time.Duration

	// retryHint is the server-provided reconnection base delay (the SSE
	// `retry:` field, in milliseconds). Zero until the server sends one.
	// Only touched from the goroutine that runs connect().
	retryHint time.Duration

	// everConnected records that at least one connect succeeded, so the
	// next successful connect is a REconnect and triggers the registered
	// refetch callbacks. Only touched from the goroutine running connect().
	everConnected bool

	metrics *metricsReporter
}

type eventCallback struct {
	id uintptr
	fn func(map[string]interface{})
}

type refetchCallback struct {
	id uintptr
	fn func()
}

var callbackIDCounter uintptr
var callbackIDMu sync.Mutex

func nextCallbackID() uintptr {
	callbackIDMu.Lock()
	defer callbackIDMu.Unlock()
	callbackIDCounter++
	return callbackIDCounter
}

func newSharedEventStream(appBaseURL, apiKey string, metrics *metricsReporter) *sharedEventStream {
	return &sharedEventStream{
		appBaseURL:       appBaseURL,
		apiKey:           apiKey,
		listeners:        make(map[string][]eventCallback),
		status:           "disconnected",
		firstConnectedCh: make(chan struct{}),
		closeCh:          make(chan struct{}),
		streamDone:       make(chan struct{}),
		httpClient:       &http.Client{},
		metrics:          metrics,
	}
}

// on registers a listener for a specific event type.
func (s *sharedEventStream) on(eventName string, callback func(map[string]interface{})) {
	s.listenersMu.Lock()
	defer s.listenersMu.Unlock()
	id := nextCallbackID()
	s.listeners[eventName] = append(s.listeners[eventName], eventCallback{id: id, fn: callback})
}

// off unregisters a listener for a specific event type (by function pointer).
func (s *sharedEventStream) off(eventName string, _ func(map[string]interface{})) {
	s.listenersMu.Lock()
	defer s.listenersMu.Unlock()
	// We can't reliably compare function values in Go, so we remove the last
	// registered callback for this event. Callers should unregister in reverse
	// order of registration. This matches the Python pattern where each module
	// registers its own handler.
	cbs := s.listeners[eventName]
	if len(cbs) > 0 {
		s.listeners[eventName] = cbs[:len(cbs)-1]
	}
}

// onReconnectRefetch registers a callback the stream invokes after every
// successful reconnect (never after the initial connect). The returned id
// unregisters it via offReconnectRefetch.
func (s *sharedEventStream) onReconnectRefetch(fn func()) uintptr {
	s.refetchMu.Lock()
	defer s.refetchMu.Unlock()
	id := nextCallbackID()
	s.refetch = append(s.refetch, refetchCallback{id: id, fn: fn})
	return id
}

// offReconnectRefetch unregisters a reconnect-refetch callback by id.
func (s *sharedEventStream) offReconnectRefetch(id uintptr) {
	s.refetchMu.Lock()
	defer s.refetchMu.Unlock()
	for i, cb := range s.refetch {
		if cb.id == id {
			s.refetch = append(s.refetch[:i], s.refetch[i+1:]...)
			return
		}
	}
}

// runRefetch invokes every registered reconnect-refetch callback, recovering
// from panics so one module's failure cannot break another's re-sync.
func (s *sharedEventStream) runRefetch() {
	s.refetchMu.Lock()
	cbs := make([]refetchCallback, len(s.refetch))
	copy(cbs, s.refetch)
	s.refetchMu.Unlock()
	debug.Debug("events", "reconnected — refetching %d module(s)", len(cbs))
	for _, cb := range cbs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("smplkit: exception in reconnect refetch: %v", r)
				}
			}()
			cb.fn()
		}()
	}
}

func (s *sharedEventStream) dispatch(eventName string, data map[string]interface{}) {
	s.listenersMu.Lock()
	cbs := make([]eventCallback, len(s.listeners[eventName]))
	copy(cbs, s.listeners[eventName])
	s.listenersMu.Unlock()

	if len(cbs) == 0 {
		// Unknown event names are ignored silently (debug output only).
		debug.Debug("events", "no handler registered for event: %q", eventName)
		return
	}
	debug.Debug("events", "routing %q to %d handler(s)", eventName, len(cbs))

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

// handleFrame converts a parsed SSE frame into a listener dispatch: the
// event name is the SSE `event:` field and the payload is the `data:` JSON
// ({"id": "<key>"} for single-resource events, {} for bulk-refresh events
// and the informational `connected` event). Frames whose data is not a JSON
// object are ignored.
func (s *sharedEventStream) handleFrame(eventName, data string) {
	payload := map[string]interface{}{}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		debug.Debug("events", "ignoring %q frame with non-JSON data", eventName)
		return
	}
	s.dispatch(eventName, payload)
}

func (s *sharedEventStream) connectionStatus() string {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status
}

func (s *sharedEventStream) setStatus(status string) {
	s.statusMu.Lock()
	s.status = status
	s.statusMu.Unlock()
	if status == "connected" {
		s.firstConnectedOnce.Do(func() { close(s.firstConnectedCh) })
	}
}

// waitConnected blocks until the event stream reaches its first "connected"
// state, the context is canceled, or the timeout elapses. It returns
// nil on connect, ctx.Err() on cancellation, or a *TimeoutError on
// timeout (matchable via errors.As, consistent with the rest of the
// error hierarchy and with SmplClient.WaitUntilReady's contract).
//
// Callers use this to avoid the race where they immediately trigger a
// write whose broadcast event arrives at the server before the stream
// subscription is registered. After the first successful connection,
// subsequent reconnects do not block (the channel stays closed).
func (s *sharedEventStream) waitConnected(ctx context.Context, timeout time.Duration) error {
	select {
	case <-s.firstConnectedCh:
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
	case <-s.firstConnectedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timeoutCh:
		return &TimeoutError{
			Base: Error{Message: fmt.Sprintf("smplkit: timed out waiting for live event stream after %s", timeout)},
		}
	}
}

// eventsLaunch starts the background connect/reconnect loop for a stream. It
// is a package-level seam: production launches the real run() goroutine. The
// unit test suite replaces it (in TestMain) with a no-op so that no test
// opens the network or leaks a reconnect goroutine — the stream is still
// fully constructed (listener registration and connection status behave
// normally), and stop() still completes. The dedicated event-stream test
// drives run() and connect() directly to exercise the real machinery against
// a mocked server.
var eventsLaunch = func(s *sharedEventStream) { go s.run() }

// start launches the background event-stream goroutine.
func (s *sharedEventStream) start() {
	debug.Debug("events", "starting event stream")
	eventsLaunch(s)
}

// stop closes the event stream and waits for the goroutine to exit.
func (s *sharedEventStream) stop() { //nolint:unused // lifecycle method called by SmplClient.stopEventStream
	s.closeOnce.Do(func() {
		close(s.closeCh)
	})
	<-s.streamDone
	s.setStatus("disconnected")
}

func (s *sharedEventStream) buildEventsURL() string {
	u := s.appBaseURL
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		u = "https://" + u
	}
	return strings.TrimRight(u, "/") + "/api/v1/events"
}

// backoffBase is the reconnect delay used after a successful connect (and as
// the first delay of a failure run): the server's `retry:` hint when one has
// been received, else the default of one second.
func (s *sharedEventStream) backoffBase() time.Duration {
	if s.retryHint > 0 {
		return s.retryHint
	}
	if s.initBackoff > 0 {
		return s.initBackoff
	}
	return time.Second
}

// maxBackoffOrDefault caps the doubling reconnect delay (default 60s).
func (s *sharedEventStream) maxBackoffOrDefault() time.Duration {
	if s.maxBackoff > 0 {
		return s.maxBackoff
	}
	return 60 * time.Second
}

// readTimeoutOrDefault is the liveness read deadline (default 45s).
func (s *sharedEventStream) readTimeoutOrDefault() time.Duration {
	if s.readTimeout > 0 {
		return s.readTimeout
	}
	return eventsReadTimeout
}

// isClosed reports whether stop() has been called.
func (s *sharedEventStream) isClosed() bool {
	select {
	case <-s.closeCh:
		return true
	default:
		return false
	}
}

// run is the connect/reconnect loop. The delay between attempts starts at
// backoffBase (seeded from the server's `retry:` hint once one is received),
// doubles on consecutive failures up to maxBackoffOrDefault, and resets to
// the base after every successful connect. No jitter.
func (s *sharedEventStream) run() {
	defer func() {
		s.setStatus("disconnected")
		close(s.streamDone)
	}()

	maxBackoff := s.maxBackoffOrDefault()
	var backoff time.Duration

	for {
		select {
		case <-s.closeCh:
			return
		default:
		}

		established, closed := s.connect()
		if closed {
			return
		}

		if established || backoff == 0 {
			// Reset to base after a successful connect (or seed the very
			// first retry of a failure run).
			backoff = s.backoffBase()
		} else {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		debug.Debug("events", "reconnecting in %s", backoff)
		s.setStatus("reconnecting")
		select {
		case <-s.closeCh:
			// Loop back to the top, which returns.
		case <-time.After(backoff):
		}
	}
}

// connect opens the event stream and consumes it until it ends.
//
// established reports a successful connect: HTTP 200 with a
// text/event-stream Content-Type. That moment is also the connect barrier —
// setStatus("connected") signals firstConnectedCh — because the server
// registers the subscription before writing the response headers, so once
// they arrive a write-triggered broadcast can no longer miss this client.
// The `connected` SSE event that follows is informational only. closed
// reports that stop() ended the connection.
func (s *sharedEventStream) connect() (established, closed bool) {
	streamURL := s.buildEventsURL()
	debug.Debug("events", "connecting to %s", streamURL)
	s.setStatus("connecting")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel the in-flight request (and the stream read) when closeCh fires.
	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-s.closeCh:
			cancel()
		case <-stopWatcher:
		}
	}()

	// Note: no Last-Event-ID header — the SDK never resumes a stream; a
	// reconnect re-syncs via the refetch callbacks instead.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		debug.Debug("events", "request build error: %v", err)
		return false, s.isClosed()
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	ua := s.callerUA
	if ua == "" {
		ua = userAgent
	}
	req.Header.Set("User-Agent", ua)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		debug.Debug("events", "connection error: %v", err)
		return false, s.isClosed()
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		// Auth failure is a plain HTTP 401; any non-200 is a failed attempt.
		log.Printf("smplkit: live event stream rejected (HTTP %d)", resp.StatusCode)
		debug.Debug("events", "unexpected status: %d", resp.StatusCode)
		return false, s.isClosed()
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		debug.Debug("events", "unexpected content type: %q", ct)
		return false, s.isClosed()
	}

	debug.Debug("events", "connected")
	s.setStatus("connected")
	if s.metrics != nil {
		s.metrics.RecordGauge("platform.event_connections", 1, "connections", nil)
	}

	// On a REconnect (not the initial connect), every loaded product module
	// re-syncs the state it may have missed while disconnected.
	wasConnected := s.everConnected
	s.everConnected = true
	if wasConnected {
		s.runRefetch()
	}

	// Liveness watchdog: readTimeoutOrDefault of silence cancels the request
	// context, which errors the blocked read below and drops the connection.
	// Every received byte (keepalive comments included) re-arms it.
	readTimeout := s.readTimeoutOrDefault()
	watchdog := time.AfterFunc(readTimeout, cancel)
	defer watchdog.Stop()

	parser := newSSEParser(s.handleFrame, func(hint time.Duration) { s.retryHint = hint })
	reader := bufio.NewReader(resp.Body)
	buf := make([]byte, 4096)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			watchdog.Reset(readTimeout)
			parser.feed(buf[:n])
		}
		if readErr != nil {
			if s.metrics != nil {
				s.metrics.RecordGauge("platform.event_connections", 0, "connections", nil)
			}
			debug.Debug("events", "stream ended: %v", readErr)
			return true, s.isClosed()
		}
	}
}

// ---------------------------------------------------------------------------
// SSE wire-format parser
// ---------------------------------------------------------------------------

// sseParser is an incremental parser for the text/event-stream wire format.
// Feed it raw response-body chunks in any sizes — field values split across
// read-buffer boundaries reassemble correctly. Per the SSE spec it accepts
// LF, CRLF, and bare-CR line terminators, strips a leading UTF-8 BOM,
// treats lines beginning with ':' as comments, joins multiple data: lines
// with '\n', ignores unknown fields, and dispatches an accumulated frame on
// each blank line (frames with no data are not dispatched).
type sseParser struct {
	onEvent func(name, data string)
	onRetry func(time.Duration)

	line      []byte
	prevCR    bool
	firstLine bool

	eventName string
	dataLines []string
}

func newSSEParser(onEvent func(name, data string), onRetry func(time.Duration)) *sseParser {
	return &sseParser{onEvent: onEvent, onRetry: onRetry, firstLine: true}
}

// feed consumes one chunk of the stream, invoking the callbacks for every
// frame or retry field completed by it.
func (p *sseParser) feed(chunk []byte) {
	for _, b := range chunk {
		if p.prevCR {
			p.prevCR = false
			if b == '\n' {
				continue // CRLF: the CR already terminated the line.
			}
		}
		switch b {
		case '\r':
			p.prevCR = true
			p.processLine()
		case '\n':
			p.processLine()
		default:
			p.line = append(p.line, b)
		}
	}
}

func (p *sseParser) processLine() {
	line := string(p.line)
	p.line = p.line[:0]

	if p.firstLine {
		p.firstLine = false
		line = strings.TrimPrefix(line, "\ufeff")
	}

	if line == "" {
		p.dispatchFrame()
		return
	}
	if strings.HasPrefix(line, ":") {
		// Comment frame (e.g. the server's ": keepalive") — liveness only.
		return
	}

	field, value := line, ""
	if idx := strings.IndexByte(line, ':'); idx >= 0 {
		field = line[:idx]
		value = strings.TrimPrefix(line[idx+1:], " ")
	}

	switch field {
	case "event":
		p.eventName = value
	case "data":
		p.dataLines = append(p.dataLines, value)
	case "retry":
		if ms, ok := parseRetryMillis(value); ok {
			p.onRetry(time.Duration(ms) * time.Millisecond)
		}
	default:
		// Unknown fields (including "id") are ignored per the SSE spec.
	}
}

func (p *sseParser) dispatchFrame() {
	if len(p.dataLines) == 0 {
		// Per the SSE spec a frame with an empty data buffer is not
		// dispatched; the event type buffer still resets.
		p.eventName = ""
		return
	}
	name := p.eventName
	if name == "" {
		name = "message"
	}
	data := strings.Join(p.dataLines, "\n")
	p.eventName = ""
	p.dataLines = nil
	p.onEvent(name, data)
}

// parseRetryMillis parses an SSE retry field value: ASCII digits only,
// per the spec. Anything else is ignored.
func parseRetryMillis(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	ms := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		ms = ms*10 + int(r-'0')
	}
	return ms, true
}
