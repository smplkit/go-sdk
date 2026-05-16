package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"reflect"
	"sync"
	"time"

	"github.com/smplkit/go-sdk/v3/internal/debug"
	genlogging "github.com/smplkit/go-sdk/v3/internal/generated/logging"
	"github.com/smplkit/go-sdk/v3/logging/adapters"
)

// LoggingClient provides management and runtime operations for logging resources.
// Obtain one via Client.Logging().
type LoggingClient struct {
	client    *Client
	generated genlogging.ClientInterface

	// Runtime state
	startOnce    sync.Once
	started      bool
	loggersCache map[string]map[string]interface{} // id → logger data
	groupsCache  map[string]map[string]interface{} // id → group data

	// Change listeners
	listenersMu     sync.Mutex
	globalListeners []func(*LoggerChangeEvent)
	keyListeners    map[string][]func(*LoggerChangeEvent)

	// Registration buffer
	buffer    *loggerRegistrationBuffer
	flushDone chan struct{}

	wsManager *sharedWebSocket

	// Pluggable adapters
	adapters []adapters.LoggingAdapter

	management *LoggingManagement
}

// Management returns the sub-object for logger and log group CRUD operations.
func (c *LoggingClient) Management() *LoggingManagement {
	if c.management == nil {
		c.management = newLoggingManagement(c.generated)
		c.management.attachRuntime(c)
	}
	return c.management
}

// newLoggingClient creates a new LoggingClient.
func newLoggingClient(c *Client, gen genlogging.ClientInterface) *LoggingClient {
	return &LoggingClient{
		client:       c,
		generated:    gen,
		loggersCache: make(map[string]map[string]interface{}),
		groupsCache:  make(map[string]map[string]interface{}),
		keyListeners: make(map[string][]func(*LoggerChangeEvent)),
		buffer:       newLoggerRegistrationBuffer(),
	}
}

// close cleans up the logging client resources.
func (c *LoggingClient) close() {
	debug.Debug("lifecycle", "LoggingClient.close() called")
	if c.wsManager != nil {
		c.wsManager.off("logger_changed", c.handleLoggerChanged)
		c.wsManager.off("logger_deleted", c.handleLoggerDeleted)
		c.wsManager.off("group_changed", c.handleGroupChanged)
		c.wsManager.off("group_deleted", c.handleGroupDeleted)
		c.wsManager.off("loggers_changed", c.handleLoggersChanged)
	}
	for _, adapter := range c.adapters {
		adapter.UninstallHook()
	}
	if c.flushDone != nil {
		close(c.flushDone)
		c.flushDone = nil
	}
}

// RegisterAdapter registers a logging adapter. Must be called before
// Install(). At least one adapter must be registered for runtime
// features to function.
func (c *LoggingClient) RegisterAdapter(adapter adapters.LoggingAdapter) {
	if c.started {
		panic("smplkit: cannot register adapters after Install()")
	}
	c.adapters = append(c.adapters, adapter)
}

// Install hooks the SDK into the application's logging machinery: it
// runs adapter discovery, fetches managed-logger definitions from the
// platform, applies resolved levels, and opens the live-updates
// WebSocket so subsequent server-side level changes propagate.
//
// Safe to call multiple times; only the first call takes effect. There
// is no companion Stop() — close the parent Client instead.
//
// Mirrors Python's client.logging.install() (rule 4 of the cross-SDK
// overhaul). The pre-existing Start name is retained as a deprecated
// shim that simply forwards to Install.
func (c *LoggingClient) Install(ctx context.Context) error {
	return c.start(ctx)
}

// Start is a deprecated alias for Install.
//
// Deprecated: Use Install.
func (c *LoggingClient) Start(ctx context.Context) error {
	return c.start(ctx)
}

// start is the unexported impl shared by Install and Start.
func (c *LoggingClient) start(ctx context.Context) error {
	var startErr error
	c.startOnce.Do(func() {
		// Warn if no adapters registered.
		if len(c.adapters) == 0 {
			log.Println("smplkit: no logging adapters registered — framework-level control disabled")
		}

		// Discover loggers from all adapters and buffer them for bulk registration.
		var discoveredCount int
		for _, adapter := range c.adapters {
			discovered := adapter.Discover()
			for _, dl := range discovered {
				normalized := NormalizeLoggerName(dl.Name)
				if normalized == "" {
					continue
				}
				debug.Debug("discovery", "discovered logger: %s (level=%s)", normalized, dl.Level)
				c.buffer.add(normalized, dl.Level, dl.Level, c.client.service, c.client.environment)
				discoveredCount++
			}
		}
		debug.Debug("lifecycle", "discovered %d loggers from adapters", discoveredCount)

		// Install hooks on all adapters.
		for _, adapter := range c.adapters {
			adapter.InstallHook(c.onNewLogger)
		}
		debug.Debug("registration", "installed hooks on %d adapters", len(c.adapters))

		// Flush any loggers registered before Start (including discovered ones).
		if err := c.Flush(ctx); err != nil {
			log.Printf("smplkit: bulk logger registration failed: %s", err.Error())
			debug.Debug("logging", "bulk logger registration error details: %+v", err)
		}
		debug.Debug("registration", "initial registration flush complete")

		// Fetch definitions.
		debug.Debug("api", "fetching logger and group definitions")
		if err := c.fetchAndCache(ctx); err != nil {
			startErr = err
			return
		}
		debug.Debug("api", "fetched %d loggers and %d groups", len(c.loggersCache), len(c.groupsCache))

		// Apply resolved levels to all adapters.
		debug.Debug("resolution", "starting initial level resolution pass")
		c.applyLevels()

		// Open WebSocket and register listeners. The WS connect happens
		// in the background — callers that need confirmed subscription
		// before firing writes should call Client.WaitUntilReady.
		ws := c.client.ensureWS()
		c.wsManager = ws
		ws.on("logger_changed", c.handleLoggerChanged)
		ws.on("logger_deleted", c.handleLoggerDeleted)
		ws.on("group_changed", c.handleGroupChanged)
		ws.on("group_deleted", c.handleGroupDeleted)
		ws.on("loggers_changed", c.handleLoggersChanged)

		// Start periodic flush timer.
		c.flushDone = make(chan struct{})
		go c.periodicFlush(c.flushDone)

		c.started = true
	})
	return startErr
}

// RegisterLogger explicitly registers a logger name for smplkit management.
// Call before or after Start().
func (c *LoggingClient) RegisterLogger(name string, level LogLevel) {
	normalized := NormalizeLoggerName(name)
	c.buffer.add(normalized, string(level), string(level), c.client.service, c.client.environment)
}

// Refresh re-fetches managed logger and log-group definitions from the
// server, re-applies resolved levels to every adapter-known logger, and
// fires change/deletion listeners (with Source = "manual") for any
// logger whose effective level changed since the previous fetch.
//
// Use this when the customer wants to bypass the WebSocket and force a
// fresh sync — e.g. after a known burst of server-side edits, or when
// running short-lived scripts that don't keep the WS open. Mirrors
// Python's client.logging.refresh().
func (c *LoggingClient) Refresh(ctx context.Context) error {
	before := c.snapshotResolvedLevels()
	if err := c.fetchAndCache(ctx); err != nil {
		return err
	}
	c.applyLevels()
	c.fireResolvedLevelDeltas(before, "manual")
	return nil
}

// OnChange registers a global change listener that fires for any logger change.
func (c *LoggingClient) OnChange(cb func(*LoggerChangeEvent)) {
	c.listenersMu.Lock()
	c.globalListeners = append(c.globalListeners, cb)
	c.listenersMu.Unlock()
}

// OnChangeKey registers a key-scoped change listener.
func (c *LoggingClient) OnChangeKey(key string, cb func(*LoggerChangeEvent)) {
	c.listenersMu.Lock()
	c.keyListeners[key] = append(c.keyListeners[key], cb)
	c.listenersMu.Unlock()
}

// applyLevels resolves and applies levels to all known loggers across adapters.
func (c *LoggingClient) applyLevels() {
	if len(c.adapters) == 0 {
		return
	}

	// Collect all logger names from adapters.
	type adapterLogger struct {
		adapter    adapters.LoggingAdapter
		loggerName string
	}
	var targets []adapterLogger
	for _, adapter := range c.adapters {
		for _, dl := range adapter.Discover() {
			if dl.Name != "" {
				targets = append(targets, adapterLogger{adapter: adapter, loggerName: dl.Name})
			}
		}
	}

	for _, t := range targets {
		normalized := NormalizeLoggerName(t.loggerName)
		resolved := resolveLoggerLevel(normalized, c.client.environment, c.loggersCache, c.groupsCache)
		debug.Debug("resolution", "resolved %s → %s", normalized, resolved)
		t.adapter.ApplyLevel(t.loggerName, string(resolved))
		debug.Debug("adapter", "applied level %s to logger %s", resolved, t.loggerName)
		if metrics := c.client.metrics; metrics != nil {
			metrics.Record("logging.level_changes", 1, "changes", map[string]string{"logger": normalized})
		}
	}
}

// onNewLogger is called when a logging framework creates a new logger.
func (c *LoggingClient) onNewLogger(name string, level string) {
	normalized := NormalizeLoggerName(name)
	if normalized == "" {
		return
	}
	debug.Debug("discovery", "new logger from hook: %s (level=%s)", normalized, level)
	c.buffer.add(normalized, level, level, c.client.service, c.client.environment)

	// If already started, resolve and apply the level immediately.
	if c.started {
		resolved := resolveLoggerLevel(normalized, c.client.environment, c.loggersCache, c.groupsCache)
		for _, adapter := range c.adapters {
			adapter.ApplyLevel(name, string(resolved))
		}
		if metrics := c.client.metrics; metrics != nil {
			metrics.Record("logging.level_changes", 1, "changes", map[string]string{"logger": normalized})
		}
	}
}

// (updateLogger, deleteLoggerByID, createGroup, updateGroup,
// deleteGroupByID moved to logging_management.go so the active-record
// save path doesn't depend on the runtime client — rule 1 of the
// cross-SDK overhaul.)

func resourceToLogger(r genlogging.LoggerResource, m *LoggingManagement) *Logger {
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}
	var level *LogLevel
	if attrs.Level != nil && *attrs.Level != "" {
		l := LogLevel(*attrs.Level)
		level = &l
	}
	managed := true
	if attrs.Managed != nil {
		managed = *attrs.Managed
	}
	var sources []map[string]interface{}
	if attrs.Sources != nil {
		sources = *attrs.Sources
	}
	var envs map[string]interface{}
	if attrs.Environments != nil {
		envs = *attrs.Environments
	} else {
		envs = map[string]interface{}{}
	}

	return &Logger{
		ID:           id,
		Name:         attrs.Name,
		Level:        level,
		Group:        attrs.Group,
		Managed:      managed,
		Sources:      sources,
		Environments: envs,
		CreatedAt:    attrs.CreatedAt,
		UpdatedAt:    attrs.UpdatedAt,
		client:       m,
	}
}

func resourceToLogGroup(r genlogging.LogGroupResource, m *LoggingManagement) *LogGroup {
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}
	var level *LogLevel
	if attrs.Level != nil && *attrs.Level != "" {
		l := LogLevel(*attrs.Level)
		level = &l
	}
	var envs map[string]interface{}
	if attrs.Environments != nil {
		envs = *attrs.Environments
	} else {
		envs = map[string]interface{}{}
	}

	return &LogGroup{
		ID:           id,
		Name:         attrs.Name,
		Level:        level,
		Group:        attrs.ParentId,
		Environments: envs,
		CreatedAt:    attrs.CreatedAt,
		UpdatedAt:    attrs.UpdatedAt,
		client:       m,
	}
}

func buildLoggerAttributes(l *Logger) genlogging.Logger {
	var level *genlogging.LoggerLevel
	if l.Level != nil {
		lv := genlogging.LoggerLevel(*l.Level)
		level = &lv
	}
	var envs *map[string]interface{}
	if l.Environments != nil {
		envs = &l.Environments
	}
	var sources *[]map[string]interface{}
	if l.Sources != nil {
		sources = &l.Sources
	}
	return genlogging.Logger{
		Name:         l.Name,
		Level:        level,
		Group:        l.Group,
		Managed:      &l.Managed,
		Sources:      sources,
		Environments: envs,
	}
}

func buildLogGroupAttributes(g *LogGroup) genlogging.LogGroup {
	var level *genlogging.LogGroupLevel
	if g.Level != nil {
		lv := genlogging.LogGroupLevel(*g.Level)
		level = &lv
	}
	var envs *map[string]interface{}
	if g.Environments != nil {
		envs = &g.Environments
	}
	return genlogging.LogGroup{
		Name:         g.Name,
		Level:        level,
		ParentId:     g.Group,
		Environments: envs,
	}
}

func (c *LoggingClient) fetchSingleLogger(ctx context.Context, key string) (map[string]interface{}, error) {
	resp, err := c.generated.GetLogger(ctx, key)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{
			Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genlogging.LoggerResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	l := resourceToLogger(result.Data, c.Management())
	entry := map[string]interface{}{
		"id":           l.ID,
		"name":         l.Name,
		"managed":      l.Managed,
		"environments": l.Environments,
	}
	if l.Level != nil {
		entry["level"] = string(*l.Level)
	}
	if l.Group != nil {
		entry["group"] = *l.Group
	}
	return entry, nil
}

func (c *LoggingClient) fetchSingleGroup(ctx context.Context, key string) (map[string]interface{}, error) {
	resp, err := c.generated.GetLogGroup(ctx, key)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{
			Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genlogging.LogGroupResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	g := resourceToLogGroup(result.Data, c.Management())
	entry := map[string]interface{}{
		"id":           g.ID,
		"name":         g.Name,
		"environments": g.Environments,
	}
	if g.Level != nil {
		entry["level"] = string(*g.Level)
	}
	if g.Group != nil {
		entry["group"] = *g.Group
	}
	return entry, nil
}

// fetchAllLoggers walks every page of /loggers and accumulates the full
// list. Stops when the server returns a short page (fewer than
// fetchAllPageSize items).
func (c *LoggingClient) fetchAllLoggers(ctx context.Context) ([]*Logger, error) {
	mgmt := c.Management()
	var all []*Logger
	for page := 1; ; page++ {
		batch, err := mgmt.List(ctx, WithPageNumber(page), WithPageSize(fetchAllPageSize))
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < fetchAllPageSize {
			return all, nil
		}
	}
}

// fetchAllLogGroups walks every page of /log_groups and accumulates the
// full list. Same termination semantics as fetchAllLoggers.
func (c *LoggingClient) fetchAllLogGroups(ctx context.Context) ([]*LogGroup, error) {
	mgmt := c.Management()
	var all []*LogGroup
	for page := 1; ; page++ {
		batch, err := mgmt.ListGroups(ctx, WithPageNumber(page), WithPageSize(fetchAllPageSize))
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < fetchAllPageSize {
			return all, nil
		}
	}
}

func (c *LoggingClient) fetchAndCache(ctx context.Context) error {
	loggers, err := c.fetchAllLoggers(ctx)
	if err != nil {
		return err
	}
	groups, err := c.fetchAllLogGroups(ctx)
	if err != nil {
		return err
	}

	loggersCache := make(map[string]map[string]interface{}, len(loggers))
	for _, l := range loggers {
		entry := map[string]interface{}{
			"id":           l.ID,
			"name":         l.Name,
			"managed":      l.Managed,
			"environments": l.Environments,
		}
		if l.Level != nil {
			entry["level"] = string(*l.Level)
		}
		if l.Group != nil {
			entry["group"] = *l.Group
		}
		loggersCache[l.ID] = entry
	}

	groupsCache := make(map[string]map[string]interface{}, len(groups))
	for _, g := range groups {
		entry := map[string]interface{}{
			"id":           g.ID,
			"name":         g.Name,
			"environments": g.Environments,
		}
		if g.Level != nil {
			entry["level"] = string(*g.Level)
		}
		if g.Group != nil {
			entry["group"] = *g.Group
		}
		groupsCache[g.ID] = entry
	}

	c.loggersCache = loggersCache
	c.groupsCache = groupsCache
	return nil
}

// Flush sends any pending logger discoveries to the server immediately
// via the bulk-register endpoint. Discoveries are buffered as adapter
// hooks fire (e.g. slog WithGroup creating a sub-handler) and on
// explicit RegisterLogger calls; they are normally flushed on a
// 5-second interval. Call this when you need them sent right away —
// e.g. before exiting a short-lived script. Returns nil immediately
// when the buffer is empty.
func (c *LoggingClient) Flush(ctx context.Context) error {
	batch := c.buffer.drain()
	if len(batch) == 0 {
		return nil
	}
	items := make([]genlogging.LoggerBulkItem, 0, len(batch))
	for _, entry := range batch {
		item := genlogging.LoggerBulkItem{Id: entry.key}
		if entry.level != "" {
			item.Level = &entry.level
		}
		if entry.resolvedLevel != "" {
			item.ResolvedLevel = &entry.resolvedLevel
		}
		if entry.service != "" {
			item.Service = &entry.service
		}
		if entry.environment != "" {
			item.Environment = &entry.environment
		}
		items = append(items, item)
	}
	reqBody := genlogging.LoggerBulkRequest{Loggers: items}
	resp, err := c.generated.BulkRegisterLoggersWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", readErr)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}
	if metrics := c.client.metrics; metrics != nil {
		metrics.Record("logging.loggers_discovered", len(batch), "loggers", nil)
	}
	return nil
}

func (c *LoggingClient) periodicFlush(done chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := c.Flush(context.Background()); err != nil {
				log.Printf("smplkit: bulk logger registration failed: %s", err.Error())
				debug.Debug("logging", "bulk logger registration error details: %+v", err)
			}
		}
	}
}

func (c *LoggingClient) handleLoggerChanged(data map[string]interface{}) {
	loggerKey, _ := data["id"].(string)
	debug.Debug("websocket", "logger_changed event received, key=%q", loggerKey)

	ctx := context.Background()

	// Scoped single fetch; skip the apply/fire pass entirely when the
	// raw entry didn't change (cheap fast path for noisy events).
	prePre := c.loggersCache[loggerKey]
	updated, err := c.fetchSingleLogger(ctx, loggerKey)
	if err != nil {
		return
	}
	if reflect.DeepEqual(prePre, updated) {
		return
	}

	before := c.snapshotResolvedLevels()
	c.loggersCache[loggerKey] = updated
	c.applyLevels()
	c.fireResolvedLevelDeltas(before, "websocket")
}

func (c *LoggingClient) handleLoggerDeleted(data map[string]interface{}) {
	loggerKey, _ := data["id"].(string)
	debug.Debug("websocket", "logger_deleted event received, key=%q", loggerKey)

	before := c.snapshotResolvedLevels()
	delete(c.loggersCache, loggerKey)
	c.applyLevels()
	c.fireResolvedLevelDeltas(before, "websocket")
}

func (c *LoggingClient) handleGroupChanged(data map[string]interface{}) {
	groupKey, _ := data["id"].(string)
	debug.Debug("websocket", "group_changed event received, key=%q", groupKey)

	ctx := context.Background()

	pre := c.groupsCache[groupKey]
	updated, err := c.fetchSingleGroup(ctx, groupKey)
	if err != nil {
		return
	}
	if reflect.DeepEqual(pre, updated) {
		return
	}

	before := c.snapshotResolvedLevels()
	c.groupsCache[groupKey] = updated
	c.applyLevels()
	c.fireResolvedLevelDeltas(before, "websocket")
}

func (c *LoggingClient) handleGroupDeleted(data map[string]interface{}) {
	groupKey, _ := data["id"].(string)
	debug.Debug("websocket", "group_deleted event received, key=%q", groupKey)

	if _, existed := c.groupsCache[groupKey]; !existed {
		return
	}
	before := c.snapshotResolvedLevels()
	delete(c.groupsCache, groupKey)
	c.applyLevels()
	c.fireResolvedLevelDeltas(before, "websocket")
}

func (c *LoggingClient) handleLoggersChanged(_ map[string]interface{}) {
	debug.Debug("websocket", "loggers_changed event received")

	ctx := context.Background()

	before := c.snapshotResolvedLevels()
	if err := c.fetchAndCache(ctx); err != nil {
		return
	}
	c.applyLevels()
	c.fireResolvedLevelDeltas(before, "websocket")
}

// snapshotResolvedLevels resolves every cached logger against the
// current loggers + groups + environment and returns the resulting map.
// Used to capture a "before" picture for diffing after a cache mutation.
func (c *LoggingClient) snapshotResolvedLevels() map[string]LogLevel {
	snap := make(map[string]LogLevel, len(c.loggersCache))
	for id := range c.loggersCache {
		snap[id] = resolveLoggerLevel(id, c.client.environment, c.loggersCache, c.groupsCache)
	}
	return snap
}

// fireResolvedLevelDeltas re-resolves every cached logger and fires
// change / deleted listeners for any logger whose effective level
// differs from the supplied snapshot. A logger that is no longer in
// loggersCache (i.e. was deleted) fires a deleted listener; everything
// else with a different (or newly-resolved) level fires a change
// listener.
func (c *LoggingClient) fireResolvedLevelDeltas(before map[string]LogLevel, source string) {
	after := c.snapshotResolvedLevels()
	for id, newLvl := range after {
		if oldLvl, ok := before[id]; !ok || oldLvl != newLvl {
			c.fireChangeListeners(id, source)
		}
	}
	for id := range before {
		if _, stillCached := c.loggersCache[id]; !stillCached {
			c.fireDeletedListeners(id, source)
		}
	}
}

func (c *LoggingClient) fireDeletedListeners(loggerID string, source string) {
	if loggerID == "" {
		return
	}

	event := &LoggerChangeEvent{ID: loggerID, Deleted: true, Source: source}

	c.listenersMu.Lock()
	globals := make([]func(*LoggerChangeEvent), len(c.globalListeners))
	copy(globals, c.globalListeners)
	keyListeners := make([]func(*LoggerChangeEvent), len(c.keyListeners[loggerID]))
	copy(keyListeners, c.keyListeners[loggerID])
	c.listenersMu.Unlock()

	for _, cb := range globals {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("smplkit: exception in global logging on_change listener: %v", r)
				}
			}()
			cb(event)
		}()
	}

	for _, cb := range keyListeners {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("smplkit: exception in key-scoped logging on_change listener: %v", r)
				}
			}()
			cb(event)
		}()
	}
}

func (c *LoggingClient) fireChangeListeners(loggerID string, source string) {
	if loggerID == "" {
		return
	}

	var level *LogLevel
	if cached, ok := c.loggersCache[loggerID]; ok {
		resolved := resolveLoggerLevel(loggerID, c.client.environment, c.loggersCache, c.groupsCache)
		level = &resolved
		_ = cached
	}

	event := &LoggerChangeEvent{ID: loggerID, Level: level, Source: source}

	c.listenersMu.Lock()
	globals := make([]func(*LoggerChangeEvent), len(c.globalListeners))
	copy(globals, c.globalListeners)
	keyListeners := make([]func(*LoggerChangeEvent), len(c.keyListeners[loggerID]))
	copy(keyListeners, c.keyListeners[loggerID])
	c.listenersMu.Unlock()

	for _, cb := range globals {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("smplkit: exception in global logging on_change listener: %v", r)
				}
			}()
			cb(event)
		}()
	}

	for _, cb := range keyListeners {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("smplkit: exception in key-scoped logging on_change listener: %v", r)
				}
			}()
			cb(event)
		}()
	}
}

type loggerRegistrationEntry struct {
	key           string
	level         string // explicitly-set level; empty string means inherited/not set
	resolvedLevel string // effective level after framework inheritance; always non-empty
	service       string
	environment   string
}

type loggerRegistrationBuffer struct {
	mu      sync.Mutex
	seen    map[string]struct{}
	pending []loggerRegistrationEntry
}

func newLoggerRegistrationBuffer() *loggerRegistrationBuffer {
	return &loggerRegistrationBuffer{
		seen: make(map[string]struct{}),
	}
}

// add buffers a logger for bulk registration. level is the explicitly-set level
// (empty string means inherited/not explicitly set). resolvedLevel is the
// effective level after framework inheritance and must be non-empty.
func (b *loggerRegistrationBuffer) add(key, level, resolvedLevel, service, environment string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.seen[key]; ok {
		return
	}
	b.seen[key] = struct{}{}
	b.pending = append(b.pending, loggerRegistrationEntry{
		key:           key,
		level:         level,
		resolvedLevel: resolvedLevel,
		service:       service,
		environment:   environment,
	})
}

func (b *loggerRegistrationBuffer) drain() []loggerRegistrationEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.pending
	b.pending = nil
	return batch
}
