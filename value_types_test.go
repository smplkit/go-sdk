package smplkit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	smplkit "github.com/smplkit/go-sdk/v3"
)

func TestFlagRule_DefensiveLogicCopy(t *testing.T) {
	logic := map[string]interface{}{"==": []interface{}{"a", "b"}}
	r := smplkit.NewFlagRule(logic, "served", "desc")

	// mutating the original logic must not affect the rule
	logic["new-key"] = true
	got := r.Logic()
	_, hasNew := got["new-key"]
	assert.False(t, hasNew)

	// mutating the returned map also must not affect future reads
	got["mutator"] = "yes"
	again := r.Logic()
	_, hasMutator := again["mutator"]
	assert.False(t, hasMutator)

	assert.Equal(t, "served", r.Value())
	assert.Equal(t, "desc", r.Description())
}

func TestFlagEnvironment_DefensiveRulesCopy(t *testing.T) {
	r1 := smplkit.NewFlagRule(nil, "x", "")
	r2 := smplkit.NewFlagRule(nil, "y", "")
	env := smplkit.NewFlagEnvironment(true, "default-val", []smplkit.FlagRule{r1, r2})

	got := env.Rules()
	assert.Len(t, got, 2)
	got[0] = smplkit.NewFlagRule(nil, "mutated", "")
	again := env.Rules()
	assert.Equal(t, "x", again[0].Value())

	assert.Equal(t, true, env.Enabled())
	assert.Equal(t, "default-val", env.Default())
}

func TestLoggerEnvironment_LevelClone(t *testing.T) {
	level := smplkit.LogLevelWarn
	env := smplkit.NewLoggerEnvironment(&level)

	// returned pointer should not alias the internal state
	got := env.Level()
	assert.NotNil(t, got)
	assert.Equal(t, smplkit.LogLevelWarn, *got)
	*got = smplkit.LogLevelError
	again := env.Level()
	assert.Equal(t, smplkit.LogLevelWarn, *again)

	// nil propagates
	emptyEnv := smplkit.NewLoggerEnvironment(nil)
	assert.Nil(t, emptyEnv.Level())
}

func TestConfigEnvironment_DefensiveValuesCopy(t *testing.T) {
	values := map[string]interface{}{"a": 1, "b": 2}
	env := smplkit.NewConfigEnvironment(values)

	values["c"] = 3
	got := env.Values()
	_, hasC := got["c"]
	assert.False(t, hasC)

	got["d"] = 4
	again := env.Values()
	_, hasD := again["d"]
	assert.False(t, hasD)
}
