package smplkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genflags "github.com/smplkit/go-sdk/internal/generated/flags"
)

// --- appServiceBaseURL ---

func TestAppServiceBaseURL(t *testing.T) {
	assert.Equal(t, "https://app.smplkit.com", appServiceBaseURL("https://config.smplkit.com"))
	assert.Equal(t, "https://app.smplkit.com", appServiceBaseURL(""))
	// When baseURL is overridden (e.g. for tests), return it directly
	assert.Equal(t, "http://localhost:8000", appServiceBaseURL("http://localhost:8000"))
}

func TestDoJSONApp_RoutesToAppBaseURL(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/context_types", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"ct-1","attributes":{"key":"user","name":"User","attributes":{}}}}`))
	}))

	body, resp, err := fc.doJSONApp(context.Background(), "GET", "/api/v1/context_types", nil)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(body), "ct-1")
}

// --- doJSONApp / doJSONWithBase ---

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

	c := &Client{
		apiKey:     "sk_test",
		baseURL:    server.URL,
		httpClient: httpClient,
	}
	fc := &FlagsClient{client: c, generated: genFlagsClient}
	fc.runtime = newFlagsRuntime(fc)
	return fc, server
}

func TestDoJSONWithBase_GET(t *testing.T) {
	fc, server := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/test", r.URL.Path)
		assert.Equal(t, "application/vnd.api+json", r.Header.Get("Accept"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	body, resp, err := fc.doJSONWithBase(context.Background(), "GET", server.URL+"/api/v1/test", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "ok")
}

func TestDoJSONWithBase_POST(t *testing.T) {
	fc, server := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		b, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(b, &payload)
		assert.Equal(t, "hello", payload["key"])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))

	body, resp, err := fc.doJSONWithBase(context.Background(), "POST", server.URL+"/api/v1/test", map[string]interface{}{"key": "hello"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Contains(t, string(body), "created")
}

func TestDoJSONApp_UsesAppBaseURL(t *testing.T) {
	// When baseURL is the production config URL, app URL is production app URL
	url := appServiceBaseURL("https://config.smplkit.com")
	assert.Equal(t, "https://app.smplkit.com", url)

	// When baseURL is overridden (e.g. test), returns the override
	url = appServiceBaseURL("http://localhost:9999")
	assert.Equal(t, "http://localhost:9999", url)
}

// --- Context type management ---

func TestParseContextType(t *testing.T) {
	body := []byte(`{"data":{"id":"ct-1","attributes":{"key":"user","name":"User","attributes":{"plan":"string"}}}}`)
	ct, err := parseContextType(body)
	require.NoError(t, err)
	assert.Equal(t, "ct-1", ct.ID)
	assert.Equal(t, "user", ct.Key)
	assert.Equal(t, "User", ct.Name)
	assert.Equal(t, "string", ct.Attributes["plan"])
}

func TestParseContextType_NilAttributes(t *testing.T) {
	body := []byte(`{"data":{"id":"ct-2","attributes":{"key":"device","name":"Device"}}}`)
	ct, err := parseContextType(body)
	require.NoError(t, err)
	assert.NotNil(t, ct.Attributes)
	assert.Len(t, ct.Attributes, 0)
}

func TestParseContextType_InvalidJSON(t *testing.T) {
	_, err := parseContextType([]byte(`not json`))
	assert.Error(t, err)
}

func TestParseContextTypeRaw_InvalidJSON(t *testing.T) {
	_, err := parseContextTypeRaw(json.RawMessage(`{invalid}`))
	assert.Error(t, err)
}

func TestListContextTypes_ParseMultiple(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[
			{"id":"ct-1","attributes":{"key":"user","name":"User","attributes":{}}},
			{"id":"ct-2","attributes":{"key":"account","name":"Account","attributes":{}}}
		]}`))
	}))

	body, resp, err := fc.doJSONWithBase(context.Background(), "GET", fc.client.baseURL+"/api/v1/context_types", nil)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	var result struct {
		Data []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Len(t, result.Data, 2)

	ct1, err := parseContextTypeRaw(result.Data[0])
	require.NoError(t, err)
	assert.Equal(t, "user", ct1.Key)
}

// --- resourceToFlag ---

func TestResourceToFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)

	id := "flag-id-1"
	flagType := "flag"
	desc := "A flag"
	now := time.Now()
	r := flagResource(id, flagType, "feature-x", "Feature X", "BOOLEAN", true, desc, now)

	flag := resourceToFlag(r, fc)
	assert.Equal(t, id, flag.ID)
	assert.Equal(t, "feature-x", flag.Key)
	assert.Equal(t, "Feature X", flag.Name)
	assert.Equal(t, "BOOLEAN", flag.Type)
	assert.Equal(t, true, flag.Default)
	assert.Equal(t, &desc, flag.Description)
	assert.NotNil(t, flag.CreatedAt)
}

func TestResourceToFlag_NilID(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)

	now := time.Now()
	r := flagResourceNoID("feature-x", "Feature X", "BOOLEAN", true, now)

	flag := resourceToFlag(r, fc)
	assert.Equal(t, "", flag.ID)
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
	req := buildFlagRequest("", "feature-x", "Feature X", "BOOLEAN", true, values, &desc, nil)
	assert.Nil(t, req.Data.Id)
	assert.Equal(t, "feature-x", req.Data.Attributes.Key)
	assert.Len(t, req.Data.Attributes.Values, 2)
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
	req := buildFlagRequest("flag-id", "feature-x", "Feature X", "BOOLEAN", true, values, nil, envs)
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
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))

	batch := []map[string]interface{}{
		{"id": "user:u1", "name": "User 1"},
	}
	// Need to route through test server — call doJSONWithBase directly
	_, _, _ = fc.doJSONWithBase(context.Background(), "PUT", fc.client.baseURL+"/api/v1/contexts/bulk",
		map[string]interface{}{"contexts": batch})

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, receivedPayload)
	contexts := receivedPayload["contexts"].([]interface{})
	assert.Len(t, contexts, 1)
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
		Key:     "feature",
		Name:    "Feature",
		Type:    "BOOLEAN",
		Default: true,
	}
	f.apply(other)
	assert.Equal(t, "new", f.ID)
	assert.Equal(t, "feature", f.Key)
}

// --- sharedWebSocket tests ---

func TestSharedWebSocket_BuildWSURL(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "sk_test")
	url := ws.buildWSURL()
	assert.Contains(t, url, "wss://app.smplkit.com")
	assert.Contains(t, url, "api_key=sk_test")
}

func TestSharedWebSocket_BuildWSURL_HTTP(t *testing.T) {
	ws := newSharedWebSocket("http://localhost:8000", "sk_test")
	url := ws.buildWSURL()
	assert.Contains(t, url, "ws://localhost:8000")
}

func TestSharedWebSocket_BuildWSURL_NoScheme(t *testing.T) {
	ws := newSharedWebSocket("app.smplkit.com", "sk_test")
	url := ws.buildWSURL()
	assert.Contains(t, url, "wss://app.smplkit.com")
}

func TestSharedWebSocket_BuildWSURL_TrailingSlash(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com/", "sk_test")
	url := ws.buildWSURL()
	assert.Contains(t, url, "wss://app.smplkit.com/api/ws/v1/events")
}

func TestSharedWebSocket_OnOff(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")

	var called bool
	cb := func(data map[string]interface{}) { called = true }
	ws.on("test_event", cb)

	ws.dispatch("test_event", map[string]interface{}{})
	assert.True(t, called)

	called = false
	ws.off("test_event", cb)
	ws.dispatch("test_event", map[string]interface{}{})
	assert.False(t, called)
}

func TestSharedWebSocket_DispatchPanicRecovery(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	ws.on("crash_event", func(data map[string]interface{}) {
		panic("test panic")
	})

	assert.NotPanics(t, func() {
		ws.dispatch("crash_event", map[string]interface{}{})
	})
}

func TestSharedWebSocket_ConnectionStatus(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	assert.Equal(t, "disconnected", ws.connectionStatus())

	ws.setStatus("connected")
	assert.Equal(t, "connected", ws.connectionStatus())
}

func TestSharedWebSocket_Off_Empty(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	// off on empty list should not panic
	ws.off("nonexistent", func(data map[string]interface{}) {})
}

func TestSharedWebSocket_DispatchNoListeners(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	// dispatch with no listeners should not panic
	ws.dispatch("no_listeners", map[string]interface{}{})
}

func TestSharedWebSocket_Stop(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	ws.dialWS = func(url string) (*websocket.Conn, error) {
		return nil, assert.AnError
	}
	ws.start()

	// Give it time to start the goroutine
	time.Sleep(50 * time.Millisecond)

	ws.stop()
	assert.Equal(t, "disconnected", ws.connectionStatus())
}

func TestSharedWebSocket_Run_ClosedImmediately(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	close(ws.closeCh)
	ws.run() // should exit immediately
	assert.Equal(t, "disconnected", ws.connectionStatus())
}

func TestSharedWebSocket_Connect_DialError(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	ws.dialWS = func(url string) (*websocket.Conn, error) {
		return nil, assert.AnError
	}
	closed := ws.connect()
	assert.False(t, closed)
}

func TestSharedWebSocket_Connect_DialError_Closed(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	close(ws.closeCh)
	ws.dialWS = func(url string) (*websocket.Conn, error) {
		return nil, assert.AnError
	}
	closed := ws.connect()
	assert.True(t, closed)
}

func TestSharedWebSocket_Connect_ErrorMessage(t *testing.T) {
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Send error message
		_ = conn.WriteJSON(map[string]interface{}{
			"type":    "error",
			"message": "unauthorized",
		})
	}))
	defer server.Close()

	ws := newSharedWebSocket(server.URL, "test")
	closed := ws.connect()
	assert.False(t, closed)
}

func TestSharedWebSocket_Connect_ReadConfirmError_Closed(t *testing.T) {
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Close immediately so ReadJSON fails
		conn.Close()
	}))
	defer server.Close()

	ws := newSharedWebSocket(server.URL, "test")
	close(ws.closeCh)
	closed := ws.connect()
	assert.True(t, closed)
}

func TestSharedWebSocket_Connect_ReadConfirmError_NotClosed(t *testing.T) {
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer server.Close()

	ws := newSharedWebSocket(server.URL, "test")
	closed := ws.connect()
	assert.False(t, closed)
}

func TestSharedWebSocket_Connect_PingPong(t *testing.T) {
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Send connected confirmation
		_ = conn.WriteJSON(map[string]interface{}{"type": "connected"})

		// Send a ping
		_ = conn.WriteMessage(websocket.TextMessage, []byte("ping"))

		// Read pong response
		_, msg, err := conn.ReadMessage()
		if err == nil {
			assert.Equal(t, "pong", string(msg))
		}

		// Send an event
		_ = conn.WriteJSON(map[string]interface{}{
			"event": "test_event",
			"data":  "hello",
		})

		// Keep alive briefly
		time.Sleep(100 * time.Millisecond)
		conn.Close()
	}))
	defer server.Close()

	ws := newSharedWebSocket(server.URL, "test")
	var dispatched bool
	var mu sync.Mutex
	ws.on("test_event", func(data map[string]interface{}) {
		mu.Lock()
		dispatched = true
		mu.Unlock()
	})

	// Run connect in goroutine
	done := make(chan bool)
	go func() {
		closed := ws.connect()
		done <- closed
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connect did not return")
	}

	mu.Lock()
	assert.True(t, dispatched)
	mu.Unlock()
}

func TestSharedWebSocket_Connect_InvalidJSON(t *testing.T) {
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_ = conn.WriteJSON(map[string]interface{}{"type": "connected"})
		// Send invalid JSON
		_ = conn.WriteMessage(websocket.TextMessage, []byte("not json"))
		// Close to end the test
		time.Sleep(50 * time.Millisecond)
		conn.Close()
	}))
	defer server.Close()

	ws := newSharedWebSocket(server.URL, "test")
	closed := ws.connect()
	assert.False(t, closed)
}

func TestSharedWebSocket_Connect_EventNoEventField(t *testing.T) {
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_ = conn.WriteJSON(map[string]interface{}{"type": "connected"})
		_ = conn.WriteJSON(map[string]interface{}{"no_event_key": true})
		time.Sleep(50 * time.Millisecond)
		conn.Close()
	}))
	defer server.Close()

	ws := newSharedWebSocket(server.URL, "test")
	closed := ws.connect()
	assert.False(t, closed)
}

func TestSharedWebSocket_Run_Reconnect(t *testing.T) {
	var connectCount int
	var mu sync.Mutex
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		connectCount++
		mu.Unlock()

		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Close immediately to trigger reconnect
		conn.Close()
	}))
	defer server.Close()

	ws := newSharedWebSocket(server.URL, "test")

	go ws.run()

	// Wait for reconnect cycles (backoff starts at 1s, doubles each time)
	time.Sleep(2500 * time.Millisecond)

	ws.closeOnce.Do(func() {
		close(ws.closeCh)
	})
	<-ws.wsDone

	mu.Lock()
	assert.True(t, connectCount >= 2, "expected at least 2 connect attempts, got %d", connectCount)
	mu.Unlock()
}

func TestSharedWebSocket_Connect_ReadError_CloseCh(t *testing.T) {
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]interface{}{"type": "connected"})
		// Keep alive
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	ws := newSharedWebSocket(server.URL, "test")

	done := make(chan bool)
	go func() {
		closed := ws.connect()
		done <- closed
	}()

	// Wait for connected status
	for i := 0; i < 100; i++ {
		if ws.connectionStatus() == "connected" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(ws.closeCh)

	select {
	case closed := <-done:
		assert.True(t, closed)
	case <-time.After(2 * time.Second):
		t.Fatal("connect did not return")
	}
}

// --- Client ensureWS / stopWS ---

func TestClient_EnsureWS(t *testing.T) {
	c := &Client{
		apiKey:  "sk_test",
		baseURL: "https://config.smplkit.com",
	}
	// Note: ensureWS creates a real WS manager and starts a goroutine.
	// We need to ensure it creates one and returns the same on subsequent calls.
	ws1 := c.ensureWS()
	ws2 := c.ensureWS()
	assert.Same(t, ws1, ws2)

	// Stop the WS to clean up
	c.stopWS()
}

// --- FlagsRuntime additional coverage ---

func TestFlagsRuntime_FireChangeListenersAll(t *testing.T) {
	rt := newFlagsRuntime(nil)
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
	rt := newFlagsRuntime(nil)
	handle := rt.BoolFlag("feature", true)

	handle.OnChange(func(e *FlagChangeEvent) {
		panic("flag listener panic")
	})

	// Should not propagate the panic
	assert.NotPanics(t, func() {
		rt.fireChangeListeners("feature", "manual")
	})
}

func TestFlagsRuntime_EvaluateHandle_NilEvaluationResult(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
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
	fc.runtime.mu.Lock()
	fc.runtime.connected = true
	fc.runtime.mu.Unlock()
	handle := fc.BoolFlag("feature", false)
	assert.NotNil(t, handle)
	assert.Equal(t, false, handle.Get(context.Background()))
}

func TestFlagsClient_StringFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.mu.Lock()
	fc.runtime.connected = true
	fc.runtime.mu.Unlock()
	handle := fc.StringFlag("theme", "light")
	assert.Equal(t, "light", handle.Get(context.Background()))
}

func TestFlagsClient_NumberFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.mu.Lock()
	fc.runtime.connected = true
	fc.runtime.mu.Unlock()
	handle := fc.NumberFlag("max-retries", 3.0)
	assert.Equal(t, 3.0, handle.Get(context.Background()))
}

func TestFlagsClient_JsonFlag(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.runtime.mu.Lock()
	fc.runtime.connected = true
	fc.runtime.mu.Unlock()
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

func TestFlagsClient_Register(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.Register(context.Background(), Context{Type: "user", Key: "u1"})
	assert.Equal(t, 1, fc.runtime.contextBuffer.pendingCount())
}

func TestFlagsClient_FlushContexts_Empty(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	// Should not panic
	fc.FlushContexts(context.Background())
}

func TestFlagsClient_Evaluate_ConnectedWithStore(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)

	// Set up connected state with a flag in the store
	fc.runtime.mu.Lock()
	fc.runtime.connected = true
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

// --- Typed flag handle GetWithContext ---

func TestBoolFlagHandle_GetWithContext(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.mu.Unlock()
	handle := rt.BoolFlag("feature", false)
	assert.Equal(t, false, handle.GetWithContext(context.Background(), nil))
}

func TestStringFlagHandle_GetWithContext(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.mu.Unlock()
	handle := rt.StringFlag("theme", "light")
	assert.Equal(t, "light", handle.GetWithContext(context.Background(), nil))
}

func TestNumberFlagHandle_GetWithContext(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.mu.Unlock()
	handle := rt.NumberFlag("retries", 5.0)
	assert.Equal(t, 5.0, handle.GetWithContext(context.Background(), nil))
}

func TestJsonFlagHandle_GetWithContext(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.mu.Unlock()
	dflt := map[string]interface{}{"a": "b"}
	handle := rt.JsonFlag("config", dflt)
	assert.Equal(t, dflt, handle.GetWithContext(context.Background(), nil))
}

// --- NumberFlagHandle type coercion ---

func TestNumberFlagHandle_GetInt(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
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

func TestNumberFlagHandle_GetWithContext_Int(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.mu.Unlock()
	handle := rt.NumberFlag("retries", 5.0)
	assert.Equal(t, 5.0, handle.GetWithContext(context.Background(), nil))
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
	rule := NewRule("empty rule").Serve(true).Build()
	logic := rule["logic"].(map[string]interface{})
	assert.Len(t, logic, 0)
}

func TestRule_SingleCondition(t *testing.T) {
	rule := NewRule("single").When("user.plan", "==", "enterprise").Serve(true).Build()
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

func sampleFlagResponseJSON(id, key, name, flagType string) string {
	return `{
		"data": {
			"id": "` + id + `",
			"type": "flag",
			"attributes": {
				"name": "` + name + `",
				"key": "` + key + `",
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

func sampleFlagListResponseJSON(id, key, name, flagType string) string {
	return `{
		"data": [{
			"id": "` + id + `",
			"type": "flag",
			"attributes": {
				"name": "` + name + `",
				"key": "` + key + `",
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

const testFlagUUID = "660e8400-e29b-41d4-a716-446655440000"

func TestFlagsClient_Get_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/flags/"+testFlagUUID {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	flag, err := fc.Get(context.Background(), testFlagUUID)
	require.NoError(t, err)
	assert.Equal(t, testFlagUUID, flag.ID)
	assert.Equal(t, "feature-x", flag.Key)
	assert.Equal(t, "Feature X", flag.Name)
	assert.Equal(t, "BOOLEAN", flag.Type)
	assert.Equal(t, true, flag.Default)
}

func TestFlagsClient_Get_NotFound(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	}))

	_, err := fc.Get(context.Background(), testFlagUUID)
	assert.Error(t, err)
}

func TestFlagsClient_Get_InvalidUUID(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	_, err := fc.Get(context.Background(), "not-a-uuid")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid flag ID")
}

func TestFlagsClient_Create_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/flags" {
			b, _ := io.ReadAll(r.Body)
			var req map[string]interface{}
			_ = json.Unmarshal(b, &req)
			data := req["data"].(map[string]interface{})
			attrs := data["attributes"].(map[string]interface{})
			assert.Equal(t, "feature-x", attrs["key"])

			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	flag, err := fc.Create(context.Background(), CreateFlagParams{
		Key:     "feature-x",
		Name:    "Feature X",
		Type:    FlagTypeBoolean,
		Default: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "feature-x", flag.Key)
}

func TestFlagsClient_Create_NonBooleanNoAutoValues(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagUUID, "color", "Color", "STRING")))
	}))

	flag, err := fc.Create(context.Background(), CreateFlagParams{
		Key:     "color",
		Name:    "Color",
		Type:    FlagTypeString,
		Default: "red",
		Values:  []FlagValue{{Name: "Red", Value: "red"}, {Name: "Blue", Value: "blue"}},
	})
	require.NoError(t, err)
	assert.NotNil(t, flag)
}

func TestFlagsClient_List_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/flags" {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleFlagListResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	flags, err := fc.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, flags, 1)
	assert.Equal(t, "feature-x", flags[0].Key)
}

func TestFlagsClient_Delete_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/flags/"+testFlagUUID {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	err := fc.Delete(context.Background(), testFlagUUID)
	assert.NoError(t, err)
}

func TestFlagsClient_Delete_InvalidUUID(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	err := fc.Delete(context.Background(), "invalid")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid flag ID")
}

func TestFlagsClient_UpdateFlag_Success(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/v1/flags/"+testFlagUUID {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagUUID, "feature-x", "Updated Name", "BOOLEAN")))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	flag := &Flag{
		ID:           testFlagUUID,
		Key:          "feature-x",
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       []FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{},
		client:       fc,
	}

	newName := "Updated Name"
	updated, err := fc.updateFlag(context.Background(), flag, UpdateFlagParams{Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", updated.Name)
}

func TestFlagsClient_UpdateFlag_InvalidUUID(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	flag := &Flag{ID: "invalid", client: fc}
	_, err := fc.updateFlag(context.Background(), flag, UpdateFlagParams{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid flag ID")
}

func TestFlagsClient_UpdateFlag_WithAllParams(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
	}))

	flag := &Flag{
		ID:           testFlagUUID,
		Key:          "feature-x",
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       []FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{},
		client:       fc,
	}

	newName := "New Name"
	newDesc := "New Description"
	_, err := fc.updateFlag(context.Background(), flag, UpdateFlagParams{
		Name:        &newName,
		Description: &newDesc,
		Default:     false,
		Values:      []FlagValue{{Name: "True", Value: true}, {Name: "False", Value: false}},
		Environments: map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
			},
		},
	})
	require.NoError(t, err)
}

// --- Context management methods (via doJSONApp) ---

// These tests exercise the actual context management methods that use doJSONApp,
// which now routes to the test server when baseURL is overridden.

func TestFlagsClient_CreateContextType_FullMethod(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/context_types" {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"ct-1","attributes":{"key":"user","name":"User","attributes":{}}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	ct, err := fc.CreateContextType(context.Background(), "user", "User")
	require.NoError(t, err)
	assert.Equal(t, "ct-1", ct.ID)
	assert.Equal(t, "user", ct.Key)
}

func TestFlagsClient_UpdateContextType_FullMethod(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/v1/context_types/ct-1" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"ct-1","attributes":{"key":"user","name":"User","attributes":{"plan":"string"}}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	ct, err := fc.UpdateContextType(context.Background(), "ct-1", map[string]interface{}{"plan": "string"})
	require.NoError(t, err)
	assert.Equal(t, "string", ct.Attributes["plan"])
}

func TestFlagsClient_ListContextTypes_FullMethod(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/context_types" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[
				{"id":"ct-1","attributes":{"key":"user","name":"User","attributes":{}}},
				{"id":"ct-2","attributes":{"key":"account","name":"Account","attributes":{}}}
			]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	types, err := fc.ListContextTypes(context.Background())
	require.NoError(t, err)
	assert.Len(t, types, 2)
	assert.Equal(t, "user", types[0].Key)
}

func TestFlagsClient_DeleteContextType_FullMethod(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/context_types/ct-1" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	err := fc.DeleteContextType(context.Background(), "ct-1")
	assert.NoError(t, err)
}

func TestFlagsClient_ListContexts_FullMethod(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/contexts" {
			assert.Contains(t, r.URL.RawQuery, "filter[context_type]=user")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"ctx-1","type":"context","attributes":{"key":"u1"}}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	contexts, err := fc.ListContexts(context.Background(), "user")
	require.NoError(t, err)
	assert.Len(t, contexts, 1)
}

func TestFlagsClient_FlushContexts_FullMethod(t *testing.T) {
	var receivedPayload map[string]interface{}
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/v1/contexts/bulk" {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &receivedPayload)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	batch := []map[string]interface{}{{"id": "user:u1", "name": "User 1"}}
	fc.flushContexts(context.Background(), batch)

	require.NotNil(t, receivedPayload)
	contexts := receivedPayload["contexts"].([]interface{})
	assert.Len(t, contexts, 1)
}

// --- fetchAllFlags / fetchFlagsList ---

func TestFlagsClient_FetchAllFlags(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
	}))

	store, err := fc.fetchAllFlags(context.Background())
	require.NoError(t, err)
	assert.Contains(t, store, "feature-x")
}

func TestFlagsClient_FetchFlagsList(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
	}))

	flags, err := fc.fetchFlagsList(context.Background())
	require.NoError(t, err)
	assert.Len(t, flags, 1)
	assert.Equal(t, "feature-x", flags[0]["key"])
}

// --- FlagsRuntime Connect / Disconnect / Refresh ---

func TestFlagsRuntime_Connect(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
	}))

	err := fc.connectInternal(context.Background(), "production")
	require.NoError(t, err)

	// Should be connected with flag in store
	fc.runtime.mu.RLock()
	assert.True(t, fc.runtime.connected)
	assert.Equal(t, "production", fc.runtime.environment)
	_, ok := fc.runtime.flagStore["feature-x"]
	assert.True(t, ok)
	fc.runtime.mu.RUnlock()

	// Clean up
	fc.Disconnect(context.Background())
	fc.client.stopWS()
}

func TestFlagsRuntime_Disconnect(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))

	_ = fc.connectInternal(context.Background(), "staging")
	fc.Disconnect(context.Background())

	fc.runtime.mu.RLock()
	assert.False(t, fc.runtime.connected)
	assert.Equal(t, "", fc.runtime.environment)
	fc.runtime.mu.RUnlock()

	fc.client.stopWS()
}

func TestFlagsRuntime_Refresh(t *testing.T) {
	callCount := 0
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
	}))

	// Manually set connected
	fc.runtime.mu.Lock()
	fc.runtime.connected = true
	fc.runtime.environment = "production"
	fc.runtime.mu.Unlock()

	err := fc.Refresh(context.Background())
	require.NoError(t, err)
	assert.True(t, callCount > 0)
}

// --- FlagsRuntime Evaluate (Tier 1 explicit) ---

func TestFlagsRuntime_Evaluate_NotConnected_Fetches(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{
			"id": "` + testFlagUUID + `",
			"type": "flag",
			"attributes": {
				"key": "feature",
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
		_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagUUID, "feature-x", "Updated", "BOOLEAN")))
	}))

	flag := &Flag{
		ID:           testFlagUUID,
		Key:          "feature-x",
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       []FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{},
		client:       fc,
	}

	err := flag.Update(context.Background(), UpdateFlagParams{})
	require.NoError(t, err)
	assert.Equal(t, "Updated", flag.Name)
}

func TestFlag_AddRule_Success(t *testing.T) {
	callCount := 0
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
	}))

	flag := &Flag{
		ID:           testFlagUUID,
		Key:          "feature-x",
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       []FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{},
		client:       fc,
	}

	rule := NewRule("test").
		Environment("production").
		When("user.plan", "==", "enterprise").
		Serve(true).
		Build()

	err := flag.AddRule(context.Background(), rule)
	require.NoError(t, err)
	// Should have called Get (to refresh) then updateFlag
	assert.True(t, callCount >= 2)
}

func TestFlag_AddRule_MissingEnvironment(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	flag := &Flag{ID: testFlagUUID, client: fc}

	err := flag.AddRule(context.Background(), map[string]interface{}{
		"logic": map[string]interface{}{},
		"value": true,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "environment")
}

// --- doJSONWithBase error paths ---

func TestDoJSONWithBase_NetworkError(t *testing.T) {
	httpClient := &http.Client{Transport: &failingTransport{}}
	c := &Client{
		apiKey:     "sk_test",
		baseURL:    "http://localhost:1",
		httpClient: httpClient,
	}
	fc := &FlagsClient{client: c}

	_, _, err := fc.doJSONWithBase(context.Background(), "GET", "http://localhost:1/api/v1/test", nil)
	assert.Error(t, err)
}

type failingTransport struct{}

func (t *failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, assert.AnError
}

// --- FlagsRuntime ConnectionStatus with wsManager ---

func TestFlagsRuntime_ConnectionStatus_WithWSManager(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	ws.setStatus("connected")

	rt := newFlagsRuntime(nil)
	rt.wsManager = ws
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
		_, _ = w.Write([]byte(`{"data":[{
			"id": "` + testFlagUUID + `",
			"type": "flag",
			"attributes": {
				"key": "feature-x",
				"name": "Feature X",
				"type": "BOOLEAN",
				"default": false,
				"values": [],
				"environments": {}
			}
		}]}`))
	}))

	var changeEvent *FlagChangeEvent
	fc.OnChange(func(e *FlagChangeEvent) {
		changeEvent = e
	})

	fc.runtime.handleFlagChanged(map[string]interface{}{"key": "feature-x"})

	assert.NotNil(t, changeEvent)
	assert.Equal(t, "feature-x", changeEvent.Key)
	assert.Equal(t, "websocket", changeEvent.Source)
}

func TestFlagsRuntime_HandleFlagDeleted(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))

	fc.runtime.handleFlagDeleted(map[string]interface{}{"key": "deleted-flag"})
	// handleFlagDeleted delegates to handleFlagChanged
}

// --- FlagsClient flushContexts with actual data ---

func TestFlagsClient_FlushContexts_Lifecycle(t *testing.T) {
	var received bool
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/v1/contexts/bulk" {
			received = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))

	fc.Register(context.Background(), Context{Type: "user", Key: "u1"})
	// Use the direct doJSONWithBase to route through test server
	batch := fc.runtime.contextBuffer.drain()
	if len(batch) > 0 {
		_, _, _ = fc.doJSONWithBase(context.Background(), "PUT", fc.client.baseURL+"/api/v1/contexts/bulk",
			map[string]interface{}{"contexts": batch})
	}
	assert.True(t, received)
}

// --- BoolFlagHandle type mismatch ---

func TestBoolFlagHandle_Get_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default":      "not-a-bool",
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.BoolFlag("feature", false)
	result := handle.Get(context.Background())
	assert.Equal(t, false, result) // falls back to default
}

func TestStringFlagHandle_Get_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
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
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
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
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
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
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
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
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
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

func TestNumberFlagHandle_GetWithContext_IntValue(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"count": {
			"default":      int(42),
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.NumberFlag("count", 0.0)
	result := handle.GetWithContext(context.Background(), nil)
	assert.Equal(t, 42.0, result)
}

func TestNumberFlagHandle_GetWithContext_Int64Value(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"count": {
			"default":      int64(99),
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.NumberFlag("count", 0.0)
	result := handle.GetWithContext(context.Background(), nil)
	assert.Equal(t, 99.0, result)
}

// --- Error path tests for full coverage ---

func TestFlagsClient_Get_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"internal error"}]}`))
	}))

	_, err := fc.Get(context.Background(), testFlagUUID)
	assert.Error(t, err)
}

func TestFlagsClient_Get_InvalidJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))

	_, err := fc.Get(context.Background(), testFlagUUID)
	assert.Error(t, err)
}

func TestFlagsClient_Create_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"bad request"}]}`))
	}))

	_, err := fc.Create(context.Background(), CreateFlagParams{
		Key: "x", Name: "X", Type: FlagTypeBoolean, Default: true,
	})
	assert.Error(t, err)
}

func TestFlagsClient_Create_InvalidJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`not json`))
	}))

	_, err := fc.Create(context.Background(), CreateFlagParams{
		Key: "x", Name: "X", Type: FlagTypeBoolean, Default: true,
	})
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

	err := fc.Delete(context.Background(), testFlagUUID)
	assert.Error(t, err)
}

func TestFlagsClient_UpdateFlag_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"bad request"}]}`))
	}))

	flag := &Flag{ID: testFlagUUID, Key: "x", Type: "BOOLEAN", Default: true, Values: []FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	_, err := fc.updateFlag(context.Background(), flag, UpdateFlagParams{})
	assert.Error(t, err)
}

func TestFlagsClient_UpdateFlag_InvalidJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))

	flag := &Flag{ID: testFlagUUID, Key: "x", Type: "BOOLEAN", Default: true, Values: []FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	_, err := fc.updateFlag(context.Background(), flag, UpdateFlagParams{})
	assert.Error(t, err)
}

func TestFlagsClient_CreateContextType_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"bad request"}]}`))
	}))

	_, err := fc.CreateContextType(context.Background(), "user", "User")
	assert.Error(t, err)
}

func TestFlagsClient_UpdateContextType_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"bad request"}]}`))
	}))

	_, err := fc.UpdateContextType(context.Background(), "ct-1", map[string]interface{}{})
	assert.Error(t, err)
}

func TestFlagsClient_ListContextTypes_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"forbidden"}]}`))
	}))

	_, err := fc.ListContextTypes(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_ListContextTypes_InvalidJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))

	_, err := fc.ListContextTypes(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_DeleteContextType_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"forbidden"}]}`))
	}))

	err := fc.DeleteContextType(context.Background(), "ct-1")
	assert.Error(t, err)
}

func TestFlagsClient_ListContexts_ServerError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"forbidden"}]}`))
	}))

	_, err := fc.ListContexts(context.Background(), "user")
	assert.Error(t, err)
}

func TestFlagsClient_ListContexts_InvalidJSON(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))

	_, err := fc.ListContexts(context.Background(), "user")
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

// --- GetWithContext type coercion and mismatch ---

func TestBoolFlagHandle_GetWithContext_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default":      "not-a-bool",
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.BoolFlag("feature", true)
	result := handle.GetWithContext(context.Background(), nil)
	assert.Equal(t, true, result)
}

func TestStringFlagHandle_GetWithContext_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"theme": {
			"default":      42,
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.StringFlag("theme", "dark")
	result := handle.GetWithContext(context.Background(), nil)
	assert.Equal(t, "dark", result)
}

func TestNumberFlagHandle_GetWithContext_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"retries": {
			"default":      "not-a-number",
			"environments": map[string]interface{}{},
		},
	}
	rt.mu.Unlock()

	handle := rt.NumberFlag("retries", 7.0)
	result := handle.GetWithContext(context.Background(), nil)
	assert.Equal(t, 7.0, result)
}

func TestJsonFlagHandle_GetWithContext_TypeMismatch(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
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
	result := handle.GetWithContext(context.Background(), nil)
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
	fc.runtime.mu.Lock()
	fc.runtime.connected = true
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

	fc.runtime.mu.Lock()
	fc.runtime.connected = true
	fc.runtime.mu.Unlock()

	err := fc.Refresh(context.Background())
	assert.Error(t, err)
}

// --- Flag.Update error ---

func TestFlag_Update_Error(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	flag := &Flag{ID: testFlagUUID, Key: "x", Type: "BOOLEAN", Default: true, Values: []FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	err := flag.Update(context.Background(), UpdateFlagParams{})
	assert.Error(t, err)
}

// --- Flag.AddRule error paths ---

func TestFlag_AddRule_GetError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	flag := &Flag{ID: testFlagUUID, Key: "x", Type: "BOOLEAN", Default: true, Values: []FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	rule := NewRule("test").Environment("prod").Serve(true).Build()
	err := flag.AddRule(context.Background(), rule)
	assert.Error(t, err)
}

func TestFlag_AddRule_NewEnvironment(t *testing.T) {
	callCount := 0
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
	}))

	flag := &Flag{
		ID:           testFlagUUID,
		Key:          "feature-x",
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       []FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{}, // No "staging" env
		client:       fc,
	}

	rule := NewRule("test").
		Environment("staging"). // New environment
		When("user.plan", "==", "enterprise").
		Serve(true).
		Build()

	err := flag.AddRule(context.Background(), rule)
	require.NoError(t, err)
}

func TestFlag_AddRule_ExistingEnvironment(t *testing.T) {
	callCount := 0
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
	}))

	flag := &Flag{
		ID:   testFlagUUID,
		Key:  "feature-x",
		Name: "Feature X",
		Type: "BOOLEAN",
		Environments: map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"rules":   []interface{}{},
			},
		},
		Values: []FlagValue{{Name: "True", Value: true}},
		client: fc,
	}

	rule := NewRule("test").
		Environment("production").
		Serve(true).
		Build()

	err := flag.AddRule(context.Background(), rule)
	require.NoError(t, err)
}

// --- FlagsRuntime handleFlagChanged fetch error ---

func TestFlagsRuntime_HandleFlagChanged_FetchError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	// Should not panic
	fc.runtime.handleFlagChanged(map[string]interface{}{"key": "feature-x"})
}

// --- FlagsRuntime evaluateHandle provider flush threshold ---

func TestFlagsRuntime_EvaluateHandle_ProviderFlushThreshold(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rt := fc.runtime
	rt.mu.Lock()
	rt.connected = true
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

	fc.runtime.mu.Lock()
	fc.runtime.connected = true
	fc.runtime.environment = "test"
	fc.runtime.mu.Unlock()

	// Disconnect without ever having connected (no wsManager)
	fc.Disconnect(context.Background())

	fc.runtime.mu.RLock()
	assert.False(t, fc.runtime.connected)
	fc.runtime.mu.RUnlock()
}

// --- ws.go run backoff cap ---

func TestSharedWebSocket_Run_BackoffCap(t *testing.T) {
	var mu sync.Mutex
	var connectCount int

	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	ws.dialWS = func(url string) (*websocket.Conn, error) {
		mu.Lock()
		connectCount++
		mu.Unlock()
		return nil, assert.AnError
	}

	go ws.run()

	// Wait for several backoff cycles
	time.Sleep(2500 * time.Millisecond)

	ws.closeOnce.Do(func() {
		close(ws.closeCh)
	})
	<-ws.wsDone

	mu.Lock()
	assert.True(t, connectCount >= 2)
	mu.Unlock()
}

// --- ws.go run: closeCh already closed before loop starts ---

func TestSharedWebSocket_Run_ClosedBeforeLoop(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	ws.initBackoff = time.Millisecond
	ws.maxBackoff = time.Millisecond
	// Close the channel before run() starts — exercises the top-of-loop select
	close(ws.closeCh)
	go ws.run()
	<-ws.wsDone
	assert.Equal(t, "disconnected", ws.connectionStatus())
}

// --- ws.go run: closeCh signaled during backoff select ---

func TestSharedWebSocket_Run_ClosedDuringBackoff(t *testing.T) {
	var mu sync.Mutex
	connectCount := 0
	ws := newSharedWebSocket("https://app.smplkit.com", "test")
	ws.initBackoff = 500 * time.Millisecond
	ws.maxBackoff = 500 * time.Millisecond
	ws.dialWS = func(url string) (*websocket.Conn, error) {
		mu.Lock()
		connectCount++
		mu.Unlock()
		return nil, assert.AnError
	}

	go ws.run()
	// Wait for first connect to fail and backoff to start
	time.Sleep(100 * time.Millisecond)
	// Signal close during the backoff select
	ws.closeOnce.Do(func() { close(ws.closeCh) })
	<-ws.wsDone
	assert.Equal(t, "disconnected", ws.connectionStatus())
}

// --- FlagsRuntime Connect error ---

func TestFlagsRuntime_Connect_FetchError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"error"}]}`))
	}))

	err := fc.connectInternal(context.Background(), "production")
	assert.Error(t, err)
}

// --- helpers for test infrastructure ---

func flagResource(id, flagType, key, name, vType string, dflt interface{}, desc string, created time.Time) genflags.FlagResource {
	return genflags.FlagResource{
		Id:   &id,
		Type: flagType,
		Attributes: genflags.Flag{
			Key:          key,
			Name:         name,
			Type:         vType,
			Default:      dflt,
			Values:       []genflags.FlagValue{{Name: "True", Value: true}},
			Description:  &desc,
			Environments: nil,
			CreatedAt:    &created,
		},
	}
}

func flagResourceNoID(key, name, vType string, dflt interface{}, created time.Time) genflags.FlagResource {
	return genflags.FlagResource{
		Id:   nil,
		Type: "flag",
		Attributes: genflags.Flag{
			Key:       key,
			Name:      name,
			Type:      vType,
			Default:   dflt,
			Values:    []genflags.FlagValue{},
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

	c := &Client{
		apiKey:     "sk_test",
		baseURL:    "http://localhost:1",
		httpClient: httpClient,
	}
	fc := &FlagsClient{client: c, generated: genFlagsClient}
	fc.runtime = newFlagsRuntime(fc)
	return fc
}

// --- Generated client network error paths ---

func TestFlagsClient_Get_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.Get(context.Background(), testFlagUUID)
	assert.Error(t, err)
}

func TestFlagsClient_Create_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.Create(context.Background(), CreateFlagParams{
		Key: "x", Name: "X", Type: FlagTypeBoolean, Default: true,
	})
	assert.Error(t, err)
}

func TestFlagsClient_List_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.List(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_Delete_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	err := fc.Delete(context.Background(), testFlagUUID)
	assert.Error(t, err)
}

func TestFlagsClient_UpdateFlag_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	flag := &Flag{ID: testFlagUUID, Key: "x", Type: "BOOLEAN", Default: true, Values: []FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	_, err := fc.updateFlag(context.Background(), flag, UpdateFlagParams{})
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
	_, err := fc.Get(context.Background(), testFlagUUID)
	assert.Error(t, err)
}

func TestFlagsClient_Create_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	_, err := fc.Create(context.Background(), CreateFlagParams{
		Key: "x", Name: "X", Type: FlagTypeBoolean, Default: true,
	})
	assert.Error(t, err)
}

func TestFlagsClient_List_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	_, err := fc.List(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_Delete_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	err := fc.Delete(context.Background(), testFlagUUID)
	assert.Error(t, err)
}

func TestFlagsClient_UpdateFlag_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	flag := &Flag{ID: testFlagUUID, Key: "x", Type: "BOOLEAN", Default: true, Values: []FlagValue{}, Environments: map[string]interface{}{}, client: fc}
	_, err := fc.updateFlag(context.Background(), flag, UpdateFlagParams{})
	assert.Error(t, err)
}

func TestFlagsClient_FetchFlagsList_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	_, err := fc.fetchFlagsList(context.Background())
	assert.Error(t, err)
}

// --- doJSONWithBase error paths ---

func TestDoJSONWithBase_MarshalError(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	// channels cannot be marshaled to JSON
	_, _, err := fc.doJSONWithBase(context.Background(), "POST", "http://localhost:1/test", map[string]interface{}{
		"bad": make(chan int),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "marshal")
}

func TestDoJSONWithBase_InvalidURL(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	// Invalid method triggers NewRequestWithContext error
	_, _, err := fc.doJSONWithBase(context.Background(), "INVALID METHOD WITH SPACE", "http://localhost:1/test", nil)
	assert.Error(t, err)
}

func TestDoJSONWithBase_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	_, _, err := fc.doJSONWithBase(context.Background(), "GET", "http://localhost:1/test", nil)
	assert.Error(t, err)
}

// --- Context management doJSONApp error paths ---

func TestFlagsClient_CreateContextType_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.CreateContextType(context.Background(), "user", "User")
	assert.Error(t, err)
}

func TestFlagsClient_UpdateContextType_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.UpdateContextType(context.Background(), "ct-1", map[string]interface{}{})
	assert.Error(t, err)
}

func TestFlagsClient_ListContextTypes_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.ListContextTypes(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_ListContextTypes_InvalidItem(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Valid outer JSON array, but item is a string not an object — parseContextTypeRaw will fail
		_, _ = w.Write([]byte(`{"data":["not an object"]}`))
	}))

	_, err := fc.ListContextTypes(context.Background())
	assert.Error(t, err)
}

func TestFlagsClient_DeleteContextType_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	err := fc.DeleteContextType(context.Background(), "ct-1")
	assert.Error(t, err)
}

func TestFlagsClient_ListContexts_NetworkError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})
	_, err := fc.ListContexts(context.Background(), "user")
	assert.Error(t, err)
}

// --- Flag.AddRule error path: updateFlag fails after successful Get ---

func TestFlag_AddRule_UpdateError(t *testing.T) {
	callCount := 0
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call: Get succeeds
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sampleFlagResponseJSON(testFlagUUID, "feature-x", "Feature X", "BOOLEAN")))
			return
		}
		// Second call: Update fails
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"update failed"}]}`))
	}))

	flag := &Flag{
		ID:           testFlagUUID,
		Key:          "feature-x",
		Name:         "Feature X",
		Type:         "BOOLEAN",
		Default:      true,
		Values:       []FlagValue{{Name: "True", Value: true}},
		Environments: map[string]interface{}{},
		client:       fc,
	}

	rule := NewRule("test").
		Environment("production").
		When("user.plan", "==", "enterprise").
		Serve(true).
		Build()

	err := flag.AddRule(context.Background(), rule)
	assert.Error(t, err)
}

// --- Flag.AddRule existing env with rules (else branch) ---

func TestFlag_AddRule_ExistingEnvWithRules(t *testing.T) {
	callCount := 0
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		// Return a flag with an existing production env containing rules
		_, _ = w.Write([]byte(`{
			"data": {
				"id": "` + testFlagUUID + `",
				"type": "flag",
				"attributes": {
					"name": "Feature X",
					"key": "feature-x",
					"type": "BOOLEAN",
					"default": true,
					"values": [{"name": "True", "value": true}],
					"environments": {
						"production": {
							"enabled": true,
							"rules": [{"logic": {}, "value": false}]
						}
					},
					"created_at": "2024-01-01T00:00:00Z",
					"updated_at": "2024-06-15T12:00:00Z"
				}
			}
		}`))
	}))

	flag := &Flag{
		ID:   testFlagUUID,
		Key:  "feature-x",
		Name: "Feature X",
		Type: "BOOLEAN",
		Environments: map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"rules":   []interface{}{map[string]interface{}{"logic": map[string]interface{}{}, "value": false}},
			},
		},
		Values: []FlagValue{{Name: "True", Value: true}},
		client: fc,
	}

	rule := NewRule("new rule").
		Environment("production").
		When("user.plan", "==", "enterprise").
		Serve(true).
		Build()

	err := flag.AddRule(context.Background(), rule)
	require.NoError(t, err)
}

// --- FlagsRuntime Evaluate fetch error path ---

func TestFlagsRuntime_Evaluate_FetchError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &failingTransport{})

	// Not connected — will try to fetch
	result := fc.Evaluate(context.Background(), "feature", "production", nil)
	assert.Nil(t, result)
}

// --- ws.go connect with nil dialWS ---

func TestSharedWebSocket_Connect_NilDialWS(t *testing.T) {
	ws := &sharedWebSocket{
		appBaseURL: "https://unreachable.example.com",
		apiKey:     "test",
		listeners:  make(map[string][]eventCallback),
		status:     "disconnected",
		closeCh:    make(chan struct{}),
		wsDone:     make(chan struct{}),
		dialWS:     nil, // nil — should use defaultDialWS fallback
	}
	// connect will fail because the URL is unreachable, but should not panic
	closed := ws.connect()
	assert.False(t, closed)
}

// --- ws.go run backoff exceeds maxBackoff ---

func TestSharedWebSocket_Run_BackoffExceedsMax(t *testing.T) {
	var mu sync.Mutex
	var connectCount int

	ws := &sharedWebSocket{
		appBaseURL:  "https://app.smplkit.com",
		apiKey:      "test",
		listeners:   make(map[string][]eventCallback),
		status:      "disconnected",
		closeCh:     make(chan struct{}),
		wsDone:      make(chan struct{}),
		initBackoff: 10 * time.Millisecond, // Start very small
		maxBackoff:  30 * time.Millisecond, // Cap at 30ms so we hit it after a few doubles
		dialWS: func(url string) (*websocket.Conn, error) {
			mu.Lock()
			connectCount++
			mu.Unlock()
			return nil, assert.AnError
		},
	}

	go ws.run()

	// With 10ms init and 30ms max: 10ms, 20ms, 30ms(cap), 30ms, ...
	// After ~100ms we should have several connects including hitting the cap
	time.Sleep(200 * time.Millisecond)

	ws.closeOnce.Do(func() {
		close(ws.closeCh)
	})
	<-ws.wsDone

	mu.Lock()
	assert.True(t, connectCount >= 3, "expected at least 3 connects (enough to cap backoff), got %d", connectCount)
	mu.Unlock()
}

// --- Service context auto-injection ---

func TestFlagsRuntime_ServiceContextAutoInjection(t *testing.T) {
	fc, _ := newTestFlagsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListResponseJSON(testFlagUUID, "feature", "Feature", "BOOLEAN")))
	}))

	// Set service on the client
	fc.client.service = "my-service"

	// Connect and set up a flag with a rule that checks service.key
	err := fc.connectInternal(context.Background(), "production")
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
	handle := fc.BoolFlag("feature", false)
	result := handle.Get(context.Background())
	assert.Equal(t, true, result, "service context should be auto-injected and match the rule")

	fc.Disconnect(context.Background())
	fc.client.stopWS()
}

func TestFlagsRuntime_ServiceContextNotOverridden(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	fc.client.service = "auto-service"

	fc.runtime.mu.Lock()
	fc.runtime.connected = true
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
	handle := fc.BoolFlag("feature", false)
	result := handle.GetWithContext(context.Background(), []Context{
		{Type: "service", Key: "custom-service"},
	})
	assert.Equal(t, true, result, "customer-provided service context should take precedence")

	fc.client.stopWS()
}
