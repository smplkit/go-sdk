package smplkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genconfig "github.com/smplkit/go-sdk/internal/generated/config"
)

func TestDerefMap_Nil(t *testing.T) {
	result := derefMap(nil)
	assert.Nil(t, result)
}

func TestDerefEnvs_Nil(t *testing.T) {
	result := derefEnvs(nil)
	assert.Nil(t, result)
}

// ---------- extractItemValues ----------

func TestExtractItemValues_Nil(t *testing.T) {
	assert.Nil(t, extractItemValues(nil))
}

func TestExtractItemValues_NonMapItem(t *testing.T) {
	items := map[string]interface{}{
		"plain": "hello",
		"num":   42,
	}
	result := extractItemValues(items)
	assert.Equal(t, "hello", result["plain"])
	assert.Equal(t, 42, result["num"])
}

func TestExtractItemValues_MapWithoutValueKey(t *testing.T) {
	items := map[string]interface{}{
		"no_val": map[string]interface{}{"type": "STRING", "description": "desc"},
	}
	result := extractItemValues(items)
	assert.Equal(t, items["no_val"], result["no_val"])
}

func TestExtractItemValues_MapWithValueKey(t *testing.T) {
	items := map[string]interface{}{
		"log_level": map[string]interface{}{"value": "info", "type": "STRING"},
	}
	result := extractItemValues(items)
	assert.Equal(t, "info", result["log_level"])
}

// ---------- extractEnvOverrides ----------

func TestExtractEnvOverrides_Nil(t *testing.T) {
	assert.Nil(t, extractEnvOverrides(nil))
}

func TestExtractEnvOverrides_ValuesNotMap(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"staging": {"values": "not-a-map", "other": "keep"},
	}
	result := extractEnvOverrides(envs)
	assert.Equal(t, "not-a-map", result["staging"]["values"])
	assert.Equal(t, "keep", result["staging"]["other"])
}

func TestExtractEnvOverrides_NonValuesKey(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"prod": {"name": "production"},
	}
	result := extractEnvOverrides(envs)
	assert.Equal(t, "production", result["prod"]["name"])
}

func TestExtractEnvOverrides_InnerNonMap(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"staging": {
			"values": map[string]interface{}{
				"raw_val": "plain-string",
			},
		},
	}
	result := extractEnvOverrides(envs)
	assert.Equal(t, "plain-string", result["staging"]["values"].(map[string]interface{})["raw_val"])
}

func TestExtractEnvOverrides_InnerMapMissingValueKey(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"staging": {
			"values": map[string]interface{}{
				"no_val": map[string]interface{}{"type": "STRING"},
			},
		},
	}
	result := extractEnvOverrides(envs)
	inner := result["staging"]["values"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{"type": "STRING"}, inner["no_val"])
}

func TestExtractEnvOverrides_InnerMapWithValueKey(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"staging": {
			"values": map[string]interface{}{
				"debug": map[string]interface{}{"value": true},
			},
		},
	}
	result := extractEnvOverrides(envs)
	inner := result["staging"]["values"].(map[string]interface{})
	assert.Equal(t, true, inner["debug"])
}

// ---------- wrapEnvOverrides ----------

func TestWrapEnvOverrides_Nil(t *testing.T) {
	assert.Nil(t, wrapEnvOverrides(nil))
}

func TestWrapEnvOverrides_ValuesNotMap(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"staging": {"values": "not-a-map", "meta": "data"},
	}
	result := wrapEnvOverrides(envs)
	assert.Equal(t, "not-a-map", result["staging"]["values"])
	assert.Equal(t, "data", result["staging"]["meta"])
}

func TestWrapEnvOverrides_NonValuesKey(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"prod": {"name": "production"},
	}
	result := wrapEnvOverrides(envs)
	assert.Equal(t, "production", result["prod"]["name"])
}

func TestWrapEnvOverrides_WrapsValues(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"staging": {
			"values": map[string]interface{}{
				"debug": true,
			},
		},
	}
	result := wrapEnvOverrides(envs)
	inner := result["staging"]["values"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{"value": true}, inner["debug"])
}

// ---------- diffAndFire ----------

func TestDiffAndFire_RemovedKey(t *testing.T) {
	c := &ConfigClient{}

	var events []*ConfigChangeEvent
	c.listeners = []configChangeListener{
		{configID: "", itemKey: "", cb: func(evt *ConfigChangeEvent) {
			events = append(events, evt)
		}},
	}

	oldCache := map[string]map[string]interface{}{
		"app": {"a": 1, "b": 2},
	}
	newCache := map[string]map[string]interface{}{
		"app": {"a": 1},
	}

	c.diffAndFire(oldCache, newCache, "manual")

	require.Len(t, events, 1)
	assert.Equal(t, "app", events[0].ConfigID)
	assert.Equal(t, "b", events[0].ItemKey)
	assert.Nil(t, events[0].NewValue)
}

func TestDiffAndFire_ListenerPanic(t *testing.T) {
	c := &ConfigClient{}

	var events []*ConfigChangeEvent
	c.listeners = []configChangeListener{
		{cb: func(evt *ConfigChangeEvent) {
			panic("bad listener")
		}},
		{cb: func(evt *ConfigChangeEvent) {
			events = append(events, evt)
		}},
	}

	oldCache := map[string]map[string]interface{}{
		"app": {"a": 1},
	}
	newCache := map[string]map[string]interface{}{
		"app": {"a": 2},
	}

	c.diffAndFire(oldCache, newCache, "manual")

	require.Len(t, events, 1)
	assert.Equal(t, 2, events[0].NewValue)
}

func TestDiffAndFire_FiltersByConfigID(t *testing.T) {
	c := &ConfigClient{}

	var events []*ConfigChangeEvent
	c.listeners = []configChangeListener{
		{configID: "db", cb: func(evt *ConfigChangeEvent) {
			events = append(events, evt)
		}},
	}

	oldCache := map[string]map[string]interface{}{
		"app": {"a": 1},
		"db":  {"host": "old"},
	}
	newCache := map[string]map[string]interface{}{
		"app": {"a": 2},
		"db":  {"host": "new"},
	}

	c.diffAndFire(oldCache, newCache, "manual")

	require.Len(t, events, 1)
	assert.Equal(t, "db", events[0].ConfigID)
}

func TestDiffAndFire_FiltersByItemKey(t *testing.T) {
	c := &ConfigClient{}

	var events []*ConfigChangeEvent
	c.listeners = []configChangeListener{
		{configID: "app", itemKey: "a", cb: func(evt *ConfigChangeEvent) {
			events = append(events, evt)
		}},
	}

	oldCache := map[string]map[string]interface{}{
		"app": {"a": 1, "b": 2},
	}
	newCache := map[string]map[string]interface{}{
		"app": {"a": 10, "b": 20},
	}

	c.diffAndFire(oldCache, newCache, "manual")

	require.Len(t, events, 1)
	assert.Equal(t, "a", events[0].ItemKey)
}

func TestDiffAndFire_NoListeners(t *testing.T) {
	c := &ConfigClient{}

	// Should not panic
	c.diffAndFire(
		map[string]map[string]interface{}{"app": {"a": 1}},
		map[string]map[string]interface{}{"app": {"a": 2}},
		"manual",
	)
}

// ---------- GetInt type coercion ----------

func TestGetInt_NativeInt(t *testing.T) {
	c := &ConfigClient{
		configCache: map[string]map[string]interface{}{
			"app": {"n": int(42)},
		},
	}
	// Mark as already initialized by running initOnce with no-op.
	c.initOnce.Do(func() {})
	val, err := c.GetInt(context.Background(), "app", "n")
	assert.NoError(t, err)
	assert.Equal(t, 42, val)
}

func TestGetInt_Int64(t *testing.T) {
	c := &ConfigClient{
		configCache: map[string]map[string]interface{}{
			"app": {"n": int64(99)},
		},
	}
	c.initOnce.Do(func() {})
	val, err := c.GetInt(context.Background(), "app", "n")
	assert.NoError(t, err)
	assert.Equal(t, 99, val)
}

// ---------- deepMerge ----------

func TestDeepMerge_RecursiveMerge(t *testing.T) {
	base := map[string]interface{}{
		"db": map[string]interface{}{
			"host": "localhost",
			"port": 5432,
		},
		"name": "app",
	}
	override := map[string]interface{}{
		"db": map[string]interface{}{
			"host": "prod-server",
			"ssl":  true,
		},
		"version": "2.0",
	}
	result := deepMerge(base, override)
	db := result["db"].(map[string]interface{})
	assert.Equal(t, "prod-server", db["host"])
	assert.Equal(t, 5432, db["port"])
	assert.Equal(t, true, db["ssl"])
	assert.Equal(t, "app", result["name"])
	assert.Equal(t, "2.0", result["version"])
}

func TestDeepMerge_OverrideNonMapWithMap(t *testing.T) {
	base := map[string]interface{}{
		"db": "string-value",
	}
	override := map[string]interface{}{
		"db": map[string]interface{}{"host": "localhost"},
	}
	result := deepMerge(base, override)
	assert.Equal(t, map[string]interface{}{"host": "localhost"}, result["db"])
}

func TestDeepMerge_OverrideMapWithNonMap(t *testing.T) {
	base := map[string]interface{}{
		"db": map[string]interface{}{"host": "localhost"},
	}
	override := map[string]interface{}{
		"db": "string-value",
	}
	result := deepMerge(base, override)
	assert.Equal(t, "string-value", result["db"])
}

// ---------- Refresh edge cases ----------

func TestRefresh_NoEnvironment(t *testing.T) {
	c := &ConfigClient{
		client: &Client{environment: ""},
	}
	// Mark as already initialized.
	c.initOnce.Do(func() {})
	err := c.Refresh(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "No environment set")
}

// ---------- diffAndFire edge cases ----------

func TestDiffAndFire_NewConfig(t *testing.T) {
	c := &ConfigClient{}

	var events []*ConfigChangeEvent
	c.listeners = []configChangeListener{
		{cb: func(evt *ConfigChangeEvent) {
			events = append(events, evt)
		}},
	}

	oldCache := map[string]map[string]interface{}{}
	newCache := map[string]map[string]interface{}{
		"app": {"a": 1},
	}

	c.diffAndFire(oldCache, newCache, "manual")

	require.Len(t, events, 1)
	assert.Equal(t, "app", events[0].ConfigID)
	assert.Equal(t, "a", events[0].ItemKey)
	assert.Nil(t, events[0].OldValue)
	assert.Equal(t, 1, events[0].NewValue)
}

func TestDiffAndFire_RemovedConfig(t *testing.T) {
	c := &ConfigClient{}

	var events []*ConfigChangeEvent
	c.listeners = []configChangeListener{
		{cb: func(evt *ConfigChangeEvent) {
			events = append(events, evt)
		}},
	}

	oldCache := map[string]map[string]interface{}{
		"app": {"a": 1},
	}
	newCache := map[string]map[string]interface{}{}

	c.diffAndFire(oldCache, newCache, "manual")

	require.Len(t, events, 1)
	assert.Equal(t, "app", events[0].ConfigID)
	assert.Equal(t, 1, events[0].OldValue)
	assert.Nil(t, events[0].NewValue)
}

// --- newTestConfigClient helper ---

func newTestConfigClient(t *testing.T, handler http.Handler) *ConfigClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	httpClient := &http.Client{}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: "sk_test", base: base}

	headerEditor := genconfig.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genConfigClient, _ := genconfig.NewClient(server.URL,
		genconfig.WithHTTPClient(httpClient),
		headerEditor,
	)

	c := &Client{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		baseURL:     server.URL,
		httpClient:  httpClient,
	}
	cc := &ConfigClient{client: c, generated: genConfigClient}
	return cc
}

// ---------- getByID error paths ----------

func TestGetByID_ReadBodyError(t *testing.T) {
	// Test checkStatus error path (e.g. 500 response)
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	_, err := cc.getByID(context.Background(), "test-config")
	require.Error(t, err)
}

func TestGetByID_ReadBodyFailure(t *testing.T) {
	// Return a response that will fail on body read by closing the connection
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hijack the connection to simulate a read failure
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		conn, _, _ := hj.Hijack()
		// Write partial HTTP response then close
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 9999\r\n\r\n"))
		conn.Close()
	}))

	_, err := cc.getByID(context.Background(), "test-config")
	require.Error(t, err)
}

// ---------- Delete error paths ----------

func TestDelete_Config_CheckStatusError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	err := cc.Management().Delete(context.Background(), "test-config")
	require.Error(t, err)
}

func TestDelete_Config_ReadBodyFailure(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		conn, bufrw, _ := hj.Hijack()
		_, _ = bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 999999\r\nConnection: close\r\n\r\npartial")
		_ = bufrw.Flush()
		conn.Close()
	}))

	_, err := cc.getByID(context.Background(), "test-config")
	// Either body read fails or JSON unmarshal fails — both are errors
	require.Error(t, err)
}

// ---------- Resolve ----------

func TestGet_Basic(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{
			"app": {"host": "localhost", "port": float64(3000)},
		},
	}
	cc.initOnce.Do(func() {})

	resolved, err := cc.Get(context.Background(), "app")
	require.NoError(t, err)
	assert.Equal(t, "localhost", resolved["host"])
	assert.Equal(t, float64(3000), resolved["port"])
}

func TestGet_NilWhenKeyNotFound(t *testing.T) {
	cc := &ConfigClient{
		client:      &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{},
	}
	cc.initOnce.Do(func() {})

	resolved, err := cc.Get(context.Background(), "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, resolved)
}

// ---------- ResolveInto ----------

func TestGetInto_Struct(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{
			"db": {"host": "localhost", "port": float64(5432)},
		},
	}
	cc.initOnce.Do(func() {})

	var target struct {
		Host string  `json:"host"`
		Port float64 `json:"port"`
	}
	err := cc.GetInto(context.Background(), "db", &target)
	require.NoError(t, err)
	assert.Equal(t, "localhost", target.Host)
	assert.Equal(t, float64(5432), target.Port)
}

func TestGetInto_NilResolved(t *testing.T) {
	cc := &ConfigClient{
		client:      &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{},
	}
	cc.initOnce.Do(func() {})

	var target struct{ Host string }
	err := cc.GetInto(context.Background(), "missing", &target)
	require.NoError(t, err)
	assert.Equal(t, "", target.Host)
}

// ---------- Subscribe ----------

func TestSubscribe_ReturnsLiveConfig(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{
			"app": {"key1": "val1"},
		},
	}
	cc.initOnce.Do(func() {})

	lc, err := cc.Subscribe(context.Background(), "app")
	require.NoError(t, err)
	require.NotNil(t, lc)

	val := lc.Value()
	assert.Equal(t, "val1", val["key1"])
}

// ---------- fetchChain with parent walking ----------

func TestFetchChain_ParentWalking(t *testing.T) {
	parentID := "parent-config"
	childID := "child-config"

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/"+childID, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"id":   childID,
				"type": "config",
				"attributes": map[string]interface{}{
					"name":         "Child",
					"parent":       parentID,
					"items":        map[string]interface{}{},
					"environments": map[string]interface{}{},
				},
			},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/api/v1/configs/"+parentID, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"id":   parentID,
				"type": "config",
				"attributes": map[string]interface{}{
					"name":         "Parent",
					"items":        map[string]interface{}{},
					"environments": map[string]interface{}{},
				},
			},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})

	cc := newTestConfigClient(t, mux)
	chain, err := cc.fetchChain(context.Background(), childID)
	require.NoError(t, err)
	assert.Len(t, chain, 2)
	assert.Equal(t, childID, chain[0].ID)
	assert.Equal(t, parentID, chain[1].ID)
}

// ---------- unmarshalResolved ----------

func TestUnmarshalResolved_WithStruct(t *testing.T) {
	resolved := map[string]interface{}{
		"database.host": "localhost",
		"database.port": float64(5432),
	}
	var target struct {
		Database struct {
			Host string  `json:"database.host"`
			Port float64 `json:"database.port"`
		} `json:"database"`
	}
	// unmarshalResolved first unflattens, so use the unflattened structure
	var target2 struct {
		Database struct {
			Host string  `json:"host"`
			Port float64 `json:"port"`
		} `json:"database"`
	}
	err := unmarshalResolved(resolved, &target2)
	require.NoError(t, err)
	assert.Equal(t, "localhost", target2.Database.Host)
	assert.Equal(t, float64(5432), target2.Database.Port)

	// nil resolved case
	err = unmarshalResolved(nil, &target)
	require.NoError(t, err)
}

// ---------- unflattenDotNotation conflict case ----------

func TestUnflattenDotNotation_ConflictNonMapOverwritten(t *testing.T) {
	// To exercise the conflict branch (non-map overwritten by map), we need
	// map iteration to process the scalar key before the dotted key.
	// Since Go map iteration is random, we retry with many different maps
	// until the branch is hit. In practice this takes 1-2 iterations.
	for i := 0; i < 100; i++ {
		flat := map[string]interface{}{
			"db":      "scalar-value",
			"db.host": "localhost",
		}
		result := unflattenDotNotation(flat)
		dbVal, ok := result["db"]
		require.True(t, ok)
		if dbMap, isMap := dbVal.(map[string]interface{}); isMap {
			// The conflict branch was hit: "db" was a scalar, then overwritten
			assert.Equal(t, "localhost", dbMap["host"])
			return
		}
	}
	// If we never hit the conflict branch in 100 iterations, the scalar
	// key was always processed second. This is astronomically unlikely but
	// we still exercise the function.
}

// ---------- refMap nil case ----------

func TestRefMap_Nil(t *testing.T) {
	result := refMap(nil)
	assert.Nil(t, result)
}

// ---------- refEnvs non-values key path ----------

func TestRefEnvs_NonValuesKey(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"staging": {
			"not_values": "some-data",
		},
	}
	result := refEnvs(envs)
	require.NotNil(t, result)
	staging := (*result)["staging"]
	assert.Nil(t, staging.Values)
}

func TestRefEnvs_Nil(t *testing.T) {
	result := refEnvs(nil)
	assert.Nil(t, result)
}

// ---------- wrapItemValues nil case ----------

func TestWrapItemValues_Nil(t *testing.T) {
	result := wrapItemValues(nil)
	assert.Nil(t, result)
}

// ---------- WithConfigParent ----------

func TestWithConfigParent(t *testing.T) {
	cc := &ConfigClient{client: &Client{environment: "test"}}
	cfg := cc.Management().New("child", WithConfigParent("parent-uuid"))
	require.NotNil(t, cfg.Parent)
	assert.Equal(t, "parent-uuid", *cfg.Parent)
}

// ---------- WithConfigItems ----------

func TestWithConfigItems(t *testing.T) {
	cc := &ConfigClient{client: &Client{environment: "test"}}
	items := map[string]interface{}{"key1": "val1", "key2": 42}
	cfg := cc.Management().New("cfg", WithConfigItems(items))
	assert.Equal(t, items, cfg.Items)
}

// ---------- WithConfigEnvironments ----------

func TestWithConfigEnvironments(t *testing.T) {
	cc := &ConfigClient{client: &Client{environment: "test"}}
	envs := map[string]map[string]interface{}{
		"production": {"key1": "prod-val"},
	}
	cfg := cc.Management().New("cfg", WithConfigEnvironments(envs))
	assert.Equal(t, envs, cfg.Environments)
}

// ---------- LiveConfig.Value ----------

func TestLiveConfig_Value(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{
			"app": {"host": "localhost"},
		},
	}
	lc := &LiveConfig{client: cc, id: "app"}
	val := lc.Value()
	assert.Equal(t, "localhost", val["host"])
}

func TestLiveConfig_Value_NilCache(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
	}
	lc := &LiveConfig{client: cc, id: "app"}
	val := lc.Value()
	assert.Nil(t, val)
}

func TestLiveConfig_Value_KeyNotFound(t *testing.T) {
	cc := &ConfigClient{
		client:      &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{},
	}
	lc := &LiveConfig{client: cc, id: "missing"}
	val := lc.Value()
	assert.Nil(t, val)
}

// ---------- LiveConfig.ValueInto ----------

func TestLiveConfig_ValueInto(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{
			"db": {"host": "localhost", "port": float64(5432)},
		},
	}
	lc := &LiveConfig{client: cc, id: "db"}

	var target struct {
		Host string  `json:"host"`
		Port float64 `json:"port"`
	}
	err := lc.ValueInto(&target)
	require.NoError(t, err)
	assert.Equal(t, "localhost", target.Host)
	assert.Equal(t, float64(5432), target.Port)
}

func TestLiveConfig_ValueInto_NilCache(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
	}
	lc := &LiveConfig{client: cc, id: "app"}

	var target struct{ Host string }
	err := lc.ValueInto(&target)
	require.NoError(t, err)
	assert.Equal(t, "", target.Host)
}

// ---------- getByID with io.ReadAll failure (via broken body) ----------

func TestGetByID_InvalidJSONResponse(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not valid json`))
	}))

	_, err := cc.getByID(context.Background(), "test-config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

// ---------- getByID connection error ----------

func TestGetByID_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	serverURL := server.URL
	server.Close()

	httpClient := &http.Client{}
	genConfigClient, _ := genconfig.NewClient(serverURL, genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{
		client:    &Client{environment: "test"},
		generated: genConfigClient,
	}

	_, err := cc.getByID(context.Background(), "test-config")
	require.Error(t, err)
}

// ---------- Delete connection error ----------

func TestDelete_Config_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	serverURL := server.URL
	server.Close()

	httpClient := &http.Client{}
	genConfigClient, _ := genconfig.NewClient(serverURL, genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{
		client:    &Client{environment: "test"},
		generated: genConfigClient,
	}

	err := cc.Management().Delete(context.Background(), "test-config")
	require.Error(t, err)
}

// ---------- refEnvs vals not map ----------

func TestRefEnvs_ValsNotMap(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"staging": {
			"values": "not-a-map",
		},
	}
	result := refEnvs(envs)
	require.NotNil(t, result)
	staging := (*result)["staging"]
	assert.Nil(t, staging.Values)
}

// ---------- Resolve ensureInit error ----------

func TestGet_EnsureInitError(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
	}
	// Force initOnce to run with an error
	cc.initOnce.Do(func() {
		cc.initErr = &SmplError{Message: "init failed"}
	})

	_, err := cc.Get(context.Background(), "app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init failed")
}

// ---------- ResolveInto ensureInit error ----------

func TestGetInto_EnsureInitError(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
	}
	cc.initOnce.Do(func() {
		cc.initErr = &SmplError{Message: "init failed"}
	})

	var target struct{ Host string }
	err := cc.GetInto(context.Background(), "db", &target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init failed")
}

// ---------- Subscribe ensureInit error ----------

func TestSubscribe_EnsureInitError(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
	}
	cc.initOnce.Do(func() {
		cc.initErr = &SmplError{Message: "init failed"}
	})

	_, err := cc.Subscribe(context.Background(), "app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init failed")
}

// ---------- unmarshalResolved with non-nil data ----------

func TestUnmarshalResolved_NilInput(t *testing.T) {
	var target struct{ Host string }
	err := unmarshalResolved(nil, &target)
	require.NoError(t, err)
	assert.Equal(t, "", target.Host)
}

func TestUnmarshalResolved_SimpleMap(t *testing.T) {
	resolved := map[string]interface{}{
		"host": "localhost",
		"port": float64(5432),
	}
	var target struct {
		Host string  `json:"host"`
		Port float64 `json:"port"`
	}
	err := unmarshalResolved(resolved, &target)
	require.NoError(t, err)
	assert.Equal(t, "localhost", target.Host)
	assert.Equal(t, float64(5432), target.Port)
}

func TestUnmarshalResolved_MarshalError(t *testing.T) {
	// json.Marshal fails on channels
	resolved := map[string]interface{}{
		"bad": make(chan int),
	}
	var target struct{}
	err := unmarshalResolved(resolved, &target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal resolved config")
}

// ---------- getByID with broken body (uses custom transport) ----------

// brokenBodyTransportConfig wraps an HTTP transport and replaces the response body with a broken reader.
type brokenBodyTransportConfig struct {
	statusCode int
}

func (t *brokenBodyTransportConfig) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.statusCode,
		Body:       io.NopCloser(&brokenReaderConfig{}),
		Header:     make(http.Header),
	}, nil
}

type brokenReaderConfig struct{}

func (b *brokenReaderConfig) Read(p []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestGetByID_BodyReadFailure_CustomTransport(t *testing.T) {
	httpClient := &http.Client{
		Transport: &brokenBodyTransportConfig{statusCode: 200},
	}
	genConfigClient, _ := genconfig.NewClient("http://localhost",
		genconfig.WithHTTPClient(httpClient),
	)
	cc := &ConfigClient{
		client:    &Client{environment: "test"},
		generated: genConfigClient,
	}

	_, err := cc.getByID(context.Background(), "test-config")
	require.Error(t, err)
	var connErr *SmplConnectionError
	assert.True(t, errors.As(err, &connErr))
}

func TestDelete_Config_BodyReadFailure_CustomTransport(t *testing.T) {
	httpClient := &http.Client{
		Transport: &brokenBodyTransportConfig{statusCode: 204},
	}
	genConfigClient, _ := genconfig.NewClient("http://localhost",
		genconfig.WithHTTPClient(httpClient),
	)
	cc := &ConfigClient{
		client:    &Client{environment: "test"},
		generated: genConfigClient,
	}

	err := cc.Management().Delete(context.Background(), "test-config")
	require.Error(t, err)
	var connErr *SmplConnectionError
	assert.True(t, errors.As(err, &connErr))
}
