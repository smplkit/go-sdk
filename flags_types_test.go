package smplkit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	smplkit "github.com/smplkit/go-sdk"
)

func TestFlagType_Constants(t *testing.T) {
	assert.Equal(t, smplkit.FlagType("BOOLEAN"), smplkit.FlagTypeBoolean)
	assert.Equal(t, smplkit.FlagType("STRING"), smplkit.FlagTypeString)
	assert.Equal(t, smplkit.FlagType("NUMERIC"), smplkit.FlagTypeNumeric)
	assert.Equal(t, smplkit.FlagType("JSON"), smplkit.FlagTypeJSON)
}

func TestNewContext_WithMap(t *testing.T) {
	ctx := smplkit.NewContext("user", "user-123", map[string]interface{}{
		"plan":      "enterprise",
		"firstName": "Alice",
	})
	assert.Equal(t, "user", ctx.Type)
	assert.Equal(t, "user-123", ctx.Key)
	assert.Equal(t, "", ctx.Name)
	assert.Equal(t, "enterprise", ctx.Attributes["plan"])
	assert.Equal(t, "Alice", ctx.Attributes["firstName"])
}

func TestNewContext_WithOptions(t *testing.T) {
	ctx := smplkit.NewContext("user", "user-123", nil,
		smplkit.WithName("Alice Smith"),
		smplkit.WithAttr("plan", "enterprise"),
	)
	assert.Equal(t, "user", ctx.Type)
	assert.Equal(t, "user-123", ctx.Key)
	assert.Equal(t, "Alice Smith", ctx.Name)
	assert.Equal(t, "enterprise", ctx.Attributes["plan"])
}

func TestNewContext_MapAndOptions(t *testing.T) {
	ctx := smplkit.NewContext("user", "user-123",
		map[string]interface{}{"plan": "enterprise"},
		smplkit.WithName("Alice"),
		smplkit.WithAttr("region", "us"),
	)
	assert.Equal(t, "Alice", ctx.Name)
	assert.Equal(t, "enterprise", ctx.Attributes["plan"])
	assert.Equal(t, "us", ctx.Attributes["region"])
}

func TestNewContext_NilAttrs(t *testing.T) {
	ctx := smplkit.NewContext("device", "dev-1", nil)
	assert.NotNil(t, ctx.Attributes)
	assert.Empty(t, ctx.Attributes)
}

func TestRule_SingleCondition(t *testing.T) {
	rule := smplkit.NewRule("Enable for enterprise").
		When("user.plan", "==", "enterprise").
		Serve(true).
		Build()

	assert.Equal(t, "Enable for enterprise", rule["description"])
	assert.Equal(t, true, rule["value"])
	logic := rule["logic"].(map[string]interface{})
	assert.Contains(t, logic, "==")
}

func TestRule_MultipleConditions(t *testing.T) {
	rule := smplkit.NewRule("Multi-condition rule").
		When("user.plan", "==", "enterprise").
		When("account.region", "==", "us").
		Serve("blue").
		Build()

	logic := rule["logic"].(map[string]interface{})
	assert.Contains(t, logic, "and")
	conditions := logic["and"].([]interface{})
	assert.Len(t, conditions, 2)
}

func TestRule_WithEnvironment(t *testing.T) {
	rule := smplkit.NewRule("Env rule").
		Environment("production").
		When("user.plan", "==", "premium").
		Serve("red").
		Build()

	assert.Equal(t, "production", rule["environment"])
}

func TestRule_NoConditions(t *testing.T) {
	rule := smplkit.NewRule("Catch-all").
		Serve("default-value").
		Build()

	logic := rule["logic"].(map[string]interface{})
	assert.Empty(t, logic)
}

func TestRule_ContainsOperator(t *testing.T) {
	rule := smplkit.NewRule("Contains check").
		When("user.tags", "contains", "beta").
		Serve(true).
		Build()

	logic := rule["logic"].(map[string]interface{})
	assert.Contains(t, logic, "in")
	operands := logic["in"].([]interface{})
	assert.Equal(t, "beta", operands[0])
}

func TestRule_NumericOperators(t *testing.T) {
	tests := []struct {
		op string
	}{
		{">"}, {"<"}, {">="}, {"<="}, {"!="}, {"in"},
	}
	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			rule := smplkit.NewRule("test").
				When("user.age", tt.op, 18).
				Serve(true).
				Build()
			logic := rule["logic"].(map[string]interface{})
			assert.Contains(t, logic, tt.op)
		})
	}
}
