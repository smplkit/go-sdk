package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	genlogging "github.com/smplkit/go-sdk/internal/generated/logging"
	"github.com/smplkit/go-sdk/logging/adapters"
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
	for _, adapter := range c.adapters {
		adapter.UninstallHook()
	}
	if c.flushDone != nil {
		close(c.flushDone)
		c.flushDone = nil
	}
}

// New creates an unsaved Logger with the given ID. Call Save(ctx) to persist.
// If name is not provided via WithLoggerName, it is auto-generated from the ID.
func (c *LoggingClient) New(id string, opts ...LoggerOption) *Logger {
	l := &Logger{
		ID:           id,
		Name:         keyToDisplayName(id),
		Managed:      true,
		Environments: map[string]interface{}{},
		client:       c,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// NewGroup creates an unsaved LogGroup with the given ID. Call Save(ctx) to persist.
func (c *LoggingClient) NewGroup(id string, opts ...LogGroupOption) *LogGroup {
	g := &LogGroup{
		ID:           id,
		Name:         keyToDisplayName(id),
		Environments: map[string]interface{}{},
		client:       c,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Get retrieves a logger by its ID.
func (c *LoggingClient) Get(ctx context.Context, id string) (*Logger, error) {
	resp, err := c.generated.GetLogger(ctx, id)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genlogging.LoggerResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	return resourceToLogger(result.Data, c), nil
}

// List returns all loggers for the account.
func (c *LoggingClient) List(ctx context.Context) ([]*Logger, error) {
	resp, err := c.generated.ListLoggers(ctx, nil)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genlogging.LoggerListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	loggers := make([]*Logger, len(result.Data))
	for i := range result.Data {
		loggers[i] = resourceToLogger(result.Data[i], c)
	}
	return loggers, nil
}

// Delete removes a logger by its ID.
func (c *LoggingClient) Delete(ctx context.Context, id string) error {
	return c.deleteLoggerByID(ctx, id)
}

// GetGroup retrieves a log group by its ID.
func (c *LoggingClient) GetGroup(ctx context.Context, id string) (*LogGroup, error) {
	// The generated client doesn't have a direct get-by-id for groups,
	// so we list all and filter client-side.
	groups, err := c.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.ID == id {
			return g, nil
		}
	}
	return nil, &SmplNotFoundError{
		SmplError: SmplError{
			Message:    fmt.Sprintf("log group with id %q not found", id),
			StatusCode: 404,
		},
	}
}

// ListGroups returns all log groups for the account.
func (c *LoggingClient) ListGroups(ctx context.Context) ([]*LogGroup, error) {
	resp, err := c.generated.ListLogGroups(ctx)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genlogging.LogGroupListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	groups := make([]*LogGroup, len(result.Data))
	for i := range result.Data {
		groups[i] = resourceToLogGroup(result.Data[i], c)
	}
	return groups, nil
}

// DeleteGroup removes a log group by its ID.
func (c *LoggingClient) DeleteGroup(ctx context.Context, id string) error {
	return c.deleteGroupByID(ctx, id)
}

// RegisterAdapter registers a logging adapter. Must be called before Start().
// At least one adapter must be registered for runtime features to function.
func (c *LoggingClient) RegisterAdapter(adapter adapters.LoggingAdapter) {
	if c.started {
		panic("smplkit: cannot register adapters after Start()")
	}
	c.adapters = append(c.adapters, adapter)
}

// Start initializes the logging runtime and begins listening for level changes.
// Safe to call multiple times; only the first call takes effect.
func (c *LoggingClient) Start(ctx context.Context) error {
	var startErr error
	c.startOnce.Do(func() {
		// Warn if no adapters registered.
		if len(c.adapters) == 0 {
			log.Println("smplkit: no logging adapters registered — framework-level control disabled")
		}

		// Discover loggers from all adapters and buffer them for bulk registration.
		for _, adapter := range c.adapters {
			discovered := adapter.Discover()
			for _, dl := range discovered {
				normalized := NormalizeLoggerName(dl.Name)
				if normalized == "" {
					continue
				}
				c.buffer.add(normalized, dl.Level, c.client.service)
			}
		}

		// Install hooks on all adapters.
		for _, adapter := range c.adapters {
			adapter.InstallHook(c.onNewLogger)
		}

		// Flush any loggers registered before Start (including discovered ones).
		c.flushBuffer(ctx)

		// Fetch definitions.
		if err := c.fetchAndCache(ctx); err != nil {
			startErr = err
			return
		}

		// Apply resolved levels to all adapters.
		c.applyLevels()

		// Open WebSocket and register listeners.
		ws := c.client.ensureWS()
		c.wsManager = ws
		ws.on("logger_changed", c.handleLoggerChanged)

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
	c.buffer.add(normalized, string(level), c.client.service)
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
		t.adapter.ApplyLevel(t.loggerName, string(resolved))
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
	c.buffer.add(normalized, level, c.client.service)

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

func (c *LoggingClient) createLogger(ctx context.Context, l *Logger) error {
	loggerType := "logger"
	reqBody := genlogging.ResponseLogger{
		Data: genlogging.ResourceLogger{
			Type:       &loggerType,
			Attributes: buildLoggerAttributes(l),
		},
	}

	resp, err := c.generated.CreateLogger(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var result genlogging.LoggerResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	l.apply(resourceToLogger(result.Data, c))
	return nil
}

func (c *LoggingClient) updateLogger(ctx context.Context, l *Logger) error {
	loggerType := "logger"
	reqBody := genlogging.ResponseLogger{
		Data: genlogging.ResourceLogger{
			Id:         &l.ID,
			Type:       &loggerType,
			Attributes: buildLoggerAttributes(l),
		},
	}

	resp, err := c.generated.UpdateLogger(ctx, l.ID, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var result genlogging.LoggerResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	l.apply(resourceToLogger(result.Data, c))
	return nil
}

func (c *LoggingClient) deleteLoggerByID(ctx context.Context, id string) error {
	resp, err := c.generated.DeleteLogger(ctx, id)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	return checkStatus(resp.StatusCode, body)
}

func (c *LoggingClient) createGroup(ctx context.Context, g *LogGroup) error {
	groupType := "log_group"
	reqBody := genlogging.ResponseLogGroup{
		Data: genlogging.ResourceLogGroup{
			Type:       &groupType,
			Attributes: buildLogGroupAttributes(g),
		},
	}

	resp, err := c.generated.CreateLogGroup(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var result genlogging.LogGroupResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	g.apply(resourceToLogGroup(result.Data, c))
	return nil
}

func (c *LoggingClient) updateGroup(ctx context.Context, g *LogGroup) error {
	groupType := "log_group"
	reqBody := genlogging.ResponseLogGroup{
		Data: genlogging.ResourceLogGroup{
			Id:         &g.ID,
			Type:       &groupType,
			Attributes: buildLogGroupAttributes(g),
		},
	}

	resp, err := c.generated.UpdateLogGroup(ctx, g.ID, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var result genlogging.LogGroupResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	g.apply(resourceToLogGroup(result.Data, c))
	return nil
}

func (c *LoggingClient) deleteGroupByID(ctx context.Context, id string) error {
	resp, err := c.generated.DeleteLogGroup(ctx, id)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	return checkStatus(resp.StatusCode, body)
}

func resourceToLogger(r genlogging.LoggerResource, c *LoggingClient) *Logger {
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
		client:       c,
	}
}

func resourceToLogGroup(r genlogging.LogGroupResource, c *LoggingClient) *LogGroup {
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
		Group:        attrs.Group,
		Environments: envs,
		CreatedAt:    attrs.CreatedAt,
		UpdatedAt:    attrs.UpdatedAt,
		client:       c,
	}
}

func buildLoggerAttributes(l *Logger) genlogging.Logger {
	id := l.ID
	var level *string
	if l.Level != nil {
		s := string(*l.Level)
		level = &s
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
		Id:           &id,
		Name:         l.Name,
		Level:        level,
		Group:        l.Group,
		Managed:      &l.Managed,
		Sources:      sources,
		Environments: envs,
	}
}

func buildLogGroupAttributes(g *LogGroup) genlogging.LogGroup {
	id := g.ID
	var level *string
	if g.Level != nil {
		s := string(*g.Level)
		level = &s
	}
	var envs *map[string]interface{}
	if g.Environments != nil {
		envs = &g.Environments
	}
	return genlogging.LogGroup{
		Id:           &id,
		Name:         g.Name,
		Level:        level,
		Group:        g.Group,
		Environments: envs,
	}
}

func (c *LoggingClient) fetchAndCache(ctx context.Context) error {
	loggers, err := c.List(ctx)
	if err != nil {
		return err
	}
	groups, err := c.ListGroups(ctx)
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

func (c *LoggingClient) flushBuffer(ctx context.Context) {
	batch := c.buffer.drain()
	if len(batch) == 0 {
		return
	}
	if metrics := c.client.metrics; metrics != nil && len(batch) > 0 {
		metrics.Record("logging.loggers_discovered", len(batch), "loggers", nil)
	}
	items := make([]genlogging.LoggerBulkItem, 0, len(batch))
	for _, entry := range batch {
		item := genlogging.LoggerBulkItem{
			Id:    entry.key,
			Level: entry.level,
		}
		if entry.service != "" {
			item.Service = &entry.service
		}
		items = append(items, item)
	}
	reqBody := genlogging.LoggerBulkRequest{Loggers: items}
	resp, err := c.generated.BulkRegisterLoggers(ctx, reqBody)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}

func (c *LoggingClient) periodicFlush(done chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			c.flushBuffer(context.Background())
		}
	}
}

func (c *LoggingClient) handleLoggerChanged(data map[string]interface{}) {
	loggerID, _ := data["id"].(string)
	if loggerID == "" {
		loggerID, _ = data["key"].(string)
	}
	if err := c.fetchAndCache(context.Background()); err != nil {
		return
	}
	c.applyLevels()
	c.fireChangeListeners(loggerID, "websocket")
}

func (c *LoggingClient) fireChangeListeners(loggerID string, source string) { //nolint:unparam // "refresh" source will be used when Refresh() is implemented
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
	key     string
	level   string
	service string
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

func (b *loggerRegistrationBuffer) add(key, level, service string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.seen[key]; ok {
		return
	}
	b.seen[key] = struct{}{}
	b.pending = append(b.pending, loggerRegistrationEntry{
		key:     key,
		level:   level,
		service: service,
	})
}

func (b *loggerRegistrationBuffer) drain() []loggerRegistrationEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.pending
	b.pending = nil
	return batch
}
