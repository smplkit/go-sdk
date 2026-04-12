package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
)

// ConfigManagement provides CRUD operations for config resources.
// Obtain one via ConfigClient.Management().
type ConfigManagement struct {
	client *ConfigClient
}

// New creates an unsaved Config with the given ID. Call Save(ctx) to persist.
// If name is not provided via WithConfigName, it is auto-generated from the ID.
func (m *ConfigManagement) New(id string, opts ...ConfigOption) *Config {
	cfg := &Config{
		ID:           id,
		Name:         keyToDisplayName(id),
		Items:        map[string]interface{}{},
		Environments: map[string]map[string]interface{}{},
		client:       m.client,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// Get retrieves a config by its ID.
// Returns SmplNotFoundError if no match.
func (m *ConfigManagement) Get(ctx context.Context, id string) (*Config, error) {
	resp, err := m.client.generated.GetConfig(ctx, id)
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

	var result genconfig.ConfigResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	return resourceToConfig(result.Data, m.client), nil
}

// List returns all configs for the account.
func (m *ConfigManagement) List(ctx context.Context) ([]*Config, error) {
	resp, err := m.client.generated.ListConfigs(ctx, nil)
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

	var result genconfig.ConfigListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	configs := make([]*Config, len(result.Data))
	for i := range result.Data {
		configs[i] = resourceToConfig(result.Data[i], m.client)
	}
	return configs, nil
}

// Delete removes a config by its ID.
func (m *ConfigManagement) Delete(ctx context.Context, id string) error {
	resp, err := m.client.generated.DeleteConfig(ctx, id)
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
