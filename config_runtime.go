package smplkit

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// chainEntry holds a single config node's resolved data for inheritance walking.
type chainEntry struct {
	ID           string
	Values       map[string]interface{}
	Environments map[string]map[string]interface{}
}

// ConfigChangeEvent describes a single value change delivered to OnChange listeners.
type ConfigChangeEvent struct {
	// Key is the top-level key that changed.
	Key string
	// OldValue is the value before the change (nil if the key was new).
	OldValue interface{}
	// NewValue is the value after the change (nil if the key was removed).
	NewValue interface{}
	// Source is "websocket" for server-pushed changes or "manual" for Refresh calls.
	Source string
}

// ConfigStats holds runtime diagnostics for a ConfigRuntime.
type ConfigStats struct {
	// FetchCount is the total number of times the full config chain was fetched.
	FetchCount int
	// LastFetchAt is the time of the most recent fetch.
	LastFetchAt time.Time
}

type changeListener struct {
	key string // "" matches all keys
	cb  func(*ConfigChangeEvent)
}

// ConfigRuntime is a live, in-process view of a resolved config for one environment.
// Obtain one via Config.Connect. Call Close when done.
type ConfigRuntime struct {
	configID    string
	environment string

	mu          sync.RWMutex
	cache       map[string]interface{}
	fetchCount  int
	lastFetchAt time.Time

	listenersMu sync.Mutex
	listeners   []changeListener

	statusMu sync.RWMutex
	status   string // "connecting" | "connected" | "disconnected"

	closeCh   chan struct{}
	closeOnce sync.Once
	wsDone    chan struct{}

	fetchChain  func() ([]chainEntry, error)
	apiKey      string
	wsBase      string // base WebSocket URL (ws:// or wss://)
	initBackoff time.Duration
	maxBackoff  time.Duration
	dialWS      func(url string) (*websocket.Conn, error)
}

func newConfigRuntime(configID, environment string, cache map[string]interface{}, fetchChain func() ([]chainEntry, error), apiKey, baseURL string) *ConfigRuntime {
	return &ConfigRuntime{
		configID:    configID,
		environment: environment,
		cache:       cache,
		fetchCount:  1,
		lastFetchAt: time.Now(),
		status:      "connecting",
		closeCh:     make(chan struct{}),
		wsDone:      make(chan struct{}),
		fetchChain:  fetchChain,
		apiKey:      apiKey,
		wsBase:      toWSBase(baseURL),
		initBackoff: time.Second,
		maxBackoff:  60 * time.Second,
		dialWS:      defaultDialWS,
	}
}

func defaultDialWS(url string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), url, nil)
	return conn, err
}

// Get returns the resolved value for key, or defaultVal (or nil) if absent.
func (rt *ConfigRuntime) Get(key string, defaultVal ...interface{}) interface{} {
	rt.mu.RLock()
	v, ok := rt.cache[key]
	rt.mu.RUnlock()
	if ok {
		return v
	}
	if len(defaultVal) > 0 {
		return defaultVal[0]
	}
	return nil
}

// GetString returns the resolved string value for key, or defaultVal if absent or wrong type.
func (rt *ConfigRuntime) GetString(key string, defaultVal ...string) string {
	v := rt.Get(key)
	if s, ok := v.(string); ok {
		return s
	}
	if len(defaultVal) > 0 {
		return defaultVal[0]
	}
	return ""
}

// GetInt returns the resolved int value for key, or defaultVal if absent or wrong type.
// JSON numbers are float64; this converts float64 to int automatically.
func (rt *ConfigRuntime) GetInt(key string, defaultVal ...int) int {
	v := rt.Get(key)
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	}
	if len(defaultVal) > 0 {
		return defaultVal[0]
	}
	return 0
}

// GetBool returns the resolved bool value for key, or defaultVal if absent or wrong type.
func (rt *ConfigRuntime) GetBool(key string, defaultVal ...bool) bool {
	v := rt.Get(key)
	if b, ok := v.(bool); ok {
		return b
	}
	if len(defaultVal) > 0 {
		return defaultVal[0]
	}
	return false
}

// Exists returns true if key is present in the resolved cache.
func (rt *ConfigRuntime) Exists(key string) bool {
	rt.mu.RLock()
	_, ok := rt.cache[key]
	rt.mu.RUnlock()
	return ok
}

// GetAll returns a shallow copy of the entire resolved cache.
func (rt *ConfigRuntime) GetAll() map[string]interface{} {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	result := make(map[string]interface{}, len(rt.cache))
	for k, v := range rt.cache {
		result[k] = v
	}
	return result
}

// Stats returns a snapshot of runtime diagnostics.
func (rt *ConfigRuntime) Stats() ConfigStats {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return ConfigStats{
		FetchCount:  rt.fetchCount,
		LastFetchAt: rt.lastFetchAt,
	}
}

// ConnectionStatus returns "connecting", "connected", or "disconnected".
func (rt *ConfigRuntime) ConnectionStatus() string {
	rt.statusMu.RLock()
	defer rt.statusMu.RUnlock()
	return rt.status
}

// OnChange registers a listener for value changes. If key is provided, the
// listener is called only when that specific key changes. Without key, it is
// called for every changed key.
func (rt *ConfigRuntime) OnChange(cb func(*ConfigChangeEvent), key ...string) {
	k := ""
	if len(key) > 0 {
		k = key[0]
	}
	rt.listenersMu.Lock()
	rt.listeners = append(rt.listeners, changeListener{key: k, cb: cb})
	rt.listenersMu.Unlock()
}

// Refresh re-fetches the full config chain, updates the cache synchronously,
// and fires OnChange listeners for any keys that changed.
func (rt *ConfigRuntime) Refresh() error {
	chain, err := rt.fetchChain()
	if err != nil {
		return err
	}
	newCache := resolveChain(chain, rt.environment)

	rt.mu.Lock()
	oldCache := rt.cache
	rt.cache = newCache
	rt.fetchCount++
	rt.lastFetchAt = time.Now()
	rt.mu.Unlock()

	rt.fireListeners(oldCache, newCache, "manual")
	return nil
}

// Close stops the WebSocket goroutine and marks the runtime as disconnected.
// Idempotent; safe to call multiple times.
func (rt *ConfigRuntime) Close() {
	rt.closeOnce.Do(func() {
		close(rt.closeCh)
	})
	<-rt.wsDone
	rt.setStatus("disconnected")
}

// --- Internal helpers ---

func (rt *ConfigRuntime) setStatus(s string) {
	rt.statusMu.Lock()
	rt.status = s
	rt.statusMu.Unlock()
}

func (rt *ConfigRuntime) fireListeners(old, newCache map[string]interface{}, source string) {
	rt.listenersMu.Lock()
	listeners := make([]changeListener, len(rt.listeners))
	copy(listeners, rt.listeners)
	rt.listenersMu.Unlock()

	if len(listeners) == 0 {
		return
	}

	// Collect changed keys.
	changed := make(map[string]struct{})
	for k, v := range newCache {
		if !reflect.DeepEqual(old[k], v) {
			changed[k] = struct{}{}
		}
	}
	for k := range old {
		if _, ok := newCache[k]; !ok {
			changed[k] = struct{}{}
		}
	}

	for k := range changed {
		evt := &ConfigChangeEvent{
			Key:      k,
			OldValue: old[k],
			NewValue: newCache[k],
			Source:   source,
		}
		for _, l := range listeners {
			if l.key == "" || l.key == k {
				l.cb(evt)
			}
		}
	}
}

// wsLoop runs the WebSocket connection lifecycle in a goroutine.
// It reconnects with exponential backoff until rt.closeCh is closed.
func (rt *ConfigRuntime) wsLoop() {
	defer func() {
		rt.setStatus("disconnected")
		close(rt.wsDone)
	}()

	backoff := rt.initBackoff
	if backoff == 0 {
		backoff = time.Second
	}

	for {
		select {
		case <-rt.closeCh:
			return
		default:
		}

		closed, _ := rt.wsConnect()
		if closed {
			return
		}

		// Connection failed or dropped — back off then retry.
		select {
		case <-rt.closeCh:
			return
		case <-time.After(backoff):
		}
		maxBo := rt.maxBackoff
		if maxBo == 0 {
			maxBo = 60 * time.Second
		}
		if backoff < maxBo {
			backoff *= 2
			if backoff > maxBo {
				backoff = maxBo
			}
		}
	}
}

// wsConnect dials, subscribes, and reads messages until an error or close.
// Returns (closed=true, nil) when rt.closeCh fires; (false, err) on error.
func (rt *ConfigRuntime) wsConnect() (closed bool, err error) {
	wsURL := rt.wsBase + "/api/ws/v1/configs?" + url.Values{"api_key": {rt.apiKey}}.Encode()

	dial := rt.dialWS
	if dial == nil {
		dial = defaultDialWS
	}
	conn, dialErr := dial(wsURL)
	if dialErr != nil {
		select {
		case <-rt.closeCh:
			return true, nil
		default:
		}
		return false, dialErr
	}

	// Close the WebSocket when we exit this function.
	defer conn.Close() //nolint:errcheck

	// Subscribe to this config.
	sub := map[string]interface{}{
		"type":        "subscribe",
		"config_id":   rt.configID,
		"environment": rt.environment,
	}
	if writeErr := conn.WriteJSON(sub); writeErr != nil {
		return false, writeErr
	}

	rt.setStatus("connected")

	// Close the WebSocket connection when rt.closeCh fires, so blocking ReadJSON unblocks.
	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-rt.closeCh:
			conn.Close() //nolint:errcheck
		case <-stopWatcher:
		}
	}()

	for {
		var msg map[string]interface{}
		if readErr := conn.ReadJSON(&msg); readErr != nil {
			select {
			case <-rt.closeCh:
				return true, nil
			default:
			}
			return false, readErr
		}

		msgType, _ := msg["type"].(string)
		switch msgType {
		case "config_changed":
			rt.handleWSUpdate()
		case "config_deleted":
			rt.setStatus("disconnected")
			return true, nil
		}
	}
}

// handleWSUpdate re-fetches the chain, updates the cache, and fires listeners.
func (rt *ConfigRuntime) handleWSUpdate() {
	chain, err := rt.fetchChain()
	if err != nil {
		return
	}
	newCache := resolveChain(chain, rt.environment)

	rt.mu.Lock()
	oldCache := rt.cache
	rt.cache = newCache
	rt.fetchCount++
	rt.lastFetchAt = time.Now()
	rt.mu.Unlock()

	rt.fireListeners(oldCache, newCache, "websocket")
}

// --- Resolver ---

// deepMerge recursively merges override onto base.
// Both-dict keys are merged recursively; other types use the override value.
func deepMerge(base, override map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(base)+len(override))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range override {
		if existing, ok := result[k]; ok {
			if existingMap, ok1 := existing.(map[string]interface{}); ok1 {
				if overrideMap, ok2 := v.(map[string]interface{}); ok2 {
					result[k] = deepMerge(existingMap, overrideMap)
					continue
				}
			}
		}
		result[k] = v
	}
	return result
}

// resolveChain computes the final resolved cache from a parent chain.
// chain is ordered child→root; we walk root→child for inheritance.
// For each node: merge base values with environment-specific values, then
// merge onto the accumulated result (child wins over parent).
func resolveChain(chain []chainEntry, environment string) map[string]interface{} {
	result := make(map[string]interface{})
	for i := len(chain) - 1; i >= 0; i-- {
		entry := chain[i]

		// Start with base values for this node.
		nodeVals := make(map[string]interface{}, len(entry.Values))
		for k, v := range entry.Values {
			nodeVals[k] = v
		}

		// Overlay environment-specific values if present.
		if environment != "" {
			if envEntry, ok := entry.Environments[environment]; ok {
				if vals, ok := envEntry["values"]; ok {
					if valsMap, ok := vals.(map[string]interface{}); ok {
						nodeVals = deepMerge(nodeVals, valsMap)
					}
				}
			}
		}

		// Merge this node's resolved values onto the accumulated result.
		result = deepMerge(result, nodeVals)
	}
	return result
}

// toWSBase converts an HTTP base URL to a WebSocket base URL.
func toWSBase(baseURL string) string {
	s := strings.Replace(baseURL, "https://", "wss://", 1)
	s = strings.Replace(s, "http://", "ws://", 1)
	return strings.TrimRight(s, "/")
}
