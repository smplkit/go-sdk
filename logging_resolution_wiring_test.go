package smplkit

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smplkit/go-sdk/v3/logging/adapters"
)

// ── Resolved-level delta wiring ─────────────────────────────────────────────
//
// These tests cover the contract that listeners fire only when a logger's
// _resolved_ level changes — not just when its raw cache entry mutates. Every
// cache-mutating handler (Refresh, loggers_changed, logger_changed,
// logger_deleted, group_changed, group_deleted) must re-resolve every cached
// logger and fire change listeners only for the keys whose effective level
// actually moved.
//
// newTestLoggingClient (defined in logging_resolution_test.go) returns a client
// with connected=true so OnChange / OnChangeKey / Refresh are ungated here.

// captured aggregates every event delivered to global + key listeners.
type captured struct {
	mu     sync.Mutex
	global []*LoggerChangeEvent
	keys   map[string][]*LoggerChangeEvent
}

func newCaptured() *captured {
	return &captured{keys: map[string][]*LoggerChangeEvent{}}
}

func (c *captured) wireGlobal(lc *LoggingClient) {
	_ = lc.OnChange(func(e *LoggerChangeEvent) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.global = append(c.global, e)
	})
}

func (c *captured) wireKey(lc *LoggingClient, key string) {
	_ = lc.OnChangeKey(key, func(e *LoggerChangeEvent) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.keys[key] = append(c.keys[key], e)
	})
}

func (c *captured) keysFired() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.global))
	for _, e := range c.global {
		out[e.ID]++
	}
	return out
}

// ── handleLoggerChanged ─────────────────────────────────────────────────────

func TestHandleLoggerChanged_FiresOnContentChange(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"my.logger","type":"logger","attributes":{"id":"my.logger","name":"My Logger","level":"WARN","managed":true,"environments":{}}}}`))
	}))
	lc.loggersCache["my.logger"] = map[string]interface{}{"id": "my.logger", "level": "DEBUG", "managed": true}

	var received *LoggerChangeEvent
	require.NoError(t, lc.OnChange(func(e *LoggerChangeEvent) { received = e }))

	lc.handleLoggerChanged(map[string]interface{}{"id": "my.logger"})
	require.NotNil(t, received)
	assert.Equal(t, "my.logger", received.ID)
	assert.Equal(t, "push", received.Source)
}

func TestHandleLoggerChanged_NoFireWhenUnchanged(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme.app","type":"logger","attributes":{"id":"com.acme.app","name":"com.acme.app","level":"DEBUG","managed":true,"environments":{}}}}`))
	}))
	// Pre-warm with the exact map representation so reflect.DeepEqual matches.
	prefetched, err := lc.fetchSingleLogger(context.Background(), "com.acme.app")
	require.NoError(t, err)
	lc.loggersCache["com.acme.app"] = prefetched

	var called bool
	require.NoError(t, lc.OnChange(func(*LoggerChangeEvent) { called = true }))
	lc.handleLoggerChanged(map[string]interface{}{"id": "com.acme.app"})
	assert.False(t, called)
}

func TestHandleLoggerChanged_FetchErrorEarlyReturn(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	var called bool
	require.NoError(t, lc.OnChange(func(*LoggerChangeEvent) { called = true }))
	lc.handleLoggerChanged(map[string]interface{}{"id": "x"})
	assert.False(t, called)
}

func TestHandleLoggerChanged_FiresDotDescendantListeners(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme","type":"logger","attributes":{"id":"com.acme","name":"com.acme","level":"ERROR","managed":true,"environments":{}}}}`))
	}))
	lc.loggersCache["com.acme"] = map[string]interface{}{"id": "com.acme", "name": "com.acme", "managed": true, "level": "WARN"}
	lc.loggersCache["com.acme.payments"] = map[string]interface{}{"id": "com.acme.payments", "name": "com.acme.payments", "managed": true}

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "com.acme.payments")

	lc.handleLoggerChanged(map[string]interface{}{"id": "com.acme"})

	fired := cap.keysFired()
	assert.Equal(t, 1, fired["com.acme"])
	assert.Equal(t, 1, fired["com.acme.payments"])
}

// ── handleLoggerDeleted ─────────────────────────────────────────────────────

func TestHandleLoggerDeleted_NoFetchNoEventForDeletedKey(t *testing.T) {
	var fetchCount int32
	lc := newTestLoggingClient(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
	}))
	lc.loggersCache["gone.logger"] = map[string]interface{}{"id": "gone.logger", "level": "WARN"}

	var evt *LoggerChangeEvent
	require.NoError(t, lc.OnChange(func(e *LoggerChangeEvent) { evt = e }))
	lc.handleLoggerDeleted(map[string]interface{}{"id": "gone.logger"})

	assert.Equal(t, int32(0), atomic.LoadInt32(&fetchCount))
	_, still := lc.loggersCache["gone.logger"]
	assert.False(t, still)
	assert.Nil(t, evt, "deleted key fires nothing")
}

func TestHandleLoggerDeleted_FiresDotDescendantListenersOnly(t *testing.T) {
	lc := newTestLoggingClient(t, nil)
	lc.loggersCache["com.acme"] = map[string]interface{}{"id": "com.acme", "name": "com.acme", "managed": true, "level": "WARN"}
	lc.loggersCache["com.acme.payments"] = map[string]interface{}{"id": "com.acme.payments", "name": "com.acme.payments", "managed": true}

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "com.acme")
	cap.wireKey(lc, "com.acme.payments")

	lc.handleLoggerDeleted(map[string]interface{}{"id": "com.acme"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.keys["com.acme"])
	require.Len(t, cap.keys["com.acme.payments"], 1)
	require.NotNil(t, cap.keys["com.acme.payments"][0].Level)
	assert.Equal(t, LogLevelInfo, *cap.keys["com.acme.payments"][0].Level)
	require.Len(t, cap.global, 1)
	assert.Equal(t, "com.acme.payments", cap.global[0].ID)
}

// ── handleGroupChanged ──────────────────────────────────────────────────────

func TestHandleGroupChanged_FiresLoggerListenerOnResolvedDelta(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"ERROR","environments":{}}}}`))
	}))
	lc.loggersCache["app.db"] = map[string]interface{}{"id": "app.db", "name": "app.db", "managed": true, "group": "sql"}
	lc.groupsCache["sql"] = map[string]interface{}{"id": "sql", "name": "SQL", "level": "WARN"}

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "app.db")

	lc.handleGroupChanged(map[string]interface{}{"id": "sql"})

	assert.Equal(t, 1, cap.keysFired()["app.db"])
	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.keys["app.db"], 1)
	require.NotNil(t, cap.keys["app.db"][0].Level)
	assert.Equal(t, LogLevelError, *cap.keys["app.db"][0].Level)
	assert.Equal(t, "push", cap.keys["app.db"][0].Source)
}

func TestHandleGroupChanged_NoFireWhenResolvedLevelUnchanged(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL Renamed","level":"WARN","environments":{}}}}`))
	}))
	lc.loggersCache["app.db"] = map[string]interface{}{"id": "app.db", "name": "app.db", "managed": true, "group": "sql"}
	lc.groupsCache["sql"] = map[string]interface{}{"id": "sql", "name": "SQL", "level": "WARN"}

	cap := newCaptured()
	cap.wireGlobal(lc)
	lc.handleGroupChanged(map[string]interface{}{"id": "sql"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.global)
}

func TestHandleGroupChanged_FetchErrorEarlyReturn(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	lc.handleGroupChanged(map[string]interface{}{"id": "sql"}) // must not panic
}

func TestHandleGroupChanged_NoFireWhenContentUnchanged(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"ERROR","environments":{}}}}`))
	}))
	prefetched, err := lc.fetchSingleGroup(context.Background(), "sql")
	require.NoError(t, err)
	lc.groupsCache["sql"] = prefetched
	lc.handleGroupChanged(map[string]interface{}{"id": "sql"}) // no diff → early return
}

// ── handleGroupDeleted ──────────────────────────────────────────────────────

func TestHandleGroupDeleted_FiresLoggerListenersOnResolvedDelta(t *testing.T) {
	lc := newTestLoggingClient(t, nil)
	lc.loggersCache["app.db"] = map[string]interface{}{"id": "app.db", "name": "app.db", "managed": true, "group": "sql"}
	lc.groupsCache["sql"] = map[string]interface{}{"id": "sql", "name": "SQL", "level": "WARN"}

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "app.db")
	lc.handleGroupDeleted(map[string]interface{}{"id": "sql"})

	assert.Equal(t, 1, cap.keysFired()["app.db"])
	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.keys["app.db"], 1)
	require.NotNil(t, cap.keys["app.db"][0].Level)
	assert.Equal(t, LogLevelInfo, *cap.keys["app.db"][0].Level)
}

func TestHandleGroupDeleted_NoFireWhenGroupNeverCached(t *testing.T) {
	lc := newTestLoggingClient(t, nil)
	cap := newCaptured()
	cap.wireGlobal(lc)
	lc.handleGroupDeleted(map[string]interface{}{"id": "never-existed"})
	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.global)
}

func TestHandleGroupDeleted_NoFireWhenNoDependents(t *testing.T) {
	lc := newTestLoggingClient(t, nil)
	lc.loggersCache["independent.logger"] = map[string]interface{}{"id": "independent.logger", "name": "independent.logger", "managed": true, "level": "DEBUG"}
	lc.groupsCache["unused-group"] = map[string]interface{}{"id": "unused-group", "name": "Unused", "level": "ERROR"}

	cap := newCaptured()
	cap.wireGlobal(lc)
	lc.handleGroupDeleted(map[string]interface{}{"id": "unused-group"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.global)
	_, still := lc.groupsCache["unused-group"]
	assert.False(t, still)
}

// ── loggers_changed ─────────────────────────────────────────────────────────

func TestHandleLoggersChanged_FiresOnGroupOnlyChange(t *testing.T) {
	var groupsLevel atomic.Value
	groupsLevel.Store("WARN")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"app.db","type":"logger","attributes":{"id":"app.db","name":"app.db","managed":true,"group":"sql","environments":{}}}]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		body := strings.ReplaceAll(`{"data":[{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"$L","environments":{}}}]}`, "$L", groupsLevel.Load().(string))
		_, _ = w.Write([]byte(body))
	})
	lc := newTestLoggingClient(t, mux)
	require.NoError(t, lc.fetchAndCache(context.Background()))

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "app.db")

	groupsLevel.Store("ERROR")
	lc.handleLoggersChanged(map[string]interface{}{})
	assert.Equal(t, 1, cap.keysFired()["app.db"])
}

func TestHandleLoggersChanged_FetchErrorEarlyReturn(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	var called bool
	require.NoError(t, lc.OnChange(func(*LoggerChangeEvent) { called = true }))
	lc.handleLoggersChanged(map[string]interface{}{})
	assert.False(t, called)
}

func TestHandleLoggersChanged_FullFetchDiffFiring(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"com.acme.app","type":"logger","attributes":{"id":"com.acme.app","name":"com.acme.app","level":"WARN","managed":true,"environments":{}}},
			{"id":"com.acme.db","type":"logger","attributes":{"id":"com.acme.db","name":"com.acme.db","level":"DEBUG","managed":true,"environments":{}}}
		]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	lc := newTestLoggingClient(t, mux)
	lc.loggersCache["com.acme.app"] = map[string]interface{}{"id": "com.acme.app", "level": "DEBUG"}

	var keyAppFired, keyDBFired bool
	require.NoError(t, lc.OnChangeKey("com.acme.app", func(*LoggerChangeEvent) { keyAppFired = true }))
	require.NoError(t, lc.OnChangeKey("com.acme.db", func(*LoggerChangeEvent) { keyDBFired = true }))

	lc.handleLoggersChanged(map[string]interface{}{})
	assert.True(t, keyAppFired)
	assert.True(t, keyDBFired)
}

// ── Refresh ─────────────────────────────────────────────────────────────────

// refreshMux serves both list endpoints from atomic pointers so the test can
// swap the served body between successive Refresh calls.
func refreshMux(loggersBody, groupsBody *atomic.Value) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(loggersBody.Load().(string)))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(groupsBody.Load().(string)))
	})
	return mux
}

func TestRefresh_FetchesAndCaches(t *testing.T) {
	var loggers, groups atomic.Value
	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"INFO","managed":true,"environments":{},"sources":[]}}]}`)
	groups.Store(`{"data":[]}`)
	lc := newTestLoggingClient(t, refreshMux(&loggers, &groups))

	require.NoError(t, lc.Refresh(context.Background()))
	assert.Contains(t, lc.loggersCache, "app")
	assert.Equal(t, "INFO", lc.loggersCache["app"]["level"])
}

func TestRefresh_FiresChangeListenerWithManualSource(t *testing.T) {
	var loggers, groups atomic.Value
	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"INFO","managed":true,"environments":{},"sources":[]}}]}`)
	groups.Store(`{"data":[]}`)
	lc := newTestLoggingClient(t, refreshMux(&loggers, &groups))
	require.NoError(t, lc.Refresh(context.Background()))

	var events []*LoggerChangeEvent
	require.NoError(t, lc.OnChange(func(e *LoggerChangeEvent) { events = append(events, e) }))

	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"DEBUG","managed":true,"environments":{},"sources":[]}}]}`)
	require.NoError(t, lc.Refresh(context.Background()))

	require.Len(t, events, 1)
	assert.Equal(t, "app", events[0].ID)
	assert.Equal(t, "manual", events[0].Source)
	require.NotNil(t, events[0].Level)
	assert.Equal(t, LogLevelDebug, *events[0].Level)
}

func TestRefresh_NoEventForDeletedKey(t *testing.T) {
	var loggers, groups atomic.Value
	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"INFO","managed":true,"environments":{},"sources":[]}}]}`)
	groups.Store(`{"data":[]}`)
	lc := newTestLoggingClient(t, refreshMux(&loggers, &groups))
	require.NoError(t, lc.Refresh(context.Background()))

	var events []*LoggerChangeEvent
	require.NoError(t, lc.OnChange(func(e *LoggerChangeEvent) { events = append(events, e) }))

	loggers.Store(`{"data":[]}`)
	require.NoError(t, lc.Refresh(context.Background()))
	assert.Empty(t, events)
}

func TestRefresh_NoListenerFireWhenUnchanged(t *testing.T) {
	var loggers, groups atomic.Value
	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"INFO","managed":true,"environments":{},"sources":[]}}]}`)
	groups.Store(`{"data":[]}`)
	lc := newTestLoggingClient(t, refreshMux(&loggers, &groups))
	require.NoError(t, lc.Refresh(context.Background()))

	var fired int
	require.NoError(t, lc.OnChange(func(*LoggerChangeEvent) { fired++ }))
	require.NoError(t, lc.Refresh(context.Background()))
	assert.Zero(t, fired)
}

func TestRefresh_AppliesResolvedLevelToAdapter(t *testing.T) {
	var loggers, groups atomic.Value
	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"INFO","managed":true,"environments":{},"sources":[]}}]}`)
	groups.Store(`{"data":[]}`)
	lc := newTestLoggingClient(t, refreshMux(&loggers, &groups))
	a := &captureAdapter{discovered: []adapters.DiscoveredLogger{{Name: "app", Level: "INFO"}}}
	// newTestLoggingClient marks the client connected; RegisterAdapter panics
	// post-Install, so register the adapter against a clear flag, then restore.
	lc.connected = false
	lc.RegisterAdapter(a)
	lc.connected = true

	require.NoError(t, lc.Refresh(context.Background()))
	require.NotEmpty(t, a.applied)
	assert.Equal(t, "app", a.applied[len(a.applied)-1].name)
	assert.Equal(t, "INFO", a.applied[len(a.applied)-1].level)

	loggers.Store(`{"data":[{"id":"app","type":"logger","attributes":{"id":"app","name":"App","level":"ERROR","managed":true,"environments":{},"sources":[]}}]}`)
	require.NoError(t, lc.Refresh(context.Background()))
	assert.Equal(t, "ERROR", a.applied[len(a.applied)-1].level)
}

func TestRefresh_ReturnsErrorOnFetchFailure(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	require.Error(t, lc.Refresh(context.Background()))
}

// ── Diagnostics (change-listener fanout) ────────────────────────────────────

func TestDiagnostic1_DotAncestorCascadeViaLoggerChanged(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme","type":"logger","attributes":{"id":"com.acme","name":"com.acme","level":"ERROR","managed":true,"environments":{}}}}`))
	}))
	lc.loggersCache["com.acme"] = map[string]interface{}{"id": "com.acme", "name": "com.acme", "managed": true, "level": "WARN"}
	descendants := []string{"com.acme.api", "com.acme.db", "com.acme.queue", "com.acme.cache", "com.acme.workers"}
	for _, id := range descendants {
		lc.loggersCache[id] = map[string]interface{}{"id": id, "name": id, "managed": true}
	}

	cap := newCaptured()
	cap.wireGlobal(lc)
	lc.handleLoggerChanged(map[string]interface{}{"id": "com.acme"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.global, 6) // N+1
	got := make(map[string]LogLevel, len(cap.global))
	for _, e := range cap.global {
		require.NotNil(t, e.Level)
		assert.Equal(t, "push", e.Source)
		got[e.ID] = *e.Level
	}
	assert.Equal(t, LogLevelError, got["com.acme"])
	for _, id := range descendants {
		assert.Equal(t, LogLevelError, got[id])
	}
}

func TestDiagnostic2_GroupCascadeViaGroupChanged(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"app","type":"log_group","attributes":{"id":"app","name":"app","level":"ERROR","environments":{}}}}`))
	}))
	lc.groupsCache["app"] = map[string]interface{}{"id": "app", "name": "app", "level": "WARN"}
	deps := []string{"app.db", "app.queue", "app.api"}
	for _, id := range deps {
		lc.loggersCache[id] = map[string]interface{}{"id": id, "name": id, "managed": true, "group": "app"}
	}

	cap := newCaptured()
	cap.wireGlobal(lc)
	lc.handleGroupChanged(map[string]interface{}{"id": "app"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.global, 3)
	got := make(map[string]LogLevel, len(cap.global))
	for _, e := range cap.global {
		require.NotNil(t, e.Level)
		got[e.ID] = *e.Level
	}
	for _, id := range deps {
		assert.Equal(t, LogLevelError, got[id])
	}
	assert.NotContains(t, got, "app")
}

func TestDiagnostic3_GroupDeletedFiresForDependentsNotDeletedKey(t *testing.T) {
	lc := newTestLoggingClient(t, nil)
	lc.groupsCache["app"] = map[string]interface{}{"id": "app", "name": "app", "level": "WARN"}
	deps := []string{"app.db", "app.queue", "app.api"}
	for _, id := range deps {
		lc.loggersCache[id] = map[string]interface{}{"id": id, "name": id, "managed": true, "group": "app"}
	}

	cap := newCaptured()
	cap.wireGlobal(lc)
	lc.handleGroupDeleted(map[string]interface{}{"id": "app"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.global, 3)
	got := make(map[string]LogLevel, len(cap.global))
	for _, e := range cap.global {
		require.NotNil(t, e.Level)
		got[e.ID] = *e.Level
	}
	for _, id := range deps {
		assert.Equal(t, LogLevelInfo, got[id])
	}
	assert.NotContains(t, got, "app")
}

func TestDiagnostic4_NoOpEditFiresNothing(t *testing.T) {
	lc := newTestLoggingClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme.api","type":"logger","attributes":{"id":"com.acme.api","name":"API Renamed","level":"WARN","managed":true,"environments":{}}}}`))
	}))
	lc.loggersCache["com.acme.api"] = map[string]interface{}{"id": "com.acme.api", "name": "API", "managed": true, "level": "WARN"}

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "com.acme.api")
	lc.handleLoggerChanged(map[string]interface{}{"id": "com.acme.api"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.global)
	assert.Empty(t, cap.keys["com.acme.api"])
}
