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
type ConfigManagement struct {
	gen     genconfig.ClientInterface
	runtime *ConfigClient // nil when constructed standalone via NewManagementClient

	// client is a backwards-compat alias for runtime that older code in
	// this package may still read. New code uses gen / runtime.
	client *ConfigClient
}

// newConfigManagement constructs a standalone ConfigManagement bound to
// the given generated client. Used by NewManagementClient to skip the
// runtime-client skeleton (rule 1).
func newConfigManagement(gen genconfig.ClientInterface) *ConfigManagement {
	return &ConfigManagement{gen: gen}
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
