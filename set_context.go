package smplkit

import (
	"context"
	"sync"
)

// ContextScope is returned by SmplClient.SetContext.
//
// Using it is optional: a bare client.SetContext(ctx, [...]) is fire-and-forget
// — the typical middleware pattern, where the context is set once at request
// entry and every context-sensitive evaluation inside that request picks it up.
// Capture the returned scope and defer Restore (or Close) to revert to the
// previously-active context on exit, useful for scoped overrides like
// impersonation:
//
//	defer client.SetContext(ctx, []smplkit.Context{userCtx}).Restore()
type ContextScope struct {
	restore func()
	once    sync.Once
}

// Restore reverts the active evaluation context to whatever was set before the
// SetContext call that produced this scope. Safe to call more than once; only
// the first call has any effect.
func (s *ContextScope) Restore() {
	if s == nil {
		return
	}
	s.once.Do(s.restore)
}

// Close calls Restore and returns nil so a ContextScope can be used with the
// io.Closer idiom (for example defer scope.Close()).
func (s *ContextScope) Close() error {
	s.Restore()
	return nil
}

// SetContext stashes contexts as the active evaluation context for
// context-sensitive evaluations (flag.Get today) and registers each one with
// the platform in the background.
//
// Typical use is from middleware — set the context once at request entry and
// every flag.Get inside that request automatically evaluates against it,
// without threading the contexts through every call. Each unique (type, key)
// is also queued for registration with the platform (deduplicated; sent in the
// background).
//
// Two usage shapes:
//
//	// Fire-and-forget (typical middleware)
//	client.SetContext(ctx, []smplkit.Context{userCtx, accountCtx})
//
//	// Scoped override (e.g. impersonation) — revert on return
//	defer client.SetContext(ctx, []smplkit.Context{impersonated}).Restore()
//
// An empty contexts slice clears the active context (and skips registration)
// but still returns a scope. This is a separate capability from
// FlagsClient.SetContextProvider: a provider, when set, takes precedence over
// the SetContext stash during evaluation.
//
// Returns a ContextScope you can ignore for fire-and-forget use, or whose
// Restore / Close reverts to the previously-active context.
func (c *SmplClient) SetContext(ctx context.Context, contexts []Context) *ContextScope {
	c.ensureStarted()

	c.ambientMu.Lock()
	prev := c.ambientContexts
	next := make([]Context, len(contexts))
	copy(next, contexts)
	c.ambientContexts = next
	c.ambientMu.Unlock()

	if len(contexts) > 0 {
		// Queue each unique (type, key) for background registration with the
		// platform; the periodic flush (armed by ensureStarted) drains it.
		_ = c.platform.contexts.Register(ctx, contexts)
	}

	return &ContextScope{restore: func() {
		c.ambientMu.Lock()
		c.ambientContexts = prev
		c.ambientMu.Unlock()
	}}
}

// getAmbientContexts returns a copy of the contexts stashed by the most recent
// SetContext call, or nil when none are active. The flags runtime reads this
// as a fallback ambient source when no explicit contexts and no context
// provider are supplied.
func (c *SmplClient) getAmbientContexts() []Context {
	c.ambientMu.RLock()
	defer c.ambientMu.RUnlock()
	if len(c.ambientContexts) == 0 {
		return nil
	}
	out := make([]Context, len(c.ambientContexts))
	copy(out, c.ambientContexts)
	return out
}
