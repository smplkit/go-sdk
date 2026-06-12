package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/smplkit/go-sdk/v3/internal/debug"
	genlogging "github.com/smplkit/go-sdk/v3/internal/generated/logging"
	"github.com/smplkit/go-sdk/v3/logging/adapters"
)

// loggerBatchFlushSize is the number of buffered logger sources that
// triggers a background flush.
const loggerBatchFlushSize = 50

// notInstalledMessage is the error text for live operations attempted
// before Install.
const notInstalledMessage = "Smpl Logging live operations require install() first — this opens a live " +
	"connection to your running service and hooks into your application's " +
	"logging framework. Call client.Logging().Install() before " +
	"OnChange()/Refresh()."

// LoggingClient is the Smpl Logging client.
//
// One client exposes the full surface, reachable as client.Logging()
// or constructed directly via NewLoggingClient.
//
// Smpl Logging has two surfaces on a single client, mirroring how the
// config, flags, audit, and jobs clients expose their full surface from
// one class:
//
//   - CRUD surface — works without Install.
//     Two sub-clients:
//   - client.Logging().Loggers() — logger CRUD + discovery: New / List /
//     Get / Delete plus Register / Flush / FlushSync / PendingCount.
//   - client.Logging().LogGroups() — log-group CRUD: New / List / Get /
//     Delete.
//
// The fused client owns the logger-discovery buffer directly; the
// Loggers sub-client shares that same buffer so discovery and explicit
// registration drain through one queue.
//
//   - Live surface — directly on the client. RegisterAdapter is a
//     PRE-install configuration call (allowed before Install).
//     Install opens the live connection (hooks the app's logging
//     framework via adapters, discovers loggers, fetches + applies
//     levels, opens the shared WebSocket). OnChange / Refresh require
//     Install first; calling them earlier returns a *NotInstalledError.
//
// The client supports two construction shapes:
//
//   - Wired into SmplClient — borrows the parent's logging transport for both
//     runtime fetch and CRUD and the parent's shared WebSocket for the
//     live channel. This is the common path.
//   - Standalone — NewLoggingClient(cfg, ...) builds and owns its own
//     logging transport and an app transport (the WebSocket gateway lives
//     on the app service), and on Install opens and owns its own
//     WebSocket.
type LoggingClient struct {
	// client is the owning parent SmplClient when wired, nil when standalone.
	client    *SmplClient
	generated genlogging.ClientInterface

	// Parent-or-own state. When wired these mirror the parent's fields;
	// when standalone they are resolved at construction so the live
	// surface can open its own WebSocket without a parent.
	environment string
	service     string
	metrics     *metricsReporter
	appURL      string
	apiKey      string

	// wsMu guards the lazily-created own WebSocket (standalone path).
	wsMu  sync.Mutex
	ownWS *sharedWebSocket

	// CRUD sub-clients.
	loggers   *LoggersClient
	logGroups *LogGroupsClient

	// Discovery buffer is owned by this client; the loggers sub-client
	// shares it so discovery and explicit registration drain together.
	buffer *loggerRegistrationBuffer

	// Live-surface state.
	startOnce    sync.Once
	connected    bool
	nameMap      map[string]string                 // original_name → normalized_id
	loggersCache map[string]map[string]interface{} // id → logger data
	groupsCache  map[string]map[string]interface{} // id → group data

	// Change listeners
	listenersMu     sync.Mutex
	globalListeners []func(*LoggerChangeEvent)
	keyListeners    map[string][]func(*LoggerChangeEvent)

	// Pluggable adapters
	adapters         []adapters.LoggingAdapter
	explicitAdapters bool

	wsManager *sharedWebSocket
	flushDone chan struct{}
}

// Loggers returns the logger CRUD + discovery sub-client.
func (c *LoggingClient) Loggers() *LoggersClient {
	return c.loggers
}

// LogGroups returns the log-group CRUD sub-client.
func (c *LoggingClient) LogGroups() *LogGroupsClient {
	return c.logGroups
}

// newLoggingClient wires a LoggingClient onto the parent SmplClient's
// pre-built generated logging transport and the parent's metrics
// reporter, and sets up the loggers / log_groups sub-clients sharing the
// owned discovery buffer.
func newLoggingClient(parent *SmplClient, gen genlogging.ClientInterface, metrics *metricsReporter) *LoggingClient {
	c := &LoggingClient{
		client:       parent,
		generated:    gen,
		environment:  parent.environment,
		service:      parent.service,
		metrics:      metrics,
		buffer:       newLoggerRegistrationBuffer(),
		loggersCache: make(map[string]map[string]interface{}),
		groupsCache:  make(map[string]map[string]interface{}),
		nameMap:      make(map[string]string),
		keyListeners: make(map[string][]func(*LoggerChangeEvent)),
	}
	c.loggers = &LoggersClient{gen: gen, buffer: c.buffer, metrics: metrics}
	c.logGroups = &LogGroupsClient{gen: gen}
	return c
}

// NewLoggingClient creates a standalone Smpl Logging client that builds
// and owns its own generated logging transport and, on first live use,
// its own WebSocket against the event gateway (the WebSocket gateway
// lives on the app service).
//
// Logging needs environment + service: the environment scopes runtime
// level resolution and discovery declarations, and the service scopes
// discovery declarations. Both resolve from cfg (or ~/.smplkit / env
// vars).
func NewLoggingClient(cfg Config, opts ...ClientOption) (*LoggingClient, error) {
	rc, err := resolveConfig(cfg)
	if err != nil {
		return nil, err
	}
	optCfg := defaultConfig()
	for _, opt := range opts {
		opt(&optCfg)
	}

	httpClient := optCfg.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: optCfg.timeout}
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: rc.apiKey, base: base}

	logURL := serviceURL(optCfg, "logging", rc)
	appURL := serviceURL(optCfg, "app", rc)

	headerEditor := func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	}
	genLogging, _ := genlogging.NewClient(logURL,
		genlogging.WithHTTPClient(httpClient),
		genlogging.WithRequestEditorFn(headerEditor),
	)

	buffer := newLoggerRegistrationBuffer()
	c := &LoggingClient{
		client:       nil,
		generated:    genLogging,
		environment:  rc.environment,
		service:      rc.service,
		metrics:      nil,
		appURL:       appURL,
		apiKey:       rc.apiKey,
		buffer:       buffer,
		loggersCache: make(map[string]map[string]interface{}),
		groupsCache:  make(map[string]map[string]interface{}),
		nameMap:      make(map[string]string),
		keyListeners: make(map[string][]func(*LoggerChangeEvent)),
	}
	c.loggers = &LoggersClient{gen: genLogging, buffer: buffer}
	c.logGroups = &LogGroupsClient{gen: genLogging}
	return c, nil
}

// ensureWS returns the shared WebSocket — the parent's when wired, else
// our own (built lazily on first live use).
func (c *LoggingClient) ensureWS() *sharedWebSocket {
	if c.client != nil {
		return c.client.ensureWS()
	}
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.ownWS == nil {
		c.ownWS = newSharedWebSocket(c.appURL, c.apiKey, c.metrics)
		c.ownWS.start()
	}
	return c.ownWS
}

// close cleans up the logging client resources.
func (c *LoggingClient) close() {
	debug.Debug("lifecycle", "LoggingClient.close() called")
	for _, adapter := range c.adapters {
		adapter.UninstallHook()
	}
	if c.wsManager != nil {
		c.wsManager.off("logger_changed", c.handleLoggerChanged)
		c.wsManager.off("logger_deleted", c.handleLoggerDeleted)
		c.wsManager.off("group_changed", c.handleGroupChanged)
		c.wsManager.off("group_deleted", c.handleGroupDeleted)
		c.wsManager.off("loggers_changed", c.handleLoggersChanged)
		if c.client == nil && c.ownWS != nil {
			c.ownWS.stop()
			c.ownWS = nil
		}
		c.wsManager = nil
	}
	if c.flushDone != nil {
		close(c.flushDone)
		c.flushDone = nil
	}
}

// RegisterAdapter registers a logging adapter. Must be called before
// Install().
//
// If called at least once, auto-loading is disabled — only explicitly
// registered adapters are used. This is a pre-install configuration
// call: it is intentionally NOT gated by Install.
func (c *LoggingClient) RegisterAdapter(adapter adapters.LoggingAdapter) {
	if c.connected {
		panic("smplkit: cannot register adapters after Install()")
	}
	c.explicitAdapters = true
	c.adapters = append(c.adapters, adapter)
}

// requireInstalled returns a *NotInstalledError when the live surface is
// touched before Install.
func (c *LoggingClient) requireInstalled() error {
	if !c.connected {
		return &NotInstalledError{Base: Error{Message: notInstalledMessage}}
	}
	return nil
}

// Install hooks smplkit into the application's logging machinery.
//
// Loads adapters, scans existing loggers, applies levels from the
// smplkit server, and wires WebSocket handlers for live updates. This
// IS the explicit consent gate — OnChange / Refresh require it first.
//
// Idempotent — safe to call multiple times.
func (c *LoggingClient) Install(ctx context.Context) error {
	debug.Debug("lifecycle", "LoggingClient.Install() called")
	if c.client != nil {
		c.client.ensureStarted()
	}

	var startErr error
	c.startOnce.Do(func() {
		// 0. Warn if no adapters registered.
		if len(c.adapters) == 0 {
			log.Println("smplkit: no logging adapters registered — framework-level control disabled")
		}

		// 1. Discover existing loggers from all adapters and buffer them
		// for bulk registration.
		var discoveredCount int
		for _, adapter := range c.adapters {
			for _, dl := range adapter.Discover() {
				normalized := NormalizeLoggerName(dl.Name)
				if normalized == "" {
					continue
				}
				debug.Debug("discovery", "discovered logger: %s (level=%s)", normalized, dl.Level)
				c.nameMap[dl.Name] = normalized
				c.buffer.add(normalized, dl.Level, dl.Level, c.service, c.environment)
				discoveredCount++
			}
		}
		debug.Debug("lifecycle", "discovered %d loggers from adapters", discoveredCount)

		// 2. Install continuous discovery hooks.
		for _, adapter := range c.adapters {
			adapter.InstallHook(c.onNewLogger)
		}
		debug.Debug("registration", "installed hooks on %d adapters", len(c.adapters))

		// 3. Flush initial batch. Non-destructive: items stay in the
		// buffer until the POST succeeds, so failures here don't lose
		// declarations.
		if err := c.loggers.Flush(ctx); err != nil {
			log.Printf("smplkit: bulk logger registration failed: %s", err.Error())
			debug.Debug("logging", "bulk logger registration error details: %+v", err)
		}
		debug.Debug("registration", "initial registration flush complete")

		// 4-6. Fetch, resolve, apply.
		debug.Debug("api", "fetching logger and group definitions")
		if err := c.fetchAndCache(ctx); err != nil {
			startErr = err
			return
		}
		debug.Debug("api", "fetched %d loggers and %d groups", len(c.loggersCache), len(c.groupsCache))
		debug.Debug("resolution", "starting initial level resolution pass")
		c.applyLevels()

		// 7. Register WebSocket event handlers for real-time level updates.
		ws := c.ensureWS()
		c.wsManager = ws
		ws.on("logger_changed", c.handleLoggerChanged)
		ws.on("logger_deleted", c.handleLoggerDeleted)
		ws.on("group_changed", c.handleGroupChanged)
		ws.on("group_deleted", c.handleGroupDeleted)
		ws.on("loggers_changed", c.handleLoggersChanged)

		// Start periodic flush timer.
		c.flushDone = make(chan struct{})
		go c.periodicFlush(c.flushDone)

		c.connected = true
	})
	return startErr
}

// RegisterLogger explicitly registers a logger name for smplkit
// management. Call before or after Install().
func (c *LoggingClient) RegisterLogger(name string, level LogLevel) {
	normalized := NormalizeLoggerName(name)
	c.buffer.add(normalized, string(level), string(level), c.service, c.environment)
}

// Refresh re-fetches all loggers and groups and fires listener events for
// any deltas.
//
// Requires Install first; returns a *NotInstalledError otherwise.
func (c *LoggingClient) Refresh(ctx context.Context) error {
	if err := c.requireInstalled(); err != nil {
		return err
	}
	debug.Debug("resolution", "refresh() called, triggering full resolution pass")
	before := c.snapshotResolvedLevels()
	if err := c.fetchAndCache(ctx); err != nil {
		return err
	}
	c.applyLevels()
	c.fireResolvedLevelDeltas(before, "manual")
	return nil
}

// OnChange registers a global change listener.
//
// Requires Install first; returns a *NotInstalledError otherwise.
func (c *LoggingClient) OnChange(cb func(*LoggerChangeEvent)) error {
	if err := c.requireInstalled(); err != nil {
		return err
	}
	c.listenersMu.Lock()
	c.globalListeners = append(c.globalListeners, cb)
	c.listenersMu.Unlock()
	return nil
}

// OnChangeKey registers a key-scoped change listener.
//
// Requires Install first; returns a *NotInstalledError otherwise.
func (c *LoggingClient) OnChangeKey(key string, cb func(*LoggerChangeEvent)) error {
	if err := c.requireInstalled(); err != nil {
		return err
	}
	c.listenersMu.Lock()
	c.keyListeners[key] = append(c.keyListeners[key], cb)
	c.listenersMu.Unlock()
	return nil
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
		resolved := resolveLoggerLevel(normalized, c.environment, c.loggersCache, c.groupsCache)
		debug.Debug("resolution", "resolved %s → %s", normalized, resolved)
		t.adapter.ApplyLevel(t.loggerName, string(resolved))
		debug.Debug("adapter", "applied level %s to logger %s", resolved, t.loggerName)
		if c.metrics != nil {
			c.metrics.Record("logging.level_changes", 1, "changes", map[string]string{"logger": normalized})
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
	c.nameMap[name] = normalized
	c.buffer.add(normalized, level, level, c.service, c.environment)

	// If already started, resolve and apply the level immediately.
	if c.connected {
		resolved := resolveLoggerLevel(normalized, c.environment, c.loggersCache, c.groupsCache)
		for _, adapter := range c.adapters {
			adapter.ApplyLevel(name, string(resolved))
		}
		if c.metrics != nil {
			c.metrics.Record("logging.level_changes", 1, "changes", map[string]string{"logger": normalized})
		}
	}
}

func resourceToLogger(r genlogging.LoggerResource, m *LoggersClient) *Logger {
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

func resourceToLogGroup(r genlogging.LogGroupResource, m *LogGroupsClient) *LogGroup {
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
	var level *genlogging.LogLevel
	if l.Level != nil {
		lv := genlogging.LogLevel(*l.Level)
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
	var level *genlogging.LogLevel
	if g.Level != nil {
		lv := genlogging.LogLevel(*g.Level)
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

	l := resourceToLogger(result.Data, c.loggers)
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

	g := resourceToLogGroup(result.Data, c.logGroups)
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
	var all []*Logger
	for page := 1; ; page++ {
		batch, err := c.loggers.List(ctx, WithPageNumber(page), WithPageSize(fetchAllPageSize))
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
	var all []*LogGroup
	for page := 1; ; page++ {
		batch, err := c.logGroups.List(ctx, WithPageNumber(page), WithPageSize(fetchAllPageSize))
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
	return c.loggers.Flush(ctx)
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
		snap[id] = resolveLoggerLevel(id, c.environment, c.loggersCache, c.groupsCache)
	}
	return snap
}

// fireResolvedLevelDeltas re-resolves every cached logger and fires a
// change listener for every logger whose effective level differs from
// the supplied snapshot. A logger that is no longer in loggersCache
// (i.e. removed server-side) fires no event for its own key — its
// dependents fire normal change events via the standard delta path
// instead.
func (c *LoggingClient) fireResolvedLevelDeltas(before map[string]LogLevel, source string) {
	after := c.snapshotResolvedLevels()
	for id, newLvl := range after {
		if oldLvl, ok := before[id]; !ok || oldLvl != newLvl {
			c.fireChangeListeners(id, source)
		}
	}
}

func (c *LoggingClient) fireChangeListeners(loggerID string, source string) {
	if loggerID == "" {
		return
	}

	var level *LogLevel
	if cached, ok := c.loggersCache[loggerID]; ok {
		resolved := resolveLoggerLevel(loggerID, c.environment, c.loggersCache, c.groupsCache)
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

// pendingCount returns the number of buffered entries awaiting flush.
func (b *loggerRegistrationBuffer) pendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}
