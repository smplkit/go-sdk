package smplkit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	smplkit "github.com/smplkit/go-sdk/v3"
)

func TestFlag_TypedEnvironments(t *testing.T) {
	f := &smplkit.Flag{
		Environments: map[string]interface{}{
			"production": map[string]interface{}{
				"enabled": true,
				"default": "blue",
				"rules": []interface{}{
					map[string]interface{}{
						"logic":       map[string]interface{}{"==": []interface{}{1, 1}},
						"value":       "served",
						"description": "always",
					},
				},
			},
			"staging": map[string]interface{}{
				"enabled": false,
			},
			"untyped-env": "not-a-map",
		},
	}

	typed := f.TypedEnvironments()

	prod := typed["production"]
	assert.True(t, prod.Enabled())
	assert.Equal(t, "blue", prod.Default())
	rules := prod.Rules()
	assert.Len(t, rules, 1)
	assert.Equal(t, "served", rules[0].Value())
	assert.Equal(t, "always", rules[0].Description())
	assert.NotEmpty(t, rules[0].Logic())

	staging := typed["staging"]
	assert.False(t, staging.Enabled())
	assert.Nil(t, staging.Default())
	assert.Empty(t, staging.Rules())

	// Defensive copy: mutating the returned map doesn't change the source.
	delete(typed, "production")
	assert.Contains(t, f.Environments, "production")

	// Untyped wire entry produces a zero FlagEnvironment without panicking.
	zero := typed["untyped-env"]
	assert.False(t, zero.Enabled())
	assert.Nil(t, zero.Default())
	assert.Empty(t, zero.Rules())
}

func TestLogger_TypedEnvironments(t *testing.T) {
	l := &smplkit.Logger{
		Environments: map[string]interface{}{
			"production": map[string]interface{}{"level": "ERROR"},
			"staging":    map[string]interface{}{"level": ""},
			"missing":    "not-a-map",
		},
	}
	typed := l.TypedEnvironments()

	prod := typed["production"]
	require := func() *smplkit.LogLevel { v := smplkit.LogLevelError; return &v }()
	assert.NotNil(t, prod.Level())
	assert.Equal(t, *require, *prod.Level())

	staging := typed["staging"]
	assert.Nil(t, staging.Level())

	missing := typed["missing"]
	assert.Nil(t, missing.Level())
}

func TestLogGroup_TypedEnvironments(t *testing.T) {
	g := &smplkit.LogGroup{
		Environments: map[string]interface{}{
			"production": map[string]interface{}{"level": "WARN"},
		},
	}
	typed := g.TypedEnvironments()
	prod := typed["production"]
	assert.NotNil(t, prod.Level())
	assert.Equal(t, smplkit.LogLevelWarn, *prod.Level())
}

func TestConfigEntry_TypedEnvironments(t *testing.T) {
	c := &smplkit.ConfigEntry{
		Environments: map[string]map[string]interface{}{
			"production": {"host": "prod-host", "max_retries": 5},
			"staging":    {"max_retries": 2},
		},
	}
	typed := c.TypedEnvironments()
	assert.Equal(t, "prod-host", typed["production"].Values()["host"])
	assert.Equal(t, 5, typed["production"].Values()["max_retries"])
	assert.Equal(t, 2, typed["staging"].Values()["max_retries"])

	// Defensive copy on the values accessor.
	mutated := typed["production"].Values()
	mutated["host"] = "mutated"
	again := typed["production"].Values()
	assert.Equal(t, "prod-host", again["host"])
}
