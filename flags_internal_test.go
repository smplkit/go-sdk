package smplkit

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
	genflags "github.com/smplkit/go-sdk/v3/internal/generated/flags"
)

// --- Test helpers ---

// newTestParentClient builds a minimal wired SmplClient parent backed by the
// given app-generated transport, with the platform sub-client (the
// evaluation-context registration seam) wired so newFlagsClient can borrow it.
func newTestParentClient(appURL string, httpClient *http.Client, genApp genapp.ClientInterface) *SmplClient {
	ctxBuf := newContextRegistrationBuffer()
	c := &SmplClient{
		apiKey:       "sk_test",
		environment:  "test",
		service:      "test-service",
		appURL:       appURL,
		httpClient:   httpClient,
		appGenerated: genApp,
		contextBuf:   ctxBuf,
	}
	c.platform = newPlatformClient(genApp, ctxBuf)
	return c
}

func newTestFlagsClient(t *testing.T, handler http.HandlerFunc) (*FlagsClient, *httptest.Server) {
	t.Helper()
	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	httpClient := &http.Client{}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: "sk_test", base: base}

	// Build a generated flags client pointed at the test server.
	flagsHeaderEditor := genflags.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genFlagsClient, _ := genflags.NewClient(server.URL,
		genflags.WithHTTPClient(httpClient),
		flagsHeaderEditor,
	)

	// Build a generated app client pointed at the test server.
	appHeaderEditor := genapp.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genAppClient, _ := genapp.NewClient(server.URL,
		genapp.WithHTTPClient(httpClient),
		appHeaderEditor,
	)

	c := newTestParentClient(server.URL, httpClient, genAppClient)
	fc := newFlagsClient(c, genFlagsClient, genAppClient, c.platform.contexts, nil)
	c.flags = fc
	return fc, server
}

// --- Context type parsing (shared helpers in flags_models.go) ---

func TestParseContextType(t *testing.T) {
	body := []byte(`{"data":{"id":"user","attributes":{"id":"user","name":"User","attributes":{"plan":{"type":"string"}}}}}`)
	ct, err := parseContextType(body)
	require.NoError(t, err)
	assert.Equal(t, "user", ct.ID)
	assert.Equal(t, "User", ct.Name)
	assert.Equal(t, map[string]interface{}{"type": "string"}, ct.Attributes["plan"])
}

func TestParseContextType_NilAttributes(t *testing.T) {
	body := []byte(`{"data":{"id":"device","attributes":{"id":"device","name":"Device"}}}`)
	ct, err := parseContextType(body)
	require.NoError(t, err)
	assert.NotNil(t, ct.Attributes)
	assert.Len(t, ct.Attributes, 0)
}

func TestParseContextType_InvalidJSON(t *testing.T) {
	_, err := parseContextType([]byte(`not json`))
	assert.Error(t, err)
}

func TestParseContextTypeRaw_FallbackToAttributeID(t *testing.T) {
	// Root id is empty; should fall back to attributes.id.
	raw := json.RawMessage(`{"id":"","attributes":{"id":"user","name":"User","attributes":{"plan":"string"}}}`)
	ct, err := parseContextTypeRaw(raw)
	require.NoError(t, err)
	assert.Equal(t, "user", ct.ID)
	assert.Equal(t, "User", ct.Name)
	// Non-map attribute value falls back to empty map.
	assert.Equal(t, map[string]interface{}{}, ct.Attributes["plan"])
}

func TestParseContextTypeRaw_InvalidJSON(t *testing.T) {
	_, err := parseContextTypeRaw(json.RawMessage(`{invalid}`))
	assert.Error(t, err)
}

func TestParseContextTypeRaw_Timestamps(t *testing.T) {
	raw := json.RawMessage(`{"id":"user","attributes":{"id":"user","name":"User","attributes":{},"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-06-15T12:00:00Z"}}`)
	ct, err := parseContextTypeRaw(raw)
	require.NoError(t, err)
	require.NotNil(t, ct.CreatedAt)
	require.NotNil(t, ct.UpdatedAt)
}

func TestParseContextTypeRaw_BadTimestamps(t *testing.T) {
	// Unparseable timestamps are ignored (left nil).
	raw := json.RawMessage(`{"id":"user","attributes":{"id":"user","name":"User","attributes":{},"created_at":"not-a-time","updated_at":"also-bad"}}`)
	ct, err := parseContextTypeRaw(raw)
	require.NoError(t, err)
	assert.Nil(t, ct.CreatedAt)
	assert.Nil(t, ct.UpdatedAt)
}

// --- resourceToFlag ---

func TestResourceToFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)

	id := "feature-x"
	flagType := "flag"
	desc := "A flag"
	now := time.Now()
	r := flagResource(id, flagType, "Feature X", "BOOLEAN", true, desc, now)

	flag := resourceToFlag(r, fc)
	assert.Equal(t, id, flag.ID)
	assert.Equal(t, "Feature X", flag.Name)
	assert.Equal(t, "BOOLEAN", flag.Type)
	assert.Equal(t, true, flag.Default)
	assert.Equal(t, &desc, flag.Description)
	assert.NotNil(t, flag.CreatedAt)
	assert.Same(t, fc, flag.client)
}

func TestResourceToFlag_NilID(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)

	now := time.Now()
	r := flagResourceNoID("Feature X", "BOOLEAN", true, now)

	flag := resourceToFlag(r, fc)
	assert.Equal(t, "", flag.ID)
}

func TestResourceToFlag_NilValues(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)

	id := "max-retries"
	flagType := "flag"
	now := time.Now()
	desc := "unconstrained"
	r := genflags.FlagResource{
		Id:   &id,
		Type: genflags.FlagResourceType(flagType),
		Attributes: genflags.Flag{
			Name:         "Max Retries",
			Type:         "NUMERIC",
			Default:      float64(3),
			Values:       nil,
			Description:  &desc,
			Environments: nil,
			CreatedAt:    &now,
		},
	}

	flag := resourceToFlag(r, fc)
	assert.Equal(t, "max-retries", flag.ID)
	assert.Nil(t, flag.Values, "unconstrained flag should have nil Values")
	assert.Equal(t, float64(3), flag.Default)
}

func TestResourceToFlag_WithValues(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	id := "theme"
	now := time.Now()
	vals := []genflags.FlagValue{{Name: "Light", Value: "light"}, {Name: "Dark", Value: "dark"}}
	r := genflags.FlagResource{
		Id:   &id,
		Type: genflags.FlagResourceTypeFlag,
		Attributes: genflags.Flag{
			Name:      "Theme",
			Type:      "STRING",
			Default:   "light",
			Values:    &vals,
			CreatedAt: &now,
		},
	}
	flag := resourceToFlag(r, fc)
	require.NotNil(t, flag.Values)
	assert.Len(t, *flag.Values, 2)
}

func TestBuildFlagRequest_NilValues(t *testing.T) {
	req := buildFlagRequest("max-retries", "Max Retries", "NUMERIC", float64(3), nil, nil, nil)
	assert.Nil(t, req.Data.Attributes.Values, "nil values should serialize as null")
}

func TestBuildFlagCreateRequest(t *testing.T) {
	desc := "A test flag"
	values := []FlagValue{{Name: "True", Value: true}}
	req := buildFlagCreateRequest("feature-x", "Feature X", "BOOLEAN", true, &values, &desc, nil)
	assert.Equal(t, "feature-x", req.Data.Id)
	assert.Equal(t, genflags.FlagCreateResourceTypeFlag, req.Data.Type)
	require.NotNil(t, req.Data.Attributes.Values)
	assert.Len(t, *req.Data.Attributes.Values, 1)
}

// --- extractFlagEnvironments ---

func TestExtractFlagEnvironments_Nil(t *testing.T) {
	result := extractFlagEnvironments(nil)
	assert.NotNil(t, result)
	assert.Len(t, result, 0)
}

func TestExtractFlagEnvironments_WithData(t *testing.T) {
	enabled := true
	dflt := "env-default"
	desc := "test rule"
	envs := map[string]genflags.FlagEnvironment{
		"production": {
			Enabled: &enabled,
			Default: dflt,
			Rules: &[]genflags.FlagRule{
				{Logic: map[string]interface{}{"==": true}, Value: true, Description: &desc},
			},
		},
	}
	result := extractFlagEnvironments(&envs)
	assert.Contains(t, result, "production")
	prodData := result["production"].(map[string]interface{})
	assert.Equal(t, true, prodData["enabled"])
	assert.Equal(t, "env-default", prodData["default"])
	rules := prodData["rules"].([]interface{})
	assert.Len(t, rules, 1)
}

func TestExtractFlagEnvironments_NilRules(t *testing.T) {
	enabled := true
	envs := map[string]genflags.FlagEnvironment{
		"staging": {
			Enabled: &enabled,
		},
	}
	result := extractFlagEnvironments(&envs)
	prodData := result["staging"].(map[string]interface{})
	rules := prodData["rules"].([]interface{})
	assert.Len(t, rules, 0)
}

func TestExtractFlagEnvironments_RuleNoDescription(t *testing.T) {
	enabled := true
	envs := map[string]genflags.FlagEnvironment{
		"staging": {
			Enabled: &enabled,
			Rules: &[]genflags.FlagRule{
				{Logic: map[string]interface{}{}, Value: false},
			},
		},
	}
	result := extractFlagEnvironments(&envs)
	prodData := result["staging"].(map[string]interface{})
	rules := prodData["rules"].([]interface{})
	rule := rules[0].(map[string]interface{})
	_, hasDesc := rule["description"]
	assert.False(t, hasDesc)
}

// --- buildFlagRequest ---

func TestBuildFlagRequest_Create(t *testing.T) {
	desc := "A test flag"
	values := []FlagValue{{Name: "True", Value: true}, {Name: "False", Value: false}}
	req := buildFlagRequest("feature-x", "Feature X", "BOOLEAN", true, &values, &desc, nil)
	require.NotNil(t, req.Data.Id)
	assert.Equal(t, "feature-x", *req.Data.Id)
	require.NotNil(t, req.Data.Attributes.Values)
	assert.Len(t, *req.Data.Attributes.Values, 2)
}

func TestBuildFlagRequest_Update(t *testing.T) {
	values := []FlagValue{{Name: "True", Value: true}}
	envs := map[string]interface{}{
		"production": map[string]interface{}{
			"enabled": true,
			"default": false,
			"rules":   []interface{}{},
		},
	}
	req := buildFlagRequest("flag-id", "Feature X", "BOOLEAN", true, &values, nil, envs)
	assert.NotNil(t, req.Data.Id)
	assert.Equal(t, "flag-id", *req.Data.Id)
	assert.NotNil(t, req.Data.Attributes.Environments)
}

// --- buildGenFlagEnvironments ---

func TestBuildGenFlagEnvironments_Nil(t *testing.T) {
	assert.Nil(t, buildGenFlagEnvironments(nil))
}

func TestBuildGenFlagEnvironments_NonMapEnvData(t *testing.T) {
	envs := map[string]interface{}{
		"bad": "not-a-map",
	}
	result := buildGenFlagEnvironments(envs)
	assert.NotNil(t, result)
	// "bad" is skipped because it's not a map
	assert.Len(t, *result, 0)
}

func TestBuildGenFlagEnvironments_WithRules(t *testing.T) {
	envs := map[string]interface{}{
		"production": map[string]interface{}{
			"enabled": true,
			"default": false,
			"rules": []interface{}{
				map[string]interface{}{
					"logic":       map[string]interface{}{"==": true},
					"value":       true,
					"description": "test rule",
				},
			},
		},
	}
	result := buildGenFlagEnvironments(envs)
	require.NotNil(t, result)
	prod, ok := (*result)["production"]
	require.True(t, ok)
	assert.NotNil(t, prod.Enabled)
	assert.True(t, *prod.Enabled)
	require.NotNil(t, prod.Rules)
	assert.Len(t, *prod.Rules, 1)
	require.NotNil(t, (*prod.Rules)[0].Description)
	assert.Equal(t, "test rule", *(*prod.Rules)[0].Description)
}

func TestBuildGenFlagEnvironments_RuleNonMapLogic(t *testing.T) {
	envs := map[string]interface{}{
		"staging": map[string]interface{}{
			"rules": []interface{}{
				map[string]interface{}{
					"logic": "not-a-map",
					"value": true,
				},
			},
		},
	}
	result := buildGenFlagEnvironments(envs)
	require.NotNil(t, result)
	staging := (*result)["staging"]
	require.NotNil(t, staging.Rules)
	rules := *staging.Rules
	// Logic should default to empty map when not a map
	assert.Equal(t, map[string]interface{}{}, rules[0].Logic)
}

func TestBuildGenFlagEnvironments_RulesNotSlice(t *testing.T) {
	envs := map[string]interface{}{
		"staging": map[string]interface{}{
			"rules": "not-a-slice",
		},
	}
	result := buildGenFlagEnvironments(envs)
	require.NotNil(t, result)
	staging := (*result)["staging"]
	// Rules remain nil because the raw value isn't a slice
	assert.Nil(t, staging.Rules)
}

func TestBuildGenFlagEnvironments_RulesItemNotMap(t *testing.T) {
	envs := map[string]interface{}{
		"staging": map[string]interface{}{
			"rules": []interface{}{
				"not-a-map",
			},
		},
	}
	result := buildGenFlagEnvironments(envs)
	require.NotNil(t, result)
	staging := (*result)["staging"]
	require.NotNil(t, staging.Rules)
	// Non-map items are skipped
	assert.Len(t, *staging.Rules, 0)
}

// --- flushContexts ---

func TestFlushContexts_Empty(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	// Should not panic with empty batch
	fc.flushContexts(context.Background(), nil)
	fc.flushContexts(context.Background(), []map[string]interface{}{})
}

func TestFlushContexts_SendsBatch(t *testing.T) {
	var receivedPayload map[string]interface{}
	var mu sync.Mutex

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/contexts/bulk" {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			_ = json.Unmarshal(b, &receivedPayload)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	batch := []map[string]interface{}{
		{"type": "user", "key": "u1", "attributes": map[string]interface{}{"plan": "free"}},
	}
	fc.flushContexts(context.Background(), batch)

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, receivedPayload)
	contexts := receivedPayload["contexts"].([]interface{})
	assert.Len(t, contexts, 1)
	item := contexts[0].(map[string]interface{})
	assert.Equal(t, "user", item["type"])
	assert.Equal(t, "u1", item["key"])
}

// --- Flag model helpers ---

func TestCopyEnvMap_Nil(t *testing.T) {
	result := copyEnvMap(nil)
	assert.NotNil(t, result)
	assert.Len(t, result, 0)
}

func TestCopyEnvMap_CopiesData(t *testing.T) {
	original := map[string]interface{}{"staging": "data"}
	result := copyEnvMap(original)
	assert.Equal(t, "data", result["staging"])
	result["staging"] = "changed"
	assert.Equal(t, "data", original["staging"]) // original unchanged
}

func TestCopyMap(t *testing.T) {
	original := map[string]interface{}{"key": "value"}
	result := copyMap(original)
	assert.Equal(t, "value", result["key"])
	result["key"] = "changed"
	assert.Equal(t, "value", original["key"])
}

func TestFlagApply(t *testing.T) {
	f := &Flag{ID: "old"}
	other := &Flag{
		ID:      "new",
		Name:    "Feature",
		Type:    "BOOLEAN",
		Default: true,
	}
	f.apply(other)
	assert.Equal(t, "new", f.ID)
	assert.Equal(t, "Feature", f.Name)
}

// --- Client ensureEventStream (SmplClient) ---

func TestSmplClient_EnsureEventStream(t *testing.T) {
	c := &SmplClient{
		apiKey: "sk_test",
		appURL: "https://app.smplkit.com",
	}
	s1 := c.ensureEventStream()
	s2 := c.ensureEventStream()
	assert.Same(t, s1, s2)

	c.stopEventStream()
}

// --- FlagsRuntime additional coverage ---

func TestFlagsRuntime_FireChangeListenersAll(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.mu.Lock()
	rt.flagStore = map[string]map[string]interface{}{
		"flag1": {"default": true},
		"flag2": {"default": false},
	}
	rt.mu.Unlock()

	var events []*FlagChangeEvent
	var mu sync.Mutex
	rt.OnChange(func(e *FlagChangeEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	rt.fireChangeListenersAll("manual")

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, events, 2)
}

func TestFlagsRuntime_HandleSpecificListener_Panic(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	handle := rt.BooleanFlag("feature", true)

	handle.OnChange(func(e *FlagChangeEvent) {
		panic("flag listener panic")
	})

	// Should not propagate the panic
	assert.NotPanics(t, func() {
		rt.fireChangeListeners("feature", "manual")
	})
}

func TestFlagsRuntime_EvaluateHandle_NilEvaluationResult(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default": nil,
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"enabled": true,
					"rules":   []interface{}{},
				},
			},
		},
	}
	rt.mu.Unlock()

	// When evaluateFlag returns nil, should use defaultVal
	value := rt.evaluateHandle(context.Background(), "feature", "fallback", nil)
	assert.Equal(t, "fallback", value)
}

func TestContextRegistrationBuffer_Eviction(t *testing.T) {
	buf := newContextRegistrationBuffer()
	// Fill to LRU size to trigger eviction
	for i := 0; i < contextRegistrationLRUSize+1; i++ {
		ctx := Context{Type: "user", Key: "u" + string(rune(i)), Attributes: map[string]interface{}{}}
		buf.observe([]Context{ctx})
	}
	// After eviction, the seen map should have been cleared and repopulated
	assert.True(t, buf.pendingCount() > contextRegistrationLRUSize)
}

// --- FlagsClient pass-through methods ---

func TestFlagsClient_BoolFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.connected = true
	handle := fc.BooleanFlag("feature", false)
	assert.NotNil(t, handle)
	assert.Equal(t, false, handle.Get(context.Background()))
}

func TestFlagsClient_StringFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.connected = true
	handle := fc.StringFlag("theme", "light")
	assert.Equal(t, "light", handle.Get(context.Background()))
}

func TestFlagsClient_NumberFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.connected = true
	handle := fc.NumberFlag("max-retries", 3.0)
	assert.Equal(t, 3.0, handle.Get(context.Background()))
}

func TestFlagsClient_JsonFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.connected = true
	dflt := map[string]interface{}{"color": "blue"}
	handle := fc.JsonFlag("settings", dflt)
	assert.Equal(t, dflt, handle.Get(context.Background()))
}

func TestFlagsClient_SetContextProvider(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	called := false
	fc.SetContextProvider(func(ctx context.Context) []Context {
		called = true
		return nil
	})
	// Provider is set but won't be called until evaluation
	assert.False(t, called)
}

func TestFlagsClient_ConnectionStatus(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	assert.Equal(t, "disconnected", fc.ConnectionStatus())
}

func TestFlagsClient_Stats(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	stats := fc.Stats()
	assert.Equal(t, 0, stats.CacheHits)
	assert.Equal(t, 0, stats.CacheMisses)
}

func TestFlagsClient_OnChange(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	called := false
	fc.OnChange(func(e *FlagChangeEvent) {
		called = true
	})
	fc.runtime.fireChangeListeners("test", "manual")
	assert.True(t, called)
}

func TestFlagsClient_Evaluate_ConnectedWithStore(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)

	// Set up connected state with a flag in the store
	fc.runtime.connected = true
	fc.runtime.mu.Lock()
	fc.runtime.environment = "production"
	fc.runtime.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default":      "flag-default",
			"environments": map[string]interface{}{},
		},
	}
	fc.runtime.mu.Unlock()

	result := fc.Evaluate(context.Background(), "feature", "production", nil)
	assert.Equal(t, "flag-default", result)
}

// --- Typed flag handle Get with variadic contexts ---

func TestBooleanFlagHandle_Get_NoContexts(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	handle := rt.BooleanFlag("feature", false)
	assert.Equal(t, false, handle.Get(context.Background()))
}

func TestStringFlagHandle_Get_NoContexts(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	handle := rt.StringFlag("theme", "light")
	assert.Equal(t, "light", handle.Get(context.Background()))
}

func TestNumberFlagHandle_Get_NoContexts(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	handle := rt.NumberFlag("retries", 5.0)
	assert.Equal(t, 5.0, handle.Get(context.Background()))
}

func TestJsonFlagHandle_Get_NoContexts(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	dflt := map[string]interface{}{"a": "b"}
	handle := rt.JsonFlag("config", dflt)
	assert.Equal(t, dflt, handle.Get(context.Background()))
}

// --- NumberFlagHandle type coercion ---

func TestNumberFlagHandle_GetInt(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"retries": {
			"default":      float64(3),
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.NumberFlag("retries", 0.0)
	result := handle.Get(context.Background())
	assert.Equal(t, 3.0, result)
}

// --- EvaluateFlag edge cases ---

func TestEvaluateFlag_NilEnvironments(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": "flag-default",
	}
	result := evaluateFlag(flagDef, "production", map[string]interface{}{})
	assert.Equal(t, "flag-default", result)
}

func TestEvaluateFlag_EnvDataNotMap(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": "flag-default",
		"environments": map[string]interface{}{
			"production": "not-a-map",
		},
	}
	result := evaluateFlag(flagDef, "production", map[string]interface{}{})
	assert.Equal(t, "flag-default", result)
}

func TestEvaluateFlag_RuleNotMap(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": "flag-default",
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"rules":   []interface{}{"not-a-map"},
			},
		},
	}
	result := evaluateFlag(flagDef, "production", map[string]interface{}{})
	assert.Equal(t, "flag-default", result)
}

func TestEvaluateFlag_RuleLogicNotMap(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": "flag-default",
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"rules": []interface{}{
					map[string]interface{}{
						"logic": "not-a-map",
						"value": true,
					},
				},
			},
		},
	}
	result := evaluateFlag(flagDef, "production", map[string]interface{}{})
	assert.Equal(t, "flag-default", result)
}

// --- marshalSorted ---

func TestMarshalSorted_NonMap(t *testing.T) {
	b, err := marshalSorted("hello")
	require.NoError(t, err)
	assert.Equal(t, `"hello"`, string(b))
}

func TestMarshalSorted_EmptyMap(t *testing.T) {
	b, err := marshalSorted(map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(b))
}

// --- joinStrings ---

func TestJoinStrings_Empty(t *testing.T) {
	assert.Equal(t, "", joinStrings(nil))
}

func TestJoinStrings_Single(t *testing.T) {
	assert.Equal(t, "a", joinStrings([]string{"a"}))
}

func TestJoinStrings_Multiple(t *testing.T) {
	assert.Equal(t, "a,b,c", joinStrings([]string{"a", "b", "c"}))
}

// --- NewContext with options ---

func TestNewContext_NilAttrs(t *testing.T) {
	c := NewContext("user", "u1", nil)
	assert.NotNil(t, c.Attributes)
	assert.Len(t, c.Attributes, 0)
}

func TestNewContext_WithOptions(t *testing.T) {
	c := NewContext("user", "u1", nil, WithName("Alice"), WithAttr("plan", "enterprise"))
	assert.Equal(t, "Alice", c.Name)
	assert.Equal(t, "enterprise", c.Attributes["plan"])
}

// --- Rule builder ---

func TestRule_ZeroConditions(t *testing.T) {
	rule := NewRule("empty rule").Environment("staging").Serve(true).Build()
	logic := rule["logic"].(map[string]interface{})
	assert.Len(t, logic, 0)
}

func TestRule_SingleCondition(t *testing.T) {
	rule := NewRule("single").Environment("staging").When("user.plan", "==", "enterprise").Serve(true).Build()
	logic := rule["logic"].(map[string]interface{})
	assert.Contains(t, logic, "==")
}

func TestRule_MultipleConditions(t *testing.T) {
	rule := NewRule("multi").
		When("user.plan", "==", "enterprise").
		When("user.age", ">", 18).
		Serve(true).
		Build()
	logic := rule["logic"].(map[string]interface{})
	assert.Contains(t, logic, "and")
}

func TestRule_ContainsOperator(t *testing.T) {
	rule := NewRule("contains").When("user.tags", "contains", "beta").Serve(true).Build()
	logic := rule["logic"].(map[string]interface{})
	assert.Contains(t, logic, "in")
}

func TestRule_WithEnvironment(t *testing.T) {
	rule := NewRule("env rule").Environment("staging").Serve(true).Build()
	assert.Equal(t, "staging", rule["environment"])
}

func TestRule_WithoutEnvironment(t *testing.T) {
	rule := NewRule("no env").Serve(true).Build()
	_, hasEnv := rule["environment"]
	assert.False(t, hasEnv)
}

// --- FlagsClient generated-client methods ---

func sampleFlagResponseJSON(id, name, flagType string) string {
	return `{
		"data": {
			"id": "` + id + `",
			"type": "flag",
			"attributes": {
				"name": "` + name + `",
				"type": "` + flagType + `",
				"default": true,
				"values": [{"name": "True", "value": true}, {"name": "False", "value": false}],
				"description": "A test flag",
				"environments": {},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}
	}`
}

func sampleFlagListResponseJSON(id, name, flagType string) string {
	return `{
		"data": [{
			"id": "` + id + `",
			"type": "flag",
			"attributes": {
				"name": "` + name + `",
				"type": "` + flagType + `",
				"default": true,
				"values": [{"name": "True", "value": true}],
				"description": null,
				"environments": {},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": null
			}
		}]
	}`
}

const testFlagID = "feature-x"

func TestFlagsClient_Get_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/flags/feature-x" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleFlagResponseJSON("feature-x", "Feature X", "BOOLEAN")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	flag, err := fc.Get(context.Background(), "feature-x")
	require.NoError(t, err)
	assert.Equal(t, "feature-x", flag.ID)
	assert.Equal(t, "Feature X", flag.Name)
	assert.Equal(t, "BOOLEAN", flag.Type)
	assert.Equal(t, true, flag.Default)
}

func TestFlagsClient_Get_NotFound(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))

	_, err := fc.Get(context.Background(), "nonexistent-flag")
	assert.Error(t, err)
}

func TestFlagsClient_Create_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/flags" {
			b, _ := io.ReadAll(r.Body)
			var req map[string]interface{}
			_ = json.Unmarshal(b, &req)
			data := req["data"].(map[string]interface{})
			assert.Equal(t, "feature-x", data["id"])

			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(sampleFlagResponseJSON("feature-x", "Feature X", "BOOLEAN")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	flag := fc.NewBooleanFlag("feature-x", true, WithFlagName("Feature X"))
	err := flag.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "feature-x", flag.ID)
}

func TestFlagsClient_Create_EmptyID_PostPath(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/flags", r.URL.Path)

		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleFlagResponseJSON("server-id", "Feature X", "BOOLEAN")))
	}))

	flag := fc.NewBooleanFlag("feature-x", true, WithFlagName("Feature X"))
	flag.ID = "" // Clear ID to trigger create (POST) path
	err := flag.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "server-id", flag.ID)
}

func TestFlagsClient_Create_Unconstrained(t *testing.T) {
	unconstrainedResp := `{
		"data": {
			"id": "max-retries",
			"type": "flag",
			"attributes": {
				"name": "Max Retries",
				"type": "NUMERIC",
				"default": 3,
				"values": null,
				"description": "",
				"environments": {},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}
	}`
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/flags" {
			b, _ := io.ReadAll(r.Body)
			var req map[string]interface{}
			_ = json.Unmarshal(b, &req)
			data := req["data"].(map[string]interface{})
			attrs := data["attributes"].(map[string]interface{})
			assert.Nil(t, attrs["values"], "unconstrained create should send values: null")

			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(unconstrainedResp))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	flag := fc.NewNumberFlag("max-retries", 3, WithFlagName("Max Retries"))
	err := flag.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "max-retries", flag.ID)
	assert.Nil(t, flag.Values, "unconstrained flag response should have nil Values")
}

func TestFlagsClient_Get_Unconstrained(t *testing.T) {
	unconstrainedResp := `{
		"data": {
			"id": "max-retries",
			"type": "flag",
			"attributes": {
				"id": "max-retries",
				"name": "Max Retries",
				"type": "NUMERIC",
				"default": 3,
				"values": null,
				"description": "",
				"environments": {},
				"created_at": null,
				"updated_at": null
			}
		}
	}`
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(unconstrainedResp))
	}))

	flag, err := fc.Get(context.Background(), "max-retries")
	require.NoError(t, err)
	assert.Equal(t, "max-retries", flag.ID)
	assert.Nil(t, flag.Values, "unconstrained flag should have nil Values")
	assert.Equal(t, float64(3), flag.Default)
}

func TestFlagsClient_List_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/flags" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleFlagListResponseJSON("feature-x", "Feature X", "BOOLEAN")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	flags, err := fc.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, flags, 1)
	assert.Equal(t, "feature-x", flags[0].ID)
}

func TestFlagsClient_List_WithOptions(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "2", r.URL.Query().Get("page[number]"))
		assert.Equal(t, "5", r.URL.Query().Get("page[size]"))
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON("feature-x", "Feature X", "BOOLEAN")))
	}))

	flags, err := fc.List(context.Background(), WithPageNumber(2), WithPageSize(5))
	require.NoError(t, err)
	assert.Len(t, flags, 1)
}

func TestFlagsClient_Delete_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/flags/feature-x" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	err := fc.Delete(context.Background(), "feature-x")
	assert.NoError(t, err)
}

func TestFlagsClient_Delete_NotFound(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))
	err := fc.Delete(context.Background(), "nonexistent-flag")
	assert.Error(t, err)
}

func TestFlagsClient_UpdateFlag_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/v1/flags/"+testFlagID {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagID, "Updated Name", "BOOLEAN")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	flag := &Flag{
		ID:           testFlagID,
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       &[]FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{},
		client:       fc,
	}

	flag.Name = "Updated Name"
	err := fc.updateFlag(context.Background(), flag)
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", flag.Name)
}

func TestFlagsClient_UpdateFlag_WithAllParams(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagID, "Feature X", "BOOLEAN")))
	}))

	flag := &Flag{
		ID:           testFlagID,
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       &[]FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{},
		client:       fc,
	}

	newDesc := "New Description"
	flag.Name = "New Name"
	flag.Description = &newDesc
	flag.Default = false
	boolVals := []FlagValue{{Name: "True", Value: true}, {Name: "False", Value: false}}
	flag.Values = &boolVals
	flag.Environments = map[string]interface{}{
		"production": map[string]interface{}{
			"enabled": true,
		},
	}
	err := fc.updateFlag(context.Background(), flag)
	require.NoError(t, err)
}

func TestFlag_Save_UpdatePath(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/v1/flags/"+testFlagID {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagID, "Updated via Save", "BOOLEAN")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	now := time.Now()
	flag := &Flag{
		ID:        testFlagID,
		Name:      "Feature X",
		Type:      "BOOLEAN",
		Default:   true,
		CreatedAt: &now,
		client:    fc,
	}

	err := flag.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Updated via Save", flag.Name)
}

func TestFlag_Save_NoClient(t *testing.T) {
	flag := &Flag{ID: "x"}
	err := flag.Save(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "without a client")
}

func TestFlag_Delete_NoClient(t *testing.T) {
	flag := &Flag{ID: "x"}
	err := flag.Delete(context.Background())
	assert.Error(t, err)
}

func TestFlag_Delete_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	flag := &Flag{ID: testFlagID, client: fc}
	err := flag.Delete(context.Background())
	assert.NoError(t, err)
}

// --- fetchAllFlags / fetchFlagsList ---

func TestFlagsClient_FetchAllFlags(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON("feature-x", "Feature X", "BOOLEAN")))
	}))

	store, err := fc.fetchAllFlags(context.Background())
	require.NoError(t, err)
	assert.Contains(t, store, "feature-x")
}

func TestFlagsClient_FetchFlagsList(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON("feature-x", "Feature X", "BOOLEAN")))
	}))

	flags, err := fc.fetchFlagsList(context.Background())
	require.NoError(t, err)
	assert.Len(t, flags, 1)
	assert.Equal(t, "feature-x", flags[0]["id"])
}

func TestFlagsClient_FetchFlagsList_Pagination(t *testing.T) {
	// First page returns a full page (fetchAllPageSize rows), second returns short.
	page := 0
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		var sb strings.Builder
		sb.WriteString(`{"data":[`)
		if page == 1 {
			for i := 0; i < fetchAllPageSize; i++ {
				if i > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(`{"id":"f` + string(rune('a'+i%26)) + string(rune('0'+i/26)) + `","type":"flag","attributes":{"name":"F","type":"BOOLEAN","default":true,"environments":{}}}`)
			}
		} else {
			sb.WriteString(`{"id":"last","type":"flag","attributes":{"name":"F","type":"BOOLEAN","default":true,"environments":{}}}`)
		}
		sb.WriteString(`]}`)
		_, _ = w.Write([]byte(sb.String()))
	}))

	flags, err := fc.fetchFlagsList(context.Background())
	require.NoError(t, err)
	assert.Equal(t, fetchAllPageSize+1, len(flags))
	assert.Equal(t, 2, page, "should fetch two pages")
}

func TestFlagsClient_FetchFlagsPage_WithValues(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"theme","type":"flag","attributes":{"name":"Theme","type":"STRING","default":"light","values":[{"name":"Light","value":"light"}],"environments":{}}}]}`))
	}))
	flags, err := fc.fetchFlagsPage(context.Background(), 1, fetchAllPageSize)
	require.NoError(t, err)
	require.Len(t, flags, 1)
	assert.NotNil(t, flags[0]["values"])
}

// --- FlagsRuntime Connect / Disconnect / Refresh ---

func TestFlagsRuntime_LazyInit(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON("feature-x", "Feature X", "BOOLEAN")))
	}))
	fc.client.environment = "production"
	fc.environment = "production"

	err := fc.runtime.ensureInit(context.Background())
	require.NoError(t, err)

	// Should have flag in store
	fc.runtime.mu.RLock()
	assert.Equal(t, "production", fc.runtime.environment)
	_, ok := fc.runtime.flagStore["feature-x"]
	assert.True(t, ok)
	fc.runtime.mu.RUnlock()

	// Clean up
	fc.Disconnect(context.Background())
	fc.client.stopEventStream()
}

func TestFlagsRuntime_Disconnect(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	fc.client.environment = "staging"
	fc.environment = "staging"

	_ = fc.runtime.ensureInit(context.Background())
	fc.Disconnect(context.Background())

	fc.runtime.mu.RLock()
	assert.Equal(t, "", fc.runtime.environment)
	fc.runtime.mu.RUnlock()

	fc.client.stopEventStream()
}

func TestFlagsRuntime_Refresh(t *testing.T) {
	callCount := 0
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON("feature-x", "Feature X", "BOOLEAN")))
	}))

	// Manually set connected
	fc.runtime.connected = true
	fc.runtime.mu.Lock()
	fc.runtime.environment = "production"
	fc.runtime.mu.Unlock()

	err := fc.Refresh(context.Background())
	require.NoError(t, err)
	assert.True(t, callCount > 0)
}

// --- FlagsRuntime Evaluate (explicit) ---

func TestFlagsRuntime_Evaluate_NotConnected_Fetches(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{
			"id": "feature",
			"type": "flag",
			"attributes": {
				"id": "feature",
				"name": "Feature",
				"type": "BOOLEAN",
				"default": true,
				"values": [{"name": "True", "value": true}],
				"environments": {}
			}
		}]}`))
	}))

	result := fc.Evaluate(context.Background(), "feature", "production", nil)
	assert.Equal(t, true, result)
}

func TestFlagsRuntime_Evaluate_NotConnected_NotFound(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))

	result := fc.Evaluate(context.Background(), "missing", "production", nil)
	assert.Nil(t, result)
}

// --- Flag.Update / Flag.AddRule ---

func TestFlag_Update(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagID, "Updated", "BOOLEAN")))
	}))

	flag := &Flag{
		ID:           testFlagID,
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       &[]FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{},
		client:       fc,
	}

	err := flag.Save(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Updated", flag.Name)
}

func TestFlag_AddRule_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)

	flag := &Flag{
		ID:           testFlagID,
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       &[]FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{},
		client:       fc,
	}

	rule := NewRule("test").
		Environment("production").
		When("user.plan", "==", "enterprise").
		Serve(true).
		Build()

	err := flag.AddRule(rule)
	require.NoError(t, err)
	// AddRule is a local mutation — verify the environment was created with the rule
	envData := flag.Environments["production"].(map[string]interface{})
	rules := envData["rules"].([]interface{})
	assert.Len(t, rules, 1)
}

func TestFlag_AddRule_MissingEnvironment(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	flag := &Flag{ID: testFlagID, client: fc}

	err := flag.AddRule(map[string]interface{}{
		"logic": map[string]interface{}{},
		"value": true,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "environment")
}

type failingTransport struct{}

func (t *failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, assert.AnError
}

// --- FlagsRuntime ConnectionStatus with streamManager ---

func TestFlagsRuntime_ConnectionStatus_WithStreamManager(t *testing.T) {
	stream := newSharedEventStream("https://app.smplkit.com", "test", nil)
	stream.setStatus("connected")

	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.streamManager = stream
	assert.Equal(t, "connected", rt.ConnectionStatus())
}

// --- newResolutionCache default maxSize ---

func TestResolutionCache_DefaultMaxSize(t *testing.T) {
	c := newResolutionCache(0)
	assert.Equal(t, defaultCacheMaxSize, c.maxSize)
}

func TestResolutionCache_NegativeMaxSize(t *testing.T) {
	c := newResolutionCache(-1)
	assert.Equal(t, defaultCacheMaxSize, c.maxSize)
}

// --- FlagsRuntime handleFlagChanged / handleFlagDeleted ---

func TestFlagsRuntime_HandleFlagChanged(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		// Scoped fetch returns single-item response.
		_, _ = w.Write([]byte(`{"data":{
			"id": "feature-x",
			"type": "flag",
			"attributes": {
				"id": "feature-x",
				"name": "Feature X",
				"type": "BOOLEAN",
				"default": false,
				"values": [],
				"environments": {}
			}
		}}`))
	}))

	var changeEvent *FlagChangeEvent
	fc.OnChange(func(e *FlagChangeEvent) {
		changeEvent = e
	})

	fc.runtime.handleFlagChanged(map[string]interface{}{"id": "feature-x"})

	assert.NotNil(t, changeEvent)
	assert.Equal(t, "feature-x", changeEvent.ID)
	assert.Equal(t, "push", changeEvent.Source)
}

func TestFlagsRuntime_HandleFlagDeleted(t *testing.T) {
	// handleFlagDeleted removes from store and fires listener — no HTTP fetch needed.
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.flagStore["deleted-flag"] = map[string]interface{}{"id": "deleted-flag"}

	var evt *FlagChangeEvent
	fc.OnChange(func(e *FlagChangeEvent) {
		evt = e
	})

	fc.runtime.handleFlagDeleted(map[string]interface{}{"id": "deleted-flag"})

	assert.Nil(t, fc.runtime.flagStore["deleted-flag"], "flag should be removed from store")
	require.NotNil(t, evt)
	assert.True(t, evt.Deleted)
	assert.Equal(t, "deleted-flag", evt.ID)
}

// --- FlagsClient flushContexts with actual data ---

func TestFlagsClient_FlushContexts_Lifecycle(t *testing.T) {
	var received bool
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/contexts/bulk" {
			received = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))

	fc.runtime.Register(context.Background(), Context{Type: "user", Key: "u1"})
	batch := fc.runtime.contextBuffer.drain()
	if len(batch) > 0 {
		fc.flushContexts(context.Background(), batch)
	}
	assert.True(t, received)
}

// --- Typed flag handle type mismatch ---

func TestBooleanFlagHandle_Get_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default":      "not-a-bool",
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.BooleanFlag("feature", false)
	result := handle.Get(context.Background())
	assert.Equal(t, false, result) // falls back to default
}

func TestStringFlagHandle_Get_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"theme": {
			"default":      42,
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.StringFlag("theme", "light")
	result := handle.Get(context.Background())
	assert.Equal(t, "light", result)
}

func TestNumberFlagHandle_Get_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"retries": {
			"default":      "not-a-number",
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.NumberFlag("retries", 5.0)
	result := handle.Get(context.Background())
	assert.Equal(t, 5.0, result)
}

func TestJsonFlagHandle_Get_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"config": {
			"default":      "not-a-map",
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	dflt := map[string]interface{}{"a": "b"}
	handle := rt.JsonFlag("config", dflt)
	result := handle.Get(context.Background())
	assert.Equal(t, dflt, result)
}

// --- NumberFlagHandle int/int64 coercion ---

func TestNumberFlagHandle_Get_IntValue(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"count": {
			"default":      int(42),
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.NumberFlag("count", 0.0)
	result := handle.Get(context.Background())
	assert.Equal(t, 42.0, result)
}

func TestNumberFlagHandle_Get_Int64Value(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"count": {
			"default":      int64(99),
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.NumberFlag("count", 0.0)
	result := handle.Get(context.Background())
	assert.Equal(t, 99.0, result)
}

// --- Error path tests for full coverage ---

func TestFlagsClient_Get_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"internal error"}]}`))
	}))

	_, err := fc.Get(context.Background(), "feature-x")
	assert.Error(t, err)
}

func TestFlagsClient_Get_InvalidJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))

	_, err := fc.Get(context.Background(), "feature-x")
	assert.Error(t, err)
}

func TestFlagsClient_Create_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"bad request"}]}`))
	}))

	flag := fc.NewBooleanFlag("x", true, WithFlagName("X"))
	err := flag.Save(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_Create_InvalidJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`not json`))
	}))

	flag := fc.NewBooleanFlag("x", true, WithFlagName("X"))
	err := flag.Save(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_List_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"forbidden"}]}`))
	}))

	_, err := fc.List(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_List_InvalidJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))

	_, err := fc.List(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_Delete_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"forbidden"}]}`))
	}))

	err := fc.Delete(context.Background(), "feature-x")
	assert.Error(t, err)
}

func TestFlagsClient_UpdateFlag_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"bad request"}]}`))
	}))

	flag := &Flag{ID: testFlagID, Type: "BOOLEAN", Default: true, Values: &[]FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	err := fc.updateFlag(context.Background(), flag)
	assert.Error(t, err)
}

func TestFlagsClient_UpdateFlag_InvalidJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))

	flag := &Flag{ID: testFlagID, Type: "BOOLEAN", Default: true, Values: &[]FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	err := fc.updateFlag(context.Background(), flag)
	assert.Error(t, err)
}

func TestFlagsClient_FetchFlagsList_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	_, err := fc.fetchFlagsList(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_FetchFlagsList_InvalidJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))

	_, err := fc.fetchFlagsList(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_FetchAllFlags_Error(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	_, err := fc.fetchAllFlags(context.Background())
	assert.Error(t, err)
}

// --- Get with contexts type coercion and mismatch (no-contexts provider path) ---

func TestBooleanFlagHandle_Get_NoContexts_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default":      "not-a-bool",
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.BooleanFlag("feature", true)
	result := handle.Get(context.Background())
	assert.Equal(t, true, result)
}

func TestStringFlagHandle_Get_NoContexts_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"theme": {
			"default":      42,
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.StringFlag("theme", "dark")
	result := handle.Get(context.Background())
	assert.Equal(t, "dark", result)
}

func TestNumberFlagHandle_Get_NoContexts_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"retries": {
			"default":      "not-a-number",
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.NumberFlag("retries", 7.0)
	result := handle.Get(context.Background())
	assert.Equal(t, 7.0, result)
}

func TestJsonFlagHandle_Get_NoContexts_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"config": {
			"default":      "not-a-map",
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	dflt := map[string]interface{}{"x": "y"}
	handle := rt.JsonFlag("config", dflt)
	result := handle.Get(context.Background())
	assert.Equal(t, dflt, result)
}

// --- FlagsRuntime evaluateFlag JSON logic error ---

func TestEvaluateFlag_JSONLogicError(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": "flag-default",
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"default": "env-default",
				"rules": []interface{}{
					map[string]interface{}{
						// Invalid logic that will cause an error
						"logic": map[string]interface{}{
							"invalid_op": []interface{}{"a", "b"},
						},
						"value": "should-not-match",
					},
				},
			},
		},
	}
	result := evaluateFlag(flagDef, "production", map[string]interface{}{})
	// Should fall through to env default on error
	assert.Equal(t, "env-default", result)
}

// --- FlagsRuntime Evaluate with contexts ---

func TestFlagsRuntime_Evaluate_Connected(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.connected = true
	fc.runtime.mu.Lock()
	fc.runtime.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default":      "val",
			"environments": map[string]interface{}{},
		},
	}
	fc.runtime.mu.Unlock()

	contexts := []Context{
		{Type: "user", Key: "u1", Attributes: map[string]interface{}{"plan": "free"}},
	}
	result := fc.Evaluate(context.Background(), "feature", "prod", contexts)
	assert.Equal(t, "val", result)
}

// --- FlagsRuntime Refresh error ---

func TestFlagsRuntime_Refresh_Error(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	fc.runtime.connected = true

	err := fc.Refresh(context.Background())
	assert.Error(t, err)
}

// --- Flag.Update error ---

func TestFlag_Update_Error(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	flag := &Flag{ID: testFlagID, Type: "BOOLEAN", Default: true, Values: &[]FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	err := flag.Save(context.Background())
	assert.Error(t, err)
}

// --- Flag.AddRule error paths ---

func TestFlag_AddRule_MissingEnvironmentKey(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)

	flag := &Flag{ID: testFlagID, Type: "BOOLEAN", Default: true, Values: &[]FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	// Rule without environment key should fail.
	rule := map[string]interface{}{
		"logic": map[string]interface{}{},
		"value": true,
	}
	err := flag.AddRule(rule)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "environment")
}

func TestFlag_AddRule_NewEnvironment(t *testing.T) {
	flag := &Flag{
		ID:           testFlagID,
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       &[]FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{}, // No "staging" env
	}

	rule := NewRule("test").
		Environment("staging"). // New environment
		When("user.plan", "==", "enterprise").
		Serve(true).
		Build()

	err := flag.AddRule(rule)
	require.NoError(t, err)
	// Verify the staging environment was created
	envData := flag.Environments["staging"].(map[string]interface{})
	assert.Equal(t, true, envData["enabled"])
	rules := envData["rules"].([]interface{})
	assert.Len(t, rules, 1)
}

func TestFlag_AddRule_ExistingEnvironment(t *testing.T) {
	flag := &Flag{
		ID:   testFlagID,
		Name: "Feature X",
		Type: "BOOLEAN",
		Environments: map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"rules":   []interface{}{},
			},
		},
		Values: &[]FlagValue{{Name: "True", Value: true}},
	}

	rule := NewRule("test").
		Environment("production").
		Serve(true).
		Build()

	err := flag.AddRule(rule)
	require.NoError(t, err)
	// Verify rule was added to existing environment
	envData := flag.Environments["production"].(map[string]interface{})
	rules := envData["rules"].([]interface{})
	assert.Len(t, rules, 1)
}

// --- FlagsRuntime handleFlagChanged fetch error ---

func TestFlagsRuntime_HandleFlagChanged_FetchError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	// Should not panic
	fc.runtime.handleFlagChanged(map[string]interface{}{"id": "feature-x"})
}

// --- FlagsRuntime evaluateHandle provider flush threshold ---

func TestFlagsRuntime_EvaluateHandle_ProviderFlushThreshold(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rt := fc.runtime
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default":      true,
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	// Fill the context buffer to near threshold
	for i := 0; i < contextBatchFlushSize; i++ {
		rt.contextBuffer.observe([]Context{
			{Type: "user", Key: "u" + string(rune('A'+i)), Attributes: map[string]interface{}{}},
		})
	}

	rt.SetContextProvider(func(ctx context.Context) []Context {
		return []Context{
			{Type: "user", Key: "trigger-flush", Attributes: map[string]interface{}{}},
		}
	})

	// This should trigger a flush because pending count >= threshold
	rt.evaluateHandle(context.Background(), "feature", true, nil)
}

// --- FlagsRuntime Disconnect without wsManager ---

func TestFlagsRuntime_Disconnect_NoWSManager(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	fc.runtime.connected = true
	fc.runtime.mu.Lock()
	fc.runtime.environment = "test"
	fc.runtime.mu.Unlock()

	// Disconnect without ever having connected (no wsManager)
	fc.Disconnect(context.Background())

	fc.runtime.mu.RLock()
	assert.Equal(t, "", fc.runtime.environment)
	fc.runtime.mu.RUnlock()
}

// --- FlagsRuntime ensureInit error ---

func TestFlagsRuntime_EnsureInit_FetchError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))
	fc.client.environment = "production"
	fc.environment = "production"

	err := fc.runtime.ensureInit(context.Background())
	assert.Error(t, err)
}

func TestFlagsRuntime_EnsureInit_NilClient(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	err := rt.ensureInit(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestFlagsRuntime_EnsureInit_AlreadyConnected(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.connected = true
	err := fc.runtime.ensureInit(context.Background())
	assert.NoError(t, err)
}

// --- helpers for test infrastructure ---

func flagResource(id, flagType, name, vType string, dflt interface{}, desc string, created time.Time) genflags.FlagResource {
	vals := []genflags.FlagValue{{Name: "True", Value: true}}
	return genflags.FlagResource{
		Id:   &id,
		Type: genflags.FlagResourceType(flagType),
		Attributes: genflags.Flag{
			Name:         name,
			Type:         genflags.FlagType(vType),
			Default:      dflt,
			Values:       &vals,
			Description:  &desc,
			Environments: nil,
			CreatedAt:    &created,
		},
	}
}

func flagResourceNoID(name, vType string, dflt interface{}, created time.Time) genflags.FlagResource {
	vals := []genflags.FlagValue{}
	return genflags.FlagResource{
		Id:   nil,
		Type: genflags.FlagResourceTypeFlag,
		Attributes: genflags.Flag{
			Name:      name,
			Type:      genflags.FlagType(vType),
			Default:   dflt,
			Values:    &vals,
			CreatedAt: &created,
		},
	}
}

// --- Additional coverage: io.ReadAll error paths ---

// errorReader is a reader that always returns an error.
type errorReader struct{}

func (r *errorReader) Read(p []byte) (int, error) {
	return 0, assert.AnError
}

func (r *errorReader) Close() error {
	return nil
}

// brokenBodyTransport returns HTTP responses with a body that errors on Read.
type brokenBodyTransport struct{}

func (t *brokenBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       &errorReader{},
		Header:     make(http.Header),
	}, nil
}

// newFlagsClientWithTransport creates a FlagsClient that uses a custom transport
// for both the generated client and the plain HTTP client.
func newFlagsClientWithTransport(t *testing.T, transport http.RoundTripper) *FlagsClient {
	t.Helper()
	httpClient := &http.Client{Transport: transport}

	flagsHeaderEditor := genflags.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genFlagsClient, _ := genflags.NewClient("http://localhost:1",
		genflags.WithHTTPClient(httpClient),
		flagsHeaderEditor,
	)

	appHeaderEditor := genapp.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genAppClient, _ := genapp.NewClient("http://localhost:1",
		genapp.WithHTTPClient(httpClient),
		appHeaderEditor,
	)

	c := newTestParentClient("http://localhost:1", httpClient, genAppClient)
	fc := newFlagsClient(c, genFlagsClient, genAppClient, c.platform.contexts, nil)
	c.flags = fc
	return fc
}

// --- Generated client network error paths ---

func TestFlagsClient_Get_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.Get(context.Background(), "feature-x")
	assert.Error(t, err)
}

func TestFlagsClient_Create_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	flag := fc.NewBooleanFlag("x", true, WithFlagName("X"))
	err := flag.Save(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_List_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.List(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_Delete_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	err := fc.Delete(context.Background(), "feature-x")
	assert.Error(t, err)
}

func TestFlagsClient_UpdateFlag_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	flag := &Flag{ID: testFlagID, Type: "BOOLEAN", Default: true, Values: &[]FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	err := fc.updateFlag(context.Background(), flag)
	assert.Error(t, err)
}

func TestFlagsClient_FetchFlagsList_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.fetchFlagsList(context.Background())
	assert.Error(t, err)
}

// --- io.ReadAll error paths ---

func TestFlagsClient_Get_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	_, err := fc.Get(context.Background(), "feature-x")
	assert.Error(t, err)
}

func TestFlagsClient_Create_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	flag := fc.NewBooleanFlag("x", true, WithFlagName("X"))
	err := flag.Save(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_List_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	_, err := fc.List(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_Delete_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	err := fc.Delete(context.Background(), "feature-x")
	assert.Error(t, err)
}

func TestFlagsClient_UpdateFlag_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	flag := &Flag{ID: testFlagID, Type: "BOOLEAN", Default: true, Values: &[]FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	err := fc.updateFlag(context.Background(), flag)
	assert.Error(t, err)
}

func TestFlagsClient_FetchFlagsList_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	_, err := fc.fetchFlagsList(context.Background())
	assert.Error(t, err)
}

// --- Flag.AddRule error path: updateFlag fails after successful AddRule ---

func TestFlag_AddRule_ThenSave_UpdateError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Update fails
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"update failed"}]}`))
	}))

	now := time.Now()
	flag := &Flag{
		ID:           testFlagID,
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       &[]FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{},
		CreatedAt:    &now,
		client:       fc,
	}

	rule := NewRule("test").
		Environment("production").
		When("user.plan", "==", "enterprise").
		Serve(true).
		Build()

	// AddRule is local — should succeed
	err := flag.AddRule(rule)
	require.NoError(t, err)

	// Save should fail with server error
	err = flag.Save(context.Background())
	assert.Error(t, err)
}

// --- Flag.AddRule existing env with rules (else branch) ---

func TestFlag_AddRule_ExistingEnvWithRules(t *testing.T) {
	flag := &Flag{
		ID:   testFlagID,
		Name: "Feature X",
		Type: "BOOLEAN",
		Environments: map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"rules":   []interface{}{map[string]interface{}{"logic": map[string]interface{}{}, "value": false}},
			},
		},
		Values: &[]FlagValue{{Name: "True", Value: true}},
	}

	rule := NewRule("new rule").
		Environment("production").
		When("user.plan", "==", "enterprise").
		Serve(true).
		Build()

	err := flag.AddRule(rule)
	require.NoError(t, err)
	// Verify rule was appended to existing rules
	envData := flag.Environments["production"].(map[string]interface{})
	rules := envData["rules"].([]interface{})
	assert.Len(t, rules, 2)
}

// --- FlagsRuntime Evaluate fetch error path ---

func TestFlagsRuntime_Evaluate_FetchError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})

	// Not connected — will try to fetch
	result := fc.Evaluate(context.Background(), "feature", "production", nil)
	assert.Nil(t, result)
}

// --- Service context auto-injection ---

func TestFlagsRuntime_ServiceContextAutoInjection(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON("feature", "Feature", "BOOLEAN")))
	}))

	// Set service on the client
	fc.client.service = "my-service"
	fc.service = "my-service"
	fc.client.environment = "production"
	fc.environment = "production"

	// Init and set up a flag with a rule that checks service.key
	err := fc.runtime.ensureInit(context.Background())
	require.NoError(t, err)

	fc.runtime.mu.Lock()
	fc.runtime.flagStore["feature"] = map[string]interface{}{
		"default": false,
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"default": false,
				"rules": []interface{}{
					map[string]interface{}{
						"logic": map[string]interface{}{
							"==": []interface{}{
								map[string]interface{}{"var": "service.key"},
								"my-service",
							},
						},
						"value": true,
					},
				},
			},
		},
	}
	fc.runtime.mu.Unlock()

	// Evaluate without providing service context — should be auto-injected
	handle := fc.BooleanFlag("feature", false)
	result := handle.Get(context.Background())
	assert.Equal(t, true, result, "service context should be auto-injected and match the rule")

	fc.Disconnect(context.Background())
	fc.client.stopEventStream()
}

func TestFlagsRuntime_ServiceContextNotOverridden(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.client.service = "auto-service"
	fc.service = "auto-service"

	fc.runtime.connected = true
	fc.runtime.mu.Lock()
	fc.runtime.environment = "production"
	fc.runtime.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default": false,
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"enabled": true,
					"default": false,
					"rules": []interface{}{
						map[string]interface{}{
							"logic": map[string]interface{}{
								"==": []interface{}{
									map[string]interface{}{"var": "service.key"},
									"custom-service",
								},
							},
							"value": true,
						},
					},
				},
			},
		},
	}
	fc.runtime.mu.Unlock()

	// Provide explicit service context — should NOT be overridden by auto-injection
	handle := fc.BooleanFlag("feature", false)
	result := handle.Get(context.Background(), Context{Type: "service", Key: "custom-service"})
	assert.Equal(t, true, result, "customer-provided service context should take precedence")

	fc.client.stopEventStream()
}

// --- Evaluate service-context auto-injection (explicit Evaluate path) ---

func TestFlagsRuntime_Evaluate_ServiceAutoInject(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.service = "svc-x"
	fc.runtime.connected = true
	fc.runtime.mu.Lock()
	fc.runtime.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default": false,
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"enabled": true,
					"default": false,
					"rules": []interface{}{
						map[string]interface{}{
							"logic": map[string]interface{}{
								"==": []interface{}{map[string]interface{}{"var": "service.key"}, "svc-x"},
							},
							"value": true,
						},
					},
				},
			},
		},
	}
	fc.runtime.mu.Unlock()

	result := fc.Evaluate(context.Background(), "feature", "production", nil)
	assert.Equal(t, true, result)
}

// ---------- NewBooleanFlag ----------

func TestNewBooleanFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	flag := fc.NewBooleanFlag("dark_mode", false)
	assert.Equal(t, "dark_mode", flag.ID)
	assert.Equal(t, "Dark Mode", flag.Name)
	assert.Equal(t, string(FlagTypeBoolean), flag.Type)
	require.NotNil(t, flag.Values)
	assert.Len(t, *flag.Values, 2)
	assert.Same(t, fc, flag.client)
}

func TestNewStringFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	flag := fc.NewStringFlag("greeting", "hello")
	assert.Equal(t, "greeting", flag.ID)
	assert.Equal(t, string(FlagTypeString), flag.Type)
	assert.Nil(t, flag.Values)
}

// ---------- NewNumberFlag ----------

func TestNewNumberFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	flag := fc.NewNumberFlag("price_multiplier", 1.5)
	assert.Equal(t, "price_multiplier", flag.ID)
	assert.Equal(t, "Price Multiplier", flag.Name)
	assert.Equal(t, string(FlagTypeNumeric), flag.Type)
	assert.Equal(t, 1.5, flag.Default)
	assert.NotNil(t, flag.client)
}

func TestNewNumberFlag_WithOptions(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	desc := "A number flag"
	flag := fc.NewNumberFlag("rate", 0.5, WithFlagName("Rate"), WithFlagDescription(desc))
	assert.Equal(t, "Rate", flag.Name)
	require.NotNil(t, flag.Description)
	assert.Equal(t, desc, *flag.Description)
}

// ---------- NewJsonFlag ----------

func TestNewJsonFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	defaultVal := map[string]interface{}{"theme": "dark"}
	flag := fc.NewJsonFlag("ui_config", defaultVal)
	assert.Equal(t, "ui_config", flag.ID)
	assert.Equal(t, "Ui Config", flag.Name)
	assert.Equal(t, string(FlagTypeJSON), flag.Type)
	assert.Equal(t, defaultVal, flag.Default)
	assert.NotNil(t, flag.client)
}

func TestNewJsonFlag_WithOptions(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	defaultVal := map[string]interface{}{"key": "val"}
	flag := fc.NewJsonFlag("config", defaultVal, WithFlagName("JSON Config"))
	assert.Equal(t, "JSON Config", flag.Name)
}

// ---------- FlagsClient.OnChangeKey ----------

func TestFlagsClient_OnChangeKey(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)

	var received *FlagChangeEvent
	fc.OnChangeKey("my-flag", func(evt *FlagChangeEvent) {
		received = evt
	})

	// Trigger via runtime
	fc.runtime.mu.Lock()
	fc.runtime.flagStore = map[string]map[string]interface{}{
		"my-flag": {"default": true},
	}
	fc.runtime.mu.Unlock()

	fc.runtime.fireChangeListeners("my-flag", "manual")

	require.NotNil(t, received)
	assert.Equal(t, "my-flag", received.ID)
	assert.Equal(t, "manual", received.Source)
}

// ---------- FlagsClient discovery (RegisterFlag / Flush / PendingCount) ----------

func TestFlagsClient_RegisterFlag_PendingCount(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	assert.Equal(t, 0, fc.PendingCount())
	fc.RegisterFlag("my-flag", "BOOLEAN", true)
	assert.Equal(t, 1, fc.PendingCount())
	// Duplicate is ignored.
	fc.RegisterFlag("my-flag", "BOOLEAN", false)
	assert.Equal(t, 1, fc.PendingCount())
}

func TestFlagsClient_RegisterFlag_ThresholdFlush(t *testing.T) {
	var bulkCalls atomic.Int32
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/flags/bulk") {
			bulkCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < flagRegistrationThreshold; i++ {
		fc.RegisterFlag("reg-"+string(rune('a'+i%26))+string(rune('0'+i/26)), "BOOLEAN", true)
	}
	time.Sleep(50 * time.Millisecond)
	assert.GreaterOrEqual(t, bulkCalls.Load(), int32(1), "threshold flush should fire")
}

func TestFlagsClient_Flush(t *testing.T) {
	var bulkCalls atomic.Int32
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/flags/bulk") {
			bulkCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	fc.RegisterFlag("flag-a", "BOOLEAN", true)
	fc.Flush(context.Background())
	assert.Equal(t, int32(1), bulkCalls.Load())
	assert.Equal(t, 0, fc.PendingCount(), "buffer committed after successful flush")
}

func TestFlagsClient_Register_BatchBuffers(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	assert.Equal(t, 0, fc.PendingCount())
	fc.Register(context.Background(), []FlagDeclaration{
		{ID: "alpha", Type: FlagTypeBoolean, Default: true},
		{ID: "beta", Type: FlagTypeString, Default: "x"},
	})
	assert.Equal(t, 2, fc.PendingCount())
}

func TestFlagsClient_Register_FlushOptionSendsImmediately(t *testing.T) {
	var captured []genflags.FlagBulkItem
	var bulkCalls atomic.Int32
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/flags/bulk") {
			bulkCalls.Add(1)
			body, _ := io.ReadAll(r.Body)
			var req genflags.FlagBulkRequest
			_ = json.Unmarshal(body, &req)
			captured = req.Flags
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Mix an explicit service/environment with declarations that inherit the
	// client's resolved values (environment="test", service="test-service").
	fc.Register(context.Background(), []FlagDeclaration{
		{ID: "inherits", Type: FlagTypeBoolean, Default: true},
		{ID: "explicit", Type: FlagTypeNumeric, Default: 1, Service: "custom-svc", Environment: "staging"},
	}, WithFlagFlush())

	assert.Equal(t, int32(1), bulkCalls.Load(), "WithFlagFlush sends immediately")
	assert.Equal(t, 0, fc.PendingCount(), "buffer committed after flush")

	byID := map[string]genflags.FlagBulkItem{}
	for _, item := range captured {
		byID[item.Id] = item
	}
	require.Contains(t, byID, "inherits")
	require.Contains(t, byID, "explicit")
	require.NotNil(t, byID["inherits"].Service)
	assert.Equal(t, "test-service", *byID["inherits"].Service)
	assert.Equal(t, "test", *byID["inherits"].Environment)
	assert.Equal(t, "custom-svc", *byID["explicit"].Service)
	assert.Equal(t, "staging", *byID["explicit"].Environment)
}

func TestFlagsClient_Register_ThresholdFlush(t *testing.T) {
	var bulkCalls atomic.Int32
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/flags/bulk") {
			bulkCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	decls := make([]FlagDeclaration, 0, flagRegistrationThreshold)
	for i := 0; i < flagRegistrationThreshold; i++ {
		decls = append(decls, FlagDeclaration{
			ID:      "reg-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Type:    FlagTypeBoolean,
			Default: true,
		})
	}
	// No WithFlagFlush — crossing the batch threshold fires a background flush.
	fc.Register(context.Background(), decls)
	time.Sleep(50 * time.Millisecond)
	assert.GreaterOrEqual(t, bulkCalls.Load(), int32(1), "threshold flush should fire")
}

func TestFlagsClient_FlushSync(t *testing.T) {
	var bulkCalls atomic.Int32
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/flags/bulk") {
			bulkCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	fc.Register(context.Background(), []FlagDeclaration{{ID: "sync-flag", Type: FlagTypeBoolean, Default: false}})
	fc.FlushSync(context.Background())
	assert.Equal(t, int32(1), bulkCalls.Load())
	assert.Equal(t, 0, fc.PendingCount())
}

// ---------- SetEnvironmentEnabled ----------

func TestSetEnvironmentEnabled(t *testing.T) {
	f := &Flag{
		Environments: map[string]interface{}{},
	}

	f.SetEnvironmentEnabled("staging", true)
	envData := f.Environments["staging"].(map[string]interface{})
	assert.True(t, envData["enabled"].(bool))

	// Test with existing environment data
	f.SetEnvironmentEnabled("staging", false)
	envData = f.Environments["staging"].(map[string]interface{})
	assert.False(t, envData["enabled"].(bool))
}

func TestSetEnvironmentEnabled_NewEnvironment(t *testing.T) {
	f := &Flag{
		Environments: map[string]interface{}{},
	}

	f.SetEnvironmentEnabled("production", true)
	envData := f.Environments["production"].(map[string]interface{})
	assert.True(t, envData["enabled"].(bool))
	// Should have rules key
	_, hasRules := envData["rules"]
	assert.True(t, hasRules)
}

// ---------- SetEnvironmentDefault ----------

func TestSetEnvironmentDefault(t *testing.T) {
	f := &Flag{
		Environments: map[string]interface{}{},
	}

	f.SetEnvironmentDefault("staging", "v2")
	envData := f.Environments["staging"].(map[string]interface{})
	assert.Equal(t, "v2", envData["default"])

	// Test with existing environment data
	f.SetEnvironmentDefault("staging", "v3")
	envData = f.Environments["staging"].(map[string]interface{})
	assert.Equal(t, "v3", envData["default"])
}

func TestSetEnvironmentDefault_NewEnvironment(t *testing.T) {
	f := &Flag{
		Environments: map[string]interface{}{},
	}

	f.SetEnvironmentDefault("production", 42)
	envData := f.Environments["production"].(map[string]interface{})
	assert.Equal(t, 42, envData["default"])
}

// ---------- ClearRules ----------

func TestClearRules(t *testing.T) {
	f := &Flag{
		Environments: map[string]interface{}{
			"staging": map[string]interface{}{
				"enabled": true,
				"rules": []interface{}{
					map[string]interface{}{"logic": map[string]interface{}{"==": true}, "value": true},
				},
			},
		},
	}

	f.ClearRules("staging")
	envData := f.Environments["staging"].(map[string]interface{})
	rules := envData["rules"].([]interface{})
	assert.Empty(t, rules)
}

func TestClearRules_NonexistentEnvironment(t *testing.T) {
	f := &Flag{
		Environments: map[string]interface{}{},
	}

	// Should not panic on non-existent environment
	f.ClearRules("nonexistent")
	assert.Empty(t, f.Environments)
}

// ---------- FlagsRuntime.OnChangeKey ----------

func TestFlagsRuntime_OnChangeKey(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	rt := fc.runtime

	var received *FlagChangeEvent
	rt.OnChangeKey("test-flag", func(evt *FlagChangeEvent) {
		received = evt
	})

	rt.mu.Lock()
	rt.flagStore = map[string]map[string]interface{}{
		"test-flag": {"default": true},
	}
	rt.mu.Unlock()

	rt.fireChangeListeners("test-flag", "push")

	require.NotNil(t, received)
	assert.Equal(t, "test-flag", received.ID)
	assert.Equal(t, "push", received.Source)
}

// ---------- fireChangeListeners key listener path ----------

func TestFireChangeListeners_KeyListeners(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	rt := fc.runtime

	var globalReceived, keyReceived *FlagChangeEvent
	rt.OnChange(func(evt *FlagChangeEvent) {
		globalReceived = evt
	})
	rt.OnChangeKey("my-flag", func(evt *FlagChangeEvent) {
		keyReceived = evt
	})

	rt.mu.Lock()
	rt.flagStore = map[string]map[string]interface{}{
		"my-flag": {"default": true},
	}
	rt.mu.Unlock()

	rt.fireChangeListeners("my-flag", "manual")

	require.NotNil(t, globalReceived)
	require.NotNil(t, keyReceived)
	assert.Equal(t, "my-flag", keyReceived.ID)
}

func TestFireChangeListeners_HandleListeners(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	rt := fc.runtime

	// Create a handle and register a listener on it
	handle := rt.BooleanFlag("my-flag", false)
	var handleReceived *FlagChangeEvent
	handle.OnChange(func(evt *FlagChangeEvent) {
		handleReceived = evt
	})

	rt.mu.Lock()
	rt.flagStore = map[string]map[string]interface{}{
		"my-flag": {"default": true},
	}
	rt.mu.Unlock()

	rt.fireChangeListeners("my-flag", "push")

	require.NotNil(t, handleReceived)
	assert.Equal(t, "my-flag", handleReceived.ID)
}

func TestFireChangeListeners_KeyListenerPanicRecovery(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	rt := fc.runtime

	var secondCalled bool
	rt.OnChangeKey("my-flag", func(evt *FlagChangeEvent) {
		panic("bad key listener")
	})
	rt.OnChangeKey("my-flag", func(evt *FlagChangeEvent) {
		secondCalled = true
	})

	rt.mu.Lock()
	rt.flagStore = map[string]map[string]interface{}{
		"my-flag": {"default": true},
	}
	rt.mu.Unlock()

	rt.fireChangeListeners("my-flag", "manual")
	assert.True(t, secondCalled)
}

func TestFireChangeListeners_HandleListenerPanicRecovery(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	rt := fc.runtime

	handle := rt.BooleanFlag("my-flag", false)
	var secondCalled bool
	handle.OnChange(func(evt *FlagChangeEvent) {
		panic("bad handle listener")
	})
	handle.OnChange(func(evt *FlagChangeEvent) {
		secondCalled = true
	})

	rt.mu.Lock()
	rt.flagStore = map[string]map[string]interface{}{
		"my-flag": {"default": true},
	}
	rt.mu.Unlock()

	rt.fireChangeListeners("my-flag", "push")
	assert.True(t, secondCalled)
}

func TestFireChangeListeners_Flags_EmptyKey(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	rt := fc.runtime

	var called bool
	rt.OnChange(func(evt *FlagChangeEvent) {
		called = true
	})

	rt.fireChangeListeners("", "manual")
	assert.False(t, called)
}

// ---------- handleFlagChanged ----------

func TestHandleFlagChanged(t *testing.T) {
	// Single-item response for scoped fetch.
	flagJSON := `{"data":{"id":"my-flag","type":"flag","attributes":{"id":"my-flag","name":"My Flag","type":"BOOLEAN","default":true,"values":[],"environments":{}}}}`

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flagJSON))
	}))
	rt := fc.runtime

	var received *FlagChangeEvent
	rt.OnChange(func(evt *FlagChangeEvent) {
		received = evt
	})

	rt.handleFlagChanged(map[string]interface{}{"id": "my-flag"})

	require.NotNil(t, received)
	assert.Equal(t, "my-flag", received.ID)
	assert.Equal(t, "push", received.Source)
}

// --- createFlag (POST) error paths ---

func TestFlagsClient_CreateFlag_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	flag := fc.NewBooleanFlag("x", true, WithFlagName("X"))
	flag.ID = "" // trigger create (POST) path
	err := flag.Save(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_CreateFlag_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	flag := fc.NewBooleanFlag("x", true, WithFlagName("X"))
	flag.ID = ""
	err := flag.Save(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_CreateFlag_HTTPError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"validation error"}]}`))
	}))

	flag := fc.NewBooleanFlag("x", true, WithFlagName("X"))
	flag.ID = ""
	err := flag.Save(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_CreateFlag_MalformedJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	}))

	flag := fc.NewBooleanFlag("x", true, WithFlagName("X"))
	flag.ID = ""
	err := flag.Save(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestHandleFlagChanged_FetchError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))
	rt := fc.runtime

	var called bool
	rt.OnChange(func(evt *FlagChangeEvent) {
		called = true
	})

	// Should not panic; error causes early return
	rt.handleFlagChanged(map[string]interface{}{"id": "my-flag"})
	assert.False(t, called)
}

// --- flagRegistrationBuffer ---

func TestFlagRegistrationBuffer_AddAndDrain(t *testing.T) {
	buf := newFlagRegistrationBuffer()
	buf.add("flag-a", "BOOLEAN", true, "svc", "prod")
	buf.add("flag-b", "STRING", "on", "svc", "prod")

	assert.Equal(t, 2, buf.pendingCount())
	batch := buf.drain()
	assert.Len(t, batch, 2)
	assert.Equal(t, 0, buf.pendingCount())

	assert.Equal(t, "flag-a", batch[0].id)
	assert.Equal(t, "BOOLEAN", batch[0].flagType)
	assert.Equal(t, true, batch[0].defaultVal)
	assert.Equal(t, "svc", batch[0].service)
	assert.Equal(t, "prod", batch[0].environment)

	assert.Equal(t, "flag-b", batch[1].id)
	assert.Equal(t, "STRING", batch[1].flagType)
}

func TestFlagRegistrationBuffer_Deduplication(t *testing.T) {
	buf := newFlagRegistrationBuffer()
	buf.add("flag-x", "BOOLEAN", true, "svc", "prod")
	buf.add("flag-x", "BOOLEAN", false, "svc", "prod") // duplicate — should be ignored

	assert.Equal(t, 1, buf.pendingCount())
	batch := buf.drain()
	assert.Len(t, batch, 1)
	assert.Equal(t, true, batch[0].defaultVal) // first registration wins
}

func TestFlagRegistrationBuffer_DrainIsNilAfterDrain(t *testing.T) {
	buf := newFlagRegistrationBuffer()
	buf.add("flag-a", "BOOLEAN", true, "", "")
	buf.drain()
	assert.Equal(t, 0, buf.pendingCount())
	// second drain returns empty slice
	assert.Nil(t, buf.drain())
}

func TestFlagRegistrationBuffer_ConcurrentSafety(t *testing.T) {
	buf := newFlagRegistrationBuffer()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			buf.add(string(rune('a'+n%26))+string(rune('0'+n%10)), "BOOLEAN", true, "svc", "prod")
		}(i)
	}
	wg.Wait()
	// No panic and count is consistent.
	assert.Greater(t, buf.pendingCount(), 0)
}

// --- Typed flag methods populate buffer ---

func TestFlagMethods_PopulateBuffer(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.service = "my-svc"
	fc.environment = "staging"
	rt := fc.runtime

	rt.BooleanFlag("bool-flag", true)
	rt.StringFlag("str-flag", "on")
	rt.NumberFlag("num-flag", 3.14)
	rt.JsonFlag("json-flag", map[string]interface{}{"k": "v"})

	assert.Equal(t, 4, rt.flagBuffer.pendingCount())

	batch := rt.flagBuffer.drain()
	require.Len(t, batch, 4)

	byID := make(map[string]flagRegistrationEntry)
	for _, e := range batch {
		byID[e.id] = e
	}

	assert.Equal(t, "BOOLEAN", byID["bool-flag"].flagType)
	assert.Equal(t, true, byID["bool-flag"].defaultVal)
	assert.Equal(t, "my-svc", byID["bool-flag"].service)
	assert.Equal(t, "staging", byID["bool-flag"].environment)

	assert.Equal(t, "STRING", byID["str-flag"].flagType)
	assert.Equal(t, "NUMERIC", byID["num-flag"].flagType)
	assert.Equal(t, "JSON", byID["json-flag"].flagType)
}

func TestFlagMethods_NoBufferWhenClientNil(t *testing.T) {
	// newFlagsRuntime(nil, newContextRegistrationBuffer()) should not panic on flag method calls.
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	assert.NotPanics(t, func() {
		rt.BooleanFlag("b", true)
		rt.StringFlag("s", "x")
		rt.NumberFlag("n", 1.0)
		rt.JsonFlag("j", nil)
	})
	// Buffer should remain empty since client is nil.
	assert.Equal(t, 0, rt.flagBuffer.pendingCount())
}

func TestFlagMethods_ThresholdFlush(t *testing.T) {
	var bulkCallCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/flags/bulk", func(w http.ResponseWriter, r *http.Request) {
		bulkCallCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":50}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(mux.ServeHTTP))
	fc.service = "my-svc"
	fc.environment = "prod"
	rt := fc.runtime

	// Add exactly flagRegistrationThreshold flags — the 50th triggers a goroutine flush.
	for i := 0; i < flagRegistrationThreshold; i++ {
		rt.BooleanFlag(string(rune('a'))+string(rune('0'+i%10))+string(rune('0'+(i/10)%10)), true)
	}

	// Give the goroutine time to fire.
	time.Sleep(50 * time.Millisecond)

	assert.GreaterOrEqual(t, bulkCallCount.Load(), int32(1), "threshold flush should have called bulk endpoint")
}

func TestFlagMethods_ThresholdFlush_AllTypes(t *testing.T) {
	var bulkCallCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/flags/bulk", func(w http.ResponseWriter, r *http.Request) {
		bulkCallCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":50}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	// Test StringFlag threshold.
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(mux.ServeHTTP))
	fc.service = "svc"
	fc.environment = "prod"
	rt := fc.runtime
	for i := 0; i < flagRegistrationThreshold; i++ {
		rt.StringFlag("str-"+string(rune('a'+i%26))+string(rune('0'+i/26)), "val")
	}
	time.Sleep(50 * time.Millisecond)
	assert.GreaterOrEqual(t, bulkCallCount.Load(), int32(1), "StringFlag: threshold flush should fire")

	// Test NumberFlag threshold.
	bulkCallCount.Store(0)
	rt2 := newFlagsRuntime(fc, newContextRegistrationBuffer())
	for i := 0; i < flagRegistrationThreshold; i++ {
		rt2.NumberFlag("num-"+string(rune('a'+i%26))+string(rune('0'+i/26)), float64(i))
	}
	time.Sleep(50 * time.Millisecond)
	assert.GreaterOrEqual(t, bulkCallCount.Load(), int32(1), "NumberFlag: threshold flush should fire")

	// Test JsonFlag threshold.
	bulkCallCount.Store(0)
	rt3 := newFlagsRuntime(fc, newContextRegistrationBuffer())
	for i := 0; i < flagRegistrationThreshold; i++ {
		rt3.JsonFlag("json-"+string(rune('a'+i%26))+string(rune('0'+i/26)), nil)
	}
	time.Sleep(50 * time.Millisecond)
	assert.GreaterOrEqual(t, bulkCallCount.Load(), int32(1), "JsonFlag: threshold flush should fire")
}

// --- flushFlagBuffer ---

func TestFlushFlagBuffer_EmptyBatch(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	rt := fc.runtime
	// Should return immediately without panicking or making HTTP calls.
	rt.flushFlagBuffer(context.Background())
}

func TestFlushFlagBuffer_Success(t *testing.T) {
	var reqBody genflags.FlagBulkRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/flags/bulk", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":2}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(mux.ServeHTTP))
	fc.service = "my-svc"
	fc.environment = "prod"
	rt := fc.runtime

	rt.flagBuffer.add("flag-a", "BOOLEAN", true, "my-svc", "prod")
	rt.flagBuffer.add("flag-b", "STRING", "val", "", "")

	rt.flushFlagBuffer(context.Background())

	require.Len(t, reqBody.Flags, 2)
	byID := make(map[string]genflags.FlagBulkItem)
	for _, item := range reqBody.Flags {
		byID[item.Id] = item
	}
	assert.Equal(t, genflags.FlagBulkItemType("BOOLEAN"), byID["flag-a"].Type)
	assert.NotNil(t, byID["flag-a"].Service)
	assert.Equal(t, "my-svc", *byID["flag-a"].Service)
	assert.NotNil(t, byID["flag-a"].Environment)
	assert.Equal(t, "prod", *byID["flag-a"].Environment)
	// Empty service/environment should be omitted (nil pointer).
	assert.Nil(t, byID["flag-b"].Service)
	assert.Nil(t, byID["flag-b"].Environment)
}

func TestFlushFlagBuffer_HTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/flags/bulk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"invalid flag"}]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(mux.ServeHTTP))
	rt := fc.runtime

	rt.flagBuffer.add("flag-a", "BOOLEAN", true, "svc", "prod")

	var logBuf strings.Builder
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	rt.flushFlagBuffer(context.Background())

	assert.Contains(t, logBuf.String(), "smplkit: bulk flag registration failed")
	assert.Contains(t, logBuf.String(), "400")
}

func TestFlushFlagBuffer_NetworkError(t *testing.T) {
	// Use a closed server to trigger a network error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	serverURL := server.URL
	server.Close()

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Override the generated client to point to the closed server.
	genFlagsClient, _ := genflags.NewClient(serverURL)
	fc.generated = genFlagsClient
	rt := fc.runtime

	rt.flagBuffer.add("flag-a", "BOOLEAN", true, "svc", "prod")

	var logBuf strings.Builder
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	rt.flushFlagBuffer(context.Background())

	assert.Contains(t, logBuf.String(), "smplkit: bulk flag registration failed")
}

// --- periodicFlagFlush / runPeriodicFlagFlush ---

func TestPeriodicFlagFlush_Stops(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	rt := fc.runtime

	done := make(chan struct{})
	go rt.periodicFlagFlush(done)
	time.Sleep(10 * time.Millisecond)
	close(done)
	// Give goroutine time to exit — no assertion needed; just verifying no deadlock.
	time.Sleep(20 * time.Millisecond)
}

func TestRunPeriodicFlagFlush_TickerFires(t *testing.T) {
	var flushCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/flags/bulk", func(w http.ResponseWriter, r *http.Request) {
		flushCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":1}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(mux.ServeHTTP))
	rt := fc.runtime

	// Add a flag so there's something to flush.
	rt.flagBuffer.add("ticker-flag", "BOOLEAN", true, "svc", "prod")

	done := make(chan struct{})
	// Use a 10ms interval — much shorter than the production 30s.
	go rt.runPeriodicFlagFlush(done, 10*time.Millisecond)

	// Wait for at least one tick.
	time.Sleep(50 * time.Millisecond)
	close(done)
	time.Sleep(10 * time.Millisecond)

	assert.GreaterOrEqual(t, flushCount.Load(), int32(1), "periodic flush should have fired at least once")
}

// --- ensureInit flush-before-fetch ordering ---

func TestEnsureInit_FlushesBeforeFetch(t *testing.T) {
	var bulkCalledBefore, fetchCalled atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/flags/bulk", func(w http.ResponseWriter, r *http.Request) {
		if !fetchCalled.Load() {
			bulkCalledBefore.Store(true)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":1}`))
	})
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		fetchCalled.Store(true)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(mux.ServeHTTP))
	fc.service = "svc"
	fc.environment = "prod"
	rt := fc.runtime

	// Add a flag before init.
	rt.BooleanFlag("pre-init-flag", true)

	err := rt.ensureInit(context.Background())
	require.NoError(t, err)

	assert.True(t, bulkCalledBefore.Load(), "bulk registration should happen before flag fetch")

	rt.disconnect(context.Background())
	fc.client.stopEventStream()
}

// --- WS listener registration after ensureInit ---

func TestFlagsEnsureInit_RegistersWSListeners(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(mux.ServeHTTP))
	rt := fc.runtime

	// Pre-inject a sharedEventStream so ensureEventStream() uses it without starting a goroutine.
	stream := &sharedEventStream{
		listeners:  make(map[string][]eventCallback),
		closeCh:    make(chan struct{}),
		streamDone: make(chan struct{}),
	}
	fc.client.stream = stream

	err := rt.ensureInit(context.Background())
	require.NoError(t, err)

	stream.listenersMu.Lock()
	_, hasChanged := stream.listeners["flag_changed"]
	_, hasDeleted := stream.listeners["flag_deleted"]
	_, hasFlagsChanged := stream.listeners["flags_changed"]
	stream.listenersMu.Unlock()

	assert.True(t, hasChanged, "flag_changed should be registered in the listener map")
	assert.True(t, hasDeleted, "flag_deleted should be registered in the listener map")
	assert.True(t, hasFlagsChanged, "flags_changed should be registered in the listener map")

	rt.disconnect(context.Background())
}

// TestFlagsEnsureInit_ReconnectRefetchReusesBulkRefresh proves ensureInit
// registers a reconnect-refetch callback with the shared stream, and that the
// callback reuses the same bulk-refresh path the flags_changed handler uses
// (a full re-fetch of all flags), and that disconnect unregisters it.
func TestFlagsEnsureInit_ReconnectRefetchReusesBulkRefresh(t *testing.T) {
	var listCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&listCalls, 1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(mux.ServeHTTP))
	rt := fc.runtime
	stream := &sharedEventStream{
		listeners:  make(map[string][]eventCallback),
		closeCh:    make(chan struct{}),
		streamDone: make(chan struct{}),
	}
	fc.client.stream = stream

	require.NoError(t, rt.ensureInit(context.Background()))
	require.Equal(t, int32(1), atomic.LoadInt32(&listCalls), "ensureInit fetches once")

	// Simulate a stream reconnect: the registered callback must run the
	// module's full refetch (a second list call).
	stream.runRefetch()
	assert.Equal(t, int32(2), atomic.LoadInt32(&listCalls),
		"reconnect refetch must re-fetch all flags via the bulk-refresh path")

	// disconnect unregisters the refetch callback: another reconnect must
	// not refetch.
	rt.disconnect(context.Background())
	stream.runRefetch()
	assert.Equal(t, int32(2), atomic.LoadInt32(&listCalls),
		"refetch must be unregistered after disconnect")
}

// --- disconnect closes the flush goroutine ---

func TestDisconnect_StopsFlagFlushGoroutine(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	fc, _ := newTestFlagsClient(t, http.HandlerFunc(mux.ServeHTTP))
	rt := fc.runtime

	err := rt.ensureInit(context.Background())
	require.NoError(t, err)

	assert.NotNil(t, rt.flagFlushDone, "flagFlushDone should be set after init")

	rt.disconnect(context.Background())
	assert.Nil(t, rt.flagFlushDone, "flagFlushDone should be nil after disconnect")

	fc.client.stopEventStream()
}

// ========== pushed-event handler tests ==========

func TestHandleFlagChanged_ScopedFetch_ContentChanged(t *testing.T) {
	var fetchCount int32
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/flags/my-flag") {
			atomic.AddInt32(&fetchCount, 1)
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"my-flag","type":"flag","attributes":{"id":"my-flag","name":"My Flag","type":"BOOLEAN","default":true,"values":[],"environments":{}}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	rt := fc.runtime

	// Pre-populate store with different content so diff fires.
	rt.flagStore["my-flag"] = map[string]interface{}{"id": "my-flag", "default": false}

	var received *FlagChangeEvent
	rt.OnChange(func(evt *FlagChangeEvent) {
		received = evt
	})

	rt.handleFlagChanged(map[string]interface{}{"id": "my-flag"})

	assert.Equal(t, int32(1), atomic.LoadInt32(&fetchCount), "should call GetFlag once")
	require.NotNil(t, received, "listener should fire when content changed")
	assert.Equal(t, "my-flag", received.ID)
	assert.Equal(t, "push", received.Source)
	assert.False(t, received.Deleted)
}

func TestHandleFlagChanged_ScopedFetch_ContentUnchanged(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/flags/my-flag") {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"my-flag","type":"flag","attributes":{"id":"my-flag","name":"My Flag","type":"BOOLEAN","default":false,"environments":{}}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	rt := fc.runtime

	// Pre-warm: fetch once to get the exact map representation.
	preFetched, err := fc.fetchSingleFlag(context.Background(), "my-flag")
	require.NoError(t, err)
	rt.flagStore["my-flag"] = preFetched

	var called bool
	rt.OnChange(func(evt *FlagChangeEvent) {
		called = true
	})

	rt.handleFlagChanged(map[string]interface{}{"id": "my-flag"})

	assert.False(t, called, "listener should NOT fire when content is unchanged")
}

func TestHandleFlagDeleted_StoreRemoval_ListenerFired(t *testing.T) {
	var fetchCount int32
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	rt := fc.runtime

	rt.flagStore["gone-flag"] = map[string]interface{}{"id": "gone-flag", "default": true}

	var evt *FlagChangeEvent
	rt.OnChange(func(e *FlagChangeEvent) {
		evt = e
	})

	rt.handleFlagDeleted(map[string]interface{}{"id": "gone-flag"})

	assert.Equal(t, int32(0), atomic.LoadInt32(&fetchCount), "flag_deleted must NOT make HTTP fetch")
	_, stillInStore := rt.flagStore["gone-flag"]
	assert.False(t, stillInStore, "flag should be removed from store")
	require.NotNil(t, evt)
	assert.True(t, evt.Deleted, "event should have Deleted=true")
	assert.Equal(t, "gone-flag", evt.ID)
}

func TestHandleFlagsChanged_FullFetch_DiffFiring(t *testing.T) {
	var fetchCount int32
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/flags" {
			atomic.AddInt32(&fetchCount, 1)
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[
				{"id":"flag-a","type":"flag","attributes":{"id":"flag-a","name":"Flag A","type":"BOOLEAN","default":true,"environments":{}}},
				{"id":"flag-b","type":"flag","attributes":{"id":"flag-b","name":"Flag B","type":"BOOLEAN","default":false,"environments":{}}}
			]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	rt := fc.runtime

	// Pre-populate with different content for flag-a; flag-b is new.
	rt.flagStore["flag-a"] = map[string]interface{}{"id": "flag-a", "default": false}

	var globalFired int
	var keyAFired, keyBFired bool

	rt.OnChange(func(evt *FlagChangeEvent) {
		globalFired++
	})
	rt.OnChangeKey("flag-a", func(evt *FlagChangeEvent) {
		keyAFired = true
	})
	rt.OnChangeKey("flag-b", func(evt *FlagChangeEvent) {
		keyBFired = true
	})

	rt.handleFlagsChanged(map[string]interface{}{})

	assert.Equal(t, int32(1), atomic.LoadInt32(&fetchCount), "should call list once")
	assert.Equal(t, 1, globalFired, "global listener should fire exactly once")
	assert.True(t, keyAFired, "flag-a key listener should fire (content changed)")
	assert.True(t, keyBFired, "flag-b key listener should fire (new flag)")
}

func TestHandleFlagsChanged_NoChange_NoListeners(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/flags" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"flag-a","type":"flag","attributes":{"id":"flag-a","name":"Flag A","type":"BOOLEAN","default":false,"environments":{}}}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	rt := fc.runtime

	// Pre-warm: fetch once so the store has the exact map representation.
	preFetched, err := fc.fetchAllFlags(context.Background())
	require.NoError(t, err)
	rt.flagStore = preFetched

	var called bool
	rt.OnChange(func(evt *FlagChangeEvent) { called = true })

	rt.handleFlagsChanged(map[string]interface{}{})

	assert.False(t, called, "no listener should fire when content is unchanged")
}

// ========== Coverage gap tests ==========

func TestFetchSingleFlag_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.fetchSingleFlag(context.Background(), "my-flag")
	assert.Error(t, err)
}

func TestFetchSingleFlag_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	_, err := fc.fetchSingleFlag(context.Background(), "my-flag")
	assert.Error(t, err)
}

func TestFetchSingleFlag_HTTPError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))
	_, err := fc.fetchSingleFlag(context.Background(), "missing-flag")
	assert.Error(t, err)
}

func TestFetchSingleFlag_MalformedJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not valid json`))
	}))
	_, err := fc.fetchSingleFlag(context.Background(), "my-flag")
	assert.Error(t, err)
}

func TestFetchSingleFlag_WithValues(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"my-flag","type":"flag","attributes":{"id":"my-flag","name":"My Flag","type":"BOOLEAN","default":true,"values":[{"name":"True","value":true}],"environments":{}}}}`))
	}))
	result, err := fc.fetchSingleFlag(context.Background(), "my-flag")
	require.NoError(t, err)
	assert.NotNil(t, result["values"])
}

func TestHandleFlagsChanged_FetchError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))
	rt := fc.runtime

	var called bool
	rt.OnChange(func(evt *FlagChangeEvent) { called = true })

	rt.handleFlagsChanged(map[string]interface{}{})
	assert.False(t, called)
}

func TestFireGlobalOnce_PanicRecovery(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.OnChange(func(e *FlagChangeEvent) {
		panic("global panic test")
	})
	assert.NotPanics(t, func() {
		rt.fireGlobalOnce("test")
	})
}

func TestFireKeyListenersOnly_EmptyKey(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	var called bool
	rt.OnChangeKey("some-key", func(e *FlagChangeEvent) { called = true })
	rt.fireKeyListenersOnly("", "test")
	assert.False(t, called)
}

func TestFireKeyListenersOnly_KeyListenerPanic(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	rt.OnChangeKey("feature", func(e *FlagChangeEvent) {
		panic("key listener panic")
	})
	assert.NotPanics(t, func() {
		rt.fireKeyListenersOnly("feature", "test")
	})
}

func TestFireKeyListenersOnly_WithHandle(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	handle := rt.BooleanFlag("feature", true)

	var handleFired bool
	handle.OnChange(func(e *FlagChangeEvent) { handleFired = true })

	rt.fireKeyListenersOnly("feature", "push")
	assert.True(t, handleFired)
}

func TestFireKeyListenersOnly_WithHandlePanic(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	handle := rt.BooleanFlag("feature", true)
	handle.OnChange(func(e *FlagChangeEvent) { panic("handle panic") })
	assert.NotPanics(t, func() {
		rt.fireKeyListenersOnly("feature", "test")
	})
}

func TestFireDeletedListener_EmptyKey(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	var called bool
	rt.OnChange(func(e *FlagChangeEvent) { called = true })
	rt.fireDeletedListener("", "test")
	assert.False(t, called)
}

// --- Runtime Register / FlushContexts ---

func TestFlagsRuntime_Register(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.Register(context.Background(), Context{Type: "user", Key: "u1"})
	assert.Equal(t, 1, fc.runtime.contextBuffer.pendingCount())
}

func TestFlagsRuntime_FlushContexts_Empty(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	// Should not panic with no pending contexts.
	fc.runtime.FlushContexts(context.Background())
}

func TestFlagsRuntime_FlushContexts_SendsBatch(t *testing.T) {
	var received bool
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/contexts/bulk" {
			received = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	fc.runtime.Register(context.Background(), Context{Type: "user", Key: "u1"})
	fc.runtime.FlushContexts(context.Background())
	assert.True(t, received)
}

// --- flagRegistrationBuffer peek/commit tests ---

func TestFlagRegistrationBuffer_CommitEmpty(t *testing.T) {
	buf := newFlagRegistrationBuffer()
	buf.add("flag1", "BOOLEAN", false, "svc", "production")
	// commit with nil/empty slice is a no-op
	buf.commit(nil)
	buf.commit([]string{})
	assert.Equal(t, 1, buf.pendingCount(), "commit with empty ids must be a no-op")
}

func TestFlagRegistrationBuffer_PeekDoesNotDrain(t *testing.T) {
	buf := newFlagRegistrationBuffer()
	buf.add("flag1", "BOOLEAN", false, "svc", "prod")
	buf.add("flag2", "STRING", "x", "svc", "prod")

	first := buf.peek()
	assert.Len(t, first, 2)
	assert.Equal(t, 2, buf.pendingCount(), "peek must not remove items")

	second := buf.peek()
	assert.Len(t, second, 2, "second peek must still see all items")
}

func TestFlagRegistrationBuffer_PeekEmpty(t *testing.T) {
	buf := newFlagRegistrationBuffer()
	assert.Nil(t, buf.peek())
}

func TestFlagRegistrationBuffer_CommitRemovesTargets(t *testing.T) {
	buf := newFlagRegistrationBuffer()
	buf.add("flag1", "BOOLEAN", false, "svc", "prod")
	buf.add("flag2", "STRING", "x", "svc", "prod")
	buf.add("flag3", "NUMERIC", 0.0, "svc", "prod")

	buf.commit([]string{"flag1", "flag3"})
	assert.Equal(t, 1, buf.pendingCount())
	remaining := buf.peek()
	require.Len(t, remaining, 1)
	assert.Equal(t, "flag2", remaining[0].id)
}

// --- flushFlagBuffer non-destructive behavior ---

func TestFlagsRuntime_FlushFlagBuffer_500RetainsBuffer(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/flags/bulk") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	fc.runtime.flagBuffer.add("my_flag", "BOOLEAN", false, "svc", "production")

	fc.runtime.flushFlagBuffer(context.Background())

	assert.Equal(t, 1, fc.runtime.flagBuffer.pendingCount(),
		"buffer must not be drained when bulk-register returns 500")
}

func TestFlagsRuntime_FlushFlagBuffer_200CommitsBuffer(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	fc.runtime.flagBuffer.add("my_flag", "BOOLEAN", false, "svc", "production")

	fc.runtime.flushFlagBuffer(context.Background())

	assert.Equal(t, 0, fc.runtime.flagBuffer.pendingCount(),
		"buffer must be committed after a successful bulk-register")
}

func TestFlagsRuntime_FlushFlagBuffer_500Then200(t *testing.T) {
	callCount := 0
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/flags/bulk") {
			callCount++
			if callCount == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	fc.runtime.flagBuffer.add("my_flag", "BOOLEAN", false, "svc", "production")

	// First flush — 500: items stay.
	fc.runtime.flushFlagBuffer(context.Background())
	assert.Equal(t, 1, fc.runtime.flagBuffer.pendingCount(), "buffer not drained on 500")

	// Second flush — 200: items committed.
	fc.runtime.flushFlagBuffer(context.Background())
	assert.Equal(t, 0, fc.runtime.flagBuffer.pendingCount(), "buffer committed on 200")
}

// --- ensureInit retry / backoff ---

func TestFlagsRuntime_EnsureInit_ConnectedFalseAfterFailure(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	fc.client.environment = "production"
	fc.environment = "production"

	err := fc.runtime.ensureInit(context.Background())
	assert.Error(t, err)

	fc.runtime.connectMu.Lock()
	connected := fc.runtime.connected
	delay := fc.runtime.retryDelay
	fc.runtime.connectMu.Unlock()

	assert.False(t, connected, "connected must be false after failed init")
	assert.Equal(t, time.Second, delay, "retry delay must be set to 1s after first failure")
}

func TestFlagsRuntime_EnsureInit_BackoffWindow(t *testing.T) {
	// Count only flag-list requests; the async service-context POST hits a
	// different path and must not perturb the backoff-window assertion.
	var listCount atomic.Int32
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/flags" {
			listCount.Add(1)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	fc.client.environment = "production"
	fc.environment = "production"

	// First attempt fails and sets a backoff window.
	_ = fc.runtime.ensureInit(context.Background())
	countAfterFirst := listCount.Load()

	// Extend the backoff window to well into the future.
	fc.runtime.connectMu.Lock()
	fc.runtime.nextRetryAt = time.Now().Add(time.Hour)
	fc.runtime.connectMu.Unlock()

	// Second attempt must return an error without hitting the network.
	err := fc.runtime.ensureInit(context.Background())
	assert.Error(t, err)
	assert.Equal(t, countAfterFirst, listCount.Load(), "no network call during backoff window")
	assert.False(t, fc.runtime.connected)
}

func TestFlagsRuntime_EnsureInit_SuccessAfterBackoff(t *testing.T) {
	var flagsBulkCalls, flagsListCalls atomic.Int32
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/contexts/bulk"):
			w.WriteHeader(http.StatusOK)
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/flags/bulk"):
			if flagsBulkCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			if flagsListCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	fc.client.environment = "production"
	fc.environment = "production"
	fc.runtime.flagBuffer.add("feature_flag", "BOOLEAN", false, "svc", "production")

	// First attempt: flags-bulk 500 → buffer retained; list-flags 500 → error returned.
	err := fc.runtime.ensureInit(context.Background())
	assert.Error(t, err)
	assert.False(t, fc.runtime.connected)
	assert.Equal(t, 1, fc.runtime.flagBuffer.pendingCount(), "buffer must be retained after failed init")

	// Expire the backoff window.
	fc.runtime.connectMu.Lock()
	fc.runtime.nextRetryAt = time.Now().Add(-time.Millisecond)
	fc.runtime.connectMu.Unlock()

	// Second attempt: flags-bulk 200 → buffer committed; list-flags 200 → connected=true.
	err = fc.runtime.ensureInit(context.Background())
	require.NoError(t, err)
	assert.True(t, fc.runtime.connected, "connected must be true after successful retry")
	assert.Equal(t, 0, fc.runtime.flagBuffer.pendingCount(), "buffer must be committed after successful init")

	fc.Disconnect(context.Background())
	fc.client.stopEventStream()
}

func TestFlagsRuntime_AdvanceBackoff_Doubling(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	// First call: 0 → 1s
	rt.advanceBackoff()
	assert.Equal(t, time.Second, rt.retryDelay)
	// Second call: 1s → 2s
	rt.advanceBackoff()
	assert.Equal(t, 2*time.Second, rt.retryDelay)
	// Third call: 2s → 4s
	rt.advanceBackoff()
	assert.Equal(t, 4*time.Second, rt.retryDelay)
}

func TestFlagsRuntime_AdvanceBackoff_Cap(t *testing.T) {
	rt := newFlagsRuntime(nil, newContextRegistrationBuffer())
	// Seed a delay just below the cap.
	rt.retryDelay = 40 * time.Second
	rt.advanceBackoff() // would be 80s, capped to 60s
	assert.Equal(t, 60*time.Second, rt.retryDelay)
	// Another call must stay at the cap.
	rt.advanceBackoff()
	assert.Equal(t, 60*time.Second, rt.retryDelay)
}

func TestFlagsRuntime_WSSubscribedOnce(t *testing.T) {
	callCount := atomic.Int32{}
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	fc.client.environment = "production"
	fc.environment = "production"

	// First attempt fails.
	_ = fc.runtime.ensureInit(context.Background())

	// Expire the backoff window.
	fc.runtime.connectMu.Lock()
	fc.runtime.nextRetryAt = time.Now().Add(-time.Millisecond)
	fc.runtime.connectMu.Unlock()

	// Second attempt succeeds.
	err := fc.runtime.ensureInit(context.Background())
	require.NoError(t, err)
	assert.True(t, fc.runtime.connected)

	// Verify event handlers registered exactly once.
	stream := fc.runtime.streamManager
	require.NotNil(t, stream)
	stream.listenersMu.Lock()
	count := len(stream.listeners["flag_changed"])
	stream.listenersMu.Unlock()
	assert.Equal(t, 1, count, "flag_changed handler must be registered exactly once")

	fc.Disconnect(context.Background())
	fc.client.stopEventStream()
}

// ========== Standalone NewFlagsClient ==========

func TestNewFlagsClient_Standalone_CRUD(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/flags/feature-x" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleFlagResponseJSON("feature-x", "Feature X", "BOOLEAN")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	fc, err := NewFlagsClient(
		Config{APIKey: "sk_test", Environment: "test", Service: "svc", DisableTelemetry: true},
		withBaseURLOverride(server.URL),
	)
	require.NoError(t, err)
	require.Nil(t, fc.client, "standalone client has no parent")
	assert.Equal(t, "test", fc.environment)
	assert.Equal(t, "svc", fc.service)

	flag, err := fc.Get(context.Background(), "feature-x")
	require.NoError(t, err)
	assert.Equal(t, "feature-x", flag.ID)
}

func TestNewFlagsClient_Standalone_ConfigError(t *testing.T) {
	// Missing API key should error in resolveConfig (environment and service
	// are optional, so clear every source of an api key).
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("SMPLKIT_PROFILE", "")
	t.Setenv("HOME", t.TempDir())
	_, err := NewFlagsClient(Config{Environment: "test"})
	assert.Error(t, err)
}

func TestNewFlagsClient_Standalone_EnsureEventStream_Own(t *testing.T) {
	fc, err := NewFlagsClient(
		Config{APIKey: "sk_test", Environment: "test", Service: "svc", DisableTelemetry: true},
		withBaseURLOverride("https://app.smplkit.com"),
	)
	require.NoError(t, err)

	// Standalone ensureEventStream builds and owns its own stream.
	s1 := fc.ensureEventStream()
	s2 := fc.ensureEventStream()
	assert.Same(t, s1, s2)
	require.NotNil(t, fc.ownStream)

	s1.stop()
}

// newFlagsClient must build its own context buffer when the contexts seam is nil.
func TestNewFlagsClient_WiredNilContexts(t *testing.T) {
	c := newTestParentClient("https://app.smplkit.com", &http.Client{}, nil)
	fc := newFlagsClient(c, nil, nil, nil, nil)
	require.NotNil(t, fc.runtime)
	require.NotNil(t, fc.runtime.contextBuffer, "runtime must have a fresh context buffer when contexts seam is nil")
}

// evaluateHandle records metrics on cache miss, cache hit, and not-found paths
// when a metrics reporter is wired.
func TestFlagsRuntime_EvaluateHandle_Metrics(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	metrics := newMetricsReporter(http.DefaultClient, "https://app.smplkit.com", "test", "svc", time.Hour)
	t.Cleanup(metrics.Close)
	fc.metrics = metrics

	fc.runtime.connected = true
	fc.runtime.mu.Lock()
	fc.runtime.environment = "production"
	fc.runtime.flagStore = map[string]map[string]interface{}{
		"feature": {"default": "val", "environments": map[string]interface{}{}},
	}
	fc.runtime.mu.Unlock()

	// Cache miss → compute → record.
	v1 := fc.runtime.evaluateHandle(context.Background(), "feature", "dflt", nil)
	assert.Equal(t, "val", v1)
	// Cache hit → record hit + evaluation.
	v2 := fc.runtime.evaluateHandle(context.Background(), "feature", "dflt", nil)
	assert.Equal(t, "val", v2)
	// Not found → cache default + record evaluation.
	v3 := fc.runtime.evaluateHandle(context.Background(), "missing", "fallback", nil)
	assert.Equal(t, "fallback", v3)
}

func TestNewFlagsClient_Standalone_LiveEvaluate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/contexts/bulk"):
			w.WriteHeader(http.StatusOK)
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/flags/bulk"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"feature","type":"flag","attributes":{"id":"feature","name":"Feature","type":"BOOLEAN","default":true,"environments":{}}}]}`))
		}
	}))
	defer server.Close()

	fc, err := NewFlagsClient(
		Config{APIKey: "sk_test", Environment: "test", Service: "svc", DisableTelemetry: true},
		withBaseURLOverride(server.URL),
	)
	require.NoError(t, err)

	// Pre-inject a sharedEventStream so the first live use does not open a real stream.
	fc.ownStream = &sharedEventStream{
		listeners:  make(map[string][]eventCallback),
		closeCh:    make(chan struct{}),
		streamDone: make(chan struct{}),
	}

	handle := fc.BooleanFlag("feature", false)
	result := handle.Get(context.Background())
	assert.Equal(t, true, result)

	fc.Disconnect(context.Background())
}
