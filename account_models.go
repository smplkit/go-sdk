package smplkit

// Active-record model for client.Account().* resources.

import "context"

// AccountSettings holds per-account configuration.
// The wire format is an opaque JSON object; documented keys are exposed as
// typed accessors, and all keys (known and unknown) are preserved in Raw.
// Mutate via the setters and call Save(ctx) to persist.
type AccountSettings struct {
	// Raw is the full settings map. Mutations here are persisted on Save.
	Raw map[string]interface{}

	client *SettingsClient
}

// EnvironmentOrder returns the canonical ordering of STANDARD environments,
// or nil when no order has been set.
func (s *AccountSettings) EnvironmentOrder() []string {
	raw, ok := s.Raw["environment_order"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// SetEnvironmentOrder sets the canonical ordering of STANDARD environments.
// order is the environment identifiers in the desired order. Call Save(ctx)
// to persist.
func (s *AccountSettings) SetEnvironmentOrder(order []string) {
	if s.Raw == nil {
		s.Raw = make(map[string]interface{})
	}
	cp := make([]interface{}, len(order))
	for i, v := range order {
		cp[i] = v
	}
	s.Raw["environment_order"] = cp
}

// Save writes the full settings object back to the server.
func (s *AccountSettings) Save(ctx context.Context) error {
	if s.client == nil {
		return &Error{Message: "AccountSettings was constructed without a client; cannot save"}
	}
	return s.client.save(ctx, s)
}

func (s *AccountSettings) apply(other *AccountSettings) {
	s.Raw = other.Raw
}
