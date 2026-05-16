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
)

// ── Resolved-level delta wiring ─────────────────────────────────────────────
//
// These tests cover the contract that listeners fire only when a logger's
// _resolved_ level changes — not just when its raw cache entry mutates.
// They mirror Python's per-event tests in `test_resolution.py` and
// `tests/unit/logging/test_client_*.py`: every cache-mutating handler
// (Refresh, loggers_changed, logger_changed, logger_deleted,
// group_changed, group_deleted) must re-resolve every cached logger and
// fire change / deleted listeners only for the keys whose effective
// level actually moved.

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
	lc.OnChange(func(e *LoggerChangeEvent) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.global = append(c.global, e)
	})
}

func (c *captured) wireKey(lc *LoggingClient, key string) {
	lc.OnChangeKey(key, func(e *LoggerChangeEvent) {
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

// ── handleGroupChanged ──────────────────────────────────────────────────────

// A group-only edit that flips a dependent logger's resolved level must
// fire that logger's listener — the customer is watching the logger
// key, not the group key.
func TestHandleGroupChanged_FiresLoggerListenerOnResolvedDelta(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups/sql", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"ERROR","environments":{}}}}`))
	})
	lc := newTestLoggingClient(t, mux)

	// Logger inherits from group "sql". Pre-state: group level WARN → logger resolves WARN.
	lc.loggersCache["app.db"] = map[string]interface{}{
		"id":      "app.db",
		"name":    "app.db",
		"managed": true,
		"group":   "sql",
	}
	lc.groupsCache["sql"] = map[string]interface{}{
		"id":    "sql",
		"name":  "SQL",
		"level": "WARN",
	}

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "app.db")

	// group_changed bumps the group's level to ERROR — the logger's
	// resolved level moves WARN → ERROR.
	lc.handleGroupChanged(map[string]interface{}{"id": "sql"})

	fired := cap.keysFired()
	assert.Equal(t, 1, fired["app.db"], "logger listener should fire when its group's level changes")
	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.keys["app.db"], 1)
	require.NotNil(t, cap.keys["app.db"][0].Level)
	assert.Equal(t, LogLevelError, *cap.keys["app.db"][0].Level)
	assert.Equal(t, "websocket", cap.keys["app.db"][0].Source)
}

// Metadata-only group edits (level unchanged) must not fire logger
// listeners — the resolved level didn't move.
func TestHandleGroupChanged_NoFireWhenResolvedLevelUnchanged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups/sql", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		// Same level as pre-state, just a different name.
		_, _ = w.Write([]byte(`{"data":{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL Renamed","level":"WARN","environments":{}}}}`))
	})
	lc := newTestLoggingClient(t, mux)

	lc.loggersCache["app.db"] = map[string]interface{}{
		"id":      "app.db",
		"name":    "app.db",
		"managed": true,
		"group":   "sql",
	}
	lc.groupsCache["sql"] = map[string]interface{}{
		"id":    "sql",
		"name":  "SQL",
		"level": "WARN",
	}

	cap := newCaptured()
	cap.wireGlobal(lc)

	lc.handleGroupChanged(map[string]interface{}{"id": "sql"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.global, "no listener should fire when the resolved level is unchanged")
}

// ── handleGroupDeleted ──────────────────────────────────────────────────────

// Deleting a group flips its dependents' resolved level (to whatever
// the next chain step yields — INFO fallback here). Listeners on those
// loggers must fire with a change event.
func TestHandleGroupDeleted_FiresLoggerListenersOnResolvedDelta(t *testing.T) {
	lc := newTestLoggingClient(t, nil)

	lc.loggersCache["app.db"] = map[string]interface{}{
		"id":      "app.db",
		"name":    "app.db",
		"managed": true,
		"group":   "sql",
	}
	lc.groupsCache["sql"] = map[string]interface{}{
		"id":    "sql",
		"name":  "SQL",
		"level": "WARN",
	}

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "app.db")

	lc.handleGroupDeleted(map[string]interface{}{"id": "sql"})

	fired := cap.keysFired()
	assert.Equal(t, 1, fired["app.db"], "logger listener should fire when its group is deleted")
	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.keys["app.db"], 1)
	require.NotNil(t, cap.keys["app.db"][0].Level)
	assert.Equal(t, LogLevelInfo, *cap.keys["app.db"][0].Level, "no group + no own level → INFO fallback")
}

// A group_deleted event for a group we don't have cached is a no-op —
// must not invoke listeners.
func TestHandleGroupDeleted_NoFireWhenGroupWasNeverCached(t *testing.T) {
	lc := newTestLoggingClient(t, nil)

	cap := newCaptured()
	cap.wireGlobal(lc)

	lc.handleGroupDeleted(map[string]interface{}{"id": "never-existed"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.global)
}

// Deleting a group nobody depends on must not fire any listeners.
func TestHandleGroupDeleted_NoFireWhenNoDependents(t *testing.T) {
	lc := newTestLoggingClient(t, nil)

	lc.loggersCache["independent.logger"] = map[string]interface{}{
		"id":      "independent.logger",
		"name":    "independent.logger",
		"managed": true,
		"level":   "DEBUG",
	}
	lc.groupsCache["unused-group"] = map[string]interface{}{
		"id":    "unused-group",
		"name":  "Unused",
		"level": "ERROR",
	}

	cap := newCaptured()
	cap.wireGlobal(lc)

	lc.handleGroupDeleted(map[string]interface{}{"id": "unused-group"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.global, "no logger inherits from the deleted group, so nothing changed")
	_, stillCached := lc.groupsCache["unused-group"]
	assert.False(t, stillCached, "group should still be removed from cache")
}

// ── Dot-ancestry propagation ────────────────────────────────────────────────

// Updating a parent logger's level must fire change listeners for
// dot-notation descendants whose level was inheriting via the ancestor
// — even though their own raw cache entry didn't move.
func TestHandleLoggerChanged_FiresDotDescendantListeners(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/com.acme", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme","type":"logger","attributes":{"id":"com.acme","name":"com.acme","level":"ERROR","managed":true,"environments":{}}}}`))
	})
	lc := newTestLoggingClient(t, mux)

	// Pre: com.acme=WARN, com.acme.payments has no own level → resolves WARN via ancestry.
	lc.loggersCache["com.acme"] = map[string]interface{}{
		"id":      "com.acme",
		"name":    "com.acme",
		"managed": true,
		"level":   "WARN",
	}
	lc.loggersCache["com.acme.payments"] = map[string]interface{}{
		"id":      "com.acme.payments",
		"name":    "com.acme.payments",
		"managed": true,
	}

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "com.acme.payments")

	// Ancestor logger flips to ERROR — both com.acme AND com.acme.payments resolve to ERROR now.
	lc.handleLoggerChanged(map[string]interface{}{"id": "com.acme"})

	fired := cap.keysFired()
	assert.Equal(t, 1, fired["com.acme"], "the changed logger fires")
	assert.Equal(t, 1, fired["com.acme.payments"], "the dot-descendant fires because its resolved level moved")
}

// Deleting a parent logger flips dot-descendants' resolved level.
// Listeners on those descendants must fire change events; the deleted
// logger itself fires a deleted event.
func TestHandleLoggerDeleted_FiresDotDescendantListeners(t *testing.T) {
	lc := newTestLoggingClient(t, nil)

	lc.loggersCache["com.acme"] = map[string]interface{}{
		"id":      "com.acme",
		"name":    "com.acme",
		"managed": true,
		"level":   "WARN",
	}
	lc.loggersCache["com.acme.payments"] = map[string]interface{}{
		"id":      "com.acme.payments",
		"name":    "com.acme.payments",
		"managed": true,
	}

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "com.acme")
	cap.wireKey(lc, "com.acme.payments")

	lc.handleLoggerDeleted(map[string]interface{}{"id": "com.acme"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.keys["com.acme"], 1)
	assert.True(t, cap.keys["com.acme"][0].Deleted, "the deleted logger fires with Deleted=true")
	require.Len(t, cap.keys["com.acme.payments"], 1)
	assert.False(t, cap.keys["com.acme.payments"][0].Deleted, "the descendant fires a change event, not a delete")
	require.NotNil(t, cap.keys["com.acme.payments"][0].Level)
	assert.Equal(t, LogLevelInfo, *cap.keys["com.acme.payments"][0].Level, "no ancestor level left → INFO fallback")
}

// ── loggers_changed ─────────────────────────────────────────────────────────

// A full refetch where only the groups data moved must still fire
// dependent logger listeners.
func TestHandleLoggersChanged_FiresOnGroupOnlyChange(t *testing.T) {
	var groupsLevel atomic.Value
	groupsLevel.Store("WARN")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[
			{"id":"app.db","type":"logger","attributes":{"id":"app.db","name":"app.db","managed":true,"group":"sql","environments":{}}}
		]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		level := groupsLevel.Load().(string)
		body := strings.ReplaceAll(`{"data":[{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"$LEVEL","environments":{}}}]}`,
			"$LEVEL", level)
		_, _ = w.Write([]byte(body))
	})
	lc := newTestLoggingClient(t, mux)

	// Prime caches with first fetch.
	require.NoError(t, lc.fetchAndCache(context.Background()))

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "app.db")

	// Server now reports the group at ERROR. The logger's raw cache entry
	// is unchanged — only the group moved — but the resolved level
	// moves WARN → ERROR.
	groupsLevel.Store("ERROR")
	lc.handleLoggersChanged(map[string]interface{}{})

	fired := cap.keysFired()
	assert.Equal(t, 1, fired["app.db"], "logger listener should fire even though only the group changed")
}

// ── Refresh ─────────────────────────────────────────────────────────────────

// Same scenario as above, but driven by Refresh (Source = "manual").
func TestRefresh_FiresOnGroupOnlyChange(t *testing.T) {
	var groupsLevel atomic.Value
	groupsLevel.Store("WARN")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[
			{"id":"app.db","type":"logger","attributes":{"id":"app.db","name":"app.db","managed":true,"group":"sql","environments":{}}}
		]}`))
	})
	mux.HandleFunc("/api/v1/log_groups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		level := groupsLevel.Load().(string)
		body := strings.ReplaceAll(`{"data":[{"id":"sql","type":"log_group","attributes":{"id":"sql","name":"SQL","level":"$LEVEL","environments":{}}}]}`,
			"$LEVEL", level)
		_, _ = w.Write([]byte(body))
	})
	lc := newTestLoggingClient(t, mux)
	require.NoError(t, lc.Refresh(context.Background()))

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "app.db")

	groupsLevel.Store("DEBUG")
	require.NoError(t, lc.Refresh(context.Background()))

	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.keys["app.db"], 1)
	assert.Equal(t, "manual", cap.keys["app.db"][0].Source)
	require.NotNil(t, cap.keys["app.db"][0].Level)
	assert.Equal(t, LogLevelDebug, *cap.keys["app.db"][0].Level)
}
