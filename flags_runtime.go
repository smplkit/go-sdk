package smplkit

import (
	"container/list"
	"context"
	"fmt"
	"log"
	"sync"

	jsonlogic "github.com/diegoholiveira/jsonlogic/v3"
)

// FlagChangeEvent describes a flag definition change.
type FlagChangeEvent struct {
	// Key is the flag key that changed.
	Key string
	// Source is "websocket" or "manual".
	Source string
}

// FlagStats holds cache statistics for the flags runtime.
type FlagStats struct {
	// CacheHits is the number of evaluation cache hits.
	CacheHits int
	// CacheMisses is the number of evaluation cache misses.
	CacheMisses int
}

// --- Resolution Cache (LRU, thread-safe) ---

const defaultCacheMaxSize = 10000

type resolutionCache struct {
	mu      sync.Mutex
	maxSize int
	items   map[string]*list.Element
	order   *list.List
	hits    int
	misses  int
}

type cacheEntry struct {
	key   string
	value interface{}
}

func newResolutionCache(maxSize int) *resolutionCache {
	if maxSize <= 0 {
		maxSize = defaultCacheMaxSize
	}
	return &resolutionCache{
		maxSize: maxSize,
		items:   make(map[string]*list.Element),
		order:   list.New(),
	}
}

func (c *resolutionCache) get(key string) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		c.hits++
		return elem.Value.(*cacheEntry).value, true
	}
	c.misses++
	return nil, false
}

func (c *resolutionCache) put(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		elem.Value.(*cacheEntry).value = value
		return
	}
	entry := &cacheEntry{key: key, value: value}
	elem := c.order.PushFront(entry)
	c.items[key] = elem
	if c.order.Len() > c.maxSize {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}
}

func (c *resolutionCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.order.Init()
}

func (c *resolutionCache) stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

// --- Context Registration Buffer ---

const (
	contextRegistrationLRUSize = 10000
	contextBatchFlushSize      = 100
)

type contextRegistrationBuffer struct {
	mu      sync.Mutex
	seen    map[string]struct{} // key = "type:key"
	pending []map[string]interface{}
}

func newContextRegistrationBuffer() *contextRegistrationBuffer {
	return &contextRegistrationBuffer{
		seen: make(map[string]struct{}),
	}
}

func (b *contextRegistrationBuffer) observe(contexts []Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ctx := range contexts {
		cacheKey := ctx.Type + ":" + ctx.Key
		if _, ok := b.seen[cacheKey]; ok {
			continue
		}
		if len(b.seen) >= contextRegistrationLRUSize {
			// Simple eviction: clear everything (unlike Python's ordered dict LRU,
			// we keep it simple for Go).
			b.seen = make(map[string]struct{})
		}
		b.seen[cacheKey] = struct{}{}
		name := ctx.Name
		if name == "" {
			name = ctx.Key
		}
		item := map[string]interface{}{
			"id":         fmt.Sprintf("%s:%s", ctx.Type, ctx.Key),
			"name":       name,
			"attributes": copyMap(ctx.Attributes),
		}
		b.pending = append(b.pending, item)
	}
}

func (b *contextRegistrationBuffer) drain() []map[string]interface{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.pending
	b.pending = nil
	return batch
}

func (b *contextRegistrationBuffer) pendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// --- Typed Flag Handles ---

// BoolFlag returns a typed handle for a boolean flag.
func (rt *FlagsRuntime) BoolFlag(key string, defaultValue bool) *BoolFlagHandle {
	h := &BoolFlagHandle{flagHandle: flagHandle{runtime: rt, key: key, defaultVal: defaultValue}}
	rt.handlesMu.Lock()
	rt.handles[key] = &h.flagHandle
	rt.handlesMu.Unlock()
	return h
}

// StringFlag returns a typed handle for a string flag.
func (rt *FlagsRuntime) StringFlag(key string, defaultValue string) *StringFlagHandle {
	h := &StringFlagHandle{flagHandle: flagHandle{runtime: rt, key: key, defaultVal: defaultValue}}
	rt.handlesMu.Lock()
	rt.handles[key] = &h.flagHandle
	rt.handlesMu.Unlock()
	return h
}

// NumberFlag returns a typed handle for a numeric flag.
func (rt *FlagsRuntime) NumberFlag(key string, defaultValue float64) *NumberFlagHandle {
	h := &NumberFlagHandle{flagHandle: flagHandle{runtime: rt, key: key, defaultVal: defaultValue}}
	rt.handlesMu.Lock()
	rt.handles[key] = &h.flagHandle
	rt.handlesMu.Unlock()
	return h
}

// JsonFlag returns a typed handle for a JSON flag.
func (rt *FlagsRuntime) JsonFlag(key string, defaultValue map[string]interface{}) *JsonFlagHandle {
	h := &JsonFlagHandle{flagHandle: flagHandle{runtime: rt, key: key, defaultVal: defaultValue}}
	rt.handlesMu.Lock()
	rt.handles[key] = &h.flagHandle
	rt.handlesMu.Unlock()
	return h
}

type flagHandle struct {
	runtime    *FlagsRuntime
	key        string
	defaultVal interface{}

	listenersMu sync.Mutex
	listeners   []func(*FlagChangeEvent)
}

// OnChange registers a flag-specific change listener.
func (h *flagHandle) OnChange(cb func(*FlagChangeEvent)) {
	h.listenersMu.Lock()
	h.listeners = append(h.listeners, cb)
	h.listenersMu.Unlock()
}

// BoolFlagHandle is a typed handle for a boolean flag.
type BoolFlagHandle struct {
	flagHandle
}

// Get evaluates the flag and returns a typed boolean value. Pass nil for ctx
// to use context.Background().
func (h *BoolFlagHandle) Get(ctx context.Context, contexts ...Context) bool {
	value := h.runtime.evaluateHandle(ctx, h.key, h.defaultVal, contexts)
	if b, ok := value.(bool); ok {
		return b
	}
	return h.defaultVal.(bool)
}

// GetWithContext evaluates the flag with explicit context override.
func (h *BoolFlagHandle) GetWithContext(ctx context.Context, contexts []Context) bool {
	value := h.runtime.evaluateHandle(ctx, h.key, h.defaultVal, contexts)
	if b, ok := value.(bool); ok {
		return b
	}
	return h.defaultVal.(bool)
}

// StringFlagHandle is a typed handle for a string flag.
type StringFlagHandle struct {
	flagHandle
}

// Get evaluates the flag and returns a typed string value.
func (h *StringFlagHandle) Get(ctx context.Context, contexts ...Context) string {
	value := h.runtime.evaluateHandle(ctx, h.key, h.defaultVal, contexts)
	if s, ok := value.(string); ok {
		return s
	}
	return h.defaultVal.(string)
}

// GetWithContext evaluates the flag with explicit context override.
func (h *StringFlagHandle) GetWithContext(ctx context.Context, contexts []Context) string {
	value := h.runtime.evaluateHandle(ctx, h.key, h.defaultVal, contexts)
	if s, ok := value.(string); ok {
		return s
	}
	return h.defaultVal.(string)
}

// NumberFlagHandle is a typed handle for a numeric flag.
type NumberFlagHandle struct {
	flagHandle
}

// Get evaluates the flag and returns a typed float64 value.
func (h *NumberFlagHandle) Get(ctx context.Context, contexts ...Context) float64 {
	value := h.runtime.evaluateHandle(ctx, h.key, h.defaultVal, contexts)
	switch n := value.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return h.defaultVal.(float64)
}

// GetWithContext evaluates the flag with explicit context override.
func (h *NumberFlagHandle) GetWithContext(ctx context.Context, contexts []Context) float64 {
	value := h.runtime.evaluateHandle(ctx, h.key, h.defaultVal, contexts)
	switch n := value.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return h.defaultVal.(float64)
}

// JsonFlagHandle is a typed handle for a JSON flag.
type JsonFlagHandle struct {
	flagHandle
}

// Get evaluates the flag and returns a typed map value.
func (h *JsonFlagHandle) Get(ctx context.Context, contexts ...Context) map[string]interface{} {
	value := h.runtime.evaluateHandle(ctx, h.key, h.defaultVal, contexts)
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return h.defaultVal.(map[string]interface{})
}

// GetWithContext evaluates the flag with explicit context override.
func (h *JsonFlagHandle) GetWithContext(ctx context.Context, contexts []Context) map[string]interface{} {
	value := h.runtime.evaluateHandle(ctx, h.key, h.defaultVal, contexts)
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return h.defaultVal.(map[string]interface{})
}

// --- FlagsRuntime ---

// FlagsRuntime holds the prescriptive runtime state for the flags namespace.
// It is created internally; access it via FlagsClient methods like BoolFlag,
// Connect, etc.
type FlagsRuntime struct {
	flagsClient *FlagsClient

	mu          sync.RWMutex
	environment string
	flagStore   map[string]map[string]interface{}
	connected   bool

	cache         *resolutionCache
	contextBuffer *contextRegistrationBuffer

	providerMu      sync.RWMutex
	contextProvider func(ctx context.Context) []Context

	handlesMu sync.RWMutex
	handles   map[string]*flagHandle

	listenersMu     sync.Mutex
	globalListeners []func(*FlagChangeEvent)

	wsManager *sharedWebSocket
}

func newFlagsRuntime(fc *FlagsClient) *FlagsRuntime {
	return &FlagsRuntime{
		flagsClient:   fc,
		flagStore:     make(map[string]map[string]interface{}),
		cache:         newResolutionCache(defaultCacheMaxSize),
		contextBuffer: newContextRegistrationBuffer(),
		handles:       make(map[string]*flagHandle),
	}
}

// SetContextProvider registers a function that provides evaluation contexts.
// The provider receives the Go context.Context and returns a slice of Contexts.
func (rt *FlagsRuntime) SetContextProvider(fn func(ctx context.Context) []Context) {
	rt.providerMu.Lock()
	rt.contextProvider = fn
	rt.providerMu.Unlock()
}

// Connect fetches flag definitions and registers on the shared WebSocket.
func (rt *FlagsRuntime) Connect(ctx context.Context, environment string) error {
	rt.mu.Lock()
	rt.environment = environment
	rt.mu.Unlock()

	store, err := rt.flagsClient.fetchAllFlags(ctx)
	if err != nil {
		return err
	}

	rt.mu.Lock()
	rt.flagStore = store
	rt.connected = true
	rt.mu.Unlock()

	rt.cache.clear()

	// Register on the shared WebSocket.
	ws := rt.flagsClient.client.ensureWS()
	rt.wsManager = ws
	ws.on("flag_changed", rt.handleFlagChanged)
	ws.on("flag_deleted", rt.handleFlagDeleted)

	return nil
}

// Disconnect unregisters from WebSocket, flushes contexts, and clears state.
func (rt *FlagsRuntime) Disconnect(ctx context.Context) {
	if rt.wsManager != nil {
		rt.wsManager.off("flag_changed", rt.handleFlagChanged)
		rt.wsManager.off("flag_deleted", rt.handleFlagDeleted)
		rt.wsManager = nil
	}

	batch := rt.contextBuffer.drain()
	rt.flagsClient.flushContexts(ctx, batch)

	rt.mu.Lock()
	rt.flagStore = make(map[string]map[string]interface{})
	rt.connected = false
	rt.environment = ""
	rt.mu.Unlock()

	rt.cache.clear()
}

// Refresh re-fetches all flag definitions and clears cache.
func (rt *FlagsRuntime) Refresh(ctx context.Context) error {
	store, err := rt.flagsClient.fetchAllFlags(ctx)
	if err != nil {
		return err
	}

	rt.mu.Lock()
	rt.flagStore = store
	rt.mu.Unlock()

	rt.cache.clear()
	rt.fireChangeListenersAll("manual")
	return nil
}

// ConnectionStatus returns the current WebSocket connection status.
func (rt *FlagsRuntime) ConnectionStatus() string {
	if rt.wsManager != nil {
		return rt.wsManager.connectionStatus()
	}
	return "disconnected"
}

// Stats returns cache statistics.
func (rt *FlagsRuntime) Stats() FlagStats {
	hits, misses := rt.cache.stats()
	return FlagStats{CacheHits: hits, CacheMisses: misses}
}

// OnChange registers a global change listener.
func (rt *FlagsRuntime) OnChange(cb func(*FlagChangeEvent)) {
	rt.listenersMu.Lock()
	rt.globalListeners = append(rt.globalListeners, cb)
	rt.listenersMu.Unlock()
}

// Register explicitly registers context(s) for background batch registration.
func (rt *FlagsRuntime) Register(ctx context.Context, contexts ...Context) {
	rt.contextBuffer.observe(contexts)
}

// FlushContexts flushes pending context registrations to the server.
func (rt *FlagsRuntime) FlushContexts(ctx context.Context) {
	batch := rt.contextBuffer.drain()
	rt.flagsClient.flushContexts(ctx, batch)
}

// Evaluate performs Tier 1 explicit evaluation — stateless, no provider or cache.
func (rt *FlagsRuntime) Evaluate(ctx context.Context, key string, environment string, contexts []Context) interface{} {
	evalDict := contextsToEvalDict(contexts)

	// Auto-inject service context if set and not already provided.
	if rt.flagsClient != nil && rt.flagsClient.client != nil && rt.flagsClient.client.service != "" {
		if _, has := evalDict["service"]; !has {
			evalDict["service"] = map[string]interface{}{"key": rt.flagsClient.client.service}
		}
	}

	rt.mu.RLock()
	connected := rt.connected
	flagDef, ok := rt.flagStore[key]
	rt.mu.RUnlock()

	if connected && ok {
		return evaluateFlag(flagDef, environment, evalDict)
	}

	// Not connected or flag not in store — fetch.
	flags, err := rt.flagsClient.fetchFlagsList(ctx)
	if err != nil {
		return nil
	}
	for _, f := range flags {
		if fKey, _ := f["key"].(string); fKey == key {
			return evaluateFlag(f, environment, evalDict)
		}
	}
	return nil
}

// --- Internal evaluation ---

func (rt *FlagsRuntime) evaluateHandle(ctx context.Context, key string, defaultVal interface{}, explicitContexts []Context) interface{} {
	rt.mu.RLock()
	connected := rt.connected
	environment := rt.environment
	rt.mu.RUnlock()

	if !connected {
		panic(ErrNotConnected)
	}

	var evalDict map[string]interface{}
	if len(explicitContexts) > 0 {
		evalDict = contextsToEvalDict(explicitContexts)
	} else {
		rt.providerMu.RLock()
		provider := rt.contextProvider
		rt.providerMu.RUnlock()

		if provider != nil {
			contexts := provider(ctx)
			evalDict = contextsToEvalDict(contexts)
			rt.contextBuffer.observe(contexts)
			if rt.contextBuffer.pendingCount() >= contextBatchFlushSize {
				go rt.flagsClient.flushContexts(context.Background(), rt.contextBuffer.drain())
			}
		} else {
			evalDict = map[string]interface{}{}
		}
	}

	// Auto-inject service context if set and not already provided.
	if rt.flagsClient != nil && rt.flagsClient.client != nil && rt.flagsClient.client.service != "" {
		if _, has := evalDict["service"]; !has {
			evalDict["service"] = map[string]interface{}{"key": rt.flagsClient.client.service}
		}
	}

	ctxHash := hashContext(evalDict)
	cacheKey := fmt.Sprintf("%s:%s", key, ctxHash)

	if cached, hit := rt.cache.get(cacheKey); hit {
		return cached
	}

	rt.mu.RLock()
	flagDef, ok := rt.flagStore[key]
	rt.mu.RUnlock()

	if !ok {
		rt.cache.put(cacheKey, defaultVal)
		return defaultVal
	}

	value := evaluateFlag(flagDef, environment, evalDict)
	if value == nil {
		value = defaultVal
	}

	rt.cache.put(cacheKey, value)
	return value
}

// --- JSON Logic evaluation (ADR-022 §2.6) ---

// evaluateFlag evaluates a flag definition against the given context.
// Returns nil if no match or no environment.
func evaluateFlag(flagDef map[string]interface{}, environment string, evalDict map[string]interface{}) interface{} {
	flagDefault := flagDef["default"]
	environments, _ := flagDef["environments"].(map[string]interface{})

	if environment == "" || environments == nil {
		return flagDefault
	}

	envDataRaw, ok := environments[environment]
	if !ok {
		return flagDefault
	}
	envConfig, ok := envDataRaw.(map[string]interface{})
	if !ok {
		return flagDefault
	}

	envDefault := envConfig["default"]
	fallback := envDefault
	if fallback == nil {
		fallback = flagDefault
	}

	enabled, _ := envConfig["enabled"].(bool)
	if !enabled {
		return fallback
	}

	rulesRaw, _ := envConfig["rules"].([]interface{})
	for _, rRaw := range rulesRaw {
		rule, ok := rRaw.(map[string]interface{})
		if !ok {
			continue
		}
		logic, ok := rule["logic"].(map[string]interface{})
		if !ok || len(logic) == 0 {
			continue
		}

		result, err := applyJSONLogic(logic, evalDict)
		if err != nil {
			log.Printf("smplkit: JSON Logic evaluation error: %v", err)
			continue
		}

		if isTruthy(result) {
			return rule["value"]
		}
	}

	return fallback
}

// applyJSONLogic evaluates a JSON Logic expression against data.
func applyJSONLogic(logic map[string]interface{}, data map[string]interface{}) (interface{}, error) {
	return jsonlogic.ApplyInterface(logic, data)
}

// isTruthy checks if a JSON Logic result is truthy.
func isTruthy(v interface{}) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case float64:
		return val != 0
	case int:
		return val != 0
	case string:
		return val != ""
	}
	return true
}

// --- Event handlers ---

func (rt *FlagsRuntime) handleFlagChanged(data map[string]interface{}) {
	flagKey, _ := data["key"].(string)
	store, err := rt.flagsClient.fetchAllFlags(context.Background())
	if err != nil {
		return
	}

	rt.mu.Lock()
	rt.flagStore = store
	rt.mu.Unlock()

	rt.cache.clear()
	rt.fireChangeListeners(flagKey, "websocket")
}

func (rt *FlagsRuntime) handleFlagDeleted(data map[string]interface{}) {
	rt.handleFlagChanged(data)
}

func (rt *FlagsRuntime) fireChangeListeners(flagKey string, source string) {
	if flagKey == "" {
		return
	}
	event := &FlagChangeEvent{Key: flagKey, Source: source}

	rt.listenersMu.Lock()
	globals := make([]func(*FlagChangeEvent), len(rt.globalListeners))
	copy(globals, rt.globalListeners)
	rt.listenersMu.Unlock()

	for _, cb := range globals {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("smplkit: exception in global flags on_change listener: %v", r)
				}
			}()
			cb(event)
		}()
	}

	rt.handlesMu.RLock()
	handle, ok := rt.handles[flagKey]
	rt.handlesMu.RUnlock()

	if ok {
		handle.listenersMu.Lock()
		listeners := make([]func(*FlagChangeEvent), len(handle.listeners))
		copy(listeners, handle.listeners)
		handle.listenersMu.Unlock()

		for _, cb := range listeners {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("smplkit: exception in flag-specific on_change listener: %v", r)
					}
				}()
				cb(event)
			}()
		}
	}
}

func (rt *FlagsRuntime) fireChangeListenersAll(source string) {
	rt.mu.RLock()
	keys := make([]string, 0, len(rt.flagStore))
	for k := range rt.flagStore {
		keys = append(keys, k)
	}
	rt.mu.RUnlock()

	for _, key := range keys {
		rt.fireChangeListeners(key, source)
	}
}
