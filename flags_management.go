package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	genapp "github.com/smplkit/go-sdk/internal/generated/app"
	genflags "github.com/smplkit/go-sdk/internal/generated/flags"
)

// FlagsManagement provides CRUD operations for flag resources.
// Obtain one via FlagsClient.Management().
type FlagsManagement struct {
	client *FlagsClient
}

// NewBooleanFlag creates an unsaved boolean flag. Call Save(ctx) to persist.
// If name is not provided via WithFlagName, it is auto-generated from the ID.
func (m *FlagsManagement) NewBooleanFlag(id string, defaultValue bool, opts ...FlagOption) *Flag {
	boolValues := []FlagValue{{Name: "True", Value: true}, {Name: "False", Value: false}}
	f := &Flag{
		ID:           id,
		Name:         keyToDisplayName(id),
		Type:         string(FlagTypeBoolean),
		Default:      defaultValue,
		Values:       &boolValues,
		Environments: map[string]interface{}{},
		client:       m.client,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// NewStringFlag creates an unsaved string flag. Call Save(ctx) to persist.
func (m *FlagsManagement) NewStringFlag(id string, defaultValue string, opts ...FlagOption) *Flag {
	f := &Flag{
		ID:           id,
		Name:         keyToDisplayName(id),
		Type:         string(FlagTypeString),
		Default:      defaultValue,
		Environments: map[string]interface{}{},
		client:       m.client,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// NewNumberFlag creates an unsaved numeric flag. Call Save(ctx) to persist.
func (m *FlagsManagement) NewNumberFlag(id string, defaultValue float64, opts ...FlagOption) *Flag {
	f := &Flag{
		ID:           id,
		Name:         keyToDisplayName(id),
		Type:         string(FlagTypeNumeric),
		Default:      defaultValue,
		Environments: map[string]interface{}{},
		client:       m.client,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// NewJsonFlag creates an unsaved JSON flag. Call Save(ctx) to persist.
func (m *FlagsManagement) NewJsonFlag(id string, defaultValue map[string]interface{}, opts ...FlagOption) *Flag {
	f := &Flag{
		ID:           id,
		Name:         keyToDisplayName(id),
		Type:         string(FlagTypeJSON),
		Default:      defaultValue,
		Environments: map[string]interface{}{},
		client:       m.client,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// Get retrieves a flag by its ID.
// Returns SmplNotFoundError if no match.
func (m *FlagsManagement) Get(ctx context.Context, id string) (*Flag, error) {
	resp, err := m.client.generated.GetFlag(ctx, id)
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

	var result genflags.FlagResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	return resourceToFlag(result.Data, m.client), nil
}

// List returns all flags for the account.
func (m *FlagsManagement) List(ctx context.Context) ([]*Flag, error) {
	resp, err := m.client.generated.ListFlags(ctx, nil)
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

	var result genflags.FlagListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	flags := make([]*Flag, len(result.Data))
	for i := range result.Data {
		flags[i] = resourceToFlag(result.Data[i], m.client)
	}
	return flags, nil
}

// Delete removes a flag by its ID.
func (m *FlagsManagement) Delete(ctx context.Context, id string) error {
	resp, err := m.client.generated.DeleteFlag(ctx, id)
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

// CreateContextType creates a new context type.
func (m *FlagsManagement) CreateContextType(ctx context.Context, id string, name string) (*ContextType, error) {
	reqBody := genapp.ContextTypeResponse{
		Data: genapp.ContextTypeResource{
			Type:       "context_type",
			Attributes: genapp.ContextType{Id: &id, Name: name},
		},
	}
	resp, err := m.client.appGenerated.CreateContextTypeWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
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
	return parseContextType(body)
}

// UpdateContextType updates a context type's attributes.
func (m *FlagsManagement) UpdateContextType(ctx context.Context, ctID string, attributes map[string]interface{}) (*ContextType, error) {
	reqBody := genapp.ContextTypeResponse{
		Data: genapp.ContextTypeResource{
			Type:       "context_type",
			Attributes: genapp.ContextType{Attributes: &attributes},
		},
	}
	resp, err := m.client.appGenerated.UpdateContextTypeWithApplicationVndAPIPlusJSONBody(ctx, ctID, reqBody)
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
	return parseContextType(body)
}

// ListContextTypes lists all context types.
func (m *FlagsManagement) ListContextTypes(ctx context.Context) ([]*ContextType, error) {
	resp, err := m.client.appGenerated.ListContextTypes(ctx)
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

	var result genapp.ContextTypeListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	types := make([]*ContextType, 0, len(result.Data))
	for _, r := range result.Data {
		ct := &ContextType{
			Name: r.Attributes.Name,
		}
		if r.Id != nil {
			ct.ID = *r.Id
		}
		if r.Attributes.Attributes != nil {
			ct.Attributes = *r.Attributes.Attributes
		}
		types = append(types, ct)
	}
	return types, nil
}

// DeleteContextType deletes a context type by its ID.
func (m *FlagsManagement) DeleteContextType(ctx context.Context, ctID string) error {
	resp, err := m.client.appGenerated.DeleteContextType(ctx, ctID)
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

// ListContexts lists context instances filtered by context type key.
func (m *FlagsManagement) ListContexts(ctx context.Context, contextTypeKey string) ([]map[string]interface{}, error) {
	params := &genapp.ListContextsParams{
		FilterContextType: &contextTypeKey,
	}
	resp, err := m.client.appGenerated.ListContexts(ctx, params)
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

	var result struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	return result.Data, nil
}
