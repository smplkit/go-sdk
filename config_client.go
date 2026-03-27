package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// ConfigClient provides CRUD operations for config resources.
// Obtain one via Client.Config().
type ConfigClient struct {
	client *Client
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
	path := fmt.Sprintf("/api/v1/configs/%s", url.PathEscape(id))
	body, _, err := c.client.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}

	var resp jsonAPISingleResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	return resp.Data.toConfig(), nil
}

// GetByKey retrieves a config by its human-readable key.
// Uses the list endpoint with a filter[key] query parameter and returns the
// first match, or SmplNotFoundError if none match.
func (c *ConfigClient) GetByKey(ctx context.Context, key string) (*Config, error) {
	path := fmt.Sprintf("/api/v1/configs?%s", url.Values{
		"filter[key]": {key},
	}.Encode())

	body, _, err := c.client.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}

	var resp jsonAPIListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	if len(resp.Data) == 0 {
		return nil, &SmplNotFoundError{
			SmplError: SmplError{
				Message:    fmt.Sprintf("config with key %q not found", key),
				StatusCode: 404,
			},
		}
	}

	return resp.Data[0].toConfig(), nil
}

// Create creates a new config resource.
func (c *ConfigClient) Create(ctx context.Context, params CreateConfigParams) (*Config, error) {
	reqBody := jsonAPIRequest{
		Data: jsonAPIResourceRequest{
			Type: "config",
			Attributes: jsonAPIConfigAttrsReq{
				Name:        params.Name,
				Key:         params.Key,
				Description: params.Description,
				Parent:      params.Parent,
				Values:      params.Values,
			},
		},
	}

	body, _, err := c.client.doRequest(ctx, "POST", "/api/v1/configs", reqBody)
	if err != nil {
		return nil, err
	}

	var resp jsonAPISingleResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	return resp.Data.toConfig(), nil
}

// List returns all configs for the account.
func (c *ConfigClient) List(ctx context.Context) ([]*Config, error) {
	body, _, err := c.client.doRequest(ctx, "GET", "/api/v1/configs", nil)
	if err != nil {
		return nil, err
	}

	var resp jsonAPIListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	configs := make([]*Config, len(resp.Data))
	for i := range resp.Data {
		configs[i] = resp.Data[i].toConfig()
	}

	return configs, nil
}

// Delete removes a config by its UUID. Returns nil on success (HTTP 204).
func (c *ConfigClient) Delete(ctx context.Context, id string) error {
	path := fmt.Sprintf("/api/v1/configs/%s", url.PathEscape(id))
	_, _, err := c.client.doRequest(ctx, "DELETE", path, nil)
	return err
}
