package smplkit

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Resolution Cache tests ---

func TestResolutionCache_PutGet(t *testing.T) {
	c := newResolutionCache(100)
	c.put("key1", "value1")
	v, ok := c.get("key1")
	assert.True(t, ok)
	assert.Equal(t, "value1", v)
}

func TestResolutionCache_Miss(t *testing.T) {
	c := newResolutionCache(100)
	v, ok := c.get("missing")
	assert.False(t, ok)
	assert.Nil(t, v)
}

func TestResolutionCache_LRUEviction(t *testing.T) {
	c := newResolutionCache(3)
	c.put("a", 1)
	c.put("b", 2)
	c.put("c", 3)
	c.put("d", 4) // evicts "a"

	_, ok := c.get("a")
	assert.False(t, ok)
	v, ok := c.get("d")
	assert.True(t, ok)
	assert.Equal(t, 4, v)
}

func TestResolutionCache_Overwrite(t *testing.T) {
	c := newResolutionCache(100)
	c.put("key", "old")
	c.put("key", "new")
	v, ok := c.get("key")
	assert.True(t, ok)
	assert.Equal(t, "new", v)
}

func TestResolutionCache_Clear(t *testing.T) {
	c := newResolutionCache(100)
	c.put("a", 1)
	c.put("b", 2)
	c.clear()
	_, ok := c.get("a")
	assert.False(t, ok)
}

func TestResolutionCache_Stats(t *testing.T) {
	c := newResolutionCache(100)
	c.put("a", 1)
	c.get("a")        // hit
	c.get("missing")  // miss
	c.get("missing2") // miss
	hits, misses := c.stats()
	assert.Equal(t, 1, hits)
	assert.Equal(t, 2, misses)
}

func TestResolutionCache_LRURefreshOnAccess(t *testing.T) {
	c := newResolutionCache(3)
	c.put("a", 1)
	c.put("b", 2)
	c.put("c", 3)

	// Access "a" to refresh it.
	c.get("a")

	// Insert "d" — should evict "b" (least recently used), not "a".
	c.put("d", 4)

	_, ok := c.get("a")
	assert.True(t, ok, "a should still be in cache after access")
	_, ok = c.get("b")
	assert.False(t, ok, "b should have been evicted")
}

// --- Context Registration Buffer tests ---

func TestContextRegistrationBuffer_ObserveDrain(t *testing.T) {
	buf := newContextRegistrationBuffer()
	ctx1 := Context{Type: "user", Key: "u1", Name: "Alice", Attributes: map[string]interface{}{"plan": "free"}}
	ctx2 := Context{Type: "account", Key: "a1", Attributes: map[string]interface{}{"region": "us"}}

	buf.observe([]Context{ctx1, ctx2})
	assert.Equal(t, 2, buf.pendingCount())

	batch := buf.drain()
	assert.Len(t, batch, 2)
	assert.Equal(t, 0, buf.pendingCount())

	// Check format.
	assert.Equal(t, "user:u1", batch[0]["id"])
	assert.Equal(t, "Alice", batch[0]["name"])
	assert.Equal(t, "account:a1", batch[1]["id"])
	assert.Equal(t, "a1", batch[1]["name"]) // Falls back to key when name is empty.
}

func TestContextRegistrationBuffer_Deduplication(t *testing.T) {
	buf := newContextRegistrationBuffer()
	ctx := Context{Type: "user", Key: "u1", Attributes: map[string]interface{}{}}

	buf.observe([]Context{ctx, ctx, ctx})
	assert.Equal(t, 1, buf.pendingCount())
}

// --- evaluateFlag tests ---

func TestEvaluateFlag_NoEnvironment(t *testing.T) {
	flagDef := map[string]interface{}{
		"default":      "flag-default",
		"environments": map[string]interface{}{},
	}
	result := evaluateFlag(flagDef, "", map[string]interface{}{})
	assert.Equal(t, "flag-default", result)
}

func TestEvaluateFlag_MissingEnvironment(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": "flag-default",
		"environments": map[string]interface{}{
			"staging": map[string]interface{}{
				"enabled": true,
				"rules":   []interface{}{},
			},
		},
	}
	result := evaluateFlag(flagDef, "production", map[string]interface{}{})
	assert.Equal(t, "flag-default", result)
}

func TestEvaluateFlag_DisabledEnvironment(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": "flag-default",
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": false,
				"default": "env-default",
				"rules":   []interface{}{},
			},
		},
	}
	result := evaluateFlag(flagDef, "production", map[string]interface{}{})
	assert.Equal(t, "env-default", result)
}

func TestEvaluateFlag_DisabledEnvironment_FallbackToFlagDefault(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": "flag-default",
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": false,
				"rules":   []interface{}{},
			},
		},
	}
	result := evaluateFlag(flagDef, "production", map[string]interface{}{})
	assert.Equal(t, "flag-default", result)
}

func TestEvaluateFlag_FirstMatchWins(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": false,
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"rules": []interface{}{
					map[string]interface{}{
						"logic": map[string]interface{}{
							"==": []interface{}{map[string]interface{}{"var": "user.plan"}, "enterprise"},
						},
						"value": true,
					},
					map[string]interface{}{
						"logic": map[string]interface{}{
							"==": []interface{}{map[string]interface{}{"var": "user.plan"}, "free"},
						},
						"value": false,
					},
				},
			},
		},
	}

	evalDict := map[string]interface{}{
		"user": map[string]interface{}{"plan": "enterprise"},
	}
	result := evaluateFlag(flagDef, "production", evalDict)
	assert.Equal(t, true, result)
}

func TestEvaluateFlag_NoMatchFallback(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": "flag-default",
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"default": "env-default",
				"rules": []interface{}{
					map[string]interface{}{
						"logic": map[string]interface{}{
							"==": []interface{}{map[string]interface{}{"var": "user.plan"}, "enterprise"},
						},
						"value": "enterprise-value",
					},
				},
			},
		},
	}

	evalDict := map[string]interface{}{
		"user": map[string]interface{}{"plan": "free"},
	}
	result := evaluateFlag(flagDef, "production", evalDict)
	assert.Equal(t, "env-default", result)
}

func TestEvaluateFlag_EmptyLogicSkipped(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": "flag-default",
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"default": "env-default",
				"rules": []interface{}{
					map[string]interface{}{
						"logic": map[string]interface{}{},
						"value": "should-not-match",
					},
				},
			},
		},
	}
	result := evaluateFlag(flagDef, "production", map[string]interface{}{})
	assert.Equal(t, "env-default", result)
}

func TestEvaluateFlag_NumericComparison(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": 0.0,
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"rules": []interface{}{
					map[string]interface{}{
						"logic": map[string]interface{}{
							">": []interface{}{map[string]interface{}{"var": "user.age"}, float64(18)},
						},
						"value": 100.0,
					},
				},
			},
		},
	}

	evalDict := map[string]interface{}{
		"user": map[string]interface{}{"age": float64(25)},
	}
	result := evaluateFlag(flagDef, "production", evalDict)
	assert.Equal(t, 100.0, result)
}

func TestEvaluateFlag_ANDConditions(t *testing.T) {
	flagDef := map[string]interface{}{
		"default": false,
		"environments": map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"rules": []interface{}{
					map[string]interface{}{
						"logic": map[string]interface{}{
							"and": []interface{}{
								map[string]interface{}{"==": []interface{}{map[string]interface{}{"var": "user.plan"}, "enterprise"}},
								map[string]interface{}{"==": []interface{}{map[string]interface{}{"var": "account.region"}, "us"}},
							},
						},
						"value": true,
					},
				},
			},
		},
	}

	evalDict := map[string]interface{}{
		"user":    map[string]interface{}{"plan": "enterprise"},
		"account": map[string]interface{}{"region": "us"},
	}
	result := evaluateFlag(flagDef, "production", evalDict)
	assert.Equal(t, true, result)
}

// --- contextsToEvalDict tests ---

func TestContextsToEvalDict(t *testing.T) {
	contexts := []Context{
		{Type: "user", Key: "user-123", Attributes: map[string]interface{}{"plan": "enterprise"}},
		{Type: "account", Key: "acme", Attributes: map[string]interface{}{"region": "us"}},
	}
	result := contextsToEvalDict(contexts)
	assert.Len(t, result, 2)

	user := result["user"].(map[string]interface{})
	assert.Equal(t, "user-123", user["key"])
	assert.Equal(t, "enterprise", user["plan"])

	account := result["account"].(map[string]interface{})
	assert.Equal(t, "acme", account["key"])
	assert.Equal(t, "us", account["region"])
}

// --- hashContext tests ---

func TestHashContext_Stable(t *testing.T) {
	dict1 := map[string]interface{}{
		"user":    map[string]interface{}{"key": "u1", "plan": "enterprise"},
		"account": map[string]interface{}{"key": "a1"},
	}
	dict2 := map[string]interface{}{
		"account": map[string]interface{}{"key": "a1"},
		"user":    map[string]interface{}{"plan": "enterprise", "key": "u1"},
	}
	h1 := hashContext(dict1)
	h2 := hashContext(dict2)
	assert.Equal(t, h1, h2, "hash should be order-independent")
}

func TestHashContext_Different(t *testing.T) {
	dict1 := map[string]interface{}{"user": map[string]interface{}{"key": "u1"}}
	dict2 := map[string]interface{}{"user": map[string]interface{}{"key": "u2"}}
	assert.NotEqual(t, hashContext(dict1), hashContext(dict2))
}

// --- Typed flag handles tests ---

func TestBoolFlagHandle_Default(t *testing.T) {
	rt := newFlagsRuntime(nil)
	handle := rt.BoolFlag("feature-x", false)
	// Not connected — should return default.
	assert.Equal(t, false, handle.Get(context.Background()))
}

func TestStringFlagHandle_Default(t *testing.T) {
	rt := newFlagsRuntime(nil)
	handle := rt.StringFlag("theme", "light")
	assert.Equal(t, "light", handle.Get(context.Background()))
}

func TestNumberFlagHandle_Default(t *testing.T) {
	rt := newFlagsRuntime(nil)
	handle := rt.NumberFlag("max-items", 10.0)
	assert.Equal(t, 10.0, handle.Get(context.Background()))
}

func TestJsonFlagHandle_Default(t *testing.T) {
	rt := newFlagsRuntime(nil)
	dflt := map[string]interface{}{"color": "blue"}
	handle := rt.JsonFlag("settings", dflt)
	assert.Equal(t, dflt, handle.Get(context.Background()))
}

func TestFlagHandle_OnChange(t *testing.T) {
	rt := newFlagsRuntime(nil)
	handle := rt.BoolFlag("feature", true)

	var called bool
	handle.OnChange(func(e *FlagChangeEvent) {
		called = true
	})

	rt.fireChangeListeners("feature", "manual")
	assert.True(t, called)
}

// --- FlagsRuntime evaluation tests ---

func TestFlagsRuntime_EvaluateHandle_NotConnected(t *testing.T) {
	rt := newFlagsRuntime(nil)
	value := rt.evaluateHandle(context.Background(), "key", "default", nil)
	assert.Equal(t, "default", value)
}

func TestFlagsRuntime_EvaluateHandle_Connected_WithStore(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"feature-x": {
			"default": false,
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"enabled": true,
					"rules": []interface{}{
						map[string]interface{}{
							"logic": map[string]interface{}{
								"==": []interface{}{map[string]interface{}{"var": "user.plan"}, "enterprise"},
							},
							"value": true,
						},
					},
				},
			},
		},
	}
	rt.mu.Unlock()

	contexts := []Context{
		{Type: "user", Key: "u1", Attributes: map[string]interface{}{"plan": "enterprise"}},
	}
	value := rt.evaluateHandle(context.Background(), "feature-x", false, contexts)
	assert.Equal(t, true, value)
}

func TestFlagsRuntime_EvaluateHandle_CacheHit(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"key": {"default": "value", "environments": map[string]interface{}{}},
	}
	rt.mu.Unlock()

	// First call populates cache.
	v1 := rt.evaluateHandle(context.Background(), "key", "default", nil)
	// Second call should be a cache hit.
	v2 := rt.evaluateHandle(context.Background(), "key", "default", nil)
	assert.Equal(t, v1, v2)

	hits, _ := rt.cache.stats()
	assert.Equal(t, 1, hits)
}

func TestFlagsRuntime_EvaluateHandle_WithProvider(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default": false,
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"enabled": true,
					"rules": []interface{}{
						map[string]interface{}{
							"logic": map[string]interface{}{
								"==": []interface{}{map[string]interface{}{"var": "user.plan"}, "premium"},
							},
							"value": true,
						},
					},
				},
			},
		},
	}
	rt.mu.Unlock()

	rt.SetContextProvider(func(ctx context.Context) []Context {
		return []Context{
			{Type: "user", Key: "u1", Attributes: map[string]interface{}{"plan": "premium"}},
		}
	})

	value := rt.evaluateHandle(context.Background(), "feature", false, nil)
	assert.Equal(t, true, value)
}

func TestFlagsRuntime_EvaluateHandle_FlagMissing(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.mu.Lock()
	rt.connected = true
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{}
	rt.mu.Unlock()

	value := rt.evaluateHandle(context.Background(), "nonexistent", "fallback", nil)
	assert.Equal(t, "fallback", value)
}

// --- Change listeners tests ---

func TestFlagsRuntime_GlobalListeners(t *testing.T) {
	rt := newFlagsRuntime(nil)

	var events []*FlagChangeEvent
	var mu sync.Mutex
	rt.OnChange(func(e *FlagChangeEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	rt.fireChangeListeners("feature-x", "websocket")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 1)
	assert.Equal(t, "feature-x", events[0].Key)
	assert.Equal(t, "websocket", events[0].Source)
}

func TestFlagsRuntime_ListenerExceptionSwallowed(t *testing.T) {
	rt := newFlagsRuntime(nil)
	rt.OnChange(func(e *FlagChangeEvent) {
		panic("test panic")
	})
	// Should not propagate the panic.
	assert.NotPanics(t, func() {
		rt.fireChangeListeners("key", "manual")
	})
}

func TestFlagsRuntime_EmptyKeyNoListeners(t *testing.T) {
	rt := newFlagsRuntime(nil)
	called := false
	rt.OnChange(func(e *FlagChangeEvent) {
		called = true
	})
	rt.fireChangeListeners("", "manual") // empty key should not fire
	assert.False(t, called)
}

func TestFlagsRuntime_Stats(t *testing.T) {
	rt := newFlagsRuntime(nil)
	stats := rt.Stats()
	assert.Equal(t, 0, stats.CacheHits)
	assert.Equal(t, 0, stats.CacheMisses)
}

func TestFlagsRuntime_ConnectionStatus_Disconnected(t *testing.T) {
	rt := newFlagsRuntime(nil)
	assert.Equal(t, "disconnected", rt.ConnectionStatus())
}

// --- isTruthy tests ---

func TestIsTruthy(t *testing.T) {
	assert.True(t, isTruthy(true))
	assert.False(t, isTruthy(false))
	assert.True(t, isTruthy(1))
	assert.False(t, isTruthy(0))
	assert.True(t, isTruthy(1.0))
	assert.False(t, isTruthy(0.0))
	assert.True(t, isTruthy("hello"))
	assert.False(t, isTruthy(""))
	assert.False(t, isTruthy(nil))
	assert.True(t, isTruthy([]int{1}))
}
