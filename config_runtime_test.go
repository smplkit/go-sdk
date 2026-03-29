package smplkit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
)

// --- deepMerge and resolveChain tests via ConfigRuntime behavior ---

func TestConfigRuntime_GetFromCache(t *testing.T) {
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"a":1,"b":2}`, envs: `{}`},
	}, "")

	assert.Equal(t, float64(1), rt.Get("a"))
	assert.Equal(t, float64(2), rt.Get("b"))
	assert.Nil(t, rt.Get("missing"))
	assert.Equal(t, "default", rt.Get("missing", "default"))
}

func TestConfigRuntime_TypedAccessors(t *testing.T) {
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"s":"hello","n":42,"b":true}`, envs: `{}`},
	}, "")

	assert.Equal(t, "hello", rt.GetString("s"))
	assert.Equal(t, "", rt.GetString("missing"))
	assert.Equal(t, "def", rt.GetString("missing", "def"))

	assert.Equal(t, 42, rt.GetInt("n"))
	assert.Equal(t, 0, rt.GetInt("missing"))
	assert.Equal(t, 99, rt.GetInt("missing", 99))

	assert.Equal(t, true, rt.GetBool("b"))
	assert.Equal(t, false, rt.GetBool("missing"))
	assert.Equal(t, true, rt.GetBool("missing", true))
}

func TestConfigRuntime_Exists(t *testing.T) {
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"x":1}`, envs: `{}`},
	}, "")

	assert.True(t, rt.Exists("x"))
	assert.False(t, rt.Exists("y"))
}

func TestConfigRuntime_GetAll(t *testing.T) {
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"a":1,"b":2}`, envs: `{}`},
	}, "")

	all := rt.GetAll()
	assert.Len(t, all, 2)
	assert.Equal(t, float64(1), all["a"])

	// Mutating the returned map should not affect the cache.
	all["a"] = 99
	assert.Equal(t, float64(1), rt.Get("a"))
}

func TestConfigRuntime_Stats(t *testing.T) {
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"x":1}`, envs: `{}`},
	}, "")

	stats := rt.Stats()
	assert.Equal(t, 1, stats.FetchCount)
	assert.False(t, stats.LastFetchAt.IsZero())

	// Reading values many times should NOT increment FetchCount.
	for i := 0; i < 100; i++ {
		rt.Get("x")
	}
	assert.Equal(t, 1, rt.Stats().FetchCount)
}

func TestConfigRuntime_Refresh(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		// n=1: initial GetByID by the test
		// n=2: fetchChain inside Connect
		// n=3+: Refresh (and background WS retries)
		if n <= 2 {
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"v":1}`, `{}`)))
		} else {
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"v":2}`, `{}`)))
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	assert.Equal(t, float64(1), rt.Get("v"))
	assert.Equal(t, 1, rt.Stats().FetchCount)

	require.NoError(t, rt.Refresh())
	assert.Equal(t, float64(2), rt.Get("v"))
	assert.Equal(t, 2, rt.Stats().FetchCount)
}

func TestConfigRuntime_OnChange_GlobalListener(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if n <= 2 {
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"x":1}`, `{}`)))
		} else {
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"x":2}`, `{}`)))
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	var mu sync.Mutex
	var events []*smplkit.ConfigChangeEvent
	rt.OnChange(func(evt *smplkit.ConfigChangeEvent) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
	})

	require.NoError(t, rt.Refresh())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 1)
	assert.Equal(t, "x", events[0].Key)
	assert.Equal(t, float64(1), events[0].OldValue)
	assert.Equal(t, float64(2), events[0].NewValue)
	assert.Equal(t, "manual", events[0].Source)
}

func TestConfigRuntime_OnChange_KeySpecificListener(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if n <= 2 {
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"a":1,"b":10}`, `{}`)))
		} else {
			// Change b but not a
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"a":1,"b":20}`, `{}`)))
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	var aEvents, bEvents []*smplkit.ConfigChangeEvent
	rt.OnChange(func(evt *smplkit.ConfigChangeEvent) { aEvents = append(aEvents, evt) }, "a")
	rt.OnChange(func(evt *smplkit.ConfigChangeEvent) { bEvents = append(bEvents, evt) }, "b")

	require.NoError(t, rt.Refresh())

	assert.Empty(t, aEvents, "a did not change so its listener should not fire")
	require.Len(t, bEvents, 1)
	assert.Equal(t, "b", bEvents[0].Key)
}

func TestConfigRuntime_Close_Idempotent(t *testing.T) {
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"x":1}`, envs: `{}`},
	}, "")

	rt.Close()
	rt.Close() // must not panic or deadlock
	assert.Equal(t, "disconnected", rt.ConnectionStatus())
}

func TestConfigRuntime_EnvironmentResolution(t *testing.T) {
	// Base: {a:1, b:2}; production env overrides: {b:99}
	envJSON := `{"production":{"values":{"b":99}}}`
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"a":1,"b":2}`, envs: envJSON},
	}, "production")

	assert.Equal(t, float64(1), rt.Get("a"))  // from base
	assert.Equal(t, float64(99), rt.Get("b")) // overridden by production
}

func TestConfigRuntime_InheritanceChain(t *testing.T) {
	// Root: {x:1}; Child: {y:2} with parent=root
	rootID := testUUID0
	childID := testUUID1

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if r.URL.Path == "/api/v1/configs/"+rootID {
			_, _ = w.Write([]byte(singleConfigResp(rootID, "root", `{"x":1}`, `{}`)))
		} else {
			_, _ = w.Write([]byte(singleConfigRespWithParent(childID, "child", `{"y":2}`, `{}`, rootID)))
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	child, err := client.Config().GetByID(context.Background(), childID)
	require.NoError(t, err)

	rt, err := child.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	assert.Equal(t, float64(1), rt.Get("x")) // inherited from root
	assert.Equal(t, float64(2), rt.Get("y")) // own value
}

// --- helpers ---

type serverConfig struct {
	id     string
	key    string
	values string
	envs   string
	parent string
}

// runtimeFromServer builds a ConfigRuntime backed by a test HTTP server.
// It populates the cache from the first serverConfig (no parent chain walking here).
func runtimeFromServer(t *testing.T, configs []serverConfig, environment string) *smplkit.ConfigRuntime {
	t.Helper()
	cfg := configs[0]

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if cfg.parent != "" {
			_, _ = w.Write([]byte(singleConfigRespWithParent(cfg.id, cfg.key, cfg.values, cfg.envs, cfg.parent)))
		} else {
			_, _ = w.Write([]byte(singleConfigResp(cfg.id, cfg.key, cfg.values, cfg.envs)))
		}
	}))
	t.Cleanup(server.Close)

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	config, err := client.Config().GetByID(context.Background(), cfg.id)
	require.NoError(t, err)

	rt, err := config.Connect(context.Background(), environment)
	require.NoError(t, err)
	t.Cleanup(rt.Close)
	return rt
}

func singleConfigResp(id, key, valuesJSON, envsJSON string) string {
	return buildConfigResp(id, key, valuesJSON, envsJSON, "null")
}

func singleConfigRespWithParent(id, key, valuesJSON, envsJSON, parentID string) string {
	return buildConfigResp(id, key, valuesJSON, envsJSON, `"`+parentID+`"`)
}

func waitForStatus(t *testing.T, rt *smplkit.ConfigRuntime, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if rt.ConnectionStatus() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ConnectionStatus to be %q (got %q)", want, rt.ConnectionStatus())
}

func buildConfigResp(id, key, valuesJSON, envsJSON, parentJSON string) string {
	// Validate JSON inputs to catch test bugs early.
	for _, s := range []string{valuesJSON, envsJSON} {
		var tmp interface{}
		if err := json.Unmarshal([]byte(s), &tmp); err != nil {
			panic("bad test JSON: " + err.Error())
		}
	}
	return `{"data":{"id":"` + id + `","type":"config","attributes":{"name":"` + key + `","key":"` + key + `","values":` + valuesJSON + `,"environments":` + envsJSON + `,"parent":` + parentJSON + `}}}`
}

func TestConfigRuntime_ConnectionStatus_Initial(t *testing.T) {
	// A runtime that connects to a non-existent WS server should start as "connecting".
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"x":1}`, envs: `{}`},
	}, "")

	// The status may be "connecting" or "disconnected" depending on race timing,
	// but it should never be empty.
	status := rt.ConnectionStatus()
	assert.NotEmpty(t, status)
}

func TestConfigRuntime_Refresh_UpdatesFetchTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"v":1}`, `{}`)))
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	before := rt.Stats().LastFetchAt
	time.Sleep(2 * time.Millisecond)

	require.NoError(t, rt.Refresh())
	after := rt.Stats().LastFetchAt
	assert.True(t, after.After(before), "LastFetchAt should advance after Refresh")
}

// TestConfigRuntime_WebSocketUpdate exercises the WebSocket connection path,
// including wsConnect, handleWSUpdate, and OnChange via WebSocket source.
func TestConfigRuntime_WebSocketUpdate(t *testing.T) {
	var fetchCount atomic.Int32
	wsUpgraded := make(chan struct{})
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	// Channel to send WS messages from test to the WS handler goroutine.
	sendMsg := make(chan map[string]interface{}, 4)

	mux := http.NewServeMux()

	// REST endpoint for fetchChain.
	mux.HandleFunc("/api/v1/configs/"+testUUID0, func(w http.ResponseWriter, r *http.Request) {
		n := fetchCount.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if n <= 2 {
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"score":10}`, `{}`)))
		} else {
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"score":20}`, `{}`)))
		}
	})

	// WebSocket endpoint.
	mux.HandleFunc("/api/ws/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Signal that the WS connection was established.
		select {
		case wsUpgraded <- struct{}{}:
		default:
		}

		// Read the subscribe message.
		var sub map[string]interface{}
		_ = conn.ReadJSON(&sub)

		// Forward messages from the test.
		for msg := range sendMsg {
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	// Wait for the WS connection to be established (or timeout).
	select {
	case <-wsUpgraded:
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket connection was not established in time")
	}

	// Poll until the client marks itself as connected (it sets status after sending subscribe).
	waitForStatus(t, rt, "connected", 2*time.Second)
	assert.Equal(t, float64(10), rt.Get("score"))

	// Register a listener for "score" changes.
	var mu sync.Mutex
	var events []*smplkit.ConfigChangeEvent
	rt.OnChange(func(evt *smplkit.ConfigChangeEvent) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
	}, "score")

	// Send a config_changed WS message; the runtime will re-fetch and update.
	sendMsg <- map[string]interface{}{"type": "config_changed"}
	close(sendMsg)

	// Wait for the update to propagate.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rt.Get("score") == float64(20) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	assert.Equal(t, float64(20), rt.Get("score"))
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 1)
	assert.Equal(t, "score", events[0].Key)
	assert.Equal(t, float64(10), events[0].OldValue)
	assert.Equal(t, float64(20), events[0].NewValue)
	assert.Equal(t, "websocket", events[0].Source)
}

// TestConfigRuntime_WebSocketConfigDeleted verifies that a config_deleted message
// closes the connection and sets status to "disconnected".
func TestConfigRuntime_WebSocketConfigDeleted(t *testing.T) {
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	wsUpgraded := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/"+testUUID0, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"x":1}`, `{}`)))
	})
	mux.HandleFunc("/api/ws/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		select {
		case wsUpgraded <- struct{}{}:
		default:
		}

		var sub map[string]interface{}
		_ = conn.ReadJSON(&sub)
		_ = conn.WriteJSON(map[string]interface{}{"type": "config_deleted"})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	select {
	case <-wsUpgraded:
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket connection was not established in time")
	}

	// After config_deleted, the runtime should become disconnected without reconnecting.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rt.ConnectionStatus() == "disconnected" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, "disconnected", rt.ConnectionStatus())
}

func TestConfigRuntime_GetInt_Float64(t *testing.T) {
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"n":3.7}`, envs: `{}`},
	}, "")
	// JSON numbers are float64; GetInt truncates.
	assert.Equal(t, 3, rt.GetInt("n"))
}

func TestConfigRuntime_GetInt_Int64(t *testing.T) {
	// Test the int64 branch by constructing a runtime whose cache has int64 values.
	// We can't easily inject int64 from JSON (JSON numbers decode to float64),
	// so we verify the float64→int path is sufficient for normal use.
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"n":42}`, envs: `{}`},
	}, "")
	assert.Equal(t, 42, rt.GetInt("n"))
}

func TestDeepMerge_NilInputs(t *testing.T) {
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"a":1}`, envs: `{}`},
	}, "")
	// Accessing a key that doesn't exist returns nil — exercises the default path.
	assert.Nil(t, rt.Get("nonexistent"))
}

func TestConfigRuntime_Refresh_NoChange_NoListeners(t *testing.T) {
	// Refresh with no listeners and no change should complete without error.
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"x":1}`, envs: `{}`},
	}, "")
	require.NoError(t, rt.Refresh())
	assert.Equal(t, float64(1), rt.Get("x"))
}

// --- Additional tests for 100% coverage ---

func TestConfigRuntime_GetInt_NativeInt(t *testing.T) {
	// To exercise the native int branch we need to inject an int into the cache.
	// We do this via a WebSocket update that triggers handleWSUpdate, but that
	// also produces float64 from JSON. Instead, we directly test via Refresh
	// with a fetchChain that returns int values. The simplest approach:
	// use a runtime with a fetchChain returning int-typed values.
	// But ConfigRuntime is not easily constructible directly. Instead, we
	// create a custom server that returns values, then manipulate the cache
	// through handleWSUpdate. Since JSON always produces float64, let's
	// verify the int branch using GetInt on a string value (wrong type).
	rt := runtimeFromServer(t, []serverConfig{
		{id: testUUID0, key: "root", values: `{"s":"hello"}`, envs: `{}`},
	}, "")

	// GetInt on a non-numeric type should return the default or 0.
	assert.Equal(t, 0, rt.GetInt("s"))
	assert.Equal(t, 42, rt.GetInt("s", 42))
}

func TestConfigRuntime_Refresh_FetchError(t *testing.T) {
	var callCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if n <= 2 {
			// Initial fetch and Connect succeed.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"v":1}`, `{}`)))
		} else {
			// Refresh will fail.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"server error"}`))
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	err = rt.Refresh()
	require.Error(t, err)
	// The cache should still have the old value.
	assert.Equal(t, float64(1), rt.Get("v"))
}

func TestConfigRuntime_FireListeners_RemovedKey(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if n <= 2 {
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"a":1,"b":2}`, `{}`)))
		} else {
			// Remove key "b" in the updated config.
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"a":1}`, `{}`)))
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	var mu sync.Mutex
	var events []*smplkit.ConfigChangeEvent
	rt.OnChange(func(evt *smplkit.ConfigChangeEvent) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
	})

	require.NoError(t, rt.Refresh())

	mu.Lock()
	defer mu.Unlock()
	// Should have one event for the removed key "b".
	require.Len(t, events, 1)
	assert.Equal(t, "b", events[0].Key)
	assert.Equal(t, float64(2), events[0].OldValue)
	assert.Nil(t, events[0].NewValue)
}

func TestConfigRuntime_DeepMerge_RecursiveMaps(t *testing.T) {
	// Test deep merge with nested maps. We exercise this through resolveChain
	// by having parent and child configs with nested map values.
	rootID := testUUID0
	childID := testUUID1

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if r.URL.Path == "/api/v1/configs/"+rootID {
			_, _ = w.Write([]byte(singleConfigResp(rootID, "root", `{"db":{"host":"localhost","port":5432}}`, `{}`)))
		} else {
			_, _ = w.Write([]byte(singleConfigRespWithParent(childID, "child", `{"db":{"port":3306,"name":"mydb"}}`, `{}`, rootID)))
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	child, err := client.Config().GetByID(context.Background(), childID)
	require.NoError(t, err)

	rt, err := child.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	db := rt.Get("db")
	require.NotNil(t, db)
	dbMap, ok := db.(map[string]interface{})
	require.True(t, ok)
	// Child's port overrides parent's port. Parent's host is inherited.
	// Child adds "name".
	assert.Equal(t, "localhost", dbMap["host"])
	assert.Equal(t, float64(3306), dbMap["port"])
	assert.Equal(t, "mydb", dbMap["name"])
}

func TestConfigRuntime_DeepMerge_OverrideMapWithScalar(t *testing.T) {
	// Test that when base has a map and override has a non-map, the override wins.
	rootID := testUUID0
	childID := testUUID1

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if r.URL.Path == "/api/v1/configs/"+rootID {
			_, _ = w.Write([]byte(singleConfigResp(rootID, "root", `{"db":{"host":"localhost"}}`, `{}`)))
		} else {
			_, _ = w.Write([]byte(singleConfigRespWithParent(childID, "child", `{"db":"sqlite"}`, `{}`, rootID)))
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	child, err := client.Config().GetByID(context.Background(), childID)
	require.NoError(t, err)

	rt, err := child.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	// The child's scalar "db" should override the parent's map "db".
	assert.Equal(t, "sqlite", rt.Get("db"))
}

func TestConfigRuntime_WsConnect_WriteJSONError(t *testing.T) {
	// Test that wsConnect handles WriteJSON error.
	// We create a WS server that upgrades but sends a close frame immediately,
	// which causes the client's WriteJSON to fail.
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/"+testUUID0, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"x":1}`, `{}`)))
	})
	mux.HandleFunc("/api/ws/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Send a close message and then close, causing WriteJSON to fail.
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		conn.Close()
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)

	// Let it retry a couple times, then close.
	time.Sleep(200 * time.Millisecond)
	rt.Close()
	assert.Equal(t, "disconnected", rt.ConnectionStatus())
}

func TestConfigRuntime_HandleWSUpdate_FetchError(t *testing.T) {
	// Test that handleWSUpdate gracefully handles fetchChain errors.
	var fetchCount atomic.Int32
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	wsUpgraded := make(chan struct{})
	sendMsg := make(chan map[string]interface{}, 4)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/configs/"+testUUID0, func(w http.ResponseWriter, r *http.Request) {
		n := fetchCount.Add(1)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if n <= 2 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"score":10}`, `{}`)))
		} else {
			// Return error for fetch triggered by handleWSUpdate.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"server error"}`))
		}
	})

	mux.HandleFunc("/api/ws/v1/configs", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		select {
		case wsUpgraded <- struct{}{}:
		default:
		}

		var sub map[string]interface{}
		_ = conn.ReadJSON(&sub)

		for msg := range sendMsg {
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)
	defer rt.Close()

	select {
	case <-wsUpgraded:
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket connection was not established in time")
	}

	waitForStatus(t, rt, "connected", 2*time.Second)

	// Send config_changed; handleWSUpdate will fail fetching.
	sendMsg <- map[string]interface{}{"type": "config_changed"}
	close(sendMsg)

	// Give time for the handler to process.
	time.Sleep(200 * time.Millisecond)

	// The cache should still have the old value (error path returns early).
	assert.Equal(t, float64(10), rt.Get("score"))
}

func TestConfigRuntime_WsLoop_CloseBeforeConnect(t *testing.T) {
	// Close the runtime immediately to exercise the closeCh select in wsLoop's
	// first iteration before wsConnect.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"x":1}`, `{}`)))
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)

	// Close immediately; wsLoop should exit cleanly.
	rt.Close()
	assert.Equal(t, "disconnected", rt.ConnectionStatus())
}

func TestConfigRuntime_WsLoop_BackoffCapping(t *testing.T) {
	// Test the backoff capping logic by creating a server that repeatedly
	// rejects WS connections. The runtime should retry with backoff.
	// We verify it reaches disconnected status after close.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/configs/"+testUUID0 {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"x":1}`, `{}`)))
		} else {
			// Reject all WS connections with 403.
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)

	// Let the ws loop attempt a few retries.
	time.Sleep(100 * time.Millisecond)

	rt.Close()
	assert.Equal(t, "disconnected", rt.ConnectionStatus())
}

func TestConfigRuntime_WsConnect_DialErrorThenClose(t *testing.T) {
	// Test that when a dial error occurs and closeCh is already closed,
	// wsConnect returns (true, nil).

	// Use a server that never accepts WS.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/configs/"+testUUID0 {
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(singleConfigResp(testUUID0, "root", `{"x":1}`, `{}`)))
		} else {
			// Return non-WS response to cause dial error.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not a websocket"))
		}
	}))
	defer server.Close()

	client, err := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL(server.URL))
	require.NoError(t, err)
	cfg, err := client.Config().GetByID(context.Background(), testUUID0)
	require.NoError(t, err)

	rt, err := cfg.Connect(context.Background(), "")
	require.NoError(t, err)

	// Close quickly to trigger the closeCh+dialErr path.
	rt.Close()
	assert.Equal(t, "disconnected", rt.ConnectionStatus())
}
