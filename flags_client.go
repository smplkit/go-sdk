package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
	genflags "github.com/smplkit/go-sdk/v3/internal/generated/flags"
)

// FlagsClient is the Smpl Flags client.
//
// One client exposes the full surface, reachable as client.Flags()
// or constructed directly via NewFlagsClient. The CRUD surface
// (NewBooleanFlag / NewStringFlag / NewNumberFlag / NewJsonFlag, Get /
// List / Delete, and the flag-declaration discovery buffer) is pure
// CRUD. The live surface (BooleanFlag / StringFlag / NumberFlag /
// JsonFlag / Refresh / Stats / OnChange) connects lazily on first use —
// the first call flushes discovery, fetches all flag definitions into
// the local cache, and opens the live-updates WebSocket. No explicit
// install step is required.
//
// The client supports two construction shapes:
//
//   - Wired into SmplClient — borrows the parent's flags transport for both
//     runtime fetch and CRUD, the parent's shared WebSocket for the live
//     channel, and the platform contexts sub-client for evaluation-context
//     registration. This is the common path.
//   - Standalone — NewFlagsClient(cfg, ...) builds and owns its own flags
//     and app transports, and on first live use opens and owns its own
//     WebSocket.
type FlagsClient struct {
	// client is the owning parent SmplClient when wired, nil when standalone.
	client       *SmplClient
	generated    genflags.ClientInterface
	appGenerated genapp.ClientInterface

	// Parent-or-own state. When wired these mirror the parent's fields;
	// when standalone they are resolved at construction so the live
	// surface can open its own WebSocket without a parent.
	environment string
	service     string
	metrics     *metricsReporter
	appURL      string
	apiKey      string

	// wsMu guards the lazily-created own WebSocket (standalone path).
	wsMu  sync.Mutex
	ownWS *sharedWebSocket

	// contexts is the platform contexts sub-client used as the
	// evaluation-context registration seam (wired path).
	contexts *ContextsClient

	runtime *FlagsRuntime
}

// newFlagsClient wires a FlagsClient onto the parent SmplClient's pre-built
// generated flags + app transports, the parent's platform contexts
// sub-client (the evaluation-context registration seam), and the
// parent's metrics reporter.
func newFlagsClient(parent *SmplClient, gen genflags.ClientInterface, appGen genapp.ClientInterface, contexts *ContextsClient, metrics *metricsReporter) *FlagsClient {
	c := &FlagsClient{
		client:       parent,
		generated:    gen,
		appGenerated: appGen,
		environment:  parent.environment,
		service:      parent.service,
		metrics:      metrics,
		contexts:     contexts,
	}
	var ctxBuf *contextRegistrationBuffer
	if contexts != nil {
		ctxBuf = contexts.contextBuf
	}
	if ctxBuf == nil {
		ctxBuf = newContextRegistrationBuffer()
	}
	c.runtime = newFlagsRuntime(c, ctxBuf)
	return c
}

// NewFlagsClient creates a standalone Smpl Flags client that builds and
// owns its own generated flags + app transports and, on first live use,
// its own WebSocket against the event gateway.
//
// Flags needs environment + service: the environment scopes runtime flag
// values and discovery declarations, and the service is auto-injected as
// an evaluation context. Both resolve from cfg (or ~/.smplkit / env vars).
func NewFlagsClient(cfg Config, opts ...ClientOption) (*FlagsClient, error) {
	rc, err := resolveConfig(cfg)
	if err != nil {
		return nil, err
	}
	optCfg := defaultConfig()
	for _, opt := range opts {
		opt(&optCfg)
	}

	httpClient := optCfg.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: optCfg.timeout}
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: rc.apiKey, base: base}

	flagsURL := serviceURL(optCfg, "flags", rc)
	appURL := serviceURL(optCfg, "app", rc)

	// Extra headers first, then SDK headers (so SDK-owned headers win on a collision).
	extraHeaders := rc.extraHeaders
	extraEditor := func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	}
	headerEditor := func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	}
	genFlags, _ := genflags.NewClient(flagsURL,
		genflags.WithHTTPClient(httpClient),
		genflags.WithRequestEditorFn(extraEditor),
		genflags.WithRequestEditorFn(headerEditor),
	)
	genApp, _ := genapp.NewClient(appURL,
		genapp.WithHTTPClient(httpClient),
		genapp.WithRequestEditorFn(extraEditor),
		genapp.WithRequestEditorFn(headerEditor),
	)

	ctxBuf := newContextRegistrationBuffer()
	c := &FlagsClient{
		client:       nil,
		generated:    genFlags,
		appGenerated: genApp,
		environment:  rc.environment,
		service:      rc.service,
		metrics:      nil,
		appURL:       appURL,
		apiKey:       rc.apiKey,
		contexts:     &ContextsClient{appClient: genApp, contextBuf: ctxBuf},
	}
	c.runtime = newFlagsRuntime(c, ctxBuf)
	return c, nil
}

// ensureWS returns the shared WebSocket — the parent's when wired, else
// our own (built lazily on first live use).
func (c *FlagsClient) ensureWS() *sharedWebSocket {
	if c.client != nil {
		return c.client.ensureWS()
	}
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.ownWS == nil {
		c.ownWS = newSharedWebSocket(c.appURL, c.apiKey, c.metrics)
		c.ownWS.start()
	}
	return c.ownWS
}

// resourceToFlag converts a generated FlagResource to the SDK Flag type.
func resourceToFlag(r genflags.FlagResource, c *FlagsClient) *Flag {
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

	flagTypeStr := string(attrs.Type)

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

// buildFlagAttributes constructs the Flag attribute payload shared by
// the create and update envelopes.
func buildFlagAttributes(name, flagType string, dflt interface{}, values *[]FlagValue, desc *string, envs map[string]interface{}) genflags.Flag {
	var genValues *[]genflags.FlagValue
	if values != nil {
		gv := make([]genflags.FlagValue, len(*values))
		for i, v := range *values {
			gv[i] = genflags.FlagValue{Name: v.Name, Value: v.Value}
		}
		genValues = &gv
	}

	return genflags.Flag{
		Name:         name,
		Type:         genflags.FlagType(flagType),
		Default:      dflt,
		Values:       genValues,
		Description:  desc,
		Environments: buildGenFlagEnvironments(envs),
	}
}

// buildFlagRequest constructs a FlagRequest envelope used for updates.
func buildFlagRequest(id, name, flagType string, dflt interface{}, values *[]FlagValue, desc *string, envs map[string]interface{}) genflags.FlagRequest {
	return genflags.FlagRequest{
		Data: genflags.FlagResource{
			Id:         &id,
			Type:       genflags.FlagResourceTypeFlag,
			Attributes: buildFlagAttributes(name, flagType, dflt, values, desc, envs),
		},
	}
}

// buildFlagCreateRequest constructs a FlagCreateRequest envelope. The
// create envelope requires a non-nullable string id (vs the update
// envelope, which optionally echoes the id).
func buildFlagCreateRequest(id, name, flagType string, dflt interface{}, values *[]FlagValue, desc *string, envs map[string]interface{}) genflags.FlagCreateRequest {
	return genflags.FlagCreateRequest{
		Data: genflags.FlagCreateResource{
			Id:         id,
			Type:       genflags.FlagCreateResourceTypeFlag,
			Attributes: buildFlagAttributes(name, flagType, dflt, values, desc, envs),
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

// fetchFlagsList fetches every flag as plain dicts, walking pagination
// until the server returns a short page.
func (c *FlagsClient) fetchFlagsList(ctx context.Context) ([]map[string]interface{}, error) {
	var all []map[string]interface{}
	for page := 1; ; page++ {
		batch, err := c.fetchFlagsPage(ctx, page, fetchAllPageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < fetchAllPageSize {
			return all, nil
		}
	}
}

// fetchFlagsPage fetches a single page of flags and converts each row to
// the runtime's plain-dict shape.
func (c *FlagsClient) fetchFlagsPage(ctx context.Context, pageNumber, pageSize int) ([]map[string]interface{}, error) {
	params := &genflags.ListFlagsParams{
		PageNumber: &pageNumber,
		PageSize:   &pageSize,
	}
	resp, err := c.generated.ListFlags(ctx, params)
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
//
// The id is the identifier of the flag to evaluate. The defaultValue is
// returned by the handle's Get when the flag is unknown or no
// environment override or rule applies. Connects lazily on first use.
//
// Returns a *BooleanFlagHandle whose Get evaluates against the live cache.
func (c *FlagsClient) BooleanFlag(key string, defaultValue bool) *BooleanFlagHandle {
	return c.runtime.BooleanFlag(key, defaultValue)
}

// StringFlag returns a typed handle for a string flag.
//
// The id is the identifier of the flag to evaluate. The defaultValue is
// returned by the handle's Get when the flag is unknown or no
// environment override or rule applies. Connects lazily on first use.
//
// Returns a *StringFlagHandle whose Get evaluates against the live cache.
func (c *FlagsClient) StringFlag(key string, defaultValue string) *StringFlagHandle {
	return c.runtime.StringFlag(key, defaultValue)
}

// NumberFlag returns a typed handle for a numeric flag.
//
// The id is the identifier of the flag to evaluate. The defaultValue is
// returned by the handle's Get when the flag is unknown or no
// environment override or rule applies. Connects lazily on first use.
//
// Returns a *NumberFlagHandle whose Get evaluates against the live cache.
func (c *FlagsClient) NumberFlag(key string, defaultValue float64) *NumberFlagHandle {
	return c.runtime.NumberFlag(key, defaultValue)
}

// JsonFlag returns a typed handle for a JSON flag.
//
// The id is the identifier of the flag to evaluate. The defaultValue is
// returned by the handle's Get when the flag is unknown or no
// environment override or rule applies. Connects lazily on first use.
//
// Returns a *JsonFlagHandle whose Get evaluates against the live cache.
func (c *FlagsClient) JsonFlag(key string, defaultValue map[string]interface{}) *JsonFlagHandle {
	return c.runtime.JsonFlag(key, defaultValue)
}

// SetContextProvider registers a function that supplies evaluation
// contexts.
//
// The fn receives a context.Context and returns a []Context. The
// returned slice supplies the evaluation contexts that targeting rules
// are evaluated against. It is invoked during evaluation on the typed-
// handle Get path whenever no contexts are passed explicitly to Get,
// providing the ambient contexts in their place.
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

// Evaluate evaluates a flag against an explicit environment and contexts
// and returns the raw resolved value.
//
// The key is the identifier of the flag to evaluate. The environment is
// the environment name whose overrides and rules are applied. The
// contexts are the Context entities that targeting rules are evaluated
// against. The returned interface{} is the resolved value, untyped (nil
// when the flag is unknown or cannot be resolved).
//
// This is the untyped, explicit-environment counterpart to the typed
// handle Get path (BooleanFlag/StringFlag/NumberFlag/JsonFlag and their
// Get): those resolve the environment from client configuration, coerce
// the result to a Go type, and fall back to a supplied default, whereas
// Evaluate names the environment directly and returns the value as-is.
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

// ------------------------------------------------------------------
// Management surface: CRUD (no live connection)
// ------------------------------------------------------------------

// NewBooleanFlag returns a new unsaved boolean Flag.
//
// The id is a stable flag identifier, unique per account. The
// defaultValue is served when no environment override or rule applies.
//
// Options:
//   - WithFlagName sets a human-readable display name; when omitted it
//     defaults to a title-cased form of id.
//   - WithFlagDescription sets an optional free-text description.
//
// Returns a new unsaved Flag; call Save to persist.
func (c *FlagsClient) NewBooleanFlag(id string, defaultValue bool, opts ...FlagOption) *Flag {
	boolValues := []FlagValue{{Name: "True", Value: true}, {Name: "False", Value: false}}
	f := &Flag{
		ID:           id,
		Name:         keyToDisplayName(id),
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

// NewStringFlag returns a new unsaved string Flag.
//
// The id is a stable flag identifier, unique per account. The
// defaultValue is served when no environment override or rule applies.
//
// Options:
//   - WithFlagName sets a human-readable display name; when omitted it
//     defaults to a title-cased form of id.
//   - WithFlagDescription sets an optional free-text description.
//   - WithFlagValues sets an optional list of allowed values constraining
//     what the flag may serve; when omitted, the flag is unconstrained.
//
// Returns a new unsaved Flag; call Save to persist.
func (c *FlagsClient) NewStringFlag(id string, defaultValue string, opts ...FlagOption) *Flag {
	f := &Flag{
		ID:           id,
		Name:         keyToDisplayName(id),
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

// NewNumberFlag returns a new unsaved numeric Flag.
//
// The id is a stable flag identifier, unique per account. The
// defaultValue is served when no environment override or rule applies.
//
// Options:
//   - WithFlagName sets a human-readable display name; when omitted it
//     defaults to a title-cased form of id.
//   - WithFlagDescription sets an optional free-text description.
//   - WithFlagValues sets an optional list of allowed values constraining
//     what the flag may serve; when omitted, the flag is unconstrained.
//
// Returns a new unsaved Flag; call Save to persist.
func (c *FlagsClient) NewNumberFlag(id string, defaultValue float64, opts ...FlagOption) *Flag {
	f := &Flag{
		ID:           id,
		Name:         keyToDisplayName(id),
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

// NewJsonFlag returns a new unsaved JSON Flag.
//
// The id is a stable flag identifier, unique per account. The
// defaultValue is served when no environment override or rule applies.
//
// Options:
//   - WithFlagName sets a human-readable display name; when omitted it
//     defaults to a title-cased form of id.
//   - WithFlagDescription sets an optional free-text description.
//   - WithFlagValues sets an optional list of allowed values constraining
//     what the flag may serve; when omitted, the flag is unconstrained.
//
// Returns a new unsaved Flag; call Save to persist.
func (c *FlagsClient) NewJsonFlag(id string, defaultValue map[string]interface{}, opts ...FlagOption) *Flag {
	f := &Flag{
		ID:           id,
		Name:         keyToDisplayName(id),
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

// Get fetches the editable Flag resource by id.
//
// The id is the identifier of the flag to fetch. The returned *Flag is
// ready to mutate and Save. Returns a *NotFoundError when no flag with
// that id exists for the account.
func (c *FlagsClient) Get(ctx context.Context, id string) (*Flag, error) {
	resp, err := c.generated.GetFlag(ctx, id)
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

	return resourceToFlag(result.Data, c), nil
}

// List lists flags for the authenticated account.
//
// Without options the server applies its defaults (page 1, page size
// 1000). Use [WithPageNumber] / [WithPageSize] to walk additional
// pages. The wrapper does not loop — callers that want every flag
// should iterate until a short page is returned.
//
// Options:
//   - WithPageNumber sets the 1-based page index to fetch; when omitted,
//     the server default applies.
//   - WithPageSize sets the number of flags per page; when omitted, the
//     server default applies.
func (c *FlagsClient) List(ctx context.Context, opts ...ListOption) ([]*Flag, error) {
	o := resolveListOptions(opts)
	params := &genflags.ListFlagsParams{
		PageNumber: o.pageNumber,
		PageSize:   o.pageSize,
	}
	resp, err := c.generated.ListFlags(ctx, params)
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
		flags[i] = resourceToFlag(result.Data[i], c)
	}
	return flags, nil
}

// Delete deletes a flag by id.
//
// The id is the identifier of the flag to delete. Returns a
// *NotFoundError when no flag with that id exists for the account.
func (c *FlagsClient) Delete(ctx context.Context, id string) error {
	resp, err := c.generated.DeleteFlag(ctx, id)
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

// createFlag creates the flag on the server and updates the local
// instance. Called from Flag.Save when CreatedAt is nil.
func (c *FlagsClient) createFlag(ctx context.Context, flag *Flag) error {
	reqBody := buildFlagCreateRequest(flag.ID, flag.Name, flag.Type, flag.Default, flag.Values, flag.Description, flag.Environments)
	resp, err := c.generated.CreateFlagWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
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
	flag.apply(resourceToFlag(result.Data, c))
	return nil
}

// updateFlag updates the flag on the server and updates the local
// instance. Called from Flag.Save when CreatedAt is set.
func (c *FlagsClient) updateFlag(ctx context.Context, flag *Flag) error {
	reqBody := buildFlagRequest(flag.ID, flag.Name, flag.Type, flag.Default, flag.Values, flag.Description, flag.Environments)
	resp, err := c.generated.UpdateFlagWithApplicationVndAPIPlusJSONBody(ctx, flag.ID, reqBody)
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
	flag.apply(resourceToFlag(result.Data, c))
	return nil
}

// ------------------------------------------------------------------
// Management surface: discovery buffer (owned directly)
// ------------------------------------------------------------------

// RegisterFlag buffers a flag declaration for bulk-discovery upload.
//
// Service and environment default to the client's resolved values. The
// declaration is queued for background flush; once the pending count
// reaches the batch threshold a flush is kicked off in the background.
// Items remain in the buffer until the POST succeeds, so failed flushes
// are retried by the next Flush() call.
func (c *FlagsClient) RegisterFlag(id, flagType string, defaultVal interface{}) {
	c.runtime.flagBuffer.add(id, flagType, defaultVal, c.service, c.environment)
	if c.runtime.flagBuffer.pendingCount() >= flagRegistrationThreshold {
		go c.runtime.flushFlagBuffer(context.Background())
	}
}

// FlagRegisterOption configures a batch Register call. Use WithFlagFlush.
type FlagRegisterOption func(*flagRegisterOpts)

type flagRegisterOpts struct {
	flush bool
}

// WithFlagFlush makes Register flush all buffered declarations immediately
// rather than waiting for the batch threshold or the next periodic flush.
func WithFlagFlush() FlagRegisterOption {
	return func(o *flagRegisterOpts) { o.flush = true }
}

// Register buffers one or more flag declarations for bulk-discovery upload.
//
// Each declaration's Service and Environment default to the client's resolved
// values when left empty. Declarations stay buffered and are sent on the next
// flush — automatic once the buffer reaches its batch threshold, or on the
// first live call — unless WithFlagFlush is passed, which sends everything
// buffered immediately. Items remain in the buffer until the POST succeeds, so
// a failed flush is retried by the next Flush call.
func (c *FlagsClient) Register(ctx context.Context, declarations []FlagDeclaration, opts ...FlagRegisterOption) {
	o := &flagRegisterOpts{}
	for _, opt := range opts {
		opt(o)
	}
	for _, d := range declarations {
		service := d.Service
		if service == "" {
			service = c.service
		}
		environment := d.Environment
		if environment == "" {
			environment = c.environment
		}
		c.runtime.flagBuffer.add(d.ID, string(d.Type), d.Default, service, environment)
	}
	if o.flush {
		c.Flush(ctx)
		return
	}
	if c.runtime.flagBuffer.pendingCount() >= flagRegistrationThreshold {
		go c.runtime.flushFlagBuffer(context.Background())
	}
}

// Flush POSTs pending declarations to the flags bulk endpoint.
//
// Items remain in the buffer until the request succeeds, so a flush
// against an unhealthy flags service is automatically retried by the
// next Flush() call (periodic background flush, install retry, or final
// flush on close).
func (c *FlagsClient) Flush(ctx context.Context) {
	c.runtime.flushFlagBuffer(ctx)
}

// FlushSync flushes pending flag declarations synchronously. It is an alias of
// Flush, mirroring LoggersClient.FlushSync for the periodic-flush path.
func (c *FlagsClient) FlushSync(ctx context.Context) {
	c.Flush(ctx)
}

// PendingCount returns the number of pending flag declarations awaiting flush.
func (c *FlagsClient) PendingCount() int {
	return c.runtime.flagBuffer.pendingCount()
}
