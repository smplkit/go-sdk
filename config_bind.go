package smplkit

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// BindOption configures a Bind call.
type BindOption func(*bindOptions)

type bindOptions struct {
	parent interface{}
}

// WithBindParent declares the named target as inheriting from a previously
// bound parent. The parent must be the same object reference previously
// returned from a successful Bind call on this client. Activates
// parent-chain inheritance at the server side for any keys the caller
// omitted from the target (map-target form only — struct fields always
// register as explicit overrides).
func WithBindParent(parent interface{}) BindOption {
	return func(o *bindOptions) { o.parent = parent }
}

// Bind declares a configuration from code and registers `target` as the
// live, in-place-mutated handle for it.
//
// `target` must be a non-nil pointer to a struct, or a non-nil
// `map[string]interface{}`. Both kinds are reference types in Go — the
// SDK mutates them in place when the server pushes updates, so the
// caller's reference always sees the current resolved value.
//
//   - Struct pointer: every exported field is registered as a config item,
//     using the field's `json` tag if present (without the `,omitempty`
//     suffix), else the field name. Nested struct fields flatten to
//     dot-notation. Every field is treated as an explicit override — Go's
//     type system has no equivalent of Pydantic's `model_fields_set`, so
//     omit-to-inherit semantics are not available on the struct path. Use
//     a `map[string]interface{}` if you need omit-to-inherit.
//   - Map: every key in the map is registered as a config item; values are
//     used as the in-code defaults; nested maps flatten to dot-notation.
//     Keys absent from the map inherit from the parent at the server side.
//
// Idempotent. Repeated calls with the same `id` return without
// re-registering — the originally bound target keeps receiving updates.
//
// After Bind returns, the local cache has been synced into `target` for
// any keys the server already had values for. On every subsequent
// server-pushed change, the SDK mutates `target` in place.
//
// Mirrors python-sdk's `client.config.bind()` and typescript-sdk's
// `client.config.bind()`.
func (c *ConfigClient) Bind(ctx context.Context, id string, target interface{}, opts ...BindOption) error {
	if target == nil {
		return &Error{Message: "bind: target cannot be nil"}
	}

	rv := reflect.ValueOf(target)
	if !isBindableTarget(rv) {
		return &Error{Message: fmt.Sprintf(
			"bind: target must be a non-nil pointer to a struct or a map[string]interface{}; got %T",
			target,
		)}
	}

	c.bindingsMu.Lock()
	if c.bindings == nil {
		c.bindings = make(map[string]interface{})
	}
	if existing, ok := c.bindings[id]; ok {
		c.bindingsMu.Unlock()
		_ = existing // already bound; subsequent target argument ignored
		return nil
	}
	c.bindingsMu.Unlock()

	var o bindOptions
	for _, opt := range opts {
		opt(&o)
	}

	parentID := ""
	if o.parent != nil {
		var ok bool
		parentID, ok = c.configIDForBoundTarget(o.parent)
		if !ok {
			return &Error{Message: "bind: parent must be a target previously bound via client.Config().Bind(). Bind the parent first."}
		}
	}

	name := bindNameForTarget(rv)
	c.observeConfigDeclaration(id, parentID, name, "")

	for _, item := range iterBindItems(rv) {
		c.observeItemDeclaration(id, item.key, item.itemType, item.value, "")
	}

	// Register the binding BEFORE ensureInit so that any pushed events
	// arriving during the initial fetch find the binding and route to it.
	c.bindingsMu.Lock()
	c.bindings[id] = target
	c.bindingsMu.Unlock()

	if err := c.ensureInit(ctx); err != nil {
		return err
	}
	c.syncTargetFromCache(target, id)
	return nil
}

// GetValue returns the resolved value of `key` within `configID`. Returns
// a NotFoundError if either the config or the key is missing. Does not
// register the key — use GetValueOr if you want default-on-miss + code-
// first console observability.
func (c *ConfigClient) GetValue(ctx context.Context, configID, key string) (interface{}, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	resolved, ok := c.configCache[configID]
	if !ok {
		return nil, &NotFoundError{Base: Error{Message: fmt.Sprintf("config with id %q not found", configID)}}
	}
	val, ok := resolved[key]
	if !ok {
		return nil, &NotFoundError{Base: Error{Message: fmt.Sprintf("config item %q not found in config %q", key, configID)}}
	}
	return val, nil
}

// GetValueOr returns the resolved value of `key` within `configID`, or
// `defaultValue` if either the config or the key is missing. Never
// returns an error. Auto-registers the config and key (with
// `defaultValue` as the in-code default) for code-first console
// observability — operators see the reference in the smplkit console
// even when no schema was declared via Bind.
//
// The item type is inferred from `defaultValue`'s Go runtime type:
// bool → BOOLEAN, int / int64 / float / float64 → NUMBER, string →
// STRING, anything else → STRING (universal fallback; an admin can
// retype the item in the console).
func (c *ConfigClient) GetValueOr(ctx context.Context, configID, key string, defaultValue interface{}) interface{} {
	// Even if init/lookup fails below, we still want the discovery payload
	// queued so the console sees the reference. Doing it up front is fine —
	// the buffer is idempotent at the (configID, itemKey) level.
	c.observeConfigDeclaration(configID, "", "", "")
	c.observeItemDeclaration(configID, key, valueToItemType(defaultValue), defaultValue, "")

	if err := c.ensureInit(ctx); err != nil {
		return defaultValue
	}
	resolved, ok := c.configCache[configID]
	if !ok {
		return defaultValue
	}
	val, ok := resolved[key]
	if !ok {
		return defaultValue
	}
	return val
}

// ----------------------------------------------------------------------
// Internal: reflection-based introspection and mutation
// ----------------------------------------------------------------------

// isBindableTarget reports whether `rv` is a valid target for Bind:
// either a non-nil pointer to a struct, or a non-nil map keyed by string.
// An invalid reflect.Value (e.g. from a nil interface) falls through to
// the default branch and reports false.
func isBindableTarget(rv reflect.Value) bool {
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return false
		}
		return rv.Elem().Kind() == reflect.Struct
	case reflect.Map:
		if rv.IsNil() {
			return false
		}
		return rv.Type().Key().Kind() == reflect.String
	default:
		return false
	}
}

// bindNameForTarget returns a human-friendly console name derived from
// the target's runtime type. For struct pointers, this is the struct's
// type name; for maps, the empty string (no class metadata to expose).
func bindNameForTarget(rv reflect.Value) string {
	if rv.Kind() == reflect.Pointer {
		return rv.Elem().Type().Name()
	}
	return ""
}

// bindItem is a single (key, type, value) triple destined for the
// registration buffer.
type bindItem struct {
	key      string
	itemType string
	value    interface{}
}

// iterBindItems walks a bindable target and yields the flattened items
// for registration. Nested structs and nested maps both flatten to dot-
// notation. Slices, arrays, and unsupported leaves fall back to STRING
// type registration (the admin can retype them in the console).
func iterBindItems(rv reflect.Value) []bindItem {
	out := make([]bindItem, 0)
	switch rv.Kind() {
	case reflect.Pointer:
		out = append(out, iterStructBindItems(rv.Elem(), "")...)
	case reflect.Map:
		out = append(out, iterMapBindItems(rv, "")...)
	}
	return out
}

// iterStructBindItems walks the exported fields of a struct via reflection.
// Field keys come from the json tag (when present and non-empty/non-"-"),
// else the field name verbatim.
func iterStructBindItems(structVal reflect.Value, prefix string) []bindItem {
	out := make([]bindItem, 0)
	t := structVal.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		key := bindKeyFromField(field)
		if key == "" {
			continue // explicit `json:"-"` — skip
		}
		flatKey := prefix + key
		fv := structVal.Field(i)
		out = append(out, walkBindLeaf(fv, flatKey)...)
	}
	return out
}

// iterMapBindItems walks a map with string keys.
func iterMapBindItems(mapVal reflect.Value, prefix string) []bindItem {
	out := make([]bindItem, 0)
	iter := mapVal.MapRange()
	for iter.Next() {
		key := iter.Key().String()
		flatKey := prefix + key
		out = append(out, walkBindLeaf(iter.Value(), flatKey)...)
	}
	return out
}

// walkBindLeaf decides whether the given value is a nested container (struct
// or map) that should be recursed into, or a leaf that lands directly in
// the output. Interface values are unwrapped to their concrete kind first.
func walkBindLeaf(v reflect.Value, flatKey string) []bindItem {
	// Unwrap interfaces / nil pointers.
	for v.Kind() == reflect.Interface && !v.IsNil() {
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		return iterStructBindItems(v, flatKey+".")
	case reflect.Map:
		if v.Type().Key().Kind() == reflect.String && !v.IsNil() {
			return iterMapBindItems(v, flatKey+".")
		}
	case reflect.Pointer:
		if !v.IsNil() && v.Elem().Kind() == reflect.Struct {
			return iterStructBindItems(v.Elem(), flatKey+".")
		}
	}
	var raw interface{}
	if v.IsValid() && v.CanInterface() {
		raw = v.Interface()
	}
	return []bindItem{{key: flatKey, itemType: valueToItemType(raw), value: raw}}
}

// bindKeyFromField returns the wire key for a struct field. Prefers the
// json tag (sans `,omitempty`) when present; falls back to the field name.
// Returns "" to signal "skip this field" (`json:"-"`).
func bindKeyFromField(field reflect.StructField) string {
	tag, ok := field.Tag.Lookup("json")
	if !ok {
		return field.Name
	}
	if comma := strings.Index(tag, ","); comma >= 0 {
		tag = tag[:comma]
	}
	if tag == "-" {
		return ""
	}
	if tag == "" {
		return field.Name
	}
	return tag
}

// valueToItemType infers the Config item type from a runtime value.
// bool is checked before int/float because Go's typeof tree happens to
// disambiguate them; we follow the same ordering as the Python SDK for
// symmetry.
func valueToItemType(value interface{}) string {
	if value == nil {
		return "STRING"
	}
	switch value.(type) {
	case bool:
		return "BOOLEAN"
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return "NUMBER"
	case string:
		return "STRING"
	default:
		return "STRING"
	}
}

// configIDForBoundTarget returns the config id under which `target` was
// previously bound, or ("", false) if it isn't currently bound. Compares
// by underlying pointer identity rather than interface equality so that
// map targets (uncomparable under `==`) work without panicking. Mirrors
// python-sdk's `is`-based identity check and typescript-sdk's `===`.
func (c *ConfigClient) configIDForBoundTarget(target interface{}) (string, bool) {
	c.bindingsMu.Lock()
	defer c.bindingsMu.Unlock()
	wanted := reflect.ValueOf(target).Pointer()
	for id, bound := range c.bindings {
		if reflect.ValueOf(bound).Pointer() == wanted {
			return id, true
		}
	}
	return "", false
}

// syncTargetFromCache applies any cached values for `configID` to the
// bound `target` in place. Handles the existing-config case: on restart,
// server-side values overlay the in-code defaults the caller passed in.
func (c *ConfigClient) syncTargetFromCache(target interface{}, configID string) {
	resolved, ok := c.configCache[configID]
	if !ok {
		return
	}
	for dottedKey, value := range resolved {
		applyChangeToTarget(target, dottedKey, value)
	}
}

// applyChangeToTarget walks `target` along the dotted key path and
// assigns `value` to the leaf. For struct paths, uses reflection's
// `Set` (so the leaf field must be exported and addressable, which is
// guaranteed by Bind's requirement that struct targets be passed by
// pointer). For map paths, uses `SetMapIndex`. Mismatches and missing
// intermediates are swallowed silently — the SDK trusts server values
// but tolerates structural drift between in-code and server-side shapes.
func applyChangeToTarget(target interface{}, dottedKey string, value interface{}) {
	if target == nil || dottedKey == "" {
		return
	}
	rv := reflect.ValueOf(target)
	// Dereference one level of pointer to land on the struct or map.
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return
		}
		rv = rv.Elem()
	}

	parts := strings.Split(dottedKey, ".")
	current := rv
	for i := 0; i < len(parts)-1; i++ {
		next, ok := walkInto(current, parts[i])
		if !ok {
			return
		}
		current = next
	}
	setLeaf(current, parts[len(parts)-1], value)
}

// walkInto returns the child at `name` within `current`, following struct
// fields and map values. Returns (zero, false) if no such child exists.
func walkInto(current reflect.Value, name string) (reflect.Value, bool) {
	// Unwrap interfaces / pointers.
	for current.Kind() == reflect.Interface {
		if current.IsNil() {
			return reflect.Value{}, false
		}
		current = current.Elem()
	}
	for current.Kind() == reflect.Pointer {
		if current.IsNil() {
			return reflect.Value{}, false
		}
		current = current.Elem()
	}
	switch current.Kind() {
	case reflect.Struct:
		if idx, ok := structFieldIndexByKey(current.Type(), name); ok {
			return current.Field(idx), true
		}
	case reflect.Map:
		if current.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, false
		}
		mv := current.MapIndex(reflect.ValueOf(name))
		if !mv.IsValid() {
			return reflect.Value{}, false
		}
		return mv, true
	}
	return reflect.Value{}, false
}

// setLeaf assigns `value` to the leaf at `name` within `current`. Tolerates
// type mismatches by silently dropping the update.
func setLeaf(current reflect.Value, name string, value interface{}) {
	for current.Kind() == reflect.Interface && !current.IsNil() {
		current = current.Elem()
	}
	for current.Kind() == reflect.Pointer && !current.IsNil() {
		current = current.Elem()
	}
	switch current.Kind() {
	case reflect.Struct:
		idx, ok := structFieldIndexByKey(current.Type(), name)
		if !ok {
			return
		}
		field := current.Field(idx)
		if !field.CanSet() {
			return
		}
		assignReflectValue(field, value)
	case reflect.Map:
		if current.Type().Key().Kind() != reflect.String {
			return
		}
		valType := current.Type().Elem()
		converted, ok := coerceForReflectType(valType, value)
		if !ok {
			return
		}
		current.SetMapIndex(reflect.ValueOf(name), converted)
	}
}

// structFieldIndexByKey resolves a wire key to a struct field index,
// honoring the same json-tag-or-field-name rule used by iterStructBindItems.
func structFieldIndexByKey(t reflect.Type, key string) (int, bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		if bindKeyFromField(field) == key {
			return i, true
		}
	}
	return 0, false
}

// assignReflectValue assigns `value` to `dst`, converting numeric types
// where Go's reflection allows (e.g. float64 from a JSON-decoded server
// payload landing in an int field). Drops the assignment silently on
// any unconvertible type pair.
func assignReflectValue(dst reflect.Value, value interface{}) {
	if value == nil {
		dst.Set(reflect.Zero(dst.Type()))
		return
	}
	src := reflect.ValueOf(value)
	if src.Type().AssignableTo(dst.Type()) {
		dst.Set(src)
		return
	}
	if src.Type().ConvertibleTo(dst.Type()) {
		dst.Set(src.Convert(dst.Type()))
		return
	}
}

// coerceForReflectType returns a reflect.Value of `targetType` populated
// from `value`. Used by map-leaf assignment where the target value type
// is known from the map's reflected element type.
func coerceForReflectType(targetType reflect.Type, value interface{}) (reflect.Value, bool) {
	if value == nil {
		return reflect.Zero(targetType), true
	}
	src := reflect.ValueOf(value)
	if src.Type().AssignableTo(targetType) {
		return src, true
	}
	if src.Type().ConvertibleTo(targetType) {
		return src.Convert(targetType), true
	}
	return reflect.Value{}, false
}

// ----------------------------------------------------------------------
// Internal: bindings registry
// ----------------------------------------------------------------------

// mutateBoundTargetsForChanges applies any (configID, itemKey, newValue)
// triples to the matching bound target in place. Invoked by diffAndFire
// before listeners are fired so that listeners reading the bound target
// see the new value.
func (c *ConfigClient) mutateBoundTargetsForChanges(
	cfgKey string,
	itemKey string,
	newVal interface{},
) {
	c.bindingsMu.Lock()
	target, ok := c.bindings[cfgKey]
	c.bindingsMu.Unlock()
	if !ok {
		return
	}
	applyChangeToTarget(target, itemKey, newVal)
}
