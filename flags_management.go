package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
	genflags "github.com/smplkit/go-sdk/v3/internal/generated/flags"
)

// FlagsManagement provides CRUD operations for flag resources.
// Obtain one via Client.Manage().Flags() or FlagsClient.Management().
//
// Owns its generated API client directly (no runtime-skeleton
// dependency); the optional runtime back-reference is set only when
// wired into a runtime Client so context-type CRUD (which lives on the
// app service) and runtime cache invalidation work via the live
// FlagsClient.
type FlagsManagement struct {
	gen     genflags.ClientInterface
	appGen  genapp.ClientInterface
	runtime *FlagsClient

	// client is a backwards-compat alias for runtime.
	client *FlagsClient
}

// newFlagsManagement constructs a standalone FlagsManagement bound to
// the given generated clients.
func newFlagsManagement(gen genflags.ClientInterface, appGen genapp.ClientInterface) *FlagsManagement {
	return &FlagsManagement{gen: gen, appGen: appGen}
}

// attachRuntime links a runtime FlagsClient.
func (m *FlagsManagement) attachRuntime(c *FlagsClient) {
	m.runtime = c
	m.client = c
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
		client:       m,
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
		client:       m,
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
		client:       m,
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
		client:       m,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// createFlag creates the flag on the server and updates the local
// instance. Called from Flag.Save when CreatedAt is nil.
func (m *FlagsManagement) createFlag(ctx context.Context, flag *Flag) error {
	reqBody := buildFlagRequest(flag.ID, flag.Name, flag.Type, flag.Default, flag.Values, flag.Description, flag.Environments)
	resp, err := m.gen.CreateFlagWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
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
	var result genflags.FlagResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	flag.apply(resourceToFlag(result.Data, m))
	return nil
}

// updateFlag updates the flag on the server and updates the local
// instance. Called from Flag.Save when CreatedAt is set.
func (m *FlagsManagement) updateFlag(ctx context.Context, flag *Flag) error {
	reqBody := buildFlagRequest(flag.ID, flag.Name, flag.Type, flag.Default, flag.Values, flag.Description, flag.Environments)
	resp, err := m.gen.UpdateFlagWithApplicationVndAPIPlusJSONBody(ctx, flag.ID, reqBody)
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
	var result genflags.FlagResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	flag.apply(resourceToFlag(result.Data, m))
	return nil
}

// Get retrieves a flag by its ID.
// Returns NotFoundError if no match.
func (m *FlagsManagement) Get(ctx context.Context, id string) (*Flag, error) {
	resp, err := m.gen.GetFlag(ctx, id)
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

	var result genflags.FlagResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	return resourceToFlag(result.Data, m), nil
}

// List returns all flags for the account.
func (m *FlagsManagement) List(ctx context.Context) ([]*Flag, error) {
	resp, err := m.gen.ListFlags(ctx, nil)
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

	var result genflags.FlagListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	flags := make([]*Flag, len(result.Data))
	for i := range result.Data {
		flags[i] = resourceToFlag(result.Data[i], m)
	}
	return flags, nil
}

// Delete removes a flag by its ID.
func (m *FlagsManagement) Delete(ctx context.Context, id string) error {
	resp, err := m.gen.DeleteFlag(ctx, id)
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

// CreateContextType creates a new context type.
func (m *FlagsManagement) CreateContextType(ctx context.Context, id string, name string) (*ContextType, error) {
	reqBody := genapp.ContextTypeRequest{
		Data: genapp.ContextTypeResource{
			Type:       "context_type",
			Id:         &id,
			Attributes: genapp.ContextType{Name: name},
		},
	}
	resp, err := m.appGen.CreateContextTypeWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
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
	return parseContextType(body)
}

// UpdateContextType updates a context type's attributes.
func (m *FlagsManagement) UpdateContextType(ctx context.Context, ctID string, attributes map[string]interface{}) (*ContextType, error) {
	reqBody := genapp.ContextTypeRequest{
		Data: genapp.ContextTypeResource{
			Type:       "context_type",
			Attributes: genapp.ContextType{Attributes: &attributes},
		},
	}
	resp, err := m.appGen.UpdateContextTypeWithApplicationVndAPIPlusJSONBody(ctx, ctID, reqBody)
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
	return parseContextType(body)
}

// ListContextTypes lists all context types.
func (m *FlagsManagement) ListContextTypes(ctx context.Context) ([]*ContextType, error) {
	resp, err := m.appGen.ListContextTypes(ctx)
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

	var result genapp.ContextTypeListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	types := make([]*ContextType, 0, len(result.Data))
	for _, r := range result.Data {
		ct := &ContextType{
			Name:       r.Attributes.Name,
			Attributes: make(map[string]map[string]interface{}),
		}
		if r.Id != nil {
			ct.ID = *r.Id
		}
		if r.Attributes.Attributes != nil {
			for k, v := range *r.Attributes.Attributes {
				if vm, ok := v.(map[string]interface{}); ok {
					ct.Attributes[k] = vm
				} else {
					ct.Attributes[k] = map[string]interface{}{}
				}
			}
		}
		types = append(types, ct)
	}
	return types, nil
}

// DeleteContextType deletes a context type by its ID.
func (m *FlagsManagement) DeleteContextType(ctx context.Context, ctID string) error {
	resp, err := m.appGen.DeleteContextType(ctx, ctID)
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

// ListContexts lists context instances filtered by context type key.
func (m *FlagsManagement) ListContexts(ctx context.Context, contextTypeKey string) ([]map[string]interface{}, error) {
	params := &genapp.ListContextsParams{
		FilterContextType: &contextTypeKey,
	}
	resp, err := m.appGen.ListContexts(ctx, params)
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

	var result struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	return result.Data, nil
}
