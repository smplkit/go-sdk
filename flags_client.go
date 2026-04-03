package smplkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	genflags "github.com/smplkit/go-sdk/internal/generated/flags"
)

// FlagsClient provides management and runtime operations for flag resources.
// Obtain one via Client.Flags().
type FlagsClient struct {
	client    *Client
	generated genflags.ClientInterface

	runtime *FlagsRuntime
}

// Get retrieves a flag by its UUID.
func (c *FlagsClient) Get(ctx context.Context, flagID string) (*Flag, error) {
	uid, err := uuid.Parse(flagID)
	if err != nil {
		return nil, fmt.Errorf("smplkit: invalid flag ID %q: %w", flagID, err)
	}

	resp, err := c.generated.GetFlag(ctx, uid)
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
	return resourceToFlag(result.Data, c), nil
}

// Create creates a new flag resource.
func (c *FlagsClient) Create(ctx context.Context, params CreateFlagParams) (*Flag, error) {
	values := params.Values
	if values == nil && params.Type == FlagTypeBoolean {
		values = []FlagValue{
			{Name: "True", Value: true},
			{Name: "False", Value: false},
		}
	}

	reqBody := buildFlagRequest("", params.Key, params.Name, string(params.Type), params.Default, values, params.Description, nil)

	resp, err := c.generated.CreateFlag(ctx, reqBody)
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
	return resourceToFlag(result.Data, c), nil
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

// Delete removes a flag by its UUID. Returns nil on success.
func (c *FlagsClient) Delete(ctx context.Context, flagID string) error {
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

// updateFlag sends a PUT request to update the flag.
func (c *FlagsClient) updateFlag(ctx context.Context, flag *Flag, params UpdateFlagParams) (*Flag, error) {
	uid, err := uuid.Parse(flag.ID)
	if err != nil {
		return nil, fmt.Errorf("smplkit: invalid flag ID %q: %w", flag.ID, err)
	}

	name := flag.Name
	if params.Name != nil {
		name = *params.Name
	}
	desc := flag.Description
	if params.Description != nil {
		desc = params.Description
	}
	dflt := flag.Default
	if params.Default != nil {
		dflt = params.Default
	}
	values := flag.Values
	if params.Values != nil {
		values = params.Values
	}
	envs := flag.Environments
	if params.Environments != nil {
		envs = params.Environments
	}

	reqBody := buildFlagRequest(flag.ID, flag.Key, name, flag.Type, dflt, values, desc, envs)

	resp, err := c.generated.UpdateFlag(ctx, uid, reqBody)
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
	return resourceToFlag(result.Data, c), nil
}

// Context type management — direct HTTP calls (no generated client for these endpoints).

// CreateContextType creates a new context type.
func (c *FlagsClient) CreateContextType(ctx context.Context, key string, name string) (*ContextType, error) {
	payload := map[string]interface{}{
		"data": map[string]interface{}{
			"type": "context_type",
			"attributes": map[string]interface{}{
				"key":  key,
				"name": name,
			},
		},
	}
	body, resp, err := c.doJSONApp(ctx, "POST", "/api/v1/context_types", payload)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}
	return parseContextType(body)
}

// UpdateContextType updates a context type's attributes.
func (c *FlagsClient) UpdateContextType(ctx context.Context, ctID string, attributes map[string]interface{}) (*ContextType, error) {
	payload := map[string]interface{}{
		"data": map[string]interface{}{
			"type": "context_type",
			"attributes": map[string]interface{}{
				"attributes": attributes,
			},
		},
	}
	body, resp, err := c.doJSONApp(ctx, "PUT", "/api/v1/context_types/"+ctID, payload)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}
	return parseContextType(body)
}

// ListContextTypes lists all context types.
func (c *FlagsClient) ListContextTypes(ctx context.Context) ([]*ContextType, error) {
	body, resp, err := c.doJSONApp(ctx, "GET", "/api/v1/context_types", nil)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	types := make([]*ContextType, 0, len(result.Data))
	for _, raw := range result.Data {
		ct, err := parseContextTypeRaw(raw)
		if err != nil {
			return nil, err
		}
		types = append(types, ct)
	}
	return types, nil
}

// DeleteContextType deletes a context type by its UUID.
func (c *FlagsClient) DeleteContextType(ctx context.Context, ctID string) error {
	body, resp, err := c.doJSONApp(ctx, "DELETE", "/api/v1/context_types/"+ctID, nil)
	if err != nil {
		return err
	}
	return checkStatus(resp.StatusCode, body)
}

// ListContexts lists context instances filtered by context type key.
func (c *FlagsClient) ListContexts(ctx context.Context, contextTypeKey string) ([]map[string]interface{}, error) {
	body, resp, err := c.doJSONApp(ctx, "GET", "/api/v1/contexts?filter[context_type]="+contextTypeKey, nil)
	if err != nil {
		return nil, err
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

// --- Helpers ---

// doJSONApp performs a JSON HTTP request against the app base URL.
// Used for context-related endpoints that are served by the app service.
func (c *FlagsClient) doJSONApp(ctx context.Context, method, path string, payload interface{}) ([]byte, *http.Response, error) {
	baseURL := appServiceBaseURL(c.client.baseURL)
	return c.doJSONWithBase(ctx, method, baseURL+path, payload)
}

// doJSONWithBase performs a JSON HTTP request against the given full URL.
func (c *FlagsClient) doJSONWithBase(ctx context.Context, method, u string, payload interface{}) ([]byte, *http.Response, error) {

	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, fmt.Errorf("smplkit: failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, nil, classifyError(err)
	}
	req.Header.Set("Accept", "application/vnd.api+json")
	req.Header.Set("User-Agent", userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.httpClient.Do(req)
	if err != nil {
		return nil, nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp, &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	return body, resp, nil
}

// appServiceBaseURL derives the app service URL from the config base URL.
// When configBaseURL is overridden (e.g. in tests), we use it directly;
// otherwise we return the production app URL.
func appServiceBaseURL(configBaseURL string) string {
	if configBaseURL != "" && configBaseURL != "https://config.smplkit.com" {
		return configBaseURL
	}
	return "https://app.smplkit.com"
}

// resourceToFlag converts a generated FlagResource to the SDK Flag type.
func resourceToFlag(r genflags.FlagResource, c *FlagsClient) *Flag {
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}

	values := make([]FlagValue, len(attrs.Values))
	for i, v := range attrs.Values {
		values[i] = FlagValue{Name: v.Name, Value: v.Value}
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
func buildFlagRequest(id, key, name, flagType string, dflt interface{}, values []FlagValue, desc *string, envs map[string]interface{}) genflags.ResponseFlag {
	var idPtr *string
	if id != "" {
		idPtr = &id
	}
	flagT := "flag"

	genValues := make([]genflags.FlagValue, len(values))
	for i, v := range values {
		genValues[i] = genflags.FlagValue{Name: v.Name, Value: v.Value}
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
		values := make([]interface{}, len(attrs.Values))
		for i, v := range attrs.Values {
			values[i] = map[string]interface{}{"name": v.Name, "value": v.Value}
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

// BoolFlag returns a typed handle for a boolean flag.
func (c *FlagsClient) BoolFlag(key string, defaultValue bool) *BoolFlagHandle {
	return c.runtime.BoolFlag(key, defaultValue)
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

// connectInternal fetches flag definitions and registers on the shared WebSocket.
// Called by Client.Connect().
func (c *FlagsClient) connectInternal(ctx context.Context, environment string) error {
	return c.runtime.Connect(ctx, environment)
}

// Disconnect unregisters from WebSocket, flushes contexts, and clears state.
func (c *FlagsClient) Disconnect(ctx context.Context) {
	c.runtime.Disconnect(ctx)
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

// OnChange registers a global change listener.
func (c *FlagsClient) OnChange(cb func(*FlagChangeEvent)) {
	c.runtime.OnChange(cb)
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
	payload := map[string]interface{}{
		"contexts": batch,
	}
	// Fire-and-forget — errors are silently ignored.
	_, _, _ = c.doJSONApp(ctx, "PUT", "/api/v1/contexts/bulk", payload)
}
