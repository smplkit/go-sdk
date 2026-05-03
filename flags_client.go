package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	genapp "github.com/smplkit/go-sdk/internal/generated/app"
	genflags "github.com/smplkit/go-sdk/internal/generated/flags"
)

// FlagsClient provides management and runtime operations for flag resources.
// Obtain one via Client.Flags().
type FlagsClient struct {
	client       *Client
	generated    genflags.ClientInterface
	appGenerated genapp.ClientInterface

	runtime    *FlagsRuntime
	management *FlagsManagement
}

// Management returns the sub-object for flag CRUD operations.
func (c *FlagsClient) Management() *FlagsManagement {
	if c.management == nil {
		c.management = newFlagsManagement(c.generated, c.appGenerated)
		c.management.attachRuntime(c)
	}
	return c.management
}

// (createFlag and updateFlag moved to flags_management.go so the
// active-record save path doesn't depend on the runtime client —
// rule 1 of the cross-SDK overhaul.)

// resourceToFlag converts a generated FlagResource to the SDK Flag type.
func resourceToFlag(r genflags.FlagResource, m *FlagsManagement) *Flag {
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}

	var values *[]FlagValue
	if attrs.Values != nil {
		v := make([]FlagValue, len(*attrs.Values))
		for i, fv := range *attrs.Values {
			v[i] = FlagValue{Name: fv.Name, Value: fv.Value}
		}
		values = &v
	}

	envs := extractFlagEnvironments(attrs.Environments)

	flagTypeStr := attrs.Type

	return &Flag{
		ID:           id,
		Name:         attrs.Name,
		Type:         flagTypeStr,
		Default:      attrs.Default,
		Values:       values,
		Description:  attrs.Description,
		Environments: envs,
		CreatedAt:    attrs.CreatedAt,
		UpdatedAt:    attrs.UpdatedAt,
		client:       m,
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

// buildFlagRequest constructs a FlagResponse for create or update.
func buildFlagRequest(id, name, flagType string, dflt interface{}, values *[]FlagValue, desc *string, envs map[string]interface{}) genflags.FlagResponse {
	var genValues *[]genflags.FlagValue
	if values != nil {
		gv := make([]genflags.FlagValue, len(*values))
		for i, v := range *values {
			gv[i] = genflags.FlagValue{Name: v.Name, Value: v.Value}
		}
		genValues = &gv
	}

	genEnvs := buildGenFlagEnvironments(envs)

	return genflags.FlagResponse{
		Data: genflags.FlagResource{
			Id:   &id,
			Type: genflags.FlagResourceTypeFlag,
			Attributes: genflags.Flag{
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
			ID         string                 `json:"id"`
			Name       string                 `json:"name"`
			Attributes map[string]interface{} `json:"attributes"`
			CreatedAt  *string                `json:"created_at"`
			UpdatedAt  *string                `json:"updated_at"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse context type: %w", err)
	}
	ctID := data.ID
	if ctID == "" {
		ctID = data.Attributes.ID
	}
	typedAttrs := make(map[string]map[string]interface{})
	for k, v := range data.Attributes.Attributes {
		if vm, ok := v.(map[string]interface{}); ok {
			typedAttrs[k] = vm
		} else {
			typedAttrs[k] = map[string]interface{}{}
		}
	}
	ct := &ContextType{
		ID:         ctID,
		Name:       data.Attributes.Name,
		Attributes: typedAttrs,
	}
	if data.Attributes.CreatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *data.Attributes.CreatedAt); err == nil {
			ct.CreatedAt = &t
		}
	}
	if data.Attributes.UpdatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *data.Attributes.UpdatedAt); err == nil {
			ct.UpdatedAt = &t
		}
	}
	return ct, nil
}

// fetchSingleFlag fetches a single flag by key and returns it as a plain dict.
func (c *FlagsClient) fetchSingleFlag(ctx context.Context, key string) (map[string]interface{}, error) {
	resp, err := c.generated.GetFlag(ctx, key)
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

	r := result.Data
	attrs := r.Attributes
	id := ""
	if r.Id != nil {
		id = *r.Id
	}

	var values interface{}
	if attrs.Values != nil {
		v := make([]interface{}, len(*attrs.Values))
		for i, fv := range *attrs.Values {
			v[i] = map[string]interface{}{"name": fv.Name, "value": fv.Value}
		}
		values = v
	}

	return map[string]interface{}{
		"id":           id,
		"name":         attrs.Name,
		"type":         attrs.Type,
		"default":      attrs.Default,
		"values":       values,
		"description":  attrs.Description,
		"environments": extractFlagEnvironments(attrs.Environments),
	}, nil
}

// fetchAllFlags fetches all flags and returns them as plain dicts keyed by flag ID.
func (c *FlagsClient) fetchAllFlags(ctx context.Context) (map[string]map[string]interface{}, error) {
	flags, err := c.fetchFlagsList(ctx)
	if err != nil {
		return nil, err
	}
	store := make(map[string]map[string]interface{}, len(flags))
	for _, f := range flags {
		id, _ := f["id"].(string)
		store[id] = f
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

	flags := make([]map[string]interface{}, 0, len(result.Data))
	for _, r := range result.Data {
		attrs := r.Attributes
		var values interface{}
		if attrs.Values != nil {
			v := make([]interface{}, len(*attrs.Values))
			for i, fv := range *attrs.Values {
				v[i] = map[string]interface{}{"name": fv.Name, "value": fv.Value}
			}
			values = v
		}

		id := ""
		if r.Id != nil {
			id = *r.Id
		}
		f := map[string]interface{}{
			"id":           id,
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

// Disconnect stops real-time updates and releases runtime resources.
func (c *FlagsClient) Disconnect(ctx context.Context) {
	c.runtime.disconnect(ctx)
}

// Refresh fetches the latest flag definitions from the server.
func (c *FlagsClient) Refresh(ctx context.Context) error {
	return c.runtime.Refresh(ctx)
}

// ConnectionStatus returns the current real-time connection status.
func (c *FlagsClient) ConnectionStatus() string {
	return c.runtime.ConnectionStatus()
}

// Stats returns runtime statistics.
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

// Evaluate evaluates a flag with the given environment and contexts.
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
	resp, err := c.appGenerated.BulkRegisterContextsWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}
