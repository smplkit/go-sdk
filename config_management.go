package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	genconfig "github.com/smplkit/go-sdk/v3/internal/generated/config"
)

// ConfigManagement provides CRUD operations for config resources.
// Obtain one via Client.Manage().Config() or ConfigClient.Management().
//
// The struct owns its generated API client directly; the optional
// runtime back-reference is set only when ConfigManagement is wired
// into a runtime Client so active-record Save/Delete can refresh the
// runtime resolved-config cache.
const configRegistrationFlushSize = 50

type ConfigManagement struct {
	gen     genconfig.ClientInterface
	runtime *ConfigClient // nil when constructed standalone via NewManagementClient

	// client is a backwards-compat alias for runtime that older code in
	// this package may still read. New code uses gen / runtime.
	client *ConfigClient

	// buffer holds pending config / item declarations for bulk-discovery
	// upload. Populated by ConfigClient.GetOrCreate and by the typed
	// getters on LiveConfig.
	buffer *configRegistrationBuffer
}

// newConfigManagement constructs a standalone ConfigManagement bound to
// the given generated client. Used by NewManagementClient to skip the
// runtime-client skeleton (rule 1).
func newConfigManagement(gen genconfig.ClientInterface) *ConfigManagement {
	return &ConfigManagement{gen: gen, buffer: newConfigRegistrationBuffer()}
}

// attachRuntime links a runtime ConfigClient. Called once by NewClient.
func (m *ConfigManagement) attachRuntime(c *ConfigClient) {
	m.runtime = c
	m.client = c
}

// New creates an unsaved ConfigEntry with the given ID. Call Save(ctx) to persist.
// If name is not provided via WithConfigName, it is auto-generated from the ID.
func (m *ConfigManagement) New(id string, opts ...ConfigOption) *ConfigEntry {
	cfg := &ConfigEntry{
		ID:           id,
		Name:         keyToDisplayName(id),
		Items:        map[string]interface{}{},
		Environments: map[string]map[string]interface{}{},
		client:       m,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// createConfig creates the config on the server and updates the local
// instance. Called from ConfigEntry.Save when CreatedAt is nil.
func (m *ConfigManagement) createConfig(ctx context.Context, cfg *ConfigEntry) error {
	reqBody := buildConfigRequest(cfg.ID, cfg.Name, cfg.Description, cfg.Parent, cfg.Items, cfg.Environments)

	resp, err := m.gen.CreateConfigWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
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
	cfg.apply(resourceToConfig(result.Data, m))
	return nil
}

// updateConfig updates the config on the server and updates the local
// instance. Called from ConfigEntry.Save when CreatedAt is set.
func (m *ConfigManagement) updateConfig(ctx context.Context, cfg *ConfigEntry) error {
	reqBody := buildConfigRequest(cfg.ID, cfg.Name, cfg.Description, cfg.Parent, cfg.Items, cfg.Environments)

	resp, err := m.gen.UpdateConfigWithApplicationVndAPIPlusJSONBody(ctx, cfg.ID, reqBody)
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
	cfg.apply(resourceToConfig(result.Data, m))
	return nil
}

// Get retrieves a config by its ID.
// Returns NotFoundError if no match.
func (m *ConfigManagement) Get(ctx context.Context, id string) (*ConfigEntry, error) {
	resp, err := m.gen.GetConfig(ctx, id)
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

	var result genconfig.ConfigResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	return resourceToConfig(result.Data, m), nil
}

// List returns one page of configs for the account.
//
// Without options the server applies its defaults (page 1, page size
// 1000). Use [WithPageNumber] / [WithPageSize] to walk additional
// pages. The wrapper does not loop — callers that want every config
// should iterate until a short page is returned.
func (m *ConfigManagement) List(ctx context.Context, opts ...ListOption) ([]*ConfigEntry, error) {
	o := resolveListOptions(opts)
	params := &genconfig.ListConfigsParams{
		PageNumber: o.pageNumber,
		PageSize:   o.pageSize,
	}
	resp, err := m.gen.ListConfigs(ctx, params)
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

	var result genconfig.ConfigListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	configs := make([]*ConfigEntry, len(result.Data))
	for i := range result.Data {
		configs[i] = resourceToConfig(result.Data[i], m)
	}
	return configs, nil
}

// Delete removes a config by its ID.
// RegisterConfig queues a configuration declaration for bulk-discovery
// upload. Internal — called by ConfigClient.GetOrCreate.
func (m *ConfigManagement) RegisterConfig(configID, service, environment, parent, name, description string) {
	if m.buffer == nil {
		m.buffer = newConfigRegistrationBuffer()
	}
	m.buffer.declare(configID, configBufferMeta{
		service:     service,
		environment: environment,
		parent:      parent,
		name:        name,
		description: description,
	})
	if m.buffer.pendingCount() >= configRegistrationFlushSize {
		go func() { _ = m.Flush(context.Background()) }()
	}
}

// RegisterConfigItem queues a config item declaration for bulk-discovery
// upload. Internal — called by LiveConfig typed getters.
func (m *ConfigManagement) RegisterConfigItem(configID, itemKey, itemType string, defaultVal interface{}, description string) {
	if m.buffer == nil {
		m.buffer = newConfigRegistrationBuffer()
	}
	m.buffer.addItem(configID, itemKey, itemType, defaultVal, description)
	if m.buffer.pendingCount() >= configRegistrationFlushSize {
		go func() { _ = m.Flush(context.Background()) }()
	}
}

// PendingCount returns the number of pending config declarations
// awaiting flush.
func (m *ConfigManagement) PendingCount() int {
	if m.buffer == nil {
		return 0
	}
	return m.buffer.pendingCount()
}

// Flush sends any pending declarations to POST /api/v1/configs/bulk.
// Per ADR-024 §2.9 the bulk endpoint is plan-limit-exempt; failures
// here never propagate to customer code. Drained entries are not
// requeued.
func (m *ConfigManagement) Flush(ctx context.Context) error {
	if m.buffer == nil {
		return nil
	}
	batch := m.buffer.drain()
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
				// Cast the default value into the wire-typed Value field.
				val := v.value
				def.Value = &val
				// Map our internal type strings to the generated enum if
				// the generator exposes one; otherwise pass-through.
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
	resp, err := m.gen.BulkRegisterConfigsWithApplicationVndAPIPlusJSONBody(ctx, body)
	if err != nil {
		// Fire-and-forget per ADR-024 §2.9.
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return nil
}

func (m *ConfigManagement) Delete(ctx context.Context, id string) error {
	resp, err := m.gen.DeleteConfig(ctx, id)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{
			Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	return checkStatus(resp.StatusCode, body)
}
