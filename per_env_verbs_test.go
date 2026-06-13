package smplkit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk/v3"
)

func TestFlag_EnableRules_PerEnv(t *testing.T) {
	f := &smplkit.Flag{Environments: map[string]interface{}{}}
	// Non-empty environment routes through SetEnvironmentEnabled.
	f.EnableRules("production")
	prod, ok := f.Environments["production"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, prod["enabled"])
}

func TestFlag_RemoveValue_NilMismatch(t *testing.T) {
	f := &smplkit.Flag{}
	f.AddValue("blue", "blue")
	// value=nil vs an existing non-nil value exercises fmtEqual's
	// one-nil-one-not branch (no match → nothing removed).
	f.RemoveValue(nil)
	require.NotNil(t, f.Values)
	assert.Len(t, *f.Values, 1)
}

func TestFlag_RemoveValue_BothNil(t *testing.T) {
	f := &smplkit.Flag{}
	f.AddValue("blank", nil)
	// value=nil vs an existing nil value exercises fmtEqual's both-nil
	// branch (match → removed).
	f.RemoveValue(nil)
	require.NotNil(t, f.Values)
	assert.Len(t, *f.Values, 0)
}

func TestFlag_SetDefault_BaseAndEnv(t *testing.T) {
	f := &smplkit.Flag{
		Default:      false,
		Environments: map[string]interface{}{},
	}

	// base default
	f.SetDefault(true, "")
	assert.Equal(t, true, f.Default)

	// per-env default — creates the env if needed
	f.SetDefault("blue", "production")
	prod, _ := f.Environments["production"].(map[string]interface{})
	assert.Equal(t, "blue", prod["default"])

	// changing env default doesn't disturb base default
	assert.Equal(t, true, f.Default)
}

func TestFlag_ClearDefault(t *testing.T) {
	f := &smplkit.Flag{
		Environments: map[string]interface{}{
			"production": map[string]interface{}{
				"default": "blue",
				"enabled": true,
			},
		},
	}

	// clearing with empty environment is a no-op (matches Python's required-kwarg)
	f.ClearDefault("")
	assert.Contains(t, f.Environments["production"].(map[string]interface{}), "default")

	// clearing with explicit env removes the override
	f.ClearDefault("production")
	prod, _ := f.Environments["production"].(map[string]interface{})
	_, hasDefault := prod["default"]
	assert.False(t, hasDefault)
	// other env state preserved
	assert.Equal(t, true, prod["enabled"])
}

func TestFlag_EnableRules_AllEnvs(t *testing.T) {
	f := &smplkit.Flag{
		Environments: map[string]interface{}{
			"production": map[string]interface{}{"enabled": false},
			"staging":    map[string]interface{}{"enabled": false},
		},
	}
	f.EnableRules("")
	for env := range f.Environments {
		envData := f.Environments[env].(map[string]interface{})
		assert.Equal(t, true, envData["enabled"], "env=%s", env)
	}
}

func TestFlag_DisableRules_OneEnv(t *testing.T) {
	f := &smplkit.Flag{
		Environments: map[string]interface{}{
			"production": map[string]interface{}{"enabled": true},
			"staging":    map[string]interface{}{"enabled": true},
		},
	}
	f.DisableRules("production")
	prod := f.Environments["production"].(map[string]interface{})
	staging := f.Environments["staging"].(map[string]interface{})
	assert.Equal(t, false, prod["enabled"])
	assert.Equal(t, true, staging["enabled"])
}

func TestFlag_DisableRules_AllEnvs(t *testing.T) {
	f := &smplkit.Flag{
		Environments: map[string]interface{}{
			"production": map[string]interface{}{"enabled": true},
			"staging":    map[string]interface{}{"enabled": true},
		},
	}
	f.DisableRules("")
	prod := f.Environments["production"].(map[string]interface{})
	staging := f.Environments["staging"].(map[string]interface{})
	assert.Equal(t, false, prod["enabled"])
	assert.Equal(t, false, staging["enabled"])
}

func TestFlag_RemoveValue_NumericAndNil(t *testing.T) {
	// Exercise the numeric-equality branch of fmtEqual so its coverage rises.
	f := &smplkit.Flag{}
	f.AddValue("Three", 3).AddValue("Four", 4)
	f.RemoveValue(3)
	assert.Len(t, *f.Values, 1)
	assert.Equal(t, 4, (*f.Values)[0].Value)

	// Removing nil from a non-nil values list is a no-op.
	f.RemoveValue(nil)
	assert.Len(t, *f.Values, 1)
}

func TestFlag_ClearRulesAll(t *testing.T) {
	f := &smplkit.Flag{
		Environments: map[string]interface{}{
			"production": map[string]interface{}{
				"rules": []interface{}{map[string]interface{}{"value": true}},
			},
			"staging": map[string]interface{}{
				"rules": []interface{}{map[string]interface{}{"value": false}},
			},
		},
	}
	f.ClearRulesAll()
	for env := range f.Environments {
		envData := f.Environments[env].(map[string]interface{})
		rules, _ := envData["rules"].([]interface{})
		assert.Empty(t, rules, "env=%s", env)
	}
}

func TestFlag_AddValue_RemoveValue_ClearValues(t *testing.T) {
	f := &smplkit.Flag{}

	// adding to a nil values list creates the slice
	f.AddValue("Red", "red").AddValue("Blue", "blue")
	assert.Len(t, *f.Values, 2)

	// removing by Value field
	f.RemoveValue("red")
	assert.Len(t, *f.Values, 1)
	assert.Equal(t, "blue", (*f.Values)[0].Value)

	// removing a non-existent value is a no-op
	f.RemoveValue("not-there")
	assert.Len(t, *f.Values, 1)

	// clearing returns to unconstrained
	f.ClearValues()
	assert.Nil(t, f.Values)

	// removing on a nil values list is a no-op
	f.RemoveValue("anything")
	assert.Nil(t, f.Values)
}

func TestLogger_SetClearLevel_BaseAndEnv(t *testing.T) {
	l := &smplkit.Logger{}

	// set base level
	l.SetLevel(smplkit.LogLevelInfo, "")
	assert.NotNil(t, l.Level)
	assert.Equal(t, smplkit.LogLevelInfo, *l.Level)

	// set per-env level
	l.SetLevel(smplkit.LogLevelDebug, "staging")
	envData, ok := l.Environments["staging"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "DEBUG", envData["level"])

	// clear base
	l.ClearLevel("")
	assert.Nil(t, l.Level)

	// clear per-env
	l.ClearLevel("staging")
	if envData, ok := l.Environments["staging"].(map[string]interface{}); ok {
		_, hasLevel := envData["level"]
		assert.False(t, hasLevel)
	}
}

func TestConfigEntry_SetX_BaseAndEnv(t *testing.T) {
	c := &smplkit.ConfigEntry{}

	c.SetString("name", "service-a", "")
	c.SetNumber("retries", 3, "")
	c.SetBoolean("enabled", true, "")
	c.SetJSON("flags", map[string]interface{}{"x": 1}, "")
	assert.Equal(t, "service-a", c.Items["name"])
	assert.Equal(t, float64(3), c.Items["retries"])
	assert.Equal(t, true, c.Items["enabled"])
	assert.Equal(t, map[string]interface{}{"x": 1}, c.Items["flags"])

	c.SetString("name", "service-b", "production")
	// Per ADR-024 §2.4 per-env overrides are flat — no "values" envelope.
	prodVals := c.Environments["production"]
	require.NotNil(t, prodVals, "per-env override map must exist after setItem")
	assert.Equal(t, "service-b", prodVals["name"])
}

func TestConfigEntry_Remove_BaseAndEnv(t *testing.T) {
	c := &smplkit.ConfigEntry{}
	c.SetString("name", "x", "")
	c.SetString("name", "y", "production")

	c.Remove("name", "production")
	// Per ADR-024 §2.4 per-env overrides are flat — no "values" envelope.
	_, hasName := c.Environments["production"]["name"]
	assert.False(t, hasName)
	assert.Equal(t, "x", c.Items["name"]) // base preserved

	c.Remove("name", "")
	_, hasBaseName := c.Items["name"]
	assert.False(t, hasBaseName)

	// removing on nil maps is a no-op
	empty := &smplkit.ConfigEntry{}
	empty.Remove("name", "")
	empty.Remove("name", "production")

	// removing a per-env item when the env exists but has no entries is a no-op
	noEntries := &smplkit.ConfigEntry{
		Environments: map[string]map[string]interface{}{
			"production": {},
		},
	}
	noEntries.Remove("name", "production")

	// removing from an env that isn't present in a non-nil Environments map is a no-op
	noEnv := &smplkit.ConfigEntry{
		Environments: map[string]map[string]interface{}{
			"production": {"name": "x"},
		},
	}
	noEnv.Remove("name", "staging")
}

func TestConfigEntry_SetX_WithDescription_ItemsRaw(t *testing.T) {
	c := &smplkit.ConfigEntry{}

	c.SetString("name", "service-a", "", smplkit.WithItemDescription("the service name"))
	c.SetNumber("retries", 3, "")
	c.SetBoolean("enabled", true, "", smplkit.WithItemDescription("feature toggle"))
	c.SetJSON("flags", map[string]interface{}{"x": 1}, "")

	raw := c.ItemsRaw()
	require.NotNil(t, raw)

	// String item carries value, type, and description.
	assert.Equal(t, "service-a", raw["name"]["value"])
	assert.Equal(t, "STRING", raw["name"]["type"])
	assert.Equal(t, "the service name", raw["name"]["description"])

	// Numeric item with no description omits the description key.
	assert.Equal(t, float64(3), raw["retries"]["value"])
	assert.Equal(t, "NUMBER", raw["retries"]["type"])
	_, hasDesc := raw["retries"]["description"]
	assert.False(t, hasDesc)

	assert.Equal(t, "BOOLEAN", raw["enabled"]["type"])
	assert.Equal(t, "feature toggle", raw["enabled"]["description"])
	assert.Equal(t, "JSON", raw["flags"]["type"])

	// ItemsRaw is a deep copy — mutating it does not affect the entry.
	raw["name"]["value"] = "tampered"
	assert.Equal(t, "service-a", c.ItemsRaw()["name"]["value"])
}

func TestConfigEntry_ItemsRaw_NilWhenNoItems(t *testing.T) {
	c := &smplkit.ConfigEntry{}
	assert.Nil(t, c.ItemsRaw())
}

func TestConfigEntry_Set_GenericConfigItem(t *testing.T) {
	c := &smplkit.ConfigEntry{}

	// Base item carries the ConfigItem's declared type and description.
	c.Set(smplkit.ConfigItem{
		Name:        "tier",
		Value:       "enterprise",
		Type:        smplkit.ItemTypeString,
		Description: "billing tier",
	}, "")
	assert.Equal(t, "enterprise", c.Items["tier"])
	raw := c.ItemsRaw()
	assert.Equal(t, "STRING", raw["tier"]["type"])
	assert.Equal(t, "billing tier", raw["tier"]["description"])

	// A per-environment override stores only the raw value; type/description
	// are ignored (they come from the base item).
	c.Set(smplkit.ConfigItem{
		Name:        "tier",
		Value:       "pro",
		Type:        smplkit.ItemTypeJSON,
		Description: "ignored for env override",
	}, "production")
	assert.Equal(t, "pro", c.Environments["production"]["tier"])
	_, hasType := c.Environments["production"]["tier"].(map[string]interface{})
	assert.False(t, hasType, "env override is a flat raw value, not a typed envelope")
}

func TestItemType_Constants(t *testing.T) {
	assert.Equal(t, smplkit.ItemType("STRING"), smplkit.ItemTypeString)
	assert.Equal(t, smplkit.ItemType("NUMBER"), smplkit.ItemTypeNumber)
	assert.Equal(t, smplkit.ItemType("BOOLEAN"), smplkit.ItemTypeBoolean)
	assert.Equal(t, smplkit.ItemType("JSON"), smplkit.ItemTypeJSON)
}

func TestFlag_AddRule_RejectsEmptyEnvironment(t *testing.T) {
	// Rule built without Environment() — AddRule must reject so the
	// boundary check holds even if customers skip the builder kwarg.
	f := &smplkit.Flag{Environments: map[string]interface{}{}}
	rule := smplkit.NewRule("no env").Serve(true).Build()
	err := f.AddRule(rule)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "environment")
}

func TestEnvironment_TypedColor_RoundTrip(t *testing.T) {
	hex := "#ef4444"
	e := &smplkit.Environment{Color: &hex}
	tc := e.TypedColor()
	assert.Equal(t, "#ef4444", tc.Hex())

	// Setting via Color round-trips back to hex string.
	c, err := smplkit.NewColor("#0066cc")
	require.NoError(t, err)
	e.SetTypedColor(c)
	assert.Equal(t, "#0066cc", *e.Color)

	// Setting zero clears.
	e.SetTypedColor(smplkit.Color{})
	assert.Nil(t, e.Color)

	// Reading nil color returns zero.
	assert.True(t, e.TypedColor().IsZero())

	// Malformed hex on the wire returns zero rather than panicking.
	bad := "not-a-hex"
	e.Color = &bad
	assert.True(t, e.TypedColor().IsZero())
}

func TestContext_PanicsOnEmpty(t *testing.T) {
	assert.Panics(t, func() { smplkit.NewContext("", "key", nil) })
	assert.Panics(t, func() { smplkit.NewContext("user", "", nil) })
}

func TestContext_CompositeID_Verbs(t *testing.T) {
	c := smplkit.NewContext("user", "u-123", nil)
	assert.Equal(t, "user:u-123", c.CompositeID())
}
