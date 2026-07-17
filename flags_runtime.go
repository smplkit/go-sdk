package smplkit

import (
	"container/list"
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	jsonlogic "github.com/diegoholiveira/jsonlogic/v3"

	"github.com/smplkit/go-sdk/v3/internal/debug"
	genflags "github.com/smplkit/go-sdk/v3/internal/generated/flags"
)

// FlagChangeEvent describes a flag definition change.
type FlagChangeEvent struct {
	// ID is the flag ID that changed.
	ID string
	// Source is "websocket" or "manual".
	Source string
	// Deleted is true when the flag was deleted server-side.
	Deleted bool
}

// FlagStats holds runtime statistics for the flags subsystem.
type FlagStats struct {
	// CacheHits is the number of evaluations served from cache.
	CacheHits int
	// CacheMisses is the number of evaluations that required computation.
	CacheMisses int
}

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
		item := map[string]interface{}{
			"type":       ctx.Type,
			"key":        ctx.Key,
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

const flagRegistrationThreshold = 50

type flagRegistrationEntry struct {
	id          string
	flagType    string
	defaultVal  interface{}
	service     string
	environment string
}

type flagRegistrationBuffer struct {
	mu      sync.Mutex
	seen    map[string]struct{}
	pending []flagRegistrationEntry
}

func newFlagRegistrationBuffer() *flagRegistrationBuffer {
	return &flagRegistrationBuffer{seen: make(map[string]struct{})}
}

func (b *flagRegistrationBuffer) add(id, flagType string, defaultVal interface{}, service, environment string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.seen[id]; ok {
		return
	}
	b.seen[id] = struct{}{}
	b.pending = append(b.pending, flagRegistrationEntry{
		id:          id,
		flagType:    flagType,
		defaultVal:  defaultVal,
		service:     service,
		environment: environment,
	})
}

func (b *flagRegistrationBuffer) drain() []flagRegistrationEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.pending
	b.pending = nil
	return batch
}

// peek returns a snapshot of pending entries without removing them.
func (b *flagRegistrationBuffer) peek() []flagRegistrationEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		return nil
	}
	result := make([]flagRegistrationEntry, len(b.pending))
	copy(result, b.pending)
	return result
}

// commit removes the entries with the given IDs from the buffer.
// Call this after a successful bulk-register POST.
func (b *flagRegistrationBuffer) commit(ids []string) {
	if len(ids) == 0 {
		return
	}
	toRemove := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		toRemove[id] = struct{}{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.pending[:0]
	for _, entry := range b.pending {
		if _, remove := toRemove[entry.id]; !remove {
			remaining = append(remaining, entry)
		}
	}
	b.pending = remaining
}

func (b *flagRegistrationBuffer) pendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// BooleanFlag returns a typed handle for a boolean flag.
func (rt *FlagsRuntime) BooleanFlag(key string, defaultValue bool) *BooleanFlagHandle {
	h := &BooleanFlagHandle{flagHandle: flagHandle{runtime: rt, key: key, defaultVal: defaultValue}}
	rt.handlesMu.Lock()
	rt.handles[key] = &h.flagHandle
	rt.handlesMu.Unlock()
	if rt.flagsClient != nil {
		rt.flagBuffer.add(key, "BOOLEAN", defaultValue, rt.flagsClient.service, rt.flagsClient.environment)
		rt.maybeThresholdFlush(context.Background())
	}
	return h
}

// StringFlag returns a typed handle for a string flag.
func (rt *FlagsRuntime) StringFlag(key string, defaultValue string) *StringFlagHandle {
	h := &StringFlagHandle{flagHandle: flagHandle{runtime: rt, key: key, defaultVal: defaultValue}}
	rt.handlesMu.Lock()
	rt.handles[key] = &h.flagHandle
	rt.handlesMu.Unlock()
	if rt.flagsClient != nil {
		rt.flagBuffer.add(key, "STRING", defaultValue, rt.flagsClient.service, rt.flagsClient.environment)
		rt.maybeThresholdFlush(context.Background())
	}
	return h
}

// NumberFlag returns a typed handle for a numeric flag.
func (rt *FlagsRuntime) NumberFlag(key string, defaultValue float64) *NumberFlagHandle {
	h := &NumberFlagHandle{flagHandle: flagHandle{runtime: rt, key: key, defaultVal: defaultValue}}
	rt.handlesMu.Lock()
	rt.handles[key] = &h.flagHandle
	rt.handlesMu.Unlock()
	if rt.flagsClient != nil {
		rt.flagBuffer.add(key, "NUMERIC", defaultValue, rt.flagsClient.service, rt.flagsClient.environment)
		rt.maybeThresholdFlush(context.Background())
	}
	return h
}

// JsonFlag returns a typed handle for a JSON flag.
func (rt *FlagsRuntime) JsonFlag(key string, defaultValue map[string]interface{}) *JsonFlagHandle {
	h := &JsonFlagHandle{flagHandle: flagHandle{runtime: rt, key: key, defaultVal: defaultValue}}
	rt.handlesMu.Lock()
	rt.handles[key] = &h.flagHandle
	rt.handlesMu.Unlock()
	if rt.flagsClient != nil {
		rt.flagBuffer.add(key, "JSON", defaultValue, rt.flagsClient.service, rt.flagsClient.environment)
		rt.maybeThresholdFlush(context.Background())
	}
	return h
}

// maybeThresholdFlush flushes the flag-registration buffer once it crosses
// the batch threshold. Normally the flush runs on a background goroutine so
// registration never blocks the caller; with Config.DisableStreaming set it
// runs inline instead — stateless mode spawns no goroutines.
func (rt *FlagsRuntime) maybeThresholdFlush(ctx context.Context) {
	if rt.flagBuffer.pendingCount() < flagRegistrationThreshold {
		return
	}
	if rt.flagsClient != nil && rt.flagsClient.disableStreaming {
		rt.flushFlagBuffer(ctx)
		return
	}
	go rt.flushFlagBuffer(context.Background())
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

// BooleanFlagHandle is a typed handle for a boolean flag.
type BooleanFlagHandle struct {
	flagHandle
}

// Get evaluates the flag and returns a typed boolean value.
//
// The variadic contexts are optional Context entities to evaluate
// targeting rules against; when omitted, the ambient request context (if
// any) is used. Returns the evaluated boolean value, or this flag's
// default when no environment override or rule applies, or when the
// evaluated value is not a bool.
func (h *BooleanFlagHandle) Get(ctx context.Context, contexts ...Context) bool {
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
//
// The variadic contexts are optional Context entities to evaluate
// targeting rules against; when omitted, the ambient request context (if
// any) is used. Returns the evaluated string value, or this flag's
// default when no environment override or rule applies, or when the
// evaluated value is not a string.
func (h *StringFlagHandle) Get(ctx context.Context, contexts ...Context) string {
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
//
// The variadic contexts are optional Context entities to evaluate
// targeting rules against; when omitted, the ambient request context (if
// any) is used. Returns the evaluated numeric value, or this flag's
// default when no environment override or rule applies, or when the
// evaluated value is not a numeric type.
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

// JsonFlagHandle is a typed handle for a JSON flag.
type JsonFlagHandle struct {
	flagHandle
}

// Get evaluates the flag and returns a typed map value.
//
// The variadic contexts are optional Context entities to evaluate
// targeting rules against; when omitted, the ambient request context (if
// any) is used. Returns the evaluated JSON object, or this flag's
// default when no environment override or rule applies, or when the
// evaluated value is not a map.
func (h *JsonFlagHandle) Get(ctx context.Context, contexts ...Context) map[string]interface{} {
	value := h.runtime.evaluateHandle(ctx, h.key, h.defaultVal, contexts)
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return h.defaultVal.(map[string]interface{})
}

// FlagsRuntime holds the runtime state for the flags subsystem.
// Access it via FlagsClient methods like BooleanFlag, Disconnect, etc.
type FlagsRuntime struct {
	flagsClient *FlagsClient

	mu          sync.RWMutex
	environment string
	flagStore   map[string]map[string]interface{}

	// connectMu guards connected, retryDelay, and nextRetryAt.
	connectMu   sync.Mutex
	connected   bool
	retryDelay  time.Duration
	nextRetryAt time.Time
	// wsOnce and periodicOnce ensure WS handlers and the flush goroutine are
	// each registered exactly once, even across multiple retry attempts.
	wsOnce       sync.Once
	periodicOnce sync.Once

	cache         *resolutionCache
	contextBuffer *contextRegistrationBuffer
	flagBuffer    *flagRegistrationBuffer
	flagFlushDone chan struct{}

	providerMu      sync.RWMutex
	contextProvider func(ctx context.Context) []Context

	handlesMu sync.RWMutex
	handles   map[string]*flagHandle

	listenersMu     sync.Mutex
	globalListeners []func(*FlagChangeEvent)
	keyListeners    map[string][]func(*FlagChangeEvent)

	wsManager *sharedWebSocket
}

func newFlagsRuntime(fc *FlagsClient, sharedBuf *contextRegistrationBuffer) *FlagsRuntime {
	return &FlagsRuntime{
		flagsClient:   fc,
		flagStore:     make(map[string]map[string]interface{}),
		cache:         newResolutionCache(defaultCacheMaxSize),
		contextBuffer: sharedBuf,
		flagBuffer:    newFlagRegistrationBuffer(),
		handles:       make(map[string]*flagHandle),
		keyListeners:  make(map[string][]func(*FlagChangeEvent)),
	}
}

// SetContextProvider registers a function that provides evaluation contexts.
func (rt *FlagsRuntime) SetContextProvider(fn func(ctx context.Context) []Context) {
	rt.providerMu.Lock()
	rt.contextProvider = fn
	rt.providerMu.Unlock()
}

// ambientContexts returns the contexts stashed via the owning SmplClient's
// SetContext, or nil when this runtime has no parent client (standalone) or
// none are active. It is the fallback ambient source consulted during
// evaluation after an explicit context and a context provider.
func (rt *FlagsRuntime) ambientContexts() []Context {
	if rt.flagsClient != nil && rt.flagsClient.client != nil {
		return rt.flagsClient.client.getAmbientContexts()
	}
	return nil
}

// ensureInit initializes the runtime on first use, with backoff retry on failure.
// On success it sets connected=true. On failure it records a backoff window and
// returns an error so callers fall back to handle defaults.
func (rt *FlagsRuntime) ensureInit(ctx context.Context) error {
	rt.connectMu.Lock()
	defer rt.connectMu.Unlock()

	if rt.connected {
		return nil
	}

	if rt.flagsClient == nil {
		return &ConnectionError{Base: Error{Message: "flags client not initialized"}}
	}

	// Kick off deferred background machinery on the parent before the
	// first live touch (no-op when standalone).
	if rt.flagsClient.client != nil {
		rt.flagsClient.client.ensureStarted()
	}

	// Still inside the backoff window — don't hammer the server.
	if !rt.nextRetryAt.IsZero() && time.Now().Before(rt.nextRetryAt) {
		return &ConnectionError{Base: Error{Message: "flags client not yet connected, retrying"}}
	}

	rt.mu.Lock()
	rt.environment = rt.flagsClient.environment
	rt.mu.Unlock()

	// Register service context (wired path only; the parent owns the
	// environment/service context registration).
	if rt.flagsClient.client != nil {
		rt.flagsClient.client.registerServiceContext(ctx)
	}

	// Flush any flags registered before init. Non-destructive: items stay in
	// the buffer until the POST succeeds, so failures here don't lose declarations.
	rt.flushFlagBuffer(ctx)

	store, err := rt.flagsClient.fetchAllFlags(ctx)
	if err != nil {
		rt.advanceBackoff()
		return err
	}

	rt.mu.Lock()
	rt.flagStore = store
	rt.mu.Unlock()

	rt.cache.clear()

	// In stateless mode (Config.DisableStreaming) no WebSocket is opened and
	// no periodic flush goroutine starts — Refresh re-fetches on demand.
	if !rt.flagsClient.disableStreaming {
		// Register WebSocket handlers exactly once across all retry attempts.
		// A second successful start after a transient failure must not double-register.
		rt.wsOnce.Do(func() {
			ws := rt.flagsClient.ensureWS()
			rt.wsManager = ws
			ws.on("flag_changed", rt.handleFlagChanged)
			ws.on("flag_deleted", rt.handleFlagDeleted)
			ws.on("flags_changed", rt.handleFlagsChanged)
		})

		// Start the periodic flag-registration flush exactly once.
		rt.periodicOnce.Do(func() {
			rt.flagFlushDone = make(chan struct{})
			go rt.periodicFlagFlush(rt.flagFlushDone)
		})
	}

	rt.connected = true
	rt.retryDelay = 0
	rt.nextRetryAt = time.Time{}
	return nil
}

// advanceBackoff doubles the retry delay (1s → 60s cap) and records nextRetryAt.
// Must be called with connectMu held.
func (rt *FlagsRuntime) advanceBackoff() {
	if rt.retryDelay == 0 {
		rt.retryDelay = time.Second
	} else {
		rt.retryDelay *= 2
		if rt.retryDelay > 60*time.Second {
			rt.retryDelay = 60 * time.Second
		}
	}
	rt.nextRetryAt = time.Now().Add(rt.retryDelay)
}

// disconnect stops real-time updates and releases runtime resources.
func (rt *FlagsRuntime) disconnect(ctx context.Context) {
	if rt.flagFlushDone != nil {
		close(rt.flagFlushDone)
		rt.flagFlushDone = nil
	}

	if rt.wsManager != nil {
		rt.wsManager.off("flag_changed", rt.handleFlagChanged)
		rt.wsManager.off("flag_deleted", rt.handleFlagDeleted)
		rt.wsManager.off("flags_changed", rt.handleFlagsChanged)
		rt.wsManager = nil
	}

	batch := rt.contextBuffer.drain()
	rt.flagsClient.flushContexts(ctx, batch)

	rt.mu.Lock()
	rt.flagStore = make(map[string]map[string]interface{})
	rt.environment = ""
	rt.mu.Unlock()

	rt.cache.clear()

	// Reset connection state so re-initialization is possible after disconnect.
	rt.connectMu.Lock()
	rt.connected = false
	rt.retryDelay = 0
	rt.nextRetryAt = time.Time{}
	rt.wsOnce = sync.Once{}
	rt.periodicOnce = sync.Once{}
	rt.connectMu.Unlock()
}

// Refresh fetches the latest flag definitions from the server.
// Change listeners fire after the refresh completes.
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

// ConnectionStatus returns the current real-time connection status.
func (rt *FlagsRuntime) ConnectionStatus() string {
	if rt.wsManager != nil {
		return rt.wsManager.connectionStatus()
	}
	return "disconnected"
}

// Stats returns runtime statistics.
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

// OnChangeKey registers a key-scoped change listener that fires only when the
// specified flag key changes.
func (rt *FlagsRuntime) OnChangeKey(key string, cb func(*FlagChangeEvent)) {
	rt.listenersMu.Lock()
	rt.keyListeners[key] = append(rt.keyListeners[key], cb)
	rt.listenersMu.Unlock()
}

// Register explicitly registers context(s) with the server.
func (rt *FlagsRuntime) Register(ctx context.Context, contexts ...Context) {
	rt.contextBuffer.observe(contexts)
}

// FlushContexts sends any pending context registrations to the server immediately.
func (rt *FlagsRuntime) FlushContexts(ctx context.Context) {
	batch := rt.contextBuffer.drain()
	rt.flagsClient.flushContexts(ctx, batch)
}

// Evaluate evaluates a flag with the given environment and contexts.
func (rt *FlagsRuntime) Evaluate(ctx context.Context, key string, environment string, contexts []Context) interface{} {
	evalDict := contextsToEvalDict(contexts)

	// Auto-inject service context if set and not already provided.
	if rt.flagsClient != nil && rt.flagsClient.service != "" {
		if _, has := evalDict["service"]; !has {
			evalDict["service"] = map[string]interface{}{"key": rt.flagsClient.service}
		}
	}

	rt.mu.RLock()
	flagDef, ok := rt.flagStore[key]
	rt.mu.RUnlock()

	if ok {
		return evaluateFlag(flagDef, environment, evalDict)
	}

	// Flag not in store — fetch.
	flags, err := rt.flagsClient.fetchFlagsList(ctx)
	if err != nil {
		return nil
	}
	for _, f := range flags {
		if fID, _ := f["id"].(string); fID == key {
			return evaluateFlag(f, environment, evalDict)
		}
	}
	return nil
}

func (rt *FlagsRuntime) evaluateHandle(ctx context.Context, key string, defaultVal interface{}, explicitContexts []Context) interface{} {
	if err := rt.ensureInit(ctx); err != nil {
		log.Printf("smplkit: flags init failed: %s", err.Error())
		debug.Debug("flags", "flags init error details: %+v", err)
		return defaultVal
	}

	rt.mu.RLock()
	environment := rt.environment
	rt.mu.RUnlock()

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
				if rt.flagsClient.disableStreaming {
					// Stateless mode: flush inline rather than spawning.
					rt.flagsClient.flushContexts(ctx, rt.contextBuffer.drain())
				} else {
					go rt.flagsClient.flushContexts(context.Background(), rt.contextBuffer.drain())
				}
			}
		} else if ambient := rt.ambientContexts(); len(ambient) > 0 {
			// Fall back to the top-level SmplClient.SetContext stash. It is
			// already registered with the platform by SetContext, so we only
			// read it here for evaluation.
			evalDict = contextsToEvalDict(ambient)
		} else {
			evalDict = map[string]interface{}{}
		}
	}

	// Auto-inject service context if set and not already provided.
	if rt.flagsClient != nil && rt.flagsClient.service != "" {
		if _, has := evalDict["service"]; !has {
			evalDict["service"] = map[string]interface{}{"key": rt.flagsClient.service}
		}
	}

	ctxHash := hashContext(evalDict)
	cacheKey := fmt.Sprintf("%s:%s", key, ctxHash)

	var metrics *metricsReporter
	if rt.flagsClient != nil {
		metrics = rt.flagsClient.metrics
	}

	if cached, hit := rt.cache.get(cacheKey); hit {
		if metrics != nil {
			metrics.Record("flags.cache_hits", 1, "hits", nil)
			metrics.Record("flags.evaluations", 1, "evaluations", map[string]string{"flag": key})
		}
		return cached
	}

	if metrics != nil {
		metrics.Record("flags.cache_misses", 1, "misses", nil)
	}

	rt.mu.RLock()
	flagDef, ok := rt.flagStore[key]
	rt.mu.RUnlock()

	if !ok {
		rt.cache.put(cacheKey, defaultVal)
		if metrics != nil {
			metrics.Record("flags.evaluations", 1, "evaluations", map[string]string{"flag": key})
		}
		return defaultVal
	}

	value := evaluateFlag(flagDef, environment, evalDict)
	if value == nil {
		value = defaultVal
	}

	rt.cache.put(cacheKey, value)
	if metrics != nil {
		metrics.Record("flags.evaluations", 1, "evaluations", map[string]string{"flag": key})
	}
	return value
}

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

func (rt *FlagsRuntime) flushFlagBuffer(ctx context.Context) {
	// Peek without draining: items remain in the buffer until the POST succeeds.
	batch := rt.flagBuffer.peek()
	if len(batch) == 0 {
		return
	}
	items := make([]genflags.FlagBulkItem, 0, len(batch))
	for _, entry := range batch {
		item := genflags.FlagBulkItem{
			Id:      entry.id,
			Type:    genflags.FlagBulkItemType(entry.flagType),
			Default: entry.defaultVal,
		}
		if entry.service != "" {
			item.Service = &entry.service
		}
		if entry.environment != "" {
			item.Environment = &entry.environment
		}
		items = append(items, item)
	}
	reqBody := genflags.FlagBulkRequest{Flags: items}
	resp, err := rt.flagsClient.generated.BulkRegisterFlagsWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		log.Printf("smplkit: bulk flag registration failed: %s", err.Error())
		debug.Debug("flags", "bulk flag registration error details: %+v", err)
		return // items remain in buffer for the next periodic flush
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Printf("smplkit: bulk flag registration failed: HTTP %d", resp.StatusCode)
		debug.Debug("flags", "bulk flag registration HTTP error: %d: %s", resp.StatusCode, string(snippet))
		return // items remain in buffer for the next periodic flush
	}
	// Success: commit all peeked items.
	ids := make([]string, len(batch))
	for i, e := range batch {
		ids[i] = e.id
	}
	rt.flagBuffer.commit(ids)
}

func (rt *FlagsRuntime) periodicFlagFlush(done chan struct{}) {
	rt.runPeriodicFlagFlush(done, 30*time.Second)
}

func (rt *FlagsRuntime) runPeriodicFlagFlush(done chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			rt.flushFlagBuffer(context.Background())
		}
	}
}

func (rt *FlagsRuntime) handleFlagChanged(data map[string]interface{}) {
	flagKey, _ := data["id"].(string)
	debug.Debug("websocket", "flag_changed event received, key=%q", flagKey)

	ctx := context.Background()

	// Snapshot pre-state.
	rt.mu.RLock()
	pre, hadPre := rt.flagStore[flagKey]
	rt.mu.RUnlock()

	// Scoped single fetch.
	updated, err := rt.flagsClient.fetchSingleFlag(ctx, flagKey)
	if err != nil {
		return
	}

	rt.mu.Lock()
	rt.flagStore[flagKey] = updated
	rt.mu.Unlock()

	rt.cache.clear()

	// Only fire if content changed.
	if !hadPre || !mapsEqual(pre, updated) {
		rt.fireChangeListeners(flagKey, "websocket")
	}
}

func (rt *FlagsRuntime) handleFlagDeleted(data map[string]interface{}) {
	flagKey, _ := data["id"].(string)
	debug.Debug("websocket", "flag_deleted event received, key=%q", flagKey)

	rt.mu.Lock()
	delete(rt.flagStore, flagKey)
	rt.mu.Unlock()

	rt.cache.clear()
	rt.fireDeletedListener(flagKey, "websocket")
}

func (rt *FlagsRuntime) handleFlagsChanged(_ map[string]interface{}) {
	debug.Debug("websocket", "flags_changed event received")

	ctx := context.Background()
	newStore, err := rt.flagsClient.fetchAllFlags(ctx)
	if err != nil {
		return
	}

	rt.mu.Lock()
	oldStore := rt.flagStore
	rt.flagStore = newStore
	rt.mu.Unlock()

	rt.cache.clear()

	// Collect changed keys.
	allKeys := make(map[string]struct{})
	for k := range oldStore {
		allKeys[k] = struct{}{}
	}
	for k := range newStore {
		allKeys[k] = struct{}{}
	}

	var changedKeys []string
	for k := range allKeys {
		if !mapsEqual(oldStore[k], newStore[k]) {
			changedKeys = append(changedKeys, k)
		}
	}

	if len(changedKeys) == 0 {
		return
	}

	// Fire global listener once.
	rt.fireGlobalOnce("websocket")

	// Fire per-key listeners only for changed keys.
	for _, k := range changedKeys {
		rt.fireKeyListenersOnly(k, "websocket")
	}
}

func (rt *FlagsRuntime) fireChangeListeners(flagKey string, source string) {
	if flagKey == "" {
		return
	}
	event := &FlagChangeEvent{ID: flagKey, Source: source}
	rt.dispatchEvent(event, flagKey)
}

// fireGlobalOnce fires the global listener exactly once with no specific key.
// Used by plural events (flags_changed) after per-key listeners have been dispatched.
func (rt *FlagsRuntime) fireGlobalOnce(source string) {
	event := &FlagChangeEvent{Source: source}

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
}

// fireKeyListenersOnly fires only key-scoped and handle listeners for the given key.
func (rt *FlagsRuntime) fireKeyListenersOnly(flagKey string, source string) {
	if flagKey == "" {
		return
	}
	event := &FlagChangeEvent{ID: flagKey, Source: source}

	rt.listenersMu.Lock()
	keyListeners := make([]func(*FlagChangeEvent), len(rt.keyListeners[flagKey]))
	copy(keyListeners, rt.keyListeners[flagKey])
	rt.listenersMu.Unlock()

	for _, cb := range keyListeners {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("smplkit: exception in key-scoped flags on_change listener: %v", r)
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

// dispatchEvent sends an event to global listeners and key-scoped listeners.
func (rt *FlagsRuntime) dispatchEvent(event *FlagChangeEvent, flagKey string) {
	rt.listenersMu.Lock()
	globals := make([]func(*FlagChangeEvent), len(rt.globalListeners))
	copy(globals, rt.globalListeners)
	keyListeners := make([]func(*FlagChangeEvent), len(rt.keyListeners[flagKey]))
	copy(keyListeners, rt.keyListeners[flagKey])
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

	for _, cb := range keyListeners {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("smplkit: exception in key-scoped flags on_change listener: %v", r)
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

func (rt *FlagsRuntime) fireDeletedListener(flagKey string, source string) {
	if flagKey == "" {
		return
	}
	event := &FlagChangeEvent{ID: flagKey, Source: source, Deleted: true}
	rt.dispatchEvent(event, flagKey)
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
