package smplkit

import (
	"context"
	"testing"

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
		{configKey: "", itemKey: "", cb: func(evt *ConfigChangeEvent) {
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
	assert.Equal(t, "app", events[0].ConfigKey)
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

func TestDiffAndFire_FiltersByConfigKey(t *testing.T) {
	c := &ConfigClient{}

	var events []*ConfigChangeEvent
	c.listeners = []configChangeListener{
		{configKey: "db", cb: func(evt *ConfigChangeEvent) {
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
	assert.Equal(t, "db", events[0].ConfigKey)
}

func TestDiffAndFire_FiltersByItemKey(t *testing.T) {
	c := &ConfigClient{}

	var events []*ConfigChangeEvent
	c.listeners = []configChangeListener{
		{configKey: "app", itemKey: "a", cb: func(evt *ConfigChangeEvent) {
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
	assert.Equal(t, "app", events[0].ConfigKey)
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
	assert.Equal(t, "app", events[0].ConfigKey)
	assert.Equal(t, 1, events[0].OldValue)
	assert.Nil(t, events[0].NewValue)
}
