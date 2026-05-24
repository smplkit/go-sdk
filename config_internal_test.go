package smplkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genconfig "github.com/smplkit/go-sdk/v3/internal/generated/config"
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
	assert.Equal(t, "localhost", resolved.Value()["host"])
	assert.Equal(t, float64(3000), resolved.Value()["port"])
}

func TestGet_ErrorWhenIDNotFound(t *testing.T) {
	cc := &ConfigClient{
		client:      &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{},
	}
	cc.initOnce.Do(func() {})

	_, err := cc.Get(context.Background(), "nonexistent")
	require.Error(t, err)
	var nf *NotFoundError
	require.True(t, errors.As(err, &nf))
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

// ---------- refMap nil case ----------

func TestRefMap_Nil(t *testing.T) {
	result := refMap(nil)
	assert.Nil(t, result)
}

// ---------- refEnvs ----------

func TestRefEnvs_Nil(t *testing.T) {
	result := refEnvs(nil)
	assert.Nil(t, result)
}

func TestRefEnvs_PassThrough(t *testing.T) {
	// Per ADR-024 §2.4 the wire shape is flat — refEnvs is a simple
	// pointer-wrap of the in-memory map, no envelope translation.
	envs := map[string]map[string]interface{}{
		"production": {"log_level": "warn", "debug": true},
	}
	result := refEnvs(envs)
	require.NotNil(t, result)
	assert.Equal(t, "warn", (*result)["production"]["log_level"])
	assert.Equal(t, true, (*result)["production"]["debug"])
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

// ---------- LiveConfig dict-like accessors (rule 10) ----------

func TestLiveConfig_DictLikeAccess(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{
			"app": {"host": "localhost", "port": float64(5432)},
		},
	}
	lc := &LiveConfig{client: cc, id: "app"}

	assert.Equal(t, "app", lc.ID())
	assert.Equal(t, 2, lc.Len())
	assert.True(t, lc.Has("host"))
	assert.False(t, lc.Has("nope"))

	v, ok := lc.Get("host")
	assert.True(t, ok)
	assert.Equal(t, "localhost", v)

	_, ok = lc.Get("nope")
	assert.False(t, ok)

	keys := lc.Keys()
	assert.ElementsMatch(t, []string{"host", "port"}, keys)
}

func TestLiveConfig_DictLikeAccess_NilCache(t *testing.T) {
	cc := &ConfigClient{client: &Client{environment: "test"}}
	lc := &LiveConfig{client: cc, id: "app"}

	assert.Equal(t, 0, lc.Len())
	assert.False(t, lc.Has("k"))
	_, ok := lc.Get("k")
	assert.False(t, ok)
	assert.Nil(t, lc.Keys())
}

func TestLiveConfig_DictLikeAccess_MissingID(t *testing.T) {
	cc := &ConfigClient{
		client:      &Client{environment: "test"},
		configCache: map[string]map[string]interface{}{},
	}
	lc := &LiveConfig{client: cc, id: "missing"}

	assert.Equal(t, 0, lc.Len())
	assert.False(t, lc.Has("k"))
	_, ok := lc.Get("k")
	assert.False(t, ok)
	assert.Nil(t, lc.Keys())
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

// ---------- Resolve ensureInit error ----------

func TestGet_EnsureInitError(t *testing.T) {
	cc := &ConfigClient{
		client: &Client{environment: "test"},
	}
	// Force initOnce to run with an error
	cc.initOnce.Do(func() {
		cc.initErr = &Error{Message: "init failed"}
	})

	_, err := cc.Get(context.Background(), "app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init failed")
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

// --- WS listener registration after ensureInit ---

func TestConfigEnsureInit_RegistersWSListeners(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cc := newTestConfigClient(t, mux)

	// Pre-inject a sharedWebSocket so ensureWS() uses it without starting a goroutine.
	ws := &sharedWebSocket{
		listeners: make(map[string][]eventCallback),
		closeCh:   make(chan struct{}),
		wsDone:    make(chan struct{}),
	}
	cc.client.ws = ws

	err := cc.ensureInit(context.Background())
	require.NoError(t, err)

	ws.listenersMu.Lock()
	_, hasChanged := ws.listeners["config_changed"]
	_, hasDeleted := ws.listeners["config_deleted"]
	_, hasConfigsChanged := ws.listeners["configs_changed"]
	ws.listenersMu.Unlock()

	assert.True(t, hasChanged, "config_changed should be registered in WS listener map")
	assert.True(t, hasDeleted, "config_deleted should be registered in WS listener map")
	assert.True(t, hasConfigsChanged, "configs_changed should be registered in WS listener map")
}

// --- handleConfigChanged ---

func TestHandleConfigChanged(t *testing.T) {
	// handleConfigChanged now calls fetchChain → GetConfig (single-item response).
	configJSON := `{"data":{"id":"svc","type":"config","attributes":{"id":"svc","name":"Svc","items":{},"environments":{}}}}`
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(configJSON))
	}))

	// Should not panic.
	cc.handleConfigChanged(map[string]interface{}{"id": "svc"})
}

// ========== New WS event handler tests for config ==========

// TestHandleConfigChanged_ScopedFetch_ContentChanged verifies that config_changed
// calls GetConfig (scoped) and fires listeners when content differs.
func TestHandleConfigChanged_ScopedFetch_ContentChanged(t *testing.T) {
	var fetchCount int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/svc", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"svc","type":"config","attributes":{"id":"svc","name":"Svc","items":{"log_level":{"value":"INFO","type":"JSON"}},"environments":{}}}}`))
	})
	cc := newTestConfigClient(t, http.HandlerFunc(mux.ServeHTTP))

	// Pre-populate cache with different content.
	cc.configCache = map[string]map[string]interface{}{
		"svc": {"log_level": "DEBUG"},
	}
	cc.client.environment = "production"

	var fired bool
	cc.OnChange(func(evt *ConfigChangeEvent) {
		fired = true
	})

	cc.handleConfigChanged(map[string]interface{}{"id": "svc"})

	assert.Equal(t, int32(1), atomic.LoadInt32(&fetchCount), "should call GetConfig once")
	assert.True(t, fired, "listener should fire when content changed")
}

// TestHandleConfigChanged_ScopedFetch_ContentUnchanged verifies that config_changed
// does NOT fire listeners when resolution is identical.
// We pre-warm the cache using fetchChain so the stored map matches exactly.
func TestHandleConfigChanged_ScopedFetch_ContentUnchanged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/svc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"svc","type":"config","attributes":{"id":"svc","name":"Svc","items":{"log_level":{"value":"DEBUG","type":"JSON"}},"environments":{}}}}`))
	})
	cc := newTestConfigClient(t, http.HandlerFunc(mux.ServeHTTP))
	cc.client.environment = "production"
	cc.configCache = make(map[string]map[string]interface{})

	// Pre-warm: fetch once so the cache has the exact map representation.
	chain, err := cc.fetchChain(context.Background(), "svc")
	require.NoError(t, err)
	cc.configCache["svc"] = resolveChain(chain, "production")

	var called bool
	cc.OnChange(func(evt *ConfigChangeEvent) { called = true })

	// Second handleConfigChanged call: server returns same data → no diff.
	cc.handleConfigChanged(map[string]interface{}{"id": "svc"})

	assert.False(t, called, "listener should NOT fire when content is unchanged")
}

// TestHandleConfigDeleted_RemovesFromCache_FiresListener verifies that config_deleted
// removes the config from cache and fires listeners without an HTTP fetch.
func TestHandleConfigDeleted_RemovesFromCache_FiresListener(t *testing.T) {
	var fetchCount int32
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		w.WriteHeader(http.StatusOK)
	}))

	cc.configCache = map[string]map[string]interface{}{
		"svc": {"log_level": "INFO"},
	}
	cc.client.environment = "production"

	var evt *ConfigChangeEvent
	cc.OnChange(func(e *ConfigChangeEvent) { evt = e })

	cc.handleConfigDeleted(map[string]interface{}{"id": "svc"})

	assert.Equal(t, int32(0), atomic.LoadInt32(&fetchCount), "config_deleted must NOT make HTTP fetch")
	_, stillInCache := cc.configCache["svc"]
	assert.False(t, stillInCache, "config should be removed from cache")
	assert.NotNil(t, evt, "listener should fire on deletion")
}

// TestHandleConfigsChanged_FullFetch verifies that configs_changed triggers
// a full Refresh.
func TestHandleConfigsChanged_FullFetch(t *testing.T) {
	var listFetched bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		listFetched = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	cc := newTestConfigClient(t, http.HandlerFunc(mux.ServeHTTP))
	cc.configCache = map[string]map[string]interface{}{}
	cc.client.environment = "production"
	// Mark init done so Refresh succeeds.
	cc.initOnce.Do(func() {})

	cc.handleConfigsChanged(map[string]interface{}{})

	assert.True(t, listFetched, "configs_changed should trigger a full list fetch")
}

// ========== Coverage gap tests ==========

// TestHandleConfigChanged_FetchError covers the fetchChain error early return.
func TestHandleConfigChanged_FetchError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))
	cc.configCache = make(map[string]map[string]interface{})
	cc.client.environment = "production"

	var called bool
	cc.OnChange(func(evt *ConfigChangeEvent) { called = true })

	// Should not panic; error causes early return.
	cc.handleConfigChanged(map[string]interface{}{"id": "svc"})
	assert.False(t, called)
}

// TestHandleConfigDeleted_NilCache covers the nil configCache early return.
func TestHandleConfigDeleted_NilCache(t *testing.T) {
	cc := newTestConfigClient(t, nil)
	// configCache is nil by default.
	assert.NotPanics(t, func() {
		cc.handleConfigDeleted(map[string]interface{}{"id": "svc"})
	})
}

// TestHandleConfigDeleted_KeyNotInCache covers the !existed early return.
func TestHandleConfigDeleted_KeyNotInCache(t *testing.T) {
	cc := newTestConfigClient(t, nil)
	cc.configCache = map[string]map[string]interface{}{
		"other": {"x": "y"},
	}

	var called bool
	cc.OnChange(func(evt *ConfigChangeEvent) { called = true })

	cc.handleConfigDeleted(map[string]interface{}{"id": "not-in-cache"})
	assert.False(t, called)
}
