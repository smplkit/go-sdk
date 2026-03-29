package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/uuid"

	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
)

// ConfigClient provides CRUD operations for config resources.
// Obtain one via Client.Config().
type ConfigClient struct {
	client    *Client
	generated genconfig.ClientInterface
}

// Get retrieves a single config using functional options. Exactly one of
// WithKey or WithID must be provided.
//
//	cfg, err := client.Config().Get(ctx, smplkit.WithKey("my-service"))
//	cfg, err := client.Config().Get(ctx, smplkit.WithID("uuid-here"))
func (c *ConfigClient) Get(ctx context.Context, opts ...GetOption) (*Config, error) {
	var gc getConfig
	for _, opt := range opts {
		opt(&gc)
	}

	if (gc.key == nil) == (gc.id == nil) {
		return nil, fmt.Errorf("smplkit: exactly one of WithKey or WithID must be provided")
	}

	if gc.id != nil {
		return c.GetByID(ctx, *gc.id)
	}
	return c.GetByKey(ctx, *gc.key)
}

// GetByID retrieves a config by its UUID.
func (c *ConfigClient) GetByID(ctx context.Context, id string) (*Config, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("smplkit: invalid config ID %q: %w", id, err)
	}

	resp, err := c.generated.GetConfig(ctx, uid)
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
	return resourceToConfig(result.Data, c), nil
}

// GetByKey retrieves a config by its human-readable key.
// Uses the list endpoint with a filter[key] query parameter and returns the
// first match, or SmplNotFoundError if none match.
func (c *ConfigClient) GetByKey(ctx context.Context, key string) (*Config, error) {
	params := &genconfig.ListConfigsParams{FilterKey: &key}
	resp, err := c.generated.ListConfigs(ctx, params)
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

	if len(result.Data) == 0 {
		return nil, &SmplNotFoundError{
			SmplError: SmplError{
				Message:    fmt.Sprintf("config with key %q not found", key),
				StatusCode: 404,
			},
		}
	}
	return resourceToConfig(result.Data[0], c), nil
}

// Create creates a new config resource.
func (c *ConfigClient) Create(ctx context.Context, params CreateConfigParams) (*Config, error) {
	reqBody := buildConfigRequest("", params.Name, params.Key, params.Description, params.Parent, params.Values, params.Environments)

	// Pre-validate marshaling to give a clear error message.
	if _, err := json.Marshal(reqBody); err != nil {
		return nil, fmt.Errorf("smplkit: failed to marshal request body: %w", err)
	}

	resp, err := c.generated.CreateConfig(ctx, genconfig.CreateConfigJSONRequestBody(reqBody))
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
	return resourceToConfig(result.Data, c), nil
}

// List returns all configs for the account.
func (c *ConfigClient) List(ctx context.Context) ([]*Config, error) {
	resp, err := c.generated.ListConfigs(ctx, nil)
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
		configs[i] = resourceToConfig(result.Data[i], c)
	}
	return configs, nil
}

// Delete removes a config by its UUID. Returns nil on success (HTTP 204).
func (c *ConfigClient) Delete(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("smplkit: invalid config ID %q: %w", id, err)
	}

	resp, err := c.generated.DeleteConfig(ctx, uid)
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

// updateByID sends a PUT request to replace the config identified by id.
func (c *ConfigClient) updateByID(ctx context.Context, id, name, key string, desc, parent *string, values map[string]interface{}, envs map[string]map[string]interface{}) (*Config, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("smplkit: invalid config ID %q: %w", id, err)
	}

	reqBody := buildConfigRequest(id, name, &key, desc, parent, values, envs)

	if _, err := json.Marshal(reqBody); err != nil {
		return nil, fmt.Errorf("smplkit: failed to marshal request body: %w", err)
	}

	resp, err := c.generated.UpdateConfig(ctx, uid, genconfig.UpdateConfigJSONRequestBody(reqBody))
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
	return resourceToConfig(result.Data, c), nil
}

// fetchChain fetches the full ancestor chain starting from rootID.
// Returns [rootID's config, its parent, grandparent, ...] in child→root order.
// Always makes live HTTP calls so callers always get fresh data.
func (c *ConfigClient) fetchChain(ctx context.Context, rootID string) ([]chainEntry, error) {
	var chain []chainEntry
	currentID := rootID
	for currentID != "" {
		node, err := c.GetByID(ctx, currentID)
		if err != nil {
			return nil, err
		}
		chain = append(chain, chainEntry{
			ID:           node.ID,
			Values:       node.Values,
			Environments: node.Environments,
		})
		if node.Parent == nil {
			break
		}
		currentID = *node.Parent
	}
	return chain, nil
}

// connect builds a ConfigRuntime for cfg in the given environment.
func (c *ConfigClient) connect(ctx context.Context, cfg *Config, environment string) (*ConfigRuntime, error) {
	chain, err := c.fetchChain(ctx, cfg.ID)
	if err != nil {
		return nil, err
	}

	cache := resolveChain(chain, environment)
	rootID := cfg.ID

	rt := newConfigRuntime(cfg.ID, environment, cache, func() ([]chainEntry, error) {
		return c.fetchChain(context.Background(), rootID)
	}, c.client.apiKey, c.client.baseURL)

	go rt.wsLoop()
	return rt, nil
}

// resourceToConfig converts a generated ConfigResource to the SDK Config type.
func resourceToConfig(r genconfig.ConfigResource, c *ConfigClient) *Config {
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}
	key := ""
	if attrs.Key != nil {
		key = *attrs.Key
	}
	return &Config{
		ID:           id,
		Key:          key,
		Name:         attrs.Name,
		Description:  attrs.Description,
		Parent:       attrs.Parent,
		Values:       derefMap(attrs.Values),
		Environments: derefEnvs(attrs.Environments),
		CreatedAt:    attrs.CreatedAt,
		UpdatedAt:    attrs.UpdatedAt,
		client:       c,
	}
}

// buildConfigRequest constructs a ResponseConfig for create or update.
// Pass empty id for create (omitted in JSON).
func buildConfigRequest(id, name string, key, desc, parent *string, values map[string]interface{}, envs map[string]map[string]interface{}) genconfig.ResponseConfig {
	var idPtr *string
	if id != "" {
		idPtr = &id
	}
	configType := "config"
	return genconfig.ResponseConfig{
		Data: genconfig.ResourceConfig{
			Id:   idPtr,
			Type: &configType,
			Attributes: genconfig.Config{
				Name:         name,
				Key:          key,
				Description:  desc,
				Parent:       parent,
				Values:       refMap(values),
				Environments: refEnvs(envs),
			},
		},
	}
}

// derefMap converts *map[string]interface{} to map[string]interface{}.
func derefMap(m *map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	return *m
}

// refMap converts map[string]interface{} to *map[string]interface{}.
func refMap(m map[string]interface{}) *map[string]interface{} {
	if m == nil {
		return nil
	}
	return &m
}

// derefEnvs converts *map[string]interface{} to map[string]map[string]interface{}.
func derefEnvs(envs *map[string]interface{}) map[string]map[string]interface{} {
	if envs == nil {
		return nil
	}
	result := make(map[string]map[string]interface{})
	for k, v := range *envs {
		if m, ok := v.(map[string]interface{}); ok {
			result[k] = m
		}
	}
	return result
}

// refEnvs converts map[string]map[string]interface{} to *map[string]interface{}.
func refEnvs(envs map[string]map[string]interface{}) *map[string]interface{} {
	if envs == nil {
		return nil
	}
	result := make(map[string]interface{})
	for k, v := range envs {
		result[k] = v
	}
	return &result
}
