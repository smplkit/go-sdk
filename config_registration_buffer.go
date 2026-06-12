package smplkit

import "sync"

// configBufferEntry is the wire-shaped payload for a single config in
// the bulk-discovery request. Items map to {value, type, description?}.
type configBufferEntry struct {
	id          string
	items       map[string]configBufferItem
	service     string
	environment string
	parent      string
	name        string
	description string
}

type configBufferItem struct {
	value       interface{}
	itemType    string
	description string
}

type configBufferMeta struct {
	service     string
	environment string
	parent      string
	name        string
	description string
}

// configRegistrationBuffer keeps per-config metadata across flushes so
// post-flush deltas re-attribute correctly, plus permanent (configID,
// itemKey) dedupe so already-sent items never re-send. Thread-safe.
type configRegistrationBuffer struct {
	mu        sync.Mutex
	pending   map[string]*configBufferEntry // ordered insertion is not guaranteed; bulk endpoint doesn't require order
	meta      map[string]configBufferMeta
	sentItems map[string]struct{} // key = configID + "::" + itemKey
}

func newConfigRegistrationBuffer() *configRegistrationBuffer {
	return &configRegistrationBuffer{
		pending:   make(map[string]*configBufferEntry),
		meta:      make(map[string]configBufferMeta),
		sentItems: make(map[string]struct{}),
	}
}

// declare registers a configuration. Idempotent within a process —
// first writer's metadata wins.
func (b *configRegistrationBuffer) declare(id string, meta configBufferMeta) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.meta[id]; ok {
		return
	}
	b.meta[id] = meta
	b.pending[id] = b.buildEntry(id, nil)
}

// addItem queues an item declaration for an already-declared config.
// Items already sent in a previous drain are skipped. Returns false if
// the config wasn't declared (caller is expected to declare first).
func (b *configRegistrationBuffer) addItem(configID, itemKey, itemType string, defaultVal interface{}, description string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.meta[configID]; !ok {
		return
	}
	if _, sent := b.sentItems[configID+"::"+itemKey]; sent {
		return
	}
	entry, ok := b.pending[configID]
	if !ok {
		// Post-drain delta — rebuild a pending entry from stored metadata.
		entry = b.buildEntry(configID, nil)
		b.pending[configID] = entry
	}
	if entry.items == nil {
		entry.items = make(map[string]configBufferItem)
	}
	if _, exists := entry.items[itemKey]; exists {
		return
	}
	entry.items[itemKey] = configBufferItem{value: defaultVal, itemType: itemType, description: description}
}

func (b *configRegistrationBuffer) buildEntry(id string, items map[string]configBufferItem) *configBufferEntry {
	meta := b.meta[id]
	return &configBufferEntry{
		id:          id,
		items:       items,
		service:     meta.service,
		environment: meta.environment,
		parent:      meta.parent,
		name:        meta.name,
		description: meta.description,
	}
}

// drain returns and clears the pending batch, marking sent items so
// they aren't re-queued by subsequent typed-getter calls.
func (b *configRegistrationBuffer) drain() []*configBufferEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		return nil
	}
	batch := make([]*configBufferEntry, 0, len(b.pending))
	for _, entry := range b.pending {
		batch = append(batch, entry)
		for itemKey := range entry.items {
			b.sentItems[entry.id+"::"+itemKey] = struct{}{}
		}
	}
	b.pending = make(map[string]*configBufferEntry)
	return batch
}

func (b *configRegistrationBuffer) pendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}
