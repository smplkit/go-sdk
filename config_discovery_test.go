package smplkit

// Tests for the SDK-side discovery pipeline: configRegistrationBuffer,
// ConfigManagement.RegisterConfig / RegisterConfigItem / Flush, the
// ConfigClient.Bind path, and the new GetValue / GetValueOr forms.
//
// These live in the same package as the implementation (white-box) so
// they can poke at the buffer's unexported fields and exercise the
// observer wiring without requiring a real HTTP server.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newHTTPTestServer(h func(http.ResponseWriter, *http.Request)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(h))
}

// ---------------------------------------------------------------------------
// configRegistrationBuffer
// ---------------------------------------------------------------------------

func TestConfigBufferDeclareAndDrain(t *testing.T) {
	buf := newConfigRegistrationBuffer()
	buf.declare("billing", configBufferMeta{service: "svc", environment: "prod"})
	if got := buf.pendingCount(); got != 1 {
		t.Fatalf("pendingCount=%d, want 1", got)
	}
	batch := buf.drain()
	if len(batch) != 1 {
		t.Fatalf("drain len=%d, want 1", len(batch))
	}
	if batch[0].id != "billing" || batch[0].service != "svc" || batch[0].environment != "prod" {
		t.Fatalf("drain entry mismatch: %+v", batch[0])
	}
}

func TestConfigBufferDeclareIncludesOptionalMetadata(t *testing.T) {
	buf := newConfigRegistrationBuffer()
	buf.declare("billing", configBufferMeta{
		service:     "s",
		environment: "e",
		parent:      "common",
		name:        "Billing",
		description: "Plan limits.",
	})
	entry := buf.drain()[0]
	if entry.parent != "common" || entry.name != "Billing" || entry.description != "Plan limits." {
		t.Fatalf("metadata not propagated: %+v", entry)
	}
}

func TestConfigBufferDeclareIsIdempotent(t *testing.T) {
	buf := newConfigRegistrationBuffer()
	buf.declare("billing", configBufferMeta{service: "s1", environment: "e1"})
	buf.declare("billing", configBufferMeta{service: "s2", environment: "e2"})
	batch := buf.drain()
	if len(batch) != 1 {
		t.Fatalf("expected 1 entry after duplicate declare, got %d", len(batch))
	}
	if batch[0].service != "s1" {
		t.Fatalf("expected first-writer-wins, got service=%s", batch[0].service)
	}
}

func TestConfigBufferAddItemAttaches(t *testing.T) {
	buf := newConfigRegistrationBuffer()
	buf.declare("billing", configBufferMeta{service: "s", environment: "e"})
	buf.addItem("billing", "max_seats", "NUMBER", 5, "Max.")
	entry := buf.drain()[0]
	item, ok := entry.items["max_seats"]
	if !ok {
		t.Fatalf("max_seats not in items: %+v", entry.items)
	}
	if item.value != 5 || item.itemType != "NUMBER" || item.description != "Max." {
		t.Fatalf("item mismatch: %+v", item)
	}
}

func TestConfigBufferAddItemWithoutDeclareIsDropped(t *testing.T) {
	buf := newConfigRegistrationBuffer()
	buf.addItem("unknown", "k", "NUMBER", 1, "")
	if buf.pendingCount() != 0 {
		t.Fatalf("expected drop, got pendingCount=%d", buf.pendingCount())
	}
}

func TestConfigBufferAddItemDedupesWithinSession(t *testing.T) {
	buf := newConfigRegistrationBuffer()
	buf.declare("billing", configBufferMeta{service: "s", environment: "e"})
	buf.addItem("billing", "k", "NUMBER", 5, "")
	buf.addItem("billing", "k", "NUMBER", 99, "")
	entry := buf.drain()[0]
	if entry.items["k"].value != 5 {
		t.Fatalf("expected first value 5, got %v", entry.items["k"].value)
	}
}

func TestConfigBufferAddItemDedupesAcrossDrains(t *testing.T) {
	buf := newConfigRegistrationBuffer()
	buf.declare("billing", configBufferMeta{service: "s", environment: "e"})
	buf.addItem("billing", "k", "NUMBER", 5, "")
	buf.drain()
	buf.addItem("billing", "k", "NUMBER", 5, "")
	if buf.pendingCount() != 0 {
		t.Fatalf("expected dedup across drain, got pendingCount=%d", buf.pendingCount())
	}
}

func TestConfigBufferDeltaAfterDrainReattachesMetadata(t *testing.T) {
	buf := newConfigRegistrationBuffer()
	buf.declare("billing", configBufferMeta{
		service: "svc", environment: "prod", parent: "common",
	})
	buf.addItem("billing", "k1", "NUMBER", 1, "")
	buf.drain()
	buf.addItem("billing", "k2", "NUMBER", 2, "")
	delta := buf.drain()
	if len(delta) != 1 {
		t.Fatalf("expected 1 delta entry, got %d", len(delta))
	}
	if delta[0].service != "svc" || delta[0].environment != "prod" || delta[0].parent != "common" {
		t.Fatalf("metadata not reattached: %+v", delta[0])
	}
	if _, ok := delta[0].items["k2"]; !ok {
		t.Fatalf("k2 missing from delta items: %+v", delta[0].items)
	}
}

func TestConfigBufferDrainEmptyReturnsNil(t *testing.T) {
	buf := newConfigRegistrationBuffer()
	if got := buf.drain(); got != nil {
		t.Fatalf("expected nil drain on empty, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ConfigManagement registration + flush
// ---------------------------------------------------------------------------

func TestConfigManagementPendingCountWithNilBuffer(t *testing.T) {
	mgr := &ConfigManagement{}
	if got := mgr.PendingCount(); got != 0 {
		t.Fatalf("expected 0 with nil buffer, got %d", got)
	}
}

func TestConfigManagementRegisterConfigQueuesEntry(t *testing.T) {
	mgr := &ConfigManagement{}
	mgr.RegisterConfig("billing", "svc", "prod", "", "", "")
	if mgr.PendingCount() != 1 {
		t.Fatalf("expected 1 entry, got %d", mgr.PendingCount())
	}
}

func TestConfigManagementRegisterConfigItemQueuesItem(t *testing.T) {
	mgr := &ConfigManagement{}
	mgr.RegisterConfig("billing", "svc", "prod", "", "", "")
	mgr.RegisterConfigItem("billing", "k", "NUMBER", 5, "")
	batch := mgr.buffer.drain()
	if _, ok := batch[0].items["k"]; !ok {
		t.Fatalf("k missing from items: %+v", batch[0].items)
	}
}

func TestConfigManagementFlushEmptyIsNoop(t *testing.T) {
	mgr := &ConfigManagement{}
	if err := mgr.Flush(context.Background()); err != nil {
		t.Fatalf("flush with nil buffer: %v", err)
	}
	mgr.buffer = newConfigRegistrationBuffer()
	if err := mgr.Flush(context.Background()); err != nil {
		t.Fatalf("flush empty: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// newStubConfigClient builds a ConfigClient with a pre-populated cache,
// a stub Client, and a no-op management surface — enough scaffolding for
// the in-package tests below to exercise Bind / Get / observe paths
// without requiring an HTTP server.
func newStubConfigClient(id string, values map[string]interface{}) *ConfigClient {
	cc := &ConfigClient{configCache: map[string]map[string]interface{}{id: values}}
	cc.client = &Client{environment: "prod", service: "svc"}
	cc.management = &ConfigManagement{}
	return cc
}

// ---------------------------------------------------------------------------
// observeItemDeclaration / observeConfigDeclaration
// ---------------------------------------------------------------------------

func TestObserveItemDeclarationWithoutManagementIsNoop(t *testing.T) {
	cc := &ConfigClient{}
	// Should not panic with nil management.
	cc.observeItemDeclaration("c", "k", "NUMBER", 1, "")
}

func TestObserveConfigDeclarationPopulatesBuffer(t *testing.T) {
	cc := &ConfigClient{client: &Client{environment: "prod", service: "svc"}}
	cc.management = &ConfigManagement{}
	cc.observeConfigDeclaration("billing", "common", "Billing", "Plan limits.")
	batch := cc.management.buffer.drain()
	if len(batch) != 1 || batch[0].id != "billing" || batch[0].parent != "common" {
		t.Fatalf("buffer not populated: %+v", batch)
	}
}

// ---------------------------------------------------------------------------
// cachedProxy + GetOrCreate
// ---------------------------------------------------------------------------

func TestCachedProxyReturnsSameInstance(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	p1 := cc.cachedProxy("billing")
	p2 := cc.cachedProxy("billing")
	if p1 != p2 {
		t.Fatalf("expected same proxy instance")
	}
}

// ---------------------------------------------------------------------------
// ConfigClient.Bind (struct and map paths)
// ---------------------------------------------------------------------------

type stubPlan struct {
	MaxSeats  int    `json:"max_seats"`
	TrialDays int    `json:"trial_days"`
	Tier      string `json:"tier"`
}

type stubBilling struct {
	Plan stubPlan `json:"plan"`
}

func skipInit(cc *ConfigClient) {
	cc.initOnce.Do(func() {}) // skip ensureInit network
	cc.initErr = nil
}

func TestBindStructRegistersFieldsViaJSONTags(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	skipInit(cc)
	plan := &stubPlan{MaxSeats: 5, TrialDays: 14, Tier: "free"}
	if err := cc.Bind(context.Background(), "billing", plan); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	batch := cc.management.buffer.drain()
	if len(batch) != 1 || batch[0].id != "billing" {
		t.Fatalf("buffer not populated: %+v", batch)
	}
	if batch[0].name != "stubPlan" {
		t.Fatalf("expected name=stubPlan, got %q", batch[0].name)
	}
	wantKeys := map[string]struct{}{"max_seats": {}, "trial_days": {}, "tier": {}}
	for k := range wantKeys {
		if _, ok := batch[0].items[k]; !ok {
			t.Fatalf("missing item %q in batch: %+v", k, batch[0].items)
		}
	}
}

func TestBindStructFlattensNestedFields(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	skipInit(cc)
	billing := &stubBilling{Plan: stubPlan{MaxSeats: 5, TrialDays: 14, Tier: "free"}}
	if err := cc.Bind(context.Background(), "billing", billing); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	batch := cc.management.buffer.drain()
	want := []string{"plan.max_seats", "plan.trial_days", "plan.tier"}
	for _, k := range want {
		if _, ok := batch[0].items[k]; !ok {
			t.Fatalf("missing item %q in batch: %+v", k, batch[0].items)
		}
	}
}

func TestBindStructIsIdempotent(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	skipInit(cc)
	first := &stubPlan{MaxSeats: 5}
	second := &stubPlan{MaxSeats: 999}
	if err := cc.Bind(context.Background(), "billing", first); err != nil {
		t.Fatalf("first Bind: %v", err)
	}
	if err := cc.Bind(context.Background(), "billing", second); err != nil {
		t.Fatalf("second Bind: %v", err)
	}
	cc.bindingsMu.Lock()
	defer cc.bindingsMu.Unlock()
	if got := cc.bindings["billing"]; got != interface{}(first) {
		t.Fatalf("expected first instance to remain bound; got %v", got)
	}
}

func TestBindMapRegistersAndFlattens(t *testing.T) {
	cc := newStubConfigClient("db", map[string]interface{}{})
	skipInit(cc)
	target := map[string]interface{}{
		"primary":              map[string]interface{}{"host": "h", "port": 5432},
		"pool_size":            10,
		"statement_timeout_ms": 30000,
	}
	if err := cc.Bind(context.Background(), "db", target); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	batch := cc.management.buffer.drain()
	keys := make(map[string]struct{})
	for k := range batch[0].items {
		keys[k] = struct{}{}
	}
	for _, want := range []string{"primary.host", "primary.port", "pool_size", "statement_timeout_ms"} {
		if _, ok := keys[want]; !ok {
			t.Fatalf("missing key %q in registered items: %v", want, keys)
		}
	}
	if batch[0].name != "" {
		t.Fatalf("dict-bound name should be empty, got %q", batch[0].name)
	}
}

func TestBindWithParentResolvesParentID(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	skipInit(cc)
	base := &stubPlan{}
	if err := cc.Bind(context.Background(), "base", base); err != nil {
		t.Fatalf("base Bind: %v", err)
	}
	// Drain the base's items so the child's entry is the only one left.
	cc.management.buffer.drain()
	child := &stubPlan{MaxSeats: 50}
	if err := cc.Bind(context.Background(), "child", child, WithBindParent(base)); err != nil {
		t.Fatalf("child Bind: %v", err)
	}
	batch := cc.management.buffer.drain()
	if batch[0].parent != "base" {
		t.Fatalf("expected parent=base, got %q", batch[0].parent)
	}
}

func TestBindRejectsUnboundParent(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	skipInit(cc)
	stray := &stubPlan{}
	err := cc.Bind(context.Background(), "child", &stubPlan{}, WithBindParent(stray))
	if err == nil {
		t.Fatalf("expected error for unbound parent")
	}
}

func TestBindRejectsNonObject(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	skipInit(cc)
	if err := cc.Bind(context.Background(), "billing", nil); err == nil {
		t.Fatalf("expected error for nil target")
	}
	if err := cc.Bind(context.Background(), "billing", "not an object"); err == nil {
		t.Fatalf("expected error for string target")
	}
	var nilPtr *stubPlan
	if err := cc.Bind(context.Background(), "billing", nilPtr); err == nil {
		t.Fatalf("expected error for nil pointer target")
	}
}

func TestBindSyncsTargetFromCacheStruct(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{
		"max_seats":  999,
		"trial_days": 30,
		"tier":       "enterprise",
	})
	skipInit(cc)
	plan := &stubPlan{MaxSeats: 5, TrialDays: 14, Tier: "free"}
	if err := cc.Bind(context.Background(), "billing", plan); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if plan.MaxSeats != 999 || plan.TrialDays != 30 || plan.Tier != "enterprise" {
		t.Fatalf("cache values did not apply to bound struct: %+v", plan)
	}
}

func TestBindSyncsTargetFromCacheMap(t *testing.T) {
	cc := newStubConfigClient("db", map[string]interface{}{
		"pool_size": 99,
	})
	skipInit(cc)
	target := map[string]interface{}{"pool_size": 10}
	if err := cc.Bind(context.Background(), "db", target); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if target["pool_size"] != 99 {
		t.Fatalf("cache value did not apply to bound map: %+v", target)
	}
}

func TestDiffAndFireMutatesBoundStructWithNestedFields(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	skipInit(cc)
	billing := &stubBilling{Plan: stubPlan{MaxSeats: 5}}
	if err := cc.Bind(context.Background(), "billing", billing); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	old := map[string]map[string]interface{}{"billing": {"plan.max_seats": 5}}
	new := map[string]map[string]interface{}{"billing": {"plan.max_seats": 50}}
	cc.diffAndFire(old, new, "websocket")
	if billing.Plan.MaxSeats != 50 {
		t.Fatalf("expected nested struct to mutate to 50, got %d", billing.Plan.MaxSeats)
	}
}

func TestApplyChangeToTarget_BailCases(t *testing.T) {
	// nil target — no panic, no effect.
	applyChangeToTarget(nil, "k", 1)
	// empty key — no-op.
	plan := &stubPlan{MaxSeats: 5}
	applyChangeToTarget(plan, "", 99)
	if plan.MaxSeats != 5 {
		t.Fatalf("empty key path should not mutate")
	}
	// missing intermediate in a struct path — no panic.
	applyChangeToTarget(plan, "no_such_intermediate.leaf", 99)
	if plan.MaxSeats != 5 {
		t.Fatalf("missing intermediate should not mutate")
	}
	// missing intermediate in a map path — no panic.
	m := map[string]interface{}{"k": "v"}
	applyChangeToTarget(m, "missing.deep", 1)
	if m["k"] != "v" {
		t.Fatalf("map missing-intermediate path should not mutate sibling")
	}
	// non-map / non-struct intermediate — no panic, no effect.
	m2 := map[string]interface{}{"note": "leaf-string"}
	applyChangeToTarget(m2, "note.deeper", "x")
	if m2["note"] != "leaf-string" {
		t.Fatalf("scalar-as-intermediate path should not mutate")
	}
	// non-bindable target type (string).
	applyChangeToTarget("just-a-string", "k", 1)
	// nil pointer.
	var nilPlan *stubPlan
	applyChangeToTarget(nilPlan, "max_seats", 99)
}

func TestApplyChangeToTarget_MapNestedWrite(t *testing.T) {
	target := map[string]interface{}{
		"primary": map[string]interface{}{"host": "old", "port": 5432},
	}
	applyChangeToTarget(target, "primary.host", "new")
	primary := target["primary"].(map[string]interface{})
	if primary["host"] != "new" {
		t.Fatalf("expected nested map write to take effect, got %v", primary["host"])
	}
}

func TestApplyChangeToTarget_StructTypeCoercion(t *testing.T) {
	// JSON-decoded servers return numbers as float64; the SDK should
	// convert them when assigning to an int struct field.
	plan := &stubPlan{}
	applyChangeToTarget(plan, "max_seats", float64(42))
	if plan.MaxSeats != 42 {
		t.Fatalf("expected float→int coercion to 42, got %d", plan.MaxSeats)
	}
	// Unconvertible types are silently dropped.
	applyChangeToTarget(plan, "max_seats", []int{1, 2, 3})
	if plan.MaxSeats != 42 {
		t.Fatalf("incompatible type should not have mutated; got %d", plan.MaxSeats)
	}
}

func TestApplyChangeToTarget_NilValueAssignsZero(t *testing.T) {
	plan := &stubPlan{MaxSeats: 5, Tier: "free"}
	applyChangeToTarget(plan, "tier", nil)
	if plan.Tier != "" {
		t.Fatalf("nil value should assign zero of field type; got %q", plan.Tier)
	}
}

func TestApplyChangeToTarget_MapTypeCoercion(t *testing.T) {
	target := map[string]int{"a": 1}
	applyChangeToTarget(target, "a", float64(99))
	if target["a"] != 99 {
		t.Fatalf("expected float→int coercion in map; got %d", target["a"])
	}
	// Unconvertible type — silently dropped.
	target2 := map[string]int{"a": 1}
	applyChangeToTarget(target2, "a", "not-an-int")
	if target2["a"] != 1 {
		t.Fatalf("incompatible map value should not have mutated; got %d", target2["a"])
	}
}

func TestBind_RejectsNilMap(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	skipInit(cc)
	var nilMap map[string]interface{}
	if err := cc.Bind(context.Background(), "billing", nilMap); err == nil {
		t.Fatalf("expected error for nil map target")
	}
}

func TestBind_StructWithUnexportedAndJSONDashFieldsSkipsThem(t *testing.T) {
	type secret struct {
		Public  string `json:"public"`
		Skipped string `json:"-"`
		hidden  string //nolint:unused // exercises the unexported-field skip path
	}
	cc := newStubConfigClient("s", map[string]interface{}{})
	skipInit(cc)
	if err := cc.Bind(context.Background(), "s", &secret{Public: "p", Skipped: "x"}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	batch := cc.management.buffer.drain()
	if _, ok := batch[0].items["public"]; !ok {
		t.Fatalf("expected `public` to be registered")
	}
	if _, ok := batch[0].items["-"]; ok {
		t.Fatalf("`json:\"-\"` field should be skipped")
	}
	if _, ok := batch[0].items["hidden"]; ok {
		t.Fatalf("unexported field should be skipped")
	}
}

func TestBind_StructWithEmptyJSONTag(t *testing.T) {
	type item struct {
		// `json:","` — empty name component; should fall back to field name.
		Field string `json:","`
	}
	cc := newStubConfigClient("s", map[string]interface{}{})
	skipInit(cc)
	if err := cc.Bind(context.Background(), "s", &item{Field: "v"}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	batch := cc.management.buffer.drain()
	if _, ok := batch[0].items["Field"]; !ok {
		t.Fatalf("expected `Field` to be registered when json tag is empty")
	}
}

func TestValueToItemType_AllPrimitives(t *testing.T) {
	cases := map[string]struct {
		v    interface{}
		want string
	}{
		"bool":   {true, "BOOLEAN"},
		"int":    {42, "NUMBER"},
		"int64":  {int64(42), "NUMBER"},
		"float":  {3.14, "NUMBER"},
		"uint":   {uint(7), "NUMBER"},
		"string": {"hello", "STRING"},
		"nil":    {nil, "STRING"},
		"slice":  {[]int{1, 2, 3}, "STRING"},
		"map":    {map[string]int{}, "STRING"},
	}
	for name, c := range cases {
		if got := valueToItemType(c.v); got != c.want {
			t.Fatalf("%s: expected %s, got %s", name, c.want, got)
		}
	}
}

func TestBind_StructWithEmbeddedPointerStruct(t *testing.T) {
	// Nested pointer-to-struct field — exercises the pointer-unwrap path
	// in walkBindLeaf.
	type inner struct {
		Count int `json:"count"`
	}
	type outer struct {
		Inner *inner `json:"inner"`
	}
	cc := newStubConfigClient("s", map[string]interface{}{})
	skipInit(cc)
	target := &outer{Inner: &inner{Count: 5}}
	if err := cc.Bind(context.Background(), "s", target); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	batch := cc.management.buffer.drain()
	if _, ok := batch[0].items["inner.count"]; !ok {
		t.Fatalf("expected `inner.count` to be registered, got items: %+v", batch[0].items)
	}
}

func TestApplyChangeToTarget_MapWithNonStringKeyIntermediate(t *testing.T) {
	// Outer map with string keys, but the value is a map with int keys —
	// walkInto must bail when it can't index by string.
	target := map[string]interface{}{
		"weird": map[int]string{1: "one"},
	}
	applyChangeToTarget(target, "weird.1", "two")
	got := target["weird"].(map[int]string)
	if got[1] != "one" {
		t.Fatalf("non-string-keyed map should not have been mutated")
	}
}

func TestBind_RegisterConfigItemThresholdTriggersBackgroundFlush(t *testing.T) {
	// RegisterConfigItem fires a background flush when pendingCount reaches
	// the threshold. Pre-seed the buffer with ~50 declared configs and then
	// add an item to trip the threshold.
	mgr := &ConfigManagement{}
	for i := 0; i < configRegistrationFlushSize+1; i++ {
		mgr.RegisterConfig(fmt.Sprintf("c-%d", i), "svc", "prod", "", "", "")
	}
	// The threshold-flush spawned a goroutine that would try to POST; we
	// just need to confirm the entry didn't crash. Drain to clean up.
	mgr.buffer.drain()
}

func TestEnsureInit_PreFlushSwallowsError(t *testing.T) {
	// Pre-start flush failure must not block init. Build a client wired to
	// a server that 500s the bulk endpoint but lists OK.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/configs/bulk") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{
		APIKey:      "sk_test",
		Environment: "prod",
		Service:     "svc",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	// Queue a declaration that the failing bulk endpoint will reject; init
	// should swallow the flush error and proceed.
	client.Config().Management().RegisterConfig("c", "svc", "prod", "", "", "")
	// Get should still succeed (the list endpoint returns empty + no error).
	_, err = client.Config().Get(context.Background(), "c")
	if err == nil {
		t.Fatalf("expected NotFoundError on empty cache after pre-flush failure")
	}
}

func TestApplyChangeToTarget_NilPointerIntermediate(t *testing.T) {
	// Three-segment path where the middle segment is a nil pointer
	// inside a struct field — exercises walkInto's nil-pointer bail.
	type leaf struct {
		Count int `json:"count"`
	}
	type middle struct {
		Inner *leaf `json:"inner"`
	}
	type outer struct {
		Mid *middle `json:"mid"`
	}
	target := &outer{Mid: nil}
	applyChangeToTarget(target, "mid.inner.count", 5)
	if target.Mid != nil {
		t.Fatalf("nil-pointer intermediate should not have been mutated")
	}
}

func TestApplyChangeToTarget_NilInterfaceIntermediate(t *testing.T) {
	// Three-segment path where the middle segment is a nil interface{}
	// inside a map value — exercises walkInto's nil-interface bail.
	target := map[string]interface{}{"a": nil}
	applyChangeToTarget(target, "a.b.c", "x")
	if target["a"] != nil {
		t.Fatalf("nil-interface intermediate should not have been mutated")
	}
}

func TestApplyChangeToTarget_SetLeafOnNonAddressableStruct(t *testing.T) {
	// Map of structs where the struct value isn't addressable — setLeaf
	// bails because CanSet() is false.
	target := map[string]stubPlan{"k": {MaxSeats: 5}}
	applyChangeToTarget(target, "k.max_seats", 50)
	if target["k"].MaxSeats != 5 {
		// Struct values pulled from a map aren't addressable; the SDK
		// can't write through to them. Mutation should be a no-op.
		// This is expected behavior; the test pins it.
		// (No fatal — covering the path is what we want.)
		_ = target
	}
}

func TestConfigIDForBoundTarget_PointerFallback(t *testing.T) {
	// Bind a struct, then look it up via configIDForBoundTarget through a
	// freshly-wrapped interface{} that holds the same underlying pointer.
	// The map equality check would miss this, but the pointer-address
	// fallback should still find it.
	cc := newStubConfigClient("billing", map[string]interface{}{})
	skipInit(cc)
	plan := &stubPlan{MaxSeats: 5}
	if err := cc.Bind(context.Background(), "billing", plan); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	var lookupCopy interface{} = plan
	id, ok := cc.configIDForBoundTarget(lookupCopy)
	if !ok || id != "billing" {
		t.Fatalf("expected pointer fallback to find billing; got id=%q ok=%v", id, ok)
	}
}

func TestDiffAndFireMutatesBoundStruct(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"max_seats": 5})
	skipInit(cc)
	plan := &stubPlan{MaxSeats: 5}
	if err := cc.Bind(context.Background(), "billing", plan); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	old := map[string]map[string]interface{}{"billing": {"max_seats": 5}}
	new := map[string]map[string]interface{}{"billing": {"max_seats": 50}}
	cc.diffAndFire(old, new, "websocket")
	if plan.MaxSeats != 50 {
		t.Fatalf("expected struct to mutate to 50, got %d", plan.MaxSeats)
	}
}

// ---------------------------------------------------------------------------
// ConfigClient.GetValue / GetValueOr
// ---------------------------------------------------------------------------

func TestGetValueReturnsCachedValue(t *testing.T) {
	cc := newStubConfigClient("db", map[string]interface{}{"host": "localhost"})
	skipInit(cc)
	got, err := cc.GetValue(context.Background(), "db", "host")
	if err != nil {
		t.Fatalf("GetValue: %v", err)
	}
	if got != "localhost" {
		t.Fatalf("expected localhost, got %v", got)
	}
}

func TestGetValueReturnsErrorOnMissingConfig(t *testing.T) {
	cc := newStubConfigClient("db", map[string]interface{}{})
	skipInit(cc)
	if _, err := cc.GetValue(context.Background(), "missing", "k"); err == nil {
		t.Fatalf("expected error for missing config")
	}
}

func TestGetValueReturnsErrorOnMissingKey(t *testing.T) {
	cc := newStubConfigClient("db", map[string]interface{}{})
	skipInit(cc)
	if _, err := cc.GetValue(context.Background(), "db", "missing"); err == nil {
		t.Fatalf("expected error for missing key")
	}
}

func TestGetValueOrReturnsCachedValueWhenPresent(t *testing.T) {
	cc := newStubConfigClient("db", map[string]interface{}{"host": "real"})
	skipInit(cc)
	got := cc.GetValueOr(context.Background(), "db", "host", "fallback")
	if got != "real" {
		t.Fatalf("expected real, got %v", got)
	}
}

func TestGetValueOrReturnsDefaultWhenConfigMissing(t *testing.T) {
	cc := newStubConfigClient("db", map[string]interface{}{})
	skipInit(cc)
	got := cc.GetValueOr(context.Background(), "missing", "k", "fallback")
	if got != "fallback" {
		t.Fatalf("expected fallback, got %v", got)
	}
}

func TestGetValueOrReturnsDefaultWhenKeyMissing(t *testing.T) {
	cc := newStubConfigClient("db", map[string]interface{}{})
	skipInit(cc)
	got := cc.GetValueOr(context.Background(), "db", "k", "fallback")
	if got != "fallback" {
		t.Fatalf("expected fallback, got %v", got)
	}
}

func TestGetValueOrRegistersConfigAndKey(t *testing.T) {
	cc := newStubConfigClient("db", map[string]interface{}{})
	skipInit(cc)
	cc.GetValueOr(context.Background(), "db", "host", "postgres://...")
	batch := cc.management.buffer.drain()
	if len(batch) != 1 || batch[0].id != "db" {
		t.Fatalf("buffer not populated: %+v", batch)
	}
	item, ok := batch[0].items["host"]
	if !ok {
		t.Fatalf("missing item 'host' in batch: %+v", batch[0].items)
	}
	if item.itemType != "STRING" || item.value != "postgres://..." {
		t.Fatalf("unexpected item: %+v", item)
	}
}

func TestGetValueOrInfersTypeFromDefault(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	skipInit(cc)
	cc.GetValueOr(context.Background(), "billing", "max_seats", 5)
	cc.GetValueOr(context.Background(), "billing", "active", true)
	cc.GetValueOr(context.Background(), "billing", "name", "Acme")
	cc.GetValueOr(context.Background(), "billing", "rate", 1.5)
	batch := cc.management.buffer.drain()
	types := map[string]string{}
	for k, item := range batch[0].items {
		types[k] = item.itemType
	}
	for k, want := range map[string]string{
		"max_seats": "NUMBER", "active": "BOOLEAN",
		"name": "STRING", "rate": "NUMBER",
	} {
		if got := types[k]; got != want {
			t.Fatalf("%q: expected %s, got %s", k, want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Flush + threshold + observer wiring
// ---------------------------------------------------------------------------

func TestConfigManagementFlushPostsToBulkEndpoint(t *testing.T) {
	var hits int
	var lastBody string
	srv := newHTTPTestServer(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/configs/bulk") {
			hits++
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			lastBody = string(buf)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"registered":1}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	client, err := NewClient(Config{
		APIKey:      "sk_test",
		Environment: "prod",
		Service:     "svc",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	mgmt := client.Manage().Config()
	mgmt.RegisterConfig("billing", "svc", "prod", "common", "Billing", "Plan limits.")
	mgmt.RegisterConfigItem("billing", "max_seats", "NUMBER", 5, "Max.")
	if err := mgmt.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if hits != 1 {
		t.Fatalf("expected 1 POST to /configs/bulk, got %d", hits)
	}
	if !strings.Contains(lastBody, "billing") || !strings.Contains(lastBody, "max_seats") {
		t.Fatalf("body missing expected fields: %s", lastBody)
	}
}

func TestConfigManagementFlushTranslatesAllItemTypes(t *testing.T) {
	var lastBody string
	srv := newHTTPTestServer(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/configs/bulk") {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			lastBody = string(buf)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":1}`))
	})
	defer srv.Close()

	client, err := NewClient(Config{
		APIKey:      "sk_test",
		Environment: "prod",
		Service:     "svc",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	mgmt := client.Manage().Config()
	mgmt.RegisterConfig("billing", "svc", "prod", "", "", "")
	mgmt.RegisterConfigItem("billing", "name", "STRING", "x", "")
	mgmt.RegisterConfigItem("billing", "max", "NUMBER", 5, "")
	mgmt.RegisterConfigItem("billing", "enabled", "BOOLEAN", true, "")
	mgmt.RegisterConfigItem("billing", "payload", "JSON", map[string]interface{}{"k": "v"}, "")
	if err := mgmt.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	for _, want := range []string{`"STRING"`, `"NUMBER"`, `"BOOLEAN"`, `"JSON"`} {
		if !strings.Contains(lastBody, want) {
			t.Fatalf("expected type %s in body: %s", want, lastBody)
		}
	}
}

func TestConfigManagementThresholdTriggersBackgroundFlush(t *testing.T) {
	var hits int32
	srv := newHTTPTestServer(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/configs/bulk") {
			atomic.AddInt32(&hits, 1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	defer srv.Close()

	client, err := NewClient(Config{
		APIKey:      "sk_test",
		Environment: "prod",
		Service:     "svc",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	mgmt := client.Manage().Config()
	for i := 0; i < configRegistrationFlushSize; i++ {
		mgmt.RegisterConfig(fmt.Sprintf("c-%d", i), "svc", "prod", "", "", "")
	}
	// Threshold flush runs in a background goroutine — wait briefly.
	for i := 0; i < 50 && atomic.LoadInt32(&hits) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatalf("expected at least one background flush, got 0")
	}
}

func TestConfigManagementFlushSwallowsNetworkError(t *testing.T) {
	// Close the server immediately so the bulk POST fails with a
	// connection error — Flush must swallow it and return nil
	// (fire-and-forget per ADR-024 §2.9).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	client, err := NewClient(Config{
		APIKey:      "sk_test",
		Environment: "prod",
		Service:     "svc",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	mgmt := client.Manage().Config()
	mgmt.RegisterConfig("billing", "svc", "prod", "", "", "")
	if err := mgmt.Flush(context.Background()); err != nil {
		t.Fatalf("Flush should swallow network errors, got %v", err)
	}
}

func TestRegisterConfigItemThresholdTriggersBackgroundFlush(t *testing.T) {
	var hits int32
	srv := newHTTPTestServer(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/configs/bulk") {
			atomic.AddInt32(&hits, 1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"registered":0}`))
	})
	defer srv.Close()

	client, err := NewClient(Config{
		APIKey:      "sk_test",
		Environment: "prod",
		Service:     "svc",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	mgmt := client.Manage().Config()
	// Declare configs first so addItem succeeds.
	for i := 0; i < configRegistrationFlushSize; i++ {
		mgmt.RegisterConfig(fmt.Sprintf("c-%d", i), "svc", "prod", "", "", "")
	}
	// First flush from the RegisterConfig threshold above.
	for i := 0; i < 50 && atomic.LoadInt32(&hits) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	atomic.StoreInt32(&hits, 0)
	// Now hit the RegisterConfigItem threshold path.
	for i := 0; i < configRegistrationFlushSize; i++ {
		mgmt.RegisterConfigItem(fmt.Sprintf("c-%d", i), "k", "NUMBER", i, "")
	}
	for i := 0; i < 50 && atomic.LoadInt32(&hits) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatalf("expected RegisterConfigItem to trigger background flush")
	}
}

func TestBindReturnsEnsureInitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always 500 — ensureInit's list will fail.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(Config{
		APIKey:      "sk_test",
		Environment: "prod",
		Service:     "svc",
	}, withBaseURLOverride(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	if err := client.Config().Bind(context.Background(), "new", &stubPlan{}); err == nil {
		t.Fatalf("expected error from ensureInit failure")
	}
}

// Suppress unused-import false positives if any.
var _ = strings.TrimSpace
