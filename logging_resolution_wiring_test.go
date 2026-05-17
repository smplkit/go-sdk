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
// Per the change-listener semantics rule, the deleted logger itself
// fires NO event (deletion is not a level change). Descendants whose
// effective level moved fire normal change events with the new level.
func TestHandleLoggerDeleted_FiresDotDescendantListenersOnly(t *testing.T) {
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
	assert.Empty(t, cap.keys["com.acme"], "deleted key fires nothing — deletion is not a level change")
	require.Len(t, cap.keys["com.acme.payments"], 1)
	require.NotNil(t, cap.keys["com.acme.payments"][0].Level)
	assert.Equal(t, LogLevelInfo, *cap.keys["com.acme.payments"][0].Level, "no ancestor level left → INFO fallback")
	require.Len(t, cap.global, 1, "global fires once — only the descendant changed")
	assert.Equal(t, "com.acme.payments", cap.global[0].ID)
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

// ── Change-listener fanout diagnostics ──────────────────────────────────────
//
// These four tests codify the change-listener fanout rules verbatim from
// the prompt that drove this fix. Every entry in this section is a
// direct port of one numbered diagnostic — keep the structure aligned so
// future SDK-wide audits can grep for the same shapes.

// Diagnostic 1: a logger_changed event for a dot-ancestor cascades to
// every descendant whose effective level moved. Global listener fires
// once per affected logger (N+1 in total), each invocation carrying the
// per-logger id and new level.
func TestDiagnostic1_DotAncestorCascadeViaLoggerChanged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/loggers/com.acme", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme","type":"logger","attributes":{"id":"com.acme","name":"com.acme","level":"ERROR","managed":true,"environments":{}}}}`))
	})
	lc := newTestLoggingClient(t, mux)

	// Ancestor at WARN; 5 descendants with no own level / no group →
	// all inherit WARN via dot-ancestry.
	lc.loggersCache["com.acme"] = map[string]interface{}{
		"id": "com.acme", "name": "com.acme", "managed": true, "level": "WARN",
	}
	descendants := []string{
		"com.acme.api", "com.acme.db", "com.acme.queue",
		"com.acme.cache", "com.acme.workers",
	}
	for _, id := range descendants {
		lc.loggersCache[id] = map[string]interface{}{"id": id, "name": id, "managed": true}
	}

	cap := newCaptured()
	cap.wireGlobal(lc)

	lc.handleLoggerChanged(map[string]interface{}{"id": "com.acme"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	// Global must fire N+1 = 6 times — once per affected logger.
	require.Len(t, cap.global, 6, "global fires once per affected logger, never a single summary event")
	gotIDs := make(map[string]LogLevel, len(cap.global))
	for _, e := range cap.global {
		require.NotNil(t, e.Level, "every event carries the new effective level")
		assert.Equal(t, "websocket", e.Source)
		gotIDs[e.ID] = *e.Level
	}
	assert.Equal(t, LogLevelError, gotIDs["com.acme"])
	for _, id := range descendants {
		assert.Equal(t, LogLevelError, gotIDs[id], "descendant %s should have fired with new level ERROR", id)
	}
}

// Diagnostic 2: a group_changed event cascades to every dependent
// logger. Global listener fires once per dependent.
func TestDiagnostic2_GroupCascadeViaGroupChanged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/log_groups/app", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"app","type":"log_group","attributes":{"id":"app","name":"app","level":"ERROR","environments":{}}}}`))
	})
	lc := newTestLoggingClient(t, mux)

	lc.groupsCache["app"] = map[string]interface{}{"id": "app", "name": "app", "level": "WARN"}
	deps := []string{"app.db", "app.queue", "app.api"}
	for _, id := range deps {
		lc.loggersCache[id] = map[string]interface{}{
			"id": id, "name": id, "managed": true, "group": "app",
		}
	}

	cap := newCaptured()
	cap.wireGlobal(lc)

	lc.handleGroupChanged(map[string]interface{}{"id": "app"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.global, 3, "global fires once per dependent logger")
	gotIDs := make(map[string]LogLevel, len(cap.global))
	for _, e := range cap.global {
		require.NotNil(t, e.Level)
		gotIDs[e.ID] = *e.Level
	}
	for _, id := range deps {
		assert.Equal(t, LogLevelError, gotIDs[id], "dependent %s should fire with new level ERROR", id)
	}
	// Crucially: NO event with id == "app" (the group key).
	assert.NotContains(t, gotIDs, "app", "group id must not appear in any event")
}

// Diagnostic 3: group_deleted fires N events for dependents whose
// effective level moved to the fallback. NO event for the deleted group
// id; no deletion-flavored events anywhere.
func TestDiagnostic3_GroupDeletedFiresForDependentsNotDeletedKey(t *testing.T) {
	lc := newTestLoggingClient(t, nil)

	lc.groupsCache["app"] = map[string]interface{}{"id": "app", "name": "app", "level": "WARN"}
	deps := []string{"app.db", "app.queue", "app.api"}
	for _, id := range deps {
		lc.loggersCache[id] = map[string]interface{}{
			"id": id, "name": id, "managed": true, "group": "app",
		}
	}

	cap := newCaptured()
	cap.wireGlobal(lc)

	lc.handleGroupDeleted(map[string]interface{}{"id": "app"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.global, 3, "global fires once per dependent whose effective level moved")
	gotIDs := make(map[string]LogLevel, len(cap.global))
	for _, e := range cap.global {
		require.NotNil(t, e.Level, "every event carries the new effective level")
		gotIDs[e.ID] = *e.Level
	}
	for _, id := range deps {
		// No own level + group is gone + no dot-ancestor → fallback INFO.
		assert.Equal(t, LogLevelInfo, gotIDs[id], "dependent %s should fall to system fallback", id)
	}
	assert.NotContains(t, gotIDs, "app", "the deleted group id must not appear in any event")
}

// Diagnostic 4: a logger_changed (or group_changed) whose effective
// level is unchanged — e.g. only a name moved — fires no listener.
func TestDiagnostic4_NoOpEditFiresNothing(t *testing.T) {
	mux := http.NewServeMux()
	// Server returns the same level but a different name field.
	mux.HandleFunc("/api/v1/loggers/com.acme.api", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"com.acme.api","type":"logger","attributes":{"id":"com.acme.api","name":"API Renamed","level":"WARN","managed":true,"environments":{}}}}`))
	})
	lc := newTestLoggingClient(t, mux)

	lc.loggersCache["com.acme.api"] = map[string]interface{}{
		"id": "com.acme.api", "name": "API", "managed": true, "level": "WARN",
	}

	cap := newCaptured()
	cap.wireGlobal(lc)
	cap.wireKey(lc, "com.acme.api")

	lc.handleLoggerChanged(map[string]interface{}{"id": "com.acme.api"})

	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.global, "global must not fire for metadata-only edits")
	assert.Empty(t, cap.keys["com.acme.api"], "key listener must not fire for metadata-only edits")
}
