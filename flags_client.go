package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/uuid"

	genapp "github.com/smplkit/go-sdk/internal/generated/app"
	genflags "github.com/smplkit/go-sdk/internal/generated/flags"
)

// FlagsClient provides management and runtime operations for flag resources.
// Obtain one via Client.Flags().
type FlagsClient struct {
	client       *Client
	generated    genflags.ClientInterface
	appGenerated genapp.ClientInterface

	runtime *FlagsRuntime
}

// --- Factory methods (Active Record pattern) ---

// NewBooleanFlag creates an unsaved boolean flag. Call Save(ctx) to persist.
// If name is not provided via WithFlagName, it is auto-generated from the key.
// Boolean values are auto-generated if not provided via WithFlagValues.
func (c *FlagsClient) NewBooleanFlag(key string, defaultValue bool, opts ...FlagOption) *Flag {
	boolValues := []FlagValue{{Name: "True", Value: true}, {Name: "False", Value: false}}
	f := &Flag{
		Key:          key,
		Name:         keyToDisplayName(key),
		Type:         string(FlagTypeBoolean),
		Default:      defaultValue,
		Values:       &boolValues,
		Environments: map[string]interface{}{},
		client:       c,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// NewStringFlag creates an unsaved string flag. Call Save(ctx) to persist.
func (c *FlagsClient) NewStringFlag(key string, defaultValue string, opts ...FlagOption) *Flag {
	f := &Flag{
		Key:          key,
		Name:         keyToDisplayName(key),
		Type:         string(FlagTypeString),
		Default:      defaultValue,
		Environments: map[string]interface{}{},
		client:       c,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// NewNumberFlag creates an unsaved numeric flag. Call Save(ctx) to persist.
func (c *FlagsClient) NewNumberFlag(key string, defaultValue float64, opts ...FlagOption) *Flag {
	f := &Flag{
		Key:          key,
		Name:         keyToDisplayName(key),
		Type:         string(FlagTypeNumeric),
		Default:      defaultValue,
		Environments: map[string]interface{}{},
		client:       c,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// NewJsonFlag creates an unsaved JSON flag. Call Save(ctx) to persist.
func (c *FlagsClient) NewJsonFlag(key string, defaultValue map[string]interface{}, opts ...FlagOption) *Flag {
	f := &Flag{
		Key:          key,
		Name:         keyToDisplayName(key),
		Type:         string(FlagTypeJSON),
		Default:      defaultValue,
		Environments: map[string]interface{}{},
		client:       c,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// --- Management CRUD ---

// Get retrieves a flag by its key.
// Uses the list endpoint with a filter[key] query parameter.
// Returns SmplNotFoundError if no match.
func (c *FlagsClient) Get(ctx context.Context, key string) (*Flag, error) {
	params := &genflags.ListFlagsParams{FilterKey: &key}
	resp, err := c.generated.ListFlags(ctx, params)
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

	if len(result.Data) == 0 {
		return nil, &SmplNotFoundError{
			SmplError: SmplError{
				Message:    fmt.Sprintf("flag with key %q not found", key),
				StatusCode: 404,
			},
		}
	}
	return resourceToFlag(result.Data[0], c), nil
}

// List returns all flags for the account.
func (c *FlagsClient) List(ctx context.Context) ([]*Flag, error) {
	resp, err := c.generated.ListFlags(ctx, nil)
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
		flags[i] = resourceToFlag(result.Data[i], c)
	}
	return flags, nil
}

// Delete removes a flag by its key. Fetches by key first to get UUID, then
// deletes by UUID. Returns SmplNotFoundError if not found.
func (c *FlagsClient) Delete(ctx context.Context, key string) error {
	flag, err := c.Get(ctx, key)
	if err != nil {
		return err
	}
	return c.deleteByID(ctx, flag.ID)
}

// deleteByID removes a flag by its UUID.
func (c *FlagsClient) deleteByID(ctx context.Context, flagID string) error {
	uid, err := uuid.Parse(flagID)
	if err != nil {
		return fmt.Errorf("smplkit: invalid flag ID %q: %w", flagID, err)
	}

	resp, err := c.generated.DeleteFlag(ctx, uid)
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

// createFlag sends a POST to create the flag, then applies the response.
func (c *FlagsClient) createFlag(ctx context.Context, flag *Flag) error {
	reqBody := buildFlagRequest("", flag.Key, flag.Name, flag.Type, flag.Default, flag.Values, flag.Description, flag.Environments)

	resp, err := c.generated.CreateFlag(ctx, reqBody)
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

	var result genflags.FlagResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	flag.apply(resourceToFlag(result.Data, c))
	return nil
}

// updateFlag sends a PUT to update the flag, then applies the response.
func (c *FlagsClient) updateFlag(ctx context.Context, flag *Flag) error {
	uid, err := uuid.Parse(flag.ID)
	if err != nil {
		return fmt.Errorf("smplkit: invalid flag ID %q: %w", flag.ID, err)
	}

	reqBody := buildFlagRequest(flag.ID, flag.Key, flag.Name, flag.Type, flag.Default, flag.Values, flag.Description, flag.Environments)

	resp, err := c.generated.UpdateFlag(ctx, uid, reqBody)
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

	var result genflags.FlagResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	flag.apply(resourceToFlag(result.Data, c))
	return nil
}

// --- Context type management — via generated app client ---

// CreateContextType creates a new context type.
func (c *FlagsClient) CreateContextType(ctx context.Context, key string, name string) (*ContextType, error) {
	reqBody := genapp.ContextTypeResponse{
		Data: genapp.ContextTypeResource{
			Type:       "context_type",
			Attributes: genapp.ContextType{Key: key, Name: name},
		},
	}
	resp, err := c.appGenerated.CreateContextTypeWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
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
func (c *FlagsClient) UpdateContextType(ctx context.Context, ctID string, attributes map[string]interface{}) (*ContextType, error) {
	uid, err := uuid.Parse(ctID)
	if err != nil {
		return nil, fmt.Errorf("smplkit: invalid context type ID %q: %w", ctID, err)
	}
	reqBody := genapp.ContextTypeResponse{
		Data: genapp.ContextTypeResource{
			Type:       "context_type",
			Attributes: genapp.ContextType{Attributes: &attributes},
		},
	}
	resp, err := c.appGenerated.UpdateContextTypeWithApplicationVndAPIPlusJSONBody(ctx, uid, reqBody)
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
func (c *FlagsClient) ListContextTypes(ctx context.Context) ([]*ContextType, error) {
	resp, err := c.appGenerated.ListContextTypes(ctx)
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
			Key:  r.Attributes.Key,
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

// DeleteContextType deletes a context type by its UUID.
func (c *FlagsClient) DeleteContextType(ctx context.Context, ctID string) error {
	uid, err := uuid.Parse(ctID)
	if err != nil {
		return fmt.Errorf("smplkit: invalid context type ID %q: %w", ctID, err)
	}
	resp, err := c.appGenerated.DeleteContextType(ctx, uid)
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
func (c *FlagsClient) ListContexts(ctx context.Context, contextTypeKey string) ([]map[string]interface{}, error) {
	params := &genapp.ListContextsParams{
		FilterContextTypeId: &contextTypeKey,
	}
	resp, err := c.appGenerated.ListContexts(ctx, params)
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

// --- Internal helpers ---

// resourceToFlag converts a generated FlagResource to the SDK Flag type.
func resourceToFlag(r genflags.FlagResource, c *FlagsClient) *Flag {
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}

	var values *[]FlagValue
	if attrs.Values != nil {
		v := make([]FlagValue, len(attrs.Values))
		for i, fv := range attrs.Values {
			v[i] = FlagValue{Name: fv.Name, Value: fv.Value}
		}
		values = &v
	}

	envs := extractFlagEnvironments(attrs.Environments)

	return &Flag{
		ID:           id,
		Key:          attrs.Key,
		Name:         attrs.Name,
		Type:         attrs.Type,
		Default:      attrs.Default,
		Values:       values,
		Description:  attrs.Description,
		Environments: envs,
		CreatedAt:    attrs.CreatedAt,
		UpdatedAt:    attrs.UpdatedAt,
		client:       c,
	}
}

// extractFlagEnvironments converts the generated *map[string]FlagEnvironment to plain maps.
func extractFlagEnvironments(envs *map[string]genflags.FlagEnvironment) map[string]interface{} {
	if envs == nil {
		return map[string]interface{}{}
	}
	result := make(map[string]interface{}, len(*envs))
	for envName, env := range *envs {
		entry := make(map[string]interface{})
		if env.Enabled != nil {
			entry["enabled"] = *env.Enabled
		}
		if env.Default != nil {
			entry["default"] = env.Default
		}
		if env.Rules != nil {
			rules := make([]interface{}, len(*env.Rules))
			for i, r := range *env.Rules {
				ruleMap := map[string]interface{}{
					"logic": r.Logic,
					"value": r.Value,
				}
				if r.Description != nil {
					ruleMap["description"] = *r.Description
				}
				rules[i] = ruleMap
			}
			entry["rules"] = rules
		} else {
			entry["rules"] = []interface{}{}
		}
		result[envName] = entry
	}
	return result
}

// buildFlagRequest constructs a ResponseFlag for create or update.
func buildFlagRequest(id, key, name, flagType string, dflt interface{}, values *[]FlagValue, desc *string, envs map[string]interface{}) genflags.ResponseFlag {
	var idPtr *string
	if id != "" {
		idPtr = &id
	}
	flagT := "flag"

	var genValues []genflags.FlagValue
	if values != nil {
		genValues = make([]genflags.FlagValue, len(*values))
		for i, v := range *values {
			genValues[i] = genflags.FlagValue{Name: v.Name, Value: v.Value}
		}
	}

	genEnvs := buildGenFlagEnvironments(envs)

	return genflags.ResponseFlag{
		Data: genflags.ResourceFlag{
			Id:   idPtr,
			Type: &flagT,
			Attributes: genflags.Flag{
				Key:          key,
				Name:         name,
				Type:         flagType,
				Default:      dflt,
				Values:       genValues,
				Description:  desc,
				Environments: genEnvs,
			},
		},
	}
}

// buildGenFlagEnvironments converts plain environment maps to generated types.
func buildGenFlagEnvironments(envs map[string]interface{}) *map[string]genflags.FlagEnvironment {
	if envs == nil {
		return nil
	}
	result := make(map[string]genflags.FlagEnvironment, len(envs))
	for envName, envData := range envs {
		envMap, ok := envData.(map[string]interface{})
		if !ok {
			continue
		}

		var env genflags.FlagEnvironment

		if enabled, ok := envMap["enabled"].(bool); ok {
			env.Enabled = &enabled
		}
		if dflt, ok := envMap["default"]; ok {
			env.Default = dflt
		}
		if rulesRaw, ok := envMap["rules"]; ok {
			if rulesSlice, ok := rulesRaw.([]interface{}); ok {
				rules := make([]genflags.FlagRule, 0, len(rulesSlice))
				for _, rRaw := range rulesSlice {
					if rMap, ok := rRaw.(map[string]interface{}); ok {
						rule := genflags.FlagRule{
							Value: rMap["value"],
						}
						if logic, ok := rMap["logic"].(map[string]interface{}); ok {
							rule.Logic = logic
						} else {
							rule.Logic = map[string]interface{}{}
						}
						if desc, ok := rMap["description"].(string); ok {
							rule.Description = &desc
						}
						rules = append(rules, rule)
					}
				}
				env.Rules = &rules
			}
		}

		result[envName] = env
	}
	return &result
}

// parseContextType parses a JSON:API response body containing a single context type.
func parseContextType(body []byte) (*ContextType, error) {
	var result struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	return parseContextTypeRaw(result.Data)
}

// parseContextTypeRaw parses a single context type resource from raw JSON.
func parseContextTypeRaw(raw json.RawMessage) (*ContextType, error) {
	var data struct {
		ID         string `json:"id"`
		Attributes struct {
			Key        string                 `json:"key"`
			Name       string                 `json:"name"`
			Attributes map[string]interface{} `json:"attributes"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse context type: %w", err)
	}
	attrs := data.Attributes.Attributes
	if attrs == nil {
		attrs = map[string]interface{}{}
	}
	return &ContextType{
		ID:         data.ID,
		Key:        data.Attributes.Key,
		Name:       data.Attributes.Name,
		Attributes: attrs,
	}, nil
}

// fetchAllFlags fetches all flags and returns them as plain dicts keyed by flag key.
func (c *FlagsClient) fetchAllFlags(ctx context.Context) (map[string]map[string]interface{}, error) {
	flags, err := c.fetchFlagsList(ctx)
	if err != nil {
		return nil, err
	}
	store := make(map[string]map[string]interface{}, len(flags))
	for _, f := range flags {
		key, _ := f["key"].(string)
		store[key] = f
	}
	return store, nil
}

// fetchFlagsList fetches all flags as plain dicts.
func (c *FlagsClient) fetchFlagsList(ctx context.Context) ([]map[string]interface{}, error) {
	resp, err := c.generated.ListFlags(ctx, nil)
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

	flags := make([]map[string]interface{}, 0, len(result.Data))
	for _, r := range result.Data {
		attrs := r.Attributes
		var values interface{}
		if attrs.Values != nil {
			v := make([]interface{}, len(attrs.Values))
			for i, fv := range attrs.Values {
				v[i] = map[string]interface{}{"name": fv.Name, "value": fv.Value}
			}
			values = v
		}

		f := map[string]interface{}{
			"key":          attrs.Key,
			"name":         attrs.Name,
			"type":         attrs.Type,
			"default":      attrs.Default,
			"values":       values,
			"description":  attrs.Description,
			"environments": extractFlagEnvironments(attrs.Environments),
		}
		flags = append(flags, f)
	}
	return flags, nil
}

// --- Runtime pass-throughs ---
// These delegate to the embedded FlagsRuntime so users access them via client.Flags().

// BooleanFlag returns a typed handle for a boolean flag.
func (c *FlagsClient) BooleanFlag(key string, defaultValue bool) *BooleanFlagHandle {
	return c.runtime.BooleanFlag(key, defaultValue)
}

// StringFlag returns a typed handle for a string flag.
func (c *FlagsClient) StringFlag(key string, defaultValue string) *StringFlagHandle {
	return c.runtime.StringFlag(key, defaultValue)
}

// NumberFlag returns a typed handle for a numeric flag.
func (c *FlagsClient) NumberFlag(key string, defaultValue float64) *NumberFlagHandle {
	return c.runtime.NumberFlag(key, defaultValue)
}

// JsonFlag returns a typed handle for a JSON flag.
func (c *FlagsClient) JsonFlag(key string, defaultValue map[string]interface{}) *JsonFlagHandle {
	return c.runtime.JsonFlag(key, defaultValue)
}

// SetContextProvider registers a function that provides evaluation contexts.
func (c *FlagsClient) SetContextProvider(fn func(ctx context.Context) []Context) {
	c.runtime.SetContextProvider(fn)
}

// Disconnect unregisters from WebSocket, flushes contexts, and clears state.
func (c *FlagsClient) Disconnect(ctx context.Context) {
	c.runtime.disconnect(ctx)
}

// Refresh re-fetches all flag definitions and clears cache.
func (c *FlagsClient) Refresh(ctx context.Context) error {
	return c.runtime.Refresh(ctx)
}

// ConnectionStatus returns the current WebSocket connection status.
func (c *FlagsClient) ConnectionStatus() string {
	return c.runtime.ConnectionStatus()
}

// Stats returns cache statistics.
func (c *FlagsClient) Stats() FlagStats {
	return c.runtime.Stats()
}

// OnChange registers a global change listener that fires for any flag change.
func (c *FlagsClient) OnChange(cb func(*FlagChangeEvent)) {
	c.runtime.OnChange(cb)
}

// OnChangeKey registers a key-scoped change listener that fires only when the
// specified flag key changes.
func (c *FlagsClient) OnChangeKey(key string, cb func(*FlagChangeEvent)) {
	c.runtime.OnChangeKey(key, cb)
}

// Register explicitly registers context(s) for background batch registration.
func (c *FlagsClient) Register(ctx context.Context, contexts ...Context) {
	c.runtime.Register(ctx, contexts...)
}

// FlushContexts flushes pending context registrations to the server.
func (c *FlagsClient) FlushContexts(ctx context.Context) {
	c.runtime.FlushContexts(ctx)
}

// Evaluate performs Tier 1 explicit evaluation — stateless, no provider or cache.
func (c *FlagsClient) Evaluate(ctx context.Context, key string, environment string, contexts []Context) interface{} {
	return c.runtime.Evaluate(ctx, key, environment, contexts)
}

// flushContexts sends pending context registrations to the server.
func (c *FlagsClient) flushContexts(ctx context.Context, batch []map[string]interface{}) {
	if len(batch) == 0 {
		return
	}
	items := make([]genapp.ContextBulkItem, 0, len(batch))
	for _, entry := range batch {
		t, _ := entry["type"].(string)
		k, _ := entry["key"].(string)
		item := genapp.ContextBulkItem{Type: t, Key: k}
		if attrs, ok := entry["attributes"].(map[string]interface{}); ok {
			item.Attributes = &attrs
		}
		items = append(items, item)
	}
	reqBody := genapp.ContextBulkRegister{Contexts: items}
	// Fire-and-forget — errors are silently ignored.
	resp, err := c.appGenerated.BulkRegisterContextsWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}
