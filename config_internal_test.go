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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genconfig "github.com/smplkit/go-sdk/v3/internal/generated/config"
)

func TestDerefMap_Nil(t *testing.T) {
	result := derefMap(nil)
	assert.Nil(t, result)
}

func TestDerefMap_WrapsValues(t *testing.T) {
	v := "info"
	m := map[string]genconfig.ConfigItemDefinition{
		"log_level": {Value: &v},
	}
	result := derefMap(&m)
	require.Contains(t, result, "log_level")
	inner := result["log_level"].(map[string]interface{})
	assert.Equal(t, &v, inner["value"])
}

func TestToItemsRaw_NonMapValue(t *testing.T) {
	// Defensive branch: a bare (non-map) value is wrapped as {value: v}.
	out := toItemsRaw(map[string]interface{}{"k": "bare"})
	require.Contains(t, out, "k")
	assert.Equal(t, "bare", out["k"]["value"])
}

func TestDerefMap_RetainsTypeAndDescription(t *testing.T) {
	v := "info"
	typ := genconfig.ConfigItemDefinitionType("STRING")
	desc := "log verbosity"
	m := map[string]genconfig.ConfigItemDefinition{
		"log_level": {Value: &v, Type: &typ, Description: &desc},
	}
	result := derefMap(&m)
	inner := result["log_level"].(map[string]interface{})
	assert.Equal(t, "STRING", inner["type"])
	assert.Equal(t, "log verbosity", inner["description"])
}

func TestDerefEnvs_Nil(t *testing.T) {
	result := derefEnvs(nil)
	assert.Nil(t, result)
}

func TestDerefEnvs_PassThrough(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"production": {"log_level": "warn"},
	}
	result := derefEnvs(&envs)
	assert.Equal(t, "warn", result["production"]["log_level"])
}

// ---------- resourceToConfig ----------

func TestResourceToConfig_NilID(t *testing.T) {
	r := genconfig.ConfigResource{
		// Id nil — the converter must default ID to "".
		Attributes: genconfig.Config{Name: "No ID"},
	}
	cfg := resourceToConfig(r, &ConfigClient{})
	assert.Equal(t, "", cfg.ID)
	assert.Equal(t, "No ID", cfg.Name)
}

func TestResourceToConfig_BackReference(t *testing.T) {
	id := "svc"
	cc := &ConfigClient{}
	r := genconfig.ConfigResource{
		Id:         &id,
		Attributes: genconfig.Config{Name: "Svc"},
	}
	cfg := resourceToConfig(r, cc)
	assert.Equal(t, "svc", cfg.ID)
	assert.Same(t, cc, cfg.client)
}

// ---------- buildConfig* request envelopes ----------

func TestBuildConfigRequest_RoundTrips(t *testing.T) {
	desc := "d"
	parent := "p"
	req := buildConfigRequest("svc", "Svc", &desc, &parent,
		map[string]interface{}{"k": "v"},
		map[string]map[string]interface{}{"k": {"value": "v", "type": "STRING", "description": "the k item"}},
		map[string]map[string]interface{}{"prod": {"k": "w"}},
	)
	require.NotNil(t, req.Data.Id)
	assert.Equal(t, "svc", *req.Data.Id)
	assert.Equal(t, genconfig.ConfigResourceTypeConfig, req.Data.Type)
	assert.Equal(t, "Svc", req.Data.Attributes.Name)
	require.NotNil(t, req.Data.Attributes.Items)
	require.NotNil(t, req.Data.Attributes.Environments)
	// The retained type + description survive serialization.
	def := (*req.Data.Attributes.Items)["k"]
	require.NotNil(t, def.Type)
	assert.Equal(t, genconfig.ConfigItemDefinitionType("STRING"), *def.Type)
	require.NotNil(t, def.Description)
	assert.Equal(t, "the k item", *def.Description)
}

func TestBuildConfigCreateRequest_RoundTrips(t *testing.T) {
	req := buildConfigCreateRequest("svc", "Svc", nil, nil, nil, nil, nil)
	assert.Equal(t, "svc", req.Data.Id)
	assert.Equal(t, genconfig.ConfigCreateResourceTypeConfig, req.Data.Type)
	assert.Equal(t, "Svc", req.Data.Attributes.Name)
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
		{cb: func(_ *ConfigChangeEvent) {
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

	// Should not panic.
	c.diffAndFire(
		map[string]map[string]interface{}{"app": {"a": 1}},
		map[string]map[string]interface{}{"app": {"a": 2}},
		"manual",
	)
}

func TestDiffAndFire_RecordsMetrics(t *testing.T) {
	// A metrics reporter is present, so the changes counter is recorded.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &ConfigClient{
		metrics: newMetricsReporter(&http.Client{}, srv.URL, "prod", "svc", 0),
	}
	defer c.metrics.Close()

	// No listeners — the metrics branch runs before the listener check.
	c.diffAndFire(
		map[string]map[string]interface{}{"app": {"a": 1}},
		map[string]map[string]interface{}{"app": {"a": 2}},
		"websocket",
	)
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

// ---------- resolveChain ----------

func TestResolveChain_EnvironmentOverlay(t *testing.T) {
	chain := []chainEntry{
		{
			ID:     "child",
			Values: map[string]interface{}{"log_level": "info"},
			Environments: map[string]map[string]interface{}{
				"prod": {"log_level": "warn"},
			},
		},
	}
	resolved := resolveChain(chain, "prod")
	assert.Equal(t, "warn", resolved["log_level"])

	// Without a matching environment the base value survives.
	resolved = resolveChain(chain, "dev")
	assert.Equal(t, "info", resolved["log_level"])

	// Empty environment skips the overlay entirely.
	resolved = resolveChain(chain, "")
	assert.Equal(t, "info", resolved["log_level"])
}

func TestResolveChain_ParentChildPrecedence(t *testing.T) {
	// chain[0] is the child (root of the slice), chain[1] the parent.
	// Child values win over parent.
	chain := []chainEntry{
		{ID: "child", Values: map[string]interface{}{"a": "child"}},
		{ID: "parent", Values: map[string]interface{}{"a": "parent", "b": "parent-only"}},
	}
	resolved := resolveChain(chain, "")
	assert.Equal(t, "child", resolved["a"])
	assert.Equal(t, "parent-only", resolved["b"])
}

// ---------- Refresh edge cases ----------

func TestRefresh_NoEnvironment(t *testing.T) {
	c := &ConfigClient{
		client: &SmplClient{environment: ""},
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

// newTestConfigClient builds a ConfigClient wired to an httptest server via a
// parent SmplClient. The fused client exposes CRUD directly (no .Management()).
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

	c := &SmplClient{
		apiKey:      "sk_test",
		environment: "test",
		service:     "test-service",
		httpClient:  httpClient,
		// Pre-mark started so ensureInit's ensureStarted() call is a no-op and
		// never spawns registerServiceContext (which would nil-deref the unset
		// appGenerated in this lightweight test parent).
		started: true,
	}
	cc := &ConfigClient{
		client:      c,
		generated:   genConfigClient,
		buffer:      newConfigRegistrationBuffer(),
		environment: c.environment,
		service:     c.service,
	}
	return cc
}

// ---------- getByID error paths ----------

func TestGetByID_ReadBodyError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	_, err := cc.getByID(context.Background(), "test-config")
	require.Error(t, err)
}

func TestGetByID_ReadBodyFailure(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		conn, _, _ := hj.Hijack()
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 9999\r\n\r\n"))
		conn.Close()
	}))

	_, err := cc.getByID(context.Background(), "test-config")
	require.Error(t, err)
}

// ---------- Delete error paths ----------

func TestDelete_Config_CheckStatusError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	err := cc.Delete(context.Background(), "test-config")
	require.Error(t, err)
}

func TestDelete_Config_ReadBodyFailure(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	// Either body read fails or JSON unmarshal fails — both are errors.
	require.Error(t, err)
}

// ---------- Subscribe (live proxy) ----------

func TestSubscribe_Basic(t *testing.T) {
	cc := &ConfigClient{
		client:      &SmplClient{environment: "test"},
		environment: "test",
		buffer:      newConfigRegistrationBuffer(),
		configCache: map[string]map[string]interface{}{
			"app": {"host": "localhost", "port": float64(3000)},
		},
	}
	cc.initOnce.Do(func() {})

	resolved, err := cc.Subscribe(context.Background(), "app")
	require.NoError(t, err)
	assert.Equal(t, "localhost", resolved.Value()["host"])
	assert.Equal(t, float64(3000), resolved.Value()["port"])
}

func TestSubscribe_RecordsMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cc := &ConfigClient{
		client:      &SmplClient{environment: "test"},
		environment: "test",
		buffer:      newConfigRegistrationBuffer(),
		metrics:     newMetricsReporter(&http.Client{}, srv.URL, "prod", "svc", 0),
		configCache: map[string]map[string]interface{}{
			"app": {"host": "localhost"},
		},
	}
	defer cc.metrics.Close()
	cc.initOnce.Do(func() {})

	proxy, err := cc.Subscribe(context.Background(), "app")
	require.NoError(t, err)
	assert.Equal(t, "app", proxy.ID())
}

func TestSubscribe_ErrorWhenIDNotFound(t *testing.T) {
	cc := &ConfigClient{
		client:      &SmplClient{environment: "test"},
		environment: "test",
		buffer:      newConfigRegistrationBuffer(),
		configCache: map[string]map[string]interface{}{},
	}
	cc.initOnce.Do(func() {})

	_, err := cc.Subscribe(context.Background(), "nonexistent")
	require.Error(t, err)
	var nf *NotFoundError
	require.True(t, errors.As(err, &nf))
}

func TestSubscribe_EnsureInitError(t *testing.T) {
	cc := &ConfigClient{
		client:      &SmplClient{environment: "test"},
		environment: "test",
		buffer:      newConfigRegistrationBuffer(),
	}
	// Force initOnce to run with an error.
	cc.initOnce.Do(func() {
		cc.initErr = &Error{Message: "init failed"}
	})

	_, err := cc.Subscribe(context.Background(), "app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init failed")
}

// ---------- Get (editable record) ----------

func TestGet_EditableRecord(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"svc","type":"config","attributes":{"name":"Svc","items":{"log_level":{"value":"info","type":"STRING"}},"environments":{}}}}`))
	}))

	cfg, err := cc.Get(context.Background(), "svc")
	require.NoError(t, err)
	assert.Equal(t, "svc", cfg.ID)
	assert.Equal(t, "Svc", cfg.Name)
	assert.Same(t, cc, cfg.client)
}

func TestGet_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	serverURL := server.URL
	server.Close()

	httpClient := &http.Client{}
	genConfigClient, _ := genconfig.NewClient(serverURL, genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{generated: genConfigClient}

	_, err := cc.Get(context.Background(), "svc")
	require.Error(t, err)
}

func TestGet_ReadBodyFailure(t *testing.T) {
	httpClient := &http.Client{Transport: &brokenBodyTransportConfig{statusCode: 200}}
	genConfigClient, _ := genconfig.NewClient("http://localhost", genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{generated: genConfigClient}

	_, err := cc.Get(context.Background(), "svc")
	require.Error(t, err)
	var connErr *ConnectionError
	assert.True(t, errors.As(err, &connErr))
}

func TestGet_CheckStatusError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"server error"}]}`))
	}))

	_, err := cc.Get(context.Background(), "svc")
	require.Error(t, err)
}

func TestGet_MalformedJSON(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid}`))
	}))

	_, err := cc.Get(context.Background(), "svc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

// ---------- List error paths (internal) ----------

func TestList_Internal_Success(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"a","type":"config","attributes":{"name":"A","items":{},"environments":{}}}]}`))
	}))

	configs, err := cc.List(context.Background())
	require.NoError(t, err)
	require.Len(t, configs, 1)
	assert.Equal(t, "a", configs[0].ID)
}

func TestList_Internal_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	serverURL := server.URL
	server.Close()

	httpClient := &http.Client{}
	genConfigClient, _ := genconfig.NewClient(serverURL, genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{generated: genConfigClient}

	_, err := cc.List(context.Background())
	require.Error(t, err)
}

func TestList_Internal_ReadBodyFailure(t *testing.T) {
	httpClient := &http.Client{Transport: &brokenBodyTransportConfig{statusCode: 200}}
	genConfigClient, _ := genconfig.NewClient("http://localhost", genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{generated: genConfigClient}

	_, err := cc.List(context.Background())
	require.Error(t, err)
	var connErr *ConnectionError
	assert.True(t, errors.As(err, &connErr))
}

func TestList_Internal_CheckStatusError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"boom"}]}`))
	}))

	_, err := cc.List(context.Background())
	require.Error(t, err)
}

func TestList_Internal_MalformedJSON(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid}`))
	}))

	_, err := cc.List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

// ---------- createConfig / updateConfig internal error paths ----------

func TestCreateConfig_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	serverURL := server.URL
	server.Close()

	httpClient := &http.Client{}
	genConfigClient, _ := genconfig.NewClient(serverURL, genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{generated: genConfigClient, buffer: newConfigRegistrationBuffer()}

	cfg := cc.New("svc")
	err := cc.createConfig(context.Background(), cfg)
	require.Error(t, err)
}

func TestCreateConfig_ReadBodyFailure(t *testing.T) {
	httpClient := &http.Client{Transport: &brokenBodyTransportConfig{statusCode: 201}}
	genConfigClient, _ := genconfig.NewClient("http://localhost", genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{generated: genConfigClient, buffer: newConfigRegistrationBuffer()}

	cfg := cc.New("svc")
	err := cc.createConfig(context.Background(), cfg)
	require.Error(t, err)
	var connErr *ConnectionError
	assert.True(t, errors.As(err, &connErr))
}

func TestCreateConfig_CheckStatusError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"bad"}]}`))
	}))

	cfg := cc.New("svc")
	err := cc.createConfig(context.Background(), cfg)
	require.Error(t, err)
	var valErr *ValidationError
	require.True(t, errors.As(err, &valErr))
}

func TestCreateConfig_MalformedJSON(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{not valid}`))
	}))

	cfg := cc.New("svc")
	err := cc.createConfig(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestUpdateConfig_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	serverURL := server.URL
	server.Close()

	httpClient := &http.Client{}
	genConfigClient, _ := genconfig.NewClient(serverURL, genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{generated: genConfigClient, buffer: newConfigRegistrationBuffer()}

	cfg := cc.New("svc")
	err := cc.updateConfig(context.Background(), cfg)
	require.Error(t, err)
}

func TestUpdateConfig_ReadBodyFailure(t *testing.T) {
	httpClient := &http.Client{Transport: &brokenBodyTransportConfig{statusCode: 200}}
	genConfigClient, _ := genconfig.NewClient("http://localhost", genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{generated: genConfigClient, buffer: newConfigRegistrationBuffer()}

	cfg := cc.New("svc")
	err := cc.updateConfig(context.Background(), cfg)
	require.Error(t, err)
	var connErr *ConnectionError
	assert.True(t, errors.As(err, &connErr))
}

func TestUpdateConfig_CheckStatusError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"missing"}]}`))
	}))

	cfg := cc.New("svc")
	err := cc.updateConfig(context.Background(), cfg)
	require.Error(t, err)
	var nf *NotFoundError
	require.True(t, errors.As(err, &nf))
}

func TestUpdateConfig_MalformedJSON(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid}`))
	}))

	cfg := cc.New("svc")
	err := cc.updateConfig(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestUpdateConfig_AppliesServerResponse(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"svc","type":"config","attributes":{"name":"Renamed","items":{},"environments":{}}}}`))
	}))

	cfg := cc.New("svc", WithConfigName("Original"))
	err := cc.updateConfig(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", cfg.Name)
}

// ---------- fetchChain with parent walking ----------

func TestFetchChain_ParentWalking(t *testing.T) {
	parentID := "parent-config"
	childID := "child-config"

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/"+childID, func(w http.ResponseWriter, _ *http.Request) {
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
	mux.HandleFunc("/api/v1/configs/"+parentID, func(w http.ResponseWriter, _ *http.Request) {
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

func TestFetchChain_FetchError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"boom"}]}`))
	}))
	_, err := cc.fetchChain(context.Background(), "svc")
	require.Error(t, err)
}

// ---------- fetchAllConfigs pagination ----------

func TestFetchAllConfigs_StopsOnShortPage(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		// A single short page (1 < fetchAllPageSize) ends pagination.
		_, _ = w.Write([]byte(`{"data":[{"id":"a","type":"config","attributes":{"name":"A","items":{},"environments":{}}}]}`))
	}))

	all, err := cc.fetchAllConfigs(context.Background())
	require.NoError(t, err)
	require.Len(t, all, 1)
}

func TestFetchAllConfigs_ListError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"boom"}]}`))
	}))

	_, err := cc.fetchAllConfigs(context.Background())
	require.Error(t, err)
}

// ---------- buildItemDefs ----------

func TestBuildItemDefs_Nil(t *testing.T) {
	result := buildItemDefs(nil, nil)
	assert.Nil(t, result)
}

func TestBuildItemDefs_RetainsTypeAndDescription(t *testing.T) {
	// A JSON item whose value happens to be a plain string proves retention
	// beats inference: inference would call it STRING, the retained type wins.
	items := map[string]interface{}{"payload": "raw-string"}
	itemsRaw := map[string]map[string]interface{}{
		"payload": {"value": "raw-string", "type": "JSON", "description": "json blob"},
	}
	result := buildItemDefs(items, itemsRaw)
	require.NotNil(t, result)
	def := (*result)["payload"]
	require.NotNil(t, def.Type)
	assert.Equal(t, genconfig.ConfigItemDefinitionType("JSON"), *def.Type)
	require.NotNil(t, def.Description)
	assert.Equal(t, "json blob", *def.Description)
	assert.Equal(t, "raw-string", def.Value)
}

// ---------- refEnvs ----------

func TestRefEnvs_Nil(t *testing.T) {
	result := refEnvs(nil)
	assert.Nil(t, result)
}

func TestRefEnvs_PassThrough(t *testing.T) {
	envs := map[string]map[string]interface{}{
		"production": {"log_level": "warn", "debug": true},
	}
	result := refEnvs(envs)
	require.NotNil(t, result)
	assert.Equal(t, "warn", (*result)["production"]["log_level"])
	assert.Equal(t, true, (*result)["production"]["debug"])
}

// ---------- buildItemDefs type inference ----------

func TestBuildItemDefs_InfersTypeWhenNoRaw(t *testing.T) {
	items := map[string]interface{}{
		"name":    "Acme",
		"count":   5,
		"enabled": true,
	}
	result := buildItemDefs(items, nil)
	require.NotNil(t, result)
	assert.Equal(t, genconfig.ConfigItemDefinitionType("STRING"), *(*result)["name"].Type)
	assert.Equal(t, genconfig.ConfigItemDefinitionType("NUMBER"), *(*result)["count"].Type)
	assert.Equal(t, genconfig.ConfigItemDefinitionType("BOOLEAN"), *(*result)["enabled"].Type)
	assert.Equal(t, "Acme", (*result)["name"].Value)
}

// ---------- ConfigOption builders ----------

func TestWithConfigParent(t *testing.T) {
	cc := &ConfigClient{client: &SmplClient{environment: "test"}, buffer: newConfigRegistrationBuffer()}
	cfg := cc.New("child", WithConfigParent("parent-uuid"))
	require.NotNil(t, cfg.Parent)
	assert.Equal(t, "parent-uuid", *cfg.Parent)
}

func TestWithConfigItems(t *testing.T) {
	cc := &ConfigClient{client: &SmplClient{environment: "test"}, buffer: newConfigRegistrationBuffer()}
	items := map[string]interface{}{"key1": "val1", "key2": 42}
	cfg := cc.New("cfg", WithConfigItems(items))
	assert.Equal(t, items, cfg.Items)
}

func TestWithConfigEnvironments(t *testing.T) {
	cc := &ConfigClient{client: &SmplClient{environment: "test"}, buffer: newConfigRegistrationBuffer()}
	envs := map[string]map[string]interface{}{
		"production": {"key1": "prod-val"},
	}
	cfg := cc.New("cfg", WithConfigEnvironments(envs))
	assert.Equal(t, envs, cfg.Environments)
}

func TestWithConfigDescription(t *testing.T) {
	cc := &ConfigClient{client: &SmplClient{environment: "test"}, buffer: newConfigRegistrationBuffer()}
	cfg := cc.New("cfg", WithConfigDescription("a description"))
	require.NotNil(t, cfg.Description)
	assert.Equal(t, "a description", *cfg.Description)
}

func TestNew_AutoGeneratesName(t *testing.T) {
	cc := &ConfigClient{client: &SmplClient{environment: "test"}, buffer: newConfigRegistrationBuffer()}
	cfg := cc.New("user_service")
	assert.Equal(t, "User Service", cfg.Name)
}

// ---------- ConfigEntry.Save / Delete back-reference guards ----------

func TestConfigEntry_Save_NoClient(t *testing.T) {
	cfg := &ConfigEntry{ID: "svc"}
	err := cfg.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot save")
}

func TestConfigEntry_Delete_NoClientOrID(t *testing.T) {
	cfg := &ConfigEntry{client: nil, ID: ""}
	err := cfg.Delete(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete")
}

func TestConfigEntry_Save_CreatePath_WhenUnsaved(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"svc","type":"config","attributes":{"name":"Svc","items":{},"environments":{}}}}`))
	}))
	cfg := cc.New("svc")
	require.Nil(t, cfg.CreatedAt)
	require.NoError(t, cfg.Save(context.Background()))
}

func TestConfigEntry_Save_UpdatePath_WhenCreated(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"svc","type":"config","attributes":{"name":"Svc","items":{},"environments":{}}}}`))
	}))
	cfg := cc.New("svc")
	now := time.Now()
	cfg.CreatedAt = &now
	require.NoError(t, cfg.Save(context.Background()))
}

func TestConfigEntry_Delete_DelegatesToClient(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	}))
	cfg := cc.New("svc")
	require.NoError(t, cfg.Delete(context.Background()))
}

// ---------- LiveConfig.Value ----------

func TestLiveConfig_Value(t *testing.T) {
	cc := &ConfigClient{
		client: &SmplClient{environment: "test"},
		configCache: map[string]map[string]interface{}{
			"app": {"host": "localhost"},
		},
	}
	lc := &LiveConfig{client: cc, id: "app"}
	val := lc.Value()
	assert.Equal(t, "localhost", val["host"])
}

func TestLiveConfig_Value_NilCache(t *testing.T) {
	cc := &ConfigClient{client: &SmplClient{environment: "test"}}
	lc := &LiveConfig{client: cc, id: "app"}
	val := lc.Value()
	assert.Nil(t, val)
}

func TestLiveConfig_Value_KeyNotFound(t *testing.T) {
	cc := &ConfigClient{
		client:      &SmplClient{environment: "test"},
		configCache: map[string]map[string]interface{}{},
	}
	lc := &LiveConfig{client: cc, id: "missing"}
	val := lc.Value()
	assert.Nil(t, val)
}

// ---------- LiveConfig dict-like accessors ----------

func TestLiveConfig_DictLikeAccess(t *testing.T) {
	cc := &ConfigClient{
		client: &SmplClient{environment: "test"},
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
	cc := &ConfigClient{client: &SmplClient{environment: "test"}}
	lc := &LiveConfig{client: cc, id: "app"}

	assert.Equal(t, 0, lc.Len())
	assert.False(t, lc.Has("k"))
	_, ok := lc.Get("k")
	assert.False(t, ok)
	assert.Nil(t, lc.Keys())
}

func TestLiveConfig_DictLikeAccess_MissingID(t *testing.T) {
	cc := &ConfigClient{
		client:      &SmplClient{environment: "test"},
		configCache: map[string]map[string]interface{}{},
	}
	lc := &LiveConfig{client: cc, id: "missing"}

	assert.Equal(t, 0, lc.Len())
	assert.False(t, lc.Has("k"))
	_, ok := lc.Get("k")
	assert.False(t, ok)
	assert.Nil(t, lc.Keys())
}

func TestLiveConfig_OnChange_And_OnChangeKey(t *testing.T) {
	cc := &ConfigClient{
		client:      &SmplClient{environment: "test"},
		configCache: map[string]map[string]interface{}{"app": {"a": 1}},
	}
	lc := &LiveConfig{client: cc, id: "app"}

	var scoped, keyed int
	lc.OnChange(func(_ *ConfigChangeEvent) { scoped++ })
	lc.OnChangeKey("a", func(_ *ConfigChangeEvent) { keyed++ })

	cc.diffAndFire(
		map[string]map[string]interface{}{"app": {"a": 1, "b": 1}},
		map[string]map[string]interface{}{"app": {"a": 2, "b": 2}},
		"manual",
	)
	// scoped fires for both a and b; keyed fires only for a.
	assert.Equal(t, 2, scoped)
	assert.Equal(t, 1, keyed)
}

// ---------- getByID broken-body transport ----------

// brokenBodyTransportConfig wraps an HTTP transport and replaces the response
// body with a broken reader.
type brokenBodyTransportConfig struct {
	statusCode int
}

func (t *brokenBodyTransportConfig) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.statusCode,
		Body:       io.NopCloser(&brokenReaderConfig{}),
		Header:     make(http.Header),
	}, nil
}

type brokenReaderConfig struct{}

func (b *brokenReaderConfig) Read(_ []byte) (int, error) {
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
		client:    &SmplClient{environment: "test"},
		generated: genConfigClient,
	}

	_, err := cc.getByID(context.Background(), "test-config")
	require.Error(t, err)
	var connErr *ConnectionError
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
		client:    &SmplClient{environment: "test"},
		generated: genConfigClient,
	}

	err := cc.Delete(context.Background(), "test-config")
	require.Error(t, err)
	var connErr *ConnectionError
	assert.True(t, errors.As(err, &connErr))
}

func TestGetByID_InvalidJSONResponse(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not valid json`))
	}))

	_, err := cc.getByID(context.Background(), "test-config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse response")
}

func TestGetByID_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	serverURL := server.URL
	server.Close()

	httpClient := &http.Client{}
	genConfigClient, _ := genconfig.NewClient(serverURL, genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{
		client:    &SmplClient{environment: "test"},
		generated: genConfigClient,
	}

	_, err := cc.getByID(context.Background(), "test-config")
	require.Error(t, err)
}

func TestDelete_Config_ConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	serverURL := server.URL
	server.Close()

	httpClient := &http.Client{}
	genConfigClient, _ := genconfig.NewClient(serverURL, genconfig.WithHTTPClient(httpClient))
	cc := &ConfigClient{
		client:    &SmplClient{environment: "test"},
		generated: genConfigClient,
	}

	err := cc.Delete(context.Background(), "test-config")
	require.Error(t, err)
}

func TestDelete_Config_Success(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	require.NoError(t, cc.Delete(context.Background(), "svc"))
}

// --- WS listener registration after ensureInit ---

func TestConfigEnsureInit_RegistersWSListeners(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, _ *http.Request) {
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

func TestConfigEnsureInit_FetchAllError(t *testing.T) {
	// The first list call fails — ensureInit records the error and returns it.
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"boom"}]}`))
	}))
	ws := &sharedWebSocket{
		listeners: make(map[string][]eventCallback),
		closeCh:   make(chan struct{}),
		wsDone:    make(chan struct{}),
	}
	cc.client.ws = ws

	err := cc.ensureInit(context.Background())
	require.Error(t, err)
}

func TestConfigEnsureInit_FetchChainError(t *testing.T) {
	// List succeeds with one config, but fetchChain (getByID) fails on it.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"svc","type":"config","attributes":{"name":"Svc","items":{},"environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/configs/svc", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"boom"}]}`))
	})
	cc := newTestConfigClient(t, mux)
	ws := &sharedWebSocket{
		listeners: make(map[string][]eventCallback),
		closeCh:   make(chan struct{}),
		wsDone:    make(chan struct{}),
	}
	cc.client.ws = ws

	err := cc.ensureInit(context.Background())
	require.Error(t, err)
}

func TestConfigEnsureInit_Idempotent(t *testing.T) {
	var listCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&listCalls, 1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cc := newTestConfigClient(t, mux)
	ws := &sharedWebSocket{
		listeners: make(map[string][]eventCallback),
		closeCh:   make(chan struct{}),
		wsDone:    make(chan struct{}),
	}
	cc.client.ws = ws

	require.NoError(t, cc.ensureInit(context.Background()))
	require.NoError(t, cc.ensureInit(context.Background()))
	assert.Equal(t, int32(1), atomic.LoadInt32(&listCalls), "ensureInit must fetch once")
}

// --- handleConfigChanged ---

func TestHandleConfigChanged(t *testing.T) {
	configJSON := `{"data":{"id":"svc","type":"config","attributes":{"id":"svc","name":"Svc","items":{},"environments":{}}}}`
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(configJSON))
	}))

	// Should not panic (nil cache is lazily created).
	cc.handleConfigChanged(map[string]interface{}{"id": "svc"})
}

func TestHandleConfigChanged_ScopedFetch_ContentChanged(t *testing.T) {
	var fetchCount int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/svc", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"svc","type":"config","attributes":{"id":"svc","name":"Svc","items":{"log_level":{"value":"INFO","type":"JSON"}},"environments":{}}}}`))
	})
	cc := newTestConfigClient(t, http.HandlerFunc(mux.ServeHTTP))

	cc.configCache = map[string]map[string]interface{}{
		"svc": {"log_level": "DEBUG"},
	}
	cc.environment = "production"

	var fired bool
	cc.OnChange(func(_ *ConfigChangeEvent) {
		fired = true
	})

	cc.handleConfigChanged(map[string]interface{}{"id": "svc"})

	assert.Equal(t, int32(1), atomic.LoadInt32(&fetchCount), "should call GetConfig once")
	assert.True(t, fired, "listener should fire when content changed")
}

func TestHandleConfigChanged_ScopedFetch_ContentUnchanged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/svc", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"svc","type":"config","attributes":{"id":"svc","name":"Svc","items":{"log_level":{"value":"DEBUG","type":"JSON"}},"environments":{}}}}`))
	})
	cc := newTestConfigClient(t, http.HandlerFunc(mux.ServeHTTP))
	cc.environment = "production"
	cc.configCache = make(map[string]map[string]interface{})

	chain, err := cc.fetchChain(context.Background(), "svc")
	require.NoError(t, err)
	cc.configCache["svc"] = resolveChain(chain, "production")

	var called bool
	cc.OnChange(func(_ *ConfigChangeEvent) { called = true })

	cc.handleConfigChanged(map[string]interface{}{"id": "svc"})

	assert.False(t, called, "listener should NOT fire when content is unchanged")
}

func TestHandleConfigChanged_FetchError(t *testing.T) {
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))
	cc.configCache = make(map[string]map[string]interface{})
	cc.environment = "production"

	var called bool
	cc.OnChange(func(_ *ConfigChangeEvent) { called = true })

	cc.handleConfigChanged(map[string]interface{}{"id": "svc"})
	assert.False(t, called)
}

// --- grandparent-absent regression (Mike-requested) ---
//
// A child config inherits from a grandparent through a parent that carries no
// override for the inherited key. fetchChain walks child → parent →
// grandparent, and resolveChain merges parent-over-grandparent then
// child-over-that, so the grandparent's value survives into the child's
// resolved values. This test LOCKS that behavior so a future refactor can't
// reintroduce the bug where a missing intermediate dropped the grandparent's
// value. The assertion runs AFTER the config_changed websocket handler fires.
func TestHandleConfigChanged_GrandparentInheritedValueSurvives(t *testing.T) {
	const (
		childID  = "child"
		parentID = "parent"
		grandID  = "grandparent"
	)
	mux := http.NewServeMux()
	// Child: declares only its own key; parent points at the parent.
	mux.HandleFunc("/api/v1/configs/"+childID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"child","type":"config","attributes":{"name":"Child","parent":"parent","items":{"child_key":{"value":"c","type":"STRING"}},"environments":{}}}}`))
	})
	// Parent: carries NO override for the grandparent's key — it just points
	// at the grandparent. This is the "intermediate absent" case.
	mux.HandleFunc("/api/v1/configs/"+parentID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"parent","type":"config","attributes":{"name":"Parent","parent":"grandparent","items":{},"environments":{}}}}`))
	})
	// Grandparent: owns the inherited key.
	mux.HandleFunc("/api/v1/configs/"+grandID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"grandparent","type":"config","attributes":{"name":"Grandparent","items":{"inherited_key":{"value":"from-grandparent","type":"STRING"}},"environments":{}}}}`))
	})

	cc := newTestConfigClient(t, mux)
	cc.environment = "production"
	// Seed the cache with a stale value so the handler detects a change and
	// re-resolves the full chain.
	cc.configCache = map[string]map[string]interface{}{
		childID: {"child_key": "c"},
	}

	var resolvedAtEvent map[string]interface{}
	cc.OnChange(func(evt *ConfigChangeEvent) {
		if evt.ConfigID == childID {
			resolvedAtEvent = cc.configCache[childID]
		}
	})

	// Fire the websocket event handler.
	cc.handleConfigChanged(map[string]interface{}{"id": childID})

	// After the handler runs, the child's resolved values must contain the
	// grandparent-inherited key even though the parent carried no override.
	final := cc.configCache[childID]
	require.NotNil(t, final)
	assert.Equal(t, "from-grandparent", final["inherited_key"],
		"child must inherit the grandparent's value through the override-free parent")
	assert.Equal(t, "c", final["child_key"])
	// And a listener observing the change sees the grandparent value too.
	require.NotNil(t, resolvedAtEvent)
	assert.Equal(t, "from-grandparent", resolvedAtEvent["inherited_key"])
}

// --- handleConfigDeleted ---

func TestHandleConfigDeleted_RemovesFromCache_FiresListener(t *testing.T) {
	var fetchCount int32
	cc := newTestConfigClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		w.WriteHeader(http.StatusOK)
	}))

	cc.configCache = map[string]map[string]interface{}{
		"svc": {"log_level": "INFO"},
	}
	cc.environment = "production"

	var evt *ConfigChangeEvent
	cc.OnChange(func(e *ConfigChangeEvent) { evt = e })

	cc.handleConfigDeleted(map[string]interface{}{"id": "svc"})

	assert.Equal(t, int32(0), atomic.LoadInt32(&fetchCount), "config_deleted must NOT make HTTP fetch")
	_, stillInCache := cc.configCache["svc"]
	assert.False(t, stillInCache, "config should be removed from cache")
	assert.NotNil(t, evt, "listener should fire on deletion")
}

func TestHandleConfigDeleted_NilCache(t *testing.T) {
	cc := newTestConfigClient(t, nil)
	assert.NotPanics(t, func() {
		cc.handleConfigDeleted(map[string]interface{}{"id": "svc"})
	})
}

func TestHandleConfigDeleted_KeyNotInCache(t *testing.T) {
	cc := newTestConfigClient(t, nil)
	cc.configCache = map[string]map[string]interface{}{
		"other": {"x": "y"},
	}

	var called bool
	cc.OnChange(func(_ *ConfigChangeEvent) { called = true })

	cc.handleConfigDeleted(map[string]interface{}{"id": "not-in-cache"})
	assert.False(t, called)
}

// --- handleConfigsChanged ---

func TestHandleConfigsChanged_FullFetch(t *testing.T) {
	var listFetched bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs", func(w http.ResponseWriter, _ *http.Request) {
		listFetched = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	cc := newTestConfigClient(t, http.HandlerFunc(mux.ServeHTTP))
	cc.configCache = map[string]map[string]interface{}{}
	cc.environment = "production"
	// Mark init done so Refresh succeeds without a second WS subscribe.
	cc.initOnce.Do(func() {})

	cc.handleConfigsChanged(map[string]interface{}{})

	assert.True(t, listFetched, "configs_changed should trigger a full list fetch")
}
