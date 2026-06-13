package smplkit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmplClient_SetContext_StashAndRestore(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	c := fc.client

	assert.Nil(t, c.getAmbientContexts())

	scope := c.SetContext(context.Background(), []Context{
		NewContext("user", "u-1", map[string]interface{}{"plan": "enterprise"}),
	})
	got := c.getAmbientContexts()
	require.Len(t, got, 1)
	assert.Equal(t, "u-1", got[0].Key)

	// getAmbientContexts returns a copy — mutating it doesn't affect the stash.
	got[0].Key = "tampered"
	assert.Equal(t, "u-1", c.getAmbientContexts()[0].Key)

	// Restore reverts to the previously-active (empty) context.
	scope.Restore()
	assert.Nil(t, c.getAmbientContexts())

	// Restore is idempotent.
	scope.Restore()
	assert.Nil(t, c.getAmbientContexts())
}

func TestSmplClient_SetContext_NestedScopesRestore(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	c := fc.client

	outer := c.SetContext(context.Background(), []Context{NewContext("user", "outer", nil)})
	inner := c.SetContext(context.Background(), []Context{NewContext("user", "inner", nil)})
	assert.Equal(t, "inner", c.getAmbientContexts()[0].Key)

	require.NoError(t, inner.Close()) // Close reverts to outer and returns nil.
	assert.Equal(t, "outer", c.getAmbientContexts()[0].Key)

	outer.Restore()
	assert.Nil(t, c.getAmbientContexts())
}

func TestSmplClient_SetContext_RegistersWithPlatform(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	c := fc.client
	c.SetContext(context.Background(), []Context{
		NewContext("user", "u-1", nil),
		NewContext("account", "acme", nil),
	})
	assert.Equal(t, 2, c.platform.contexts.PendingCount(), "each context queued for background registration")
}

func TestSmplClient_SetContext_EmptyClearsAndRegistersNothing(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	c := fc.client
	c.SetContext(context.Background(), []Context{NewContext("user", "u-1", nil)})
	require.Equal(t, 1, c.platform.contexts.PendingCount())

	// An empty list clears the stash and enqueues no registration.
	scope := c.SetContext(context.Background(), nil)
	require.NotNil(t, scope)
	assert.Nil(t, c.getAmbientContexts())
	assert.Equal(t, 1, c.platform.contexts.PendingCount(), "empty SetContext registers nothing new")
}

func TestFlagsRuntime_EvaluateHandle_AmbientContext(t *testing.T) {
	fc, _ := newTestFlagsClient(t, nil)
	rt := fc.runtime
	rt.connected = true
	rt.mu.Lock()
	rt.environment = "production"
	rt.flagStore = map[string]map[string]interface{}{
		"feature": {
			"default": "off",
			"environments": map[string]interface{}{
				"production": map[string]interface{}{
					"enabled": true,
					"rules": []interface{}{
						map[string]interface{}{
							"logic": map[string]interface{}{
								"==": []interface{}{map[string]interface{}{"var": "user.plan"}, "enterprise"},
							},
							"value": "on",
						},
					},
				},
			},
		},
	}
	rt.mu.Unlock()

	// No ambient context and no provider → rule can't match → default value.
	assert.Equal(t, "off", rt.evaluateHandle(context.Background(), "feature", "off", nil))

	// SetContext to a matching user → ambient drives targeting → rule value.
	scope := fc.client.SetContext(context.Background(),
		[]Context{NewContext("user", "u-1", map[string]interface{}{"plan": "enterprise"})})
	assert.Equal(t, "on", rt.evaluateHandle(context.Background(), "feature", "off", nil))

	// Restoring clears the ambient context → back to the default value.
	scope.Restore()
	assert.Equal(t, "off", rt.evaluateHandle(context.Background(), "feature", "off", nil))
}

func TestFlagsRuntime_AmbientContexts_StandaloneNilClient(t *testing.T) {
	// A runtime whose FlagsClient has no parent SmplClient (standalone) has no
	// ambient stash to read.
	rt := newFlagsRuntime(&FlagsClient{}, newContextRegistrationBuffer())
	assert.Nil(t, rt.ambientContexts())
}

func TestContextScope_NilSafeRestore(t *testing.T) {
	var s *ContextScope
	s.Restore() // must not panic on a nil scope
}
