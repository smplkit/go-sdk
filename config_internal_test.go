package smplkit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDerefMap_Nil(t *testing.T) {
	result := derefMap(nil)
	assert.Nil(t, result)
}

func TestDerefEnvs_Nil(t *testing.T) {
	result := derefEnvs(nil)
	assert.Nil(t, result)
}

func TestGetInt_NativeInt(t *testing.T) {
	rt := &ConfigRuntime{
		cache: map[string]interface{}{"n": int(42)},
	}
	assert.Equal(t, 42, rt.GetInt("n"))
}

func TestGetInt_Int64(t *testing.T) {
	rt := &ConfigRuntime{
		cache: map[string]interface{}{"n": int64(99)},
	}
	assert.Equal(t, 99, rt.GetInt("n"))
}

func TestWsLoop_ClosedBeforeConnect(t *testing.T) {
	rt := &ConfigRuntime{
		configID:    "test-id",
		environment: "test",
		cache:       map[string]interface{}{},
		status:      "connecting",
		closeCh:     make(chan struct{}),
		wsDone:      make(chan struct{}),
		fetchChain:  func() ([]chainEntry, error) { return nil, nil },
		apiKey:      "test",
		wsBase:      "ws://localhost:0",
		initBackoff: time.Second,
	}

	close(rt.closeCh)
	rt.wsLoop()

	assert.Equal(t, "disconnected", rt.status)
}

func TestWsLoop_ZeroBackoff(t *testing.T) {
	// Test that initBackoff=0 defaults to 1 second.
	rt := &ConfigRuntime{
		configID:    "test-id",
		environment: "test",
		cache:       map[string]interface{}{},
		status:      "connecting",
		closeCh:     make(chan struct{}),
		wsDone:      make(chan struct{}),
		fetchChain:  func() ([]chainEntry, error) { return nil, nil },
		apiKey:      "test",
		wsBase:      "ws://localhost:0",
		initBackoff: 0, // Should default to time.Second
		dialWS: func(url string) (*websocket.Conn, error) {
			return nil, fmt.Errorf("dial error")
		},
	}

	go rt.wsLoop()

	// Close quickly during the backoff wait.
	time.Sleep(50 * time.Millisecond)
	close(rt.closeCh)
	<-rt.wsDone

	assert.Equal(t, "disconnected", rt.status)
}

func TestWsLoop_BackoffWaitAndClose(t *testing.T) {
	// Test the close-during-backoff path.
	rt := &ConfigRuntime{
		configID:    "test-id",
		environment: "test",
		cache:       map[string]interface{}{},
		status:      "connecting",
		closeCh:     make(chan struct{}),
		wsDone:      make(chan struct{}),
		fetchChain:  func() ([]chainEntry, error) { return nil, nil },
		apiKey:      "test",
		wsBase:      "ws://127.0.0.1:1",
		initBackoff: time.Second,
	}

	go rt.wsLoop()

	// Wait for the first dial to fail and the loop to enter backoff.
	time.Sleep(200 * time.Millisecond)

	close(rt.closeCh)
	<-rt.wsDone

	assert.Equal(t, "disconnected", rt.status)
}

func TestWsLoop_BackoffTimerFiresAndRetries(t *testing.T) {
	// Test that the backoff timer fires, the loop retries, and we get multiple
	// dial attempts.
	var dialCount int32
	var mu sync.Mutex

	rt := &ConfigRuntime{
		configID:    "test-id",
		environment: "test",
		cache:       map[string]interface{}{},
		status:      "connecting",
		closeCh:     make(chan struct{}),
		wsDone:      make(chan struct{}),
		fetchChain:  func() ([]chainEntry, error) { return nil, nil },
		apiKey:      "test",
		wsBase:      "ws://localhost:0",
		initBackoff: time.Millisecond,
		dialWS: func(url string) (*websocket.Conn, error) {
			mu.Lock()
			dialCount++
			mu.Unlock()
			return nil, fmt.Errorf("dial error")
		},
	}

	go rt.wsLoop()

	// With 1ms backoff doubling, we should get many attempts quickly.
	time.Sleep(300 * time.Millisecond)
	close(rt.closeCh)
	<-rt.wsDone

	mu.Lock()
	count := dialCount
	mu.Unlock()

	assert.True(t, count >= 3, "expected at least 3 dial attempts, got %d", count)
	assert.Equal(t, "disconnected", rt.status)
}

func TestWsLoop_BackoffCaps(t *testing.T) {
	// Test that the backoff caps at maxBackoff. Use initBackoff=5ms, maxBackoff=20ms.
	// Doubling: 5, 10, 20->20 (capped), 20, 20...
	// So after ~3 iterations (5+10+20=35ms) the cap kicks in.
	var mu sync.Mutex
	var dialCount int

	rt := &ConfigRuntime{
		configID:    "test-id",
		environment: "test",
		cache:       map[string]interface{}{},
		status:      "connecting",
		closeCh:     make(chan struct{}),
		wsDone:      make(chan struct{}),
		fetchChain:  func() ([]chainEntry, error) { return nil, nil },
		apiKey:      "test",
		wsBase:      "ws://localhost:0",
		initBackoff: 5 * time.Millisecond,
		maxBackoff:  8 * time.Millisecond, // 5 doubles to 10 > 8, triggers the inner cap
		dialWS: func(url string) (*websocket.Conn, error) {
			mu.Lock()
			dialCount++
			mu.Unlock()
			return nil, fmt.Errorf("dial error")
		},
	}

	go rt.wsLoop()

	// Wait for backoff doubling to exceed maxBackoff.
	// 5ms + 10ms + 20ms + 20ms + 20ms = 75ms. Wait 200ms to be safe.
	time.Sleep(200 * time.Millisecond)
	close(rt.closeCh)
	<-rt.wsDone

	mu.Lock()
	count := dialCount
	mu.Unlock()

	// Should have at least 4 dial attempts (enough for backoff to cap).
	assert.True(t, count >= 4, "expected at least 4 dial attempts, got %d", count)
	assert.Equal(t, "disconnected", rt.status)
}

func TestWsConnect_WriteJSONError(t *testing.T) {
	// Use a real WS server but inject a dialer that sets the write deadline
	// to the past, forcing WriteJSON to fail.
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Keep connection open for a bit so dial succeeds.
		time.Sleep(time.Second)
	}))
	defer server.Close()

	rt := &ConfigRuntime{
		configID:    "test-id",
		environment: "test",
		cache:       map[string]interface{}{},
		status:      "connecting",
		closeCh:     make(chan struct{}),
		wsDone:      make(chan struct{}),
		fetchChain:  func() ([]chainEntry, error) { return nil, nil },
		apiKey:      "test",
		wsBase:      toWSBase(server.URL),
		initBackoff: time.Millisecond,
		dialWS: func(wsURL string) (*websocket.Conn, error) {
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				return nil, err
			}
			// Set write deadline to the past so WriteJSON fails immediately.
			_ = conn.SetWriteDeadline(time.Now().Add(-time.Hour))
			return conn, nil
		},
	}

	closed, err := rt.wsConnect()
	assert.False(t, closed)
	assert.Error(t, err)
}

func TestWsConnect_DialError_CloseCh(t *testing.T) {
	rt := &ConfigRuntime{
		configID:    "test-id",
		environment: "test",
		cache:       map[string]interface{}{},
		status:      "connecting",
		closeCh:     make(chan struct{}),
		wsDone:      make(chan struct{}),
		fetchChain:  func() ([]chainEntry, error) { return nil, nil },
		apiKey:      "test",
		wsBase:      "ws://127.0.0.1:1",
		initBackoff: time.Millisecond,
	}

	close(rt.closeCh)

	closed, err := rt.wsConnect()
	assert.True(t, closed)
	assert.NoError(t, err)
}

func TestWsConnect_ReadError_CloseCh(t *testing.T) {
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	subscribed := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		var msg map[string]interface{}
		_ = conn.ReadJSON(&msg)

		close(subscribed)

		// Block until client disconnects.
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()

	rt := &ConfigRuntime{
		configID:    "test-id",
		environment: "test",
		cache:       map[string]interface{}{},
		status:      "connecting",
		closeCh:     make(chan struct{}),
		wsDone:      make(chan struct{}),
		fetchChain:  func() ([]chainEntry, error) { return nil, nil },
		apiKey:      "test",
		wsBase:      toWSBase(server.URL),
		initBackoff: time.Millisecond,
	}

	type result struct {
		closed bool
		err    error
	}
	done := make(chan result, 1)
	go func() {
		c, e := rt.wsConnect()
		done <- result{c, e}
	}()

	select {
	case <-subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscribe")
	}

	close(rt.closeCh)

	select {
	case r := <-done:
		assert.True(t, r.closed)
		assert.NoError(t, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for wsConnect to return")
	}
}

func TestFireListeners_RemovedKey_InternalPath(t *testing.T) {
	rt := &ConfigRuntime{}

	var events []*ConfigChangeEvent
	var mu sync.Mutex
	rt.listeners = []changeListener{
		{key: "", cb: func(evt *ConfigChangeEvent) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		}},
	}

	oldCache := map[string]interface{}{"a": 1, "b": 2}
	newCache := map[string]interface{}{"a": 1}

	rt.fireListeners(oldCache, newCache, "manual")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 1)
	assert.Equal(t, "b", events[0].Key)
	assert.Nil(t, events[0].NewValue)
}
