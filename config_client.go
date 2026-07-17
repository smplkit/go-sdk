package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"

	"github.com/smplkit/go-sdk/v3/internal/debug"
	genconfig "github.com/smplkit/go-sdk/v3/internal/generated/config"
)

// configRegistrationFlushSize is the pending-queue size that triggers an
// immediate background flush of the config discovery buffer.
const configRegistrationFlushSize = 50

// ConfigChangeEvent describes a single value change detected on refresh.
type ConfigChangeEvent struct {
	// ConfigID is the config ID that changed (e.g. "user_service").
	ConfigID string
	// ItemKey is the item key within the config that changed.
	ItemKey string
	// OldValue is the value before the change (nil if the key was new).
	OldValue interface{}
	// NewValue is the value after the change (nil if the key was removed).
	NewValue interface{}
	// Source is "websocket" for server-pushed changes or "manual" for Refresh calls.
	Source string
}

type configChangeListener struct {
	configID string // "" matches all configs
	itemKey  string // "" matches all items
	cb       func(*ConfigChangeEvent)
}

// ConfigClient is the Smpl Config client.
//
// One client exposes the full surface, reachable as client.Config()
// (SmplClient) or constructed directly:
//
//	config, err := smplkit.NewConfigClient(smplkit.Config{Environment: "production", Service: "billing"})
//	billing := config.New("billing", smplkit.WithConfigName("Billing"))
//	billing.Save(ctx)
//	proxy, _ := config.Subscribe(ctx, "billing")
//	v, _ := proxy.Get("max_seats")
//
// The CRUD surface (New / Get / List / Delete and discovery) is pure
// CRUD. The live surface (Subscribe / GetValue / Bind / OnChange / Refresh)
// connects lazily on first use — the first call flushes discovery, fetches
// and resolves all configs into the local cache, and opens the live-updates
// WebSocket. No explicit install step is required.
type ConfigClient struct {
	client    *SmplClient // parent; nil when constructed standalone
	generated genconfig.ClientInterface

	// buffer holds pending config / item declarations for bulk-discovery
	// upload. Owned by this client.
	buffer *configRegistrationBuffer

	// Runtime / standalone configuration mirrored from the parent (or
	// resolved directly when standalone) so the live surface works in both
	// construction shapes.
	environment string
	service     string
	metrics     *metricsReporter
	appURL      string
	apiKey      string

	// disableStreaming mirrors Config.DisableStreaming: the live surface
	// fetches synchronously and never opens a WebSocket or spawns
	// background goroutines; threshold flushes run inline.
	disableStreaming bool

	configCache map[string]map[string]interface{}

	initOnce sync.Once
	initErr  error

	listenersMu sync.Mutex
	listeners   []configChangeListener

	// proxyCache returns the same *LiveConfig instance on repeat
	// Subscribe calls so callers can hold one as a parent reference.
	proxyCacheMu sync.Mutex
	proxyCache   map[string]*LiveConfig

	// bindings holds targets (struct pointers or string-keyed maps)
	// registered via Bind. WebSocket dispatch mutates these in place
	// when values change.
	bindingsMu sync.Mutex
	bindings   map[string]interface{}

	wsMu      sync.Mutex
	ownWS     *sharedWebSocket // standalone-owned WebSocket (nil when wired)
	wsManager *sharedWebSocket
}

// newConfigClient wires a ConfigClient onto a parent SmplClient (the common
// path), borrowing the parent's config transport, metrics, and WebSocket.
func newConfigClient(parent *SmplClient, gen genconfig.ClientInterface, metrics *metricsReporter) *ConfigClient {
	return &ConfigClient{
		client:           parent,
		generated:        gen,
		buffer:           newConfigRegistrationBuffer(),
		environment:      parent.environment,
		service:          parent.service,
		metrics:          metrics,
		disableStreaming: parent.disableStreaming,
	}
}

// NewConfigClient creates a standalone Smpl Config client that builds and
// owns its own config transport, and on first live use opens and owns its
// own WebSocket.
func NewConfigClient(cfg Config, opts ...ClientOption) (*ConfigClient, error) {
	rc, err := resolveConfig(cfg)
	if err != nil {
		return nil, err
	}
	if rc.debug {
		debug.Enable()
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
	configURL := serviceURL(optCfg, "config", rc)
	appURL := serviceURL(optCfg, "app", rc)
	// Extra headers first, then SDK headers (so SDK-owned headers win on a collision).
	extraHeaders := rc.extraHeaders
	extraEditor := genconfig.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	})
	headerEditor := genconfig.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	gen, _ := genconfig.NewClient(configURL, genconfig.WithHTTPClient(httpClient), extraEditor, headerEditor)
	return &ConfigClient{
		generated:        gen,
		buffer:           newConfigRegistrationBuffer(),
		environment:      rc.environment,
		service:          rc.service,
		appURL:           appURL,
		apiKey:           rc.apiKey,
		disableStreaming: rc.disableStreaming,
	}, nil
}

// ensureWS returns the shared WebSocket — the parent's when wired, else our own.
func (c *ConfigClient) ensureWS() *sharedWebSocket {
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

// ------------------------------------------------------------------
// CRUD surface (no live connection)
// ------------------------------------------------------------------

// New returns a new unsaved ConfigEntry. Call ConfigEntry.Save to persist.
// If name is not provided via WithConfigName it is auto-generated from the ID.
func (c *ConfigClient) New(id string, opts ...ConfigOption) *ConfigEntry {
	cfg := &ConfigEntry{
		ID:           id,
		Name:         keyToDisplayName(id),
		Items:        map[string]interface{}{},
		Environments: map[string]map[string]interface{}{},
		client:       c,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// Get fetches the editable ConfigEntry resource by id.
//
// Returns a NotFoundError if no config with that id exists. For a live,
// dict-like view of resolved values use Subscribe; for typed access via a
// bound struct/map use Bind.
func (c *ConfigClient) Get(ctx context.Context, id string) (*ConfigEntry, error) {
	resp, err := c.generated.GetConfig(ctx, id)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genconfig.ConfigResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	return resourceToConfig(result.Data, c), nil
}

// List returns one page of configs for the account.
//
// Without options the server applies its defaults (page 1, page size
// 1000). Use [WithPageNumber] / [WithPageSize] to walk additional pages.
func (c *ConfigClient) List(ctx context.Context, opts ...ListOption) ([]*ConfigEntry, error) {
	o := resolveListOptions(opts)
	params := &genconfig.ListConfigsParams{
		PageNumber: o.pageNumber,
		PageSize:   o.pageSize,
	}
	resp, err := c.generated.ListConfigs(ctx, params)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genconfig.ConfigListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	configs := make([]*ConfigEntry, len(result.Data))
	for i := range result.Data {
		configs[i] = resourceToConfig(result.Data[i], c)
	}
	return configs, nil
}

// Delete removes a config by id.
func (c *ConfigClient) Delete(ctx context.Context, id string) error {
	resp, err := c.generated.DeleteConfig(ctx, id)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	return checkStatus(resp.StatusCode, body)
}

// createConfig creates the config on the server and updates the local
// instance. Called from ConfigEntry.Save when CreatedAt is nil.
func (c *ConfigClient) createConfig(ctx context.Context, cfg *ConfigEntry) error {
	reqBody := buildConfigCreateRequest(cfg.ID, cfg.Name, cfg.Description, cfg.Parent, cfg.Items, cfg.itemsRaw, cfg.Environments)
	resp, err := c.generated.CreateConfigWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}
	var result genconfig.ConfigResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	cfg.apply(resourceToConfig(result.Data, c))
	return nil
}

// updateConfig updates the config on the server and updates the local
// instance. Called from ConfigEntry.Save when CreatedAt is set.
func (c *ConfigClient) updateConfig(ctx context.Context, cfg *ConfigEntry) error {
	reqBody := buildConfigRequest(cfg.ID, cfg.Name, cfg.Description, cfg.Parent, cfg.Items, cfg.itemsRaw, cfg.Environments)
	resp, err := c.generated.UpdateConfigWithApplicationVndAPIPlusJSONBody(ctx, cfg.ID, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}
	var result genconfig.ConfigResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	cfg.apply(resourceToConfig(result.Data, c))
	return nil
}

// ------------------------------------------------------------------
// CRUD surface: discovery buffer (owned directly)
// ------------------------------------------------------------------

// RegisterConfig queues a configuration declaration for bulk-discovery upload.
//
// The declaration is buffered and sent in the background; it surfaces the
// config in the smplkit console even if no values are set yet. configID is
// the config identifier (slug) being declared. service is the name of the
// service declaring the config. environment is the environment the
// declaration is scoped to. parent is an optional parent config id this
// config inherits from. name is an optional display name. description is an
// optional human-readable description.
func (c *ConfigClient) RegisterConfig(configID, service, environment, parent, name, description string) {
	c.buffer.declare(configID, configBufferMeta{
		service:     service,
		environment: environment,
		parent:      parent,
		name:        name,
		description: description,
	})
	c.maybeThresholdFlush()
}

// RegisterConfigItem queues a config item declaration. RegisterConfig must
// run first.
//
// The declaration is buffered and sent in the background, surfacing the item
// (with its type and default) in the smplkit console. configID is the config
// identifier (slug) the item belongs to. itemKey is the key of the item
// within the config. itemType is the item value type — one of "STRING",
// "NUMBER", "BOOLEAN", or "JSON". defaultVal is the in-code default value for
// the item. description is an optional human-readable description.
func (c *ConfigClient) RegisterConfigItem(configID, itemKey, itemType string, defaultVal interface{}, description string) {
	c.buffer.addItem(configID, itemKey, itemType, defaultVal, description)
	c.maybeThresholdFlush()
}

// maybeThresholdFlush flushes the discovery buffer once it crosses the batch
// threshold. Normally the flush runs on a background goroutine so declaring
// never blocks the caller; with Config.DisableStreaming set it runs inline
// instead — stateless mode spawns no goroutines. Flush is fire-and-forget
// either way (ADR-024 §2.9): failures never propagate to customer code.
func (c *ConfigClient) maybeThresholdFlush() {
	if c.buffer.pendingCount() < configRegistrationFlushSize {
		return
	}
	if c.disableStreaming {
		_ = c.Flush(context.Background())
		return
	}
	go func() { _ = c.Flush(context.Background()) }()
}

// PendingCount returns the number of pending config declarations awaiting flush.
func (c *ConfigClient) PendingCount() int {
	return c.buffer.pendingCount()
}

// Flush sends any queued config and item declarations to the server.
//
// Discovery is best-effort — failures here never propagate to your code.
// Drained entries are not requeued; the SDK re-observes them on the next
// process start.
func (c *ConfigClient) Flush(ctx context.Context) error {
	batch := c.buffer.drain()
	if len(batch) == 0 {
		return nil
	}
	configs := make([]genconfig.ConfigBulkItem, 0, len(batch))
	for _, entry := range batch {
		item := genconfig.ConfigBulkItem{Id: entry.id}
		if entry.service != "" {
			s := entry.service
			item.Service = &s
		}
		if entry.environment != "" {
			e := entry.environment
			item.Environment = &e
		}
		if entry.parent != "" {
			p := entry.parent
			item.Parent = &p
		}
		if entry.name != "" {
			n := entry.name
			item.Name = &n
		}
		if entry.description != "" {
			d := entry.description
			item.Description = &d
		}
		if len(entry.items) > 0 {
			items := make(map[string]genconfig.ConfigItemDefinition, len(entry.items))
			for k, v := range entry.items {
				def := genconfig.ConfigItemDefinition{}
				val := v.value
				def.Value = &val
				switch v.itemType {
				case "STRING":
					typed := genconfig.STRING
					def.Type = &typed
				case "NUMBER":
					typed := genconfig.NUMBER
					def.Type = &typed
				case "BOOLEAN":
					typed := genconfig.BOOLEAN
					def.Type = &typed
				case "JSON":
					typed := genconfig.JSON
					def.Type = &typed
				}
				if v.description != "" {
					d := v.description
					def.Description = &d
				}
				items[k] = def
			}
			item.Items = &items
		}
		configs = append(configs, item)
	}
	body := genconfig.ConfigBulkRequest{Configs: configs}
	resp, err := c.generated.BulkRegisterConfigsWithApplicationVndAPIPlusJSONBody(ctx, body)
	if err != nil {
		// Fire-and-forget per ADR-024 §2.9.
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return nil
}

// getByID retrieves a config by its ID (internal use for chain walking).
func (c *ConfigClient) getByID(ctx context.Context, id string) (*ConfigEntry, error) {
	resp, err := c.generated.GetConfig(ctx, id)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genconfig.ConfigResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	return resourceToConfig(result.Data, c), nil
}

// ------------------------------------------------------------------
// Live surface: subscribe, lazy connect
// ------------------------------------------------------------------

// Subscribe returns a live, dict-like, read-only LiveConfig proxy whose reads
// always reflect the latest resolved values for the given config id.
// WebSocket updates are picked up automatically. Subscribing registers the
// config declaration for code-first observability so the reference appears in
// the smplkit console.
//
// Connects lazily on first use — no explicit install step. Returns a
// NotFoundError if the config is unknown. For typed access via an
// in-place-mutated struct or map, use Bind instead.
func (c *ConfigClient) Subscribe(ctx context.Context, id string) (*LiveConfig, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	c.observeConfigDeclaration(id, "", "", "")
	if _, ok := c.configCache[id]; !ok {
		return nil, &NotFoundError{Base: Error{Message: fmt.Sprintf("config with id %q not found", id)}}
	}
	if c.metrics != nil {
		c.metrics.Record("config.resolutions", 1, "resolutions", map[string]string{"config": id})
	}
	return c.cachedProxy(id), nil
}

// cachedProxy returns the canonical *LiveConfig for an id, caching so
// repeat calls return the same handle (parent-by-reference support).
func (c *ConfigClient) cachedProxy(id string) *LiveConfig {
	c.proxyCacheMu.Lock()
	defer c.proxyCacheMu.Unlock()
	if c.proxyCache == nil {
		c.proxyCache = make(map[string]*LiveConfig)
	}
	if proxy, ok := c.proxyCache[id]; ok {
		return proxy
	}
	proxy := &LiveConfig{client: c, id: id}
	c.proxyCache[id] = proxy
	return proxy
}

// observeConfigDeclaration queues a config declaration with the owned
// discovery buffer. Called by Bind, Subscribe and GetValueOr.
func (c *ConfigClient) observeConfigDeclaration(configID, parent, name, description string) {
	c.RegisterConfig(configID, c.service, c.environment, parent, name, description)
}

// observeItemDeclaration queues a config item declaration with the owned
// discovery buffer. Called by Bind and GetValueOr.
func (c *ConfigClient) observeItemDeclaration(configID, itemKey, itemType string, defaultVal interface{}, description string) {
	c.RegisterConfigItem(configID, itemKey, itemType, defaultVal, description)
}

// ensureInit performs initialization on first runtime access.
//
// Flushes any buffered discovery declarations, fetches and resolves every
// config for the configured environment into the local cache, opens the
// shared WebSocket, and subscribes to config_changed / config_deleted /
// configs_changed events. Idempotent and internal — every live method calls
// it on first use, so the live surface auto-connects with no explicit step.
func (c *ConfigClient) ensureInit(ctx context.Context) error {
	c.initOnce.Do(func() {
		if c.client != nil {
			c.client.ensureStarted()
		}
		environment := c.environment

		// Flush any buffered discovery declarations BEFORE the initial
		// fetch so newly-bound configs appear in the cache on first read.
		// Per ADR-024 §2.9, Flush is fire-and-forget — failures never
		// propagate to customer code, so we don't observe its return.
		_ = c.Flush(ctx)

		debug.Debug("api", "fetching config definitions")
		configs, err := c.fetchAllConfigs(ctx)
		if err != nil {
			c.initErr = err
			return
		}
		debug.Debug("api", "fetched %d configs", len(configs))

		cache := make(map[string]map[string]interface{})
		for _, cfg := range configs {
			chain, fetchErr := c.fetchChain(ctx, cfg.ID)
			if fetchErr != nil {
				c.initErr = fetchErr
				return
			}
			cache[cfg.ID] = resolveChain(chain, environment)
		}
		c.configCache = cache

		// In stateless mode (Config.DisableStreaming) no WebSocket is opened
		// — Refresh re-fetches on demand.
		if c.disableStreaming {
			return
		}

		// Register WebSocket listeners for real-time config updates.
		ws := c.ensureWS()
		c.wsManager = ws
		ws.on("config_changed", c.handleConfigChanged)
		ws.on("config_deleted", c.handleConfigDeleted)
		ws.on("configs_changed", c.handleConfigsChanged)
	})
	return c.initErr
}

func (c *ConfigClient) handleConfigChanged(data map[string]interface{}) {
	configKey, _ := data["id"].(string)
	debug.Debug("websocket", "config_changed event received, key=%q", configKey)

	ctx := context.Background()
	environment := c.environment

	if c.configCache == nil {
		c.configCache = make(map[string]map[string]interface{})
	}

	oldResolved := c.configCache[configKey]

	chain, err := c.fetchChain(ctx, configKey)
	if err != nil {
		return
	}
	newResolved := resolveChain(chain, environment)

	if reflect.DeepEqual(oldResolved, newResolved) {
		return
	}

	oldCache := map[string]map[string]interface{}{configKey: oldResolved}
	newCache := map[string]map[string]interface{}{configKey: newResolved}

	c.configCache[configKey] = newResolved
	c.diffAndFire(oldCache, newCache, "websocket")
}

func (c *ConfigClient) handleConfigDeleted(data map[string]interface{}) {
	configKey, _ := data["id"].(string)
	debug.Debug("websocket", "config_deleted event received, key=%q", configKey)

	if c.configCache == nil {
		return
	}

	oldResolved, existed := c.configCache[configKey]
	if !existed {
		return
	}

	delete(c.configCache, configKey)

	oldCache := map[string]map[string]interface{}{configKey: oldResolved}
	newCache := map[string]map[string]interface{}{configKey: {}}
	c.diffAndFire(oldCache, newCache, "websocket")
}

func (c *ConfigClient) handleConfigsChanged(_ map[string]interface{}) {
	debug.Debug("websocket", "configs_changed event received")
	_ = c.Refresh(context.Background())
}

// Refresh re-fetches all configs and resolves current values.
// OnChange listeners fire for any values that changed.
func (c *ConfigClient) Refresh(ctx context.Context) error {
	if err := c.ensureInit(ctx); err != nil {
		return err
	}
	environment := c.environment
	if environment == "" {
		return &Error{Message: "No environment set."}
	}

	configs, err := c.fetchAllConfigs(ctx)
	if err != nil {
		return err
	}

	newCache := make(map[string]map[string]interface{})
	for _, cfg := range configs {
		chain, fetchErr := c.fetchChain(ctx, cfg.ID)
		if fetchErr != nil {
			return fetchErr
		}
		newCache[cfg.ID] = resolveChain(chain, environment)
	}
	oldCache := c.configCache
	c.configCache = newCache
	c.diffAndFire(oldCache, newCache, "manual")
	return nil
}

// OnChange registers a listener that fires when a config value changes.
// Use WithConfigID and/or WithItemKey to scope the listener.
func (c *ConfigClient) OnChange(cb func(*ConfigChangeEvent), opts ...ChangeListenerOption) {
	var cfg changeListenerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	c.listenersMu.Lock()
	c.listeners = append(c.listeners, configChangeListener{
		configID: cfg.configID,
		itemKey:  cfg.itemKey,
		cb:       cb,
	})
	c.listenersMu.Unlock()
}

// ChangeListenerOption configures an OnChange listener.
type ChangeListenerOption func(*changeListenerConfig)

type changeListenerConfig struct {
	configID string
	itemKey  string
}

// WithConfigID restricts the listener to changes in the given config.
func WithConfigID(id string) ChangeListenerOption {
	return func(c *changeListenerConfig) {
		c.configID = id
	}
}

// WithItemKey restricts the listener to changes of the given item key.
func WithItemKey(key string) ChangeListenerOption {
	return func(c *changeListenerConfig) {
		c.itemKey = key
	}
}

// diffAndFire compares old and new values, mutates any bound targets
// in place, and fires change listeners.
func (c *ConfigClient) diffAndFire(oldCache, newCache map[string]map[string]interface{}, source string) { //nolint:unparam // "websocket" source will be used when real-time config push is wired up
	c.listenersMu.Lock()
	listeners := make([]configChangeListener, len(c.listeners))
	copy(listeners, c.listeners)
	c.listenersMu.Unlock()

	allConfigKeys := make(map[string]struct{})
	for k := range oldCache {
		allConfigKeys[k] = struct{}{}
	}
	for k := range newCache {
		allConfigKeys[k] = struct{}{}
	}

	for cfgKey := range allConfigKeys {
		oldItems := oldCache[cfgKey]
		newItems := newCache[cfgKey]
		if oldItems == nil {
			oldItems = map[string]interface{}{}
		}
		if newItems == nil {
			newItems = map[string]interface{}{}
		}

		allItemKeys := make(map[string]struct{})
		for k := range oldItems {
			allItemKeys[k] = struct{}{}
		}
		for k := range newItems {
			allItemKeys[k] = struct{}{}
		}

		for iKey := range allItemKeys {
			oldVal := oldItems[iKey]
			newVal := newItems[iKey]
			if !reflect.DeepEqual(oldVal, newVal) {
				// Mutate any bound target in place FIRST so listeners
				// reading the bound object see the new value.
				c.mutateBoundTargetsForChanges(cfgKey, iKey, newVal)

				if c.metrics != nil {
					c.metrics.Record("config.changes", 1, "changes", map[string]string{"config": cfgKey})
				}
				if len(listeners) == 0 {
					continue
				}
				evt := &ConfigChangeEvent{
					ConfigID: cfgKey,
					ItemKey:  iKey,
					OldValue: oldVal,
					NewValue: newVal,
					Source:   source,
				}
				for _, l := range listeners {
					if l.configID != "" && l.configID != cfgKey {
						continue
					}
					if l.itemKey != "" && l.itemKey != iKey {
						continue
					}
					func() {
						defer func() { recover() }() //nolint:errcheck
						l.cb(evt)
					}()
				}
			}
		}
	}
}

// fetchAllConfigs walks every page of /configs and accumulates the full
// list. The server caps page size at fetchAllPageSize; we stop when a
// short page (fewer than fetchAllPageSize items) comes back.
func (c *ConfigClient) fetchAllConfigs(ctx context.Context) ([]*ConfigEntry, error) {
	var all []*ConfigEntry
	for page := 1; ; page++ {
		batch, err := c.List(ctx, WithPageNumber(page), WithPageSize(fetchAllPageSize))
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < fetchAllPageSize {
			return all, nil
		}
	}
}

// fetchChain fetches the full ancestor chain starting from rootID.
func (c *ConfigClient) fetchChain(ctx context.Context, rootID string) ([]chainEntry, error) {
	var chain []chainEntry
	currentID := rootID
	for currentID != "" {
		node, err := c.getByID(ctx, currentID)
		if err != nil {
			return nil, err
		}
		chain = append(chain, chainEntry{
			ID:           node.ID,
			Values:       node.Items,
			Environments: node.Environments,
		})
		if node.Parent == nil {
			break
		}
		currentID = *node.Parent
	}
	return chain, nil
}

// resourceToConfig converts a generated ConfigResource to the SDK
// ConfigEntry type. The client back-reference allows the active record to
// Save / Delete itself.
func resourceToConfig(r genconfig.ConfigResource, c *ConfigClient) *ConfigEntry {
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}
	rawItems := derefMap(attrs.Items)
	return &ConfigEntry{
		ID:           id,
		Name:         attrs.Name,
		Description:  attrs.Description,
		Parent:       attrs.Parent,
		Items:        extractItemValues(rawItems),
		itemsRaw:     toItemsRaw(rawItems),
		Environments: derefEnvs(attrs.Environments),
		CreatedAt:    attrs.CreatedAt,
		UpdatedAt:    attrs.UpdatedAt,
		client:       c,
	}
}

// toItemsRaw narrows the deref'd item map (each value already a typed
// {value, type, description} sub-map) into the typed itemsRaw shape.
func toItemsRaw(items map[string]interface{}) map[string]map[string]interface{} {
	if items == nil {
		return nil
	}
	out := make(map[string]map[string]interface{}, len(items))
	for k, v := range items {
		if m, ok := v.(map[string]interface{}); ok {
			out[k] = m
		} else {
			out[k] = map[string]interface{}{"value": v}
		}
	}
	return out
}

// buildConfigAttributes constructs the Config attribute payload shared
// by the create and update envelopes.
func buildConfigAttributes(name string, desc, parent *string, items map[string]interface{}, itemsRaw map[string]map[string]interface{}, envs map[string]map[string]interface{}) genconfig.Config {
	return genconfig.Config{
		Name:         name,
		Description:  desc,
		Parent:       parent,
		Items:        buildItemDefs(items, itemsRaw),
		Environments: refEnvs(envs),
	}
}

// buildConfigRequest constructs a ConfigRequest envelope used for updates.
func buildConfigRequest(id, name string, desc, parent *string, items map[string]interface{}, itemsRaw map[string]map[string]interface{}, envs map[string]map[string]interface{}) genconfig.ConfigRequest {
	return genconfig.ConfigRequest{
		Data: genconfig.ConfigResource{
			Id:         &id,
			Type:       genconfig.ConfigResourceTypeConfig,
			Attributes: buildConfigAttributes(name, desc, parent, items, itemsRaw, envs),
		},
	}
}

// buildConfigCreateRequest constructs a ConfigCreateRequest envelope.
// The create envelope requires a non-nullable string id (vs the update
// envelope, which optionally echoes the id).
func buildConfigCreateRequest(id, name string, desc, parent *string, items map[string]interface{}, itemsRaw map[string]map[string]interface{}, envs map[string]map[string]interface{}) genconfig.ConfigCreateRequest {
	return genconfig.ConfigCreateRequest{
		Data: genconfig.ConfigCreateResource{
			Id:         id,
			Type:       genconfig.ConfigCreateResourceTypeConfig,
			Attributes: buildConfigAttributes(name, desc, parent, items, itemsRaw, envs),
		},
	}
}

// derefMap converts the generated item-definition map into the typed
// in-memory shape, retaining each item's declared type and description so a
// get-mutate-put preserves them rather than re-inferring.
func derefMap(m *map[string]genconfig.ConfigItemDefinition) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{}, len(*m))
	for k, v := range *m {
		inner := map[string]interface{}{"value": v.Value}
		if v.Type != nil {
			inner["type"] = string(*v.Type)
		}
		if v.Description != nil {
			inner["description"] = *v.Description
		}
		result[k] = inner
	}
	return result
}

// buildItemDefs builds the generated item-definition map for the wire. The
// value comes from the flat items map (authoritative); the declared type and
// description come from the retained typed shape (itemsRaw) when present,
// falling back to inferring the type from the value otherwise.
func buildItemDefs(items map[string]interface{}, itemsRaw map[string]map[string]interface{}) *map[string]genconfig.ConfigItemDefinition {
	if items == nil {
		return nil
	}
	result := make(map[string]genconfig.ConfigItemDefinition, len(items))
	for k, v := range items {
		def := genconfig.ConfigItemDefinition{Value: v}
		typeStr := valueToItemType(v)
		if raw, ok := itemsRaw[k]; ok {
			if t, ok := raw["type"].(string); ok && t != "" {
				typeStr = t
			}
			if d, ok := raw["description"].(string); ok && d != "" {
				dd := d
				def.Description = &dd
			}
		}
		t := genconfig.ConfigItemDefinitionType(typeStr)
		def.Type = &t
		result[k] = def
	}
	return &result
}

// derefEnvs unwraps the pointer to the per-env override map from the
// generated client. The wire shape is now flat ({env: {key: rawValue}})
// per ADR-024 §2.4, so it matches the in-memory shape used by the SDK
// directly — no per-env envelope translation needed.
func derefEnvs(envs *map[string]map[string]interface{}) map[string]map[string]interface{} {
	if envs == nil {
		return nil
	}
	return *envs
}

// refEnvs pointer-wraps the in-memory per-env override map for the
// generated request envelope. Since the wire and in-memory shapes are
// both flat ({env: {key: rawValue}}), this is now a simple address-of.
func refEnvs(envs map[string]map[string]interface{}) *map[string]map[string]interface{} {
	if envs == nil {
		return nil
	}
	return &envs
}

func extractItemValues(items map[string]interface{}) map[string]interface{} {
	if items == nil {
		return nil
	}
	result := make(map[string]interface{}, len(items))
	for k, v := range items {
		if m, ok := v.(map[string]interface{}); ok {
			if val, exists := m["value"]; exists {
				result[k] = val
				continue
			}
		}
		result[k] = v
	}
	return result
}
