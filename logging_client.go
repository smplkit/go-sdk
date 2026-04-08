package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	genlogging "github.com/smplkit/go-sdk/internal/generated/logging"
)

// LoggingClient provides management and runtime operations for logging resources.
// Obtain one via Client.Logging().
type LoggingClient struct {
	client    *Client
	generated genlogging.ClientInterface

	// Runtime state
	startOnce    sync.Once
	started      bool
	loggersCache map[string]map[string]interface{} // key → logger data
	groupsCache  map[string]map[string]interface{} // id → group data

	// Change listeners
	listenersMu     sync.Mutex
	globalListeners []func(*LoggerChangeEvent)
	keyListeners    map[string][]func(*LoggerChangeEvent)

	// Registration buffer
	buffer    *loggerRegistrationBuffer
	flushDone chan struct{}

	wsManager *sharedWebSocket
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
	if c.flushDone != nil {
		close(c.flushDone)
		c.flushDone = nil
	}
}

// --- Factory methods ---

// New creates an unsaved Logger with the given key. Call Save(ctx) to persist.
// If name is not provided via WithLoggerName, it is auto-generated from the key.
func (c *LoggingClient) New(key string, opts ...LoggerOption) *Logger {
	l := &Logger{
		Key:          key,
		Name:         keyToDisplayName(key),
		Managed:      true,
		Environments: map[string]interface{}{},
		client:       c,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// NewGroup creates an unsaved LogGroup with the given key. Call Save(ctx) to persist.
func (c *LoggingClient) NewGroup(key string, opts ...LogGroupOption) *LogGroup {
	g := &LogGroup{
		Key:          key,
		Name:         keyToDisplayName(key),
		Environments: map[string]interface{}{},
		client:       c,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// --- Logger CRUD ---

// Get retrieves a logger by its key.
func (c *LoggingClient) Get(ctx context.Context, key string) (*Logger, error) {
	params := &genlogging.ListLoggersParams{FilterKey: &key}
	resp, err := c.generated.ListLoggers(ctx, params)
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

	if len(result.Data) == 0 {
		return nil, &SmplNotFoundError{
			SmplError: SmplError{
				Message:    fmt.Sprintf("logger with key %q not found", key),
				StatusCode: 404,
			},
		}
	}
	return resourceToLogger(result.Data[0], c), nil
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

// Delete removes a logger by its key.
func (c *LoggingClient) Delete(ctx context.Context, key string) error {
	logger, err := c.Get(ctx, key)
	if err != nil {
		return err
	}
	return c.deleteLoggerByID(ctx, logger.ID)
}

// --- LogGroup CRUD ---

// GetGroup retrieves a log group by its key.
func (c *LoggingClient) GetGroup(ctx context.Context, key string) (*LogGroup, error) {
	// The generated client doesn't have a filter param for groups,
	// so we list all and filter client-side.
	groups, err := c.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.Key == key {
			return g, nil
		}
	}
	return nil, &SmplNotFoundError{
		SmplError: SmplError{
			Message:    fmt.Sprintf("log group with key %q not found", key),
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

// DeleteGroup removes a log group by its key.
func (c *LoggingClient) DeleteGroup(ctx context.Context, key string) error {
	group, err := c.GetGroup(ctx, key)
	if err != nil {
		return err
	}
	return c.deleteGroupByID(ctx, group.ID)
}

// --- Runtime ---

// Start initializes the logging runtime. Idempotent.
// Fetches all logger/group definitions, resolves levels, opens WebSocket,
// and starts the periodic flush timer.
func (c *LoggingClient) Start(ctx context.Context) error {
	var startErr error
	c.startOnce.Do(func() {
		// Flush any loggers registered before Start.
		c.flushBuffer(ctx)

		// Fetch definitions.
		if err := c.fetchAndCache(ctx); err != nil {
			startErr = err
			return
		}

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
// Call before or after Start(). Names are normalized (slash/colon → dot, lowercase).
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

// --- Internal: CRUD helpers ---

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
	uid, err := uuid.Parse(l.ID)
	if err != nil {
		return fmt.Errorf("smplkit: invalid logger ID %q: %w", l.ID, err)
	}

	loggerType := "logger"
	reqBody := genlogging.ResponseLogger{
		Data: genlogging.ResourceLogger{
			Id:         &l.ID,
			Type:       &loggerType,
			Attributes: buildLoggerAttributes(l),
		},
	}

	resp, err := c.generated.UpdateLogger(ctx, uid, reqBody)
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
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("smplkit: invalid logger ID %q: %w", id, err)
	}
	resp, err := c.generated.DeleteLogger(ctx, uid)
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
	uid, err := uuid.Parse(g.ID)
	if err != nil {
		return fmt.Errorf("smplkit: invalid log group ID %q: %w", g.ID, err)
	}

	groupType := "log_group"
	reqBody := genlogging.ResponseLogGroup{
		Data: genlogging.ResourceLogGroup{
			Id:         &g.ID,
			Type:       &groupType,
			Attributes: buildLogGroupAttributes(g),
		},
	}

	resp, err := c.generated.UpdateLogGroup(ctx, uid, reqBody)
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
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("smplkit: invalid log group ID %q: %w", id, err)
	}
	resp, err := c.generated.DeleteLogGroup(ctx, uid)
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

// --- Internal: resource conversion ---

func resourceToLogger(r genlogging.LoggerResource, c *LoggingClient) *Logger {
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}
	key := ""
	if attrs.Key != nil {
		key = *attrs.Key
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
		Key:          key,
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
	key := ""
	if attrs.Key != nil {
		key = *attrs.Key
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
		Key:          key,
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
	key := l.Key
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
		Key:          &key,
		Name:         l.Name,
		Level:        level,
		Group:        l.Group,
		Managed:      &l.Managed,
		Sources:      sources,
		Environments: envs,
	}
}

func buildLogGroupAttributes(g *LogGroup) genlogging.LogGroup {
	key := g.Key
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
		Key:          &key,
		Name:         g.Name,
		Level:        level,
		Group:        g.Group,
		Environments: envs,
	}
}

// --- Internal: runtime helpers ---

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
			"key":          l.Key,
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
		loggersCache[l.Key] = entry
	}

	groupsCache := make(map[string]map[string]interface{}, len(groups))
	for _, g := range groups {
		entry := map[string]interface{}{
			"key":          g.Key,
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
	items := make([]genlogging.LoggerBulkItem, 0, len(batch))
	for _, entry := range batch {
		item := genlogging.LoggerBulkItem{
			Key:   entry.key,
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
	loggerKey, _ := data["key"].(string)
	if err := c.fetchAndCache(context.Background()); err != nil {
		return
	}
	c.fireChangeListeners(loggerKey, "websocket")
}

func (c *LoggingClient) fireChangeListeners(loggerKey string, source string) {
	if loggerKey == "" {
		return
	}

	var level *LogLevel
	if cached, ok := c.loggersCache[loggerKey]; ok {
		resolved := resolveLoggerLevel(loggerKey, c.client.environment, c.loggersCache, c.groupsCache)
		level = &resolved
		_ = cached
	}

	event := &LoggerChangeEvent{Key: loggerKey, Level: level, Source: source}

	c.listenersMu.Lock()
	globals := make([]func(*LoggerChangeEvent), len(c.globalListeners))
	copy(globals, c.globalListeners)
	keyListeners := make([]func(*LoggerChangeEvent), len(c.keyListeners[loggerKey]))
	copy(keyListeners, c.keyListeners[loggerKey])
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

// --- Logger registration buffer ---

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
