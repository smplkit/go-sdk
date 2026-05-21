package smplkit

// Tests for the SDK-side discovery pipeline: configRegistrationBuffer,
// ConfigManagement.RegisterConfig / RegisterConfigItem / Flush, the
// ConfigClient.GetOrCreate path, and the typed getters on LiveConfig.
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
// LiveConfig typed getters
// ---------------------------------------------------------------------------

func newStubConfigClient(id string, values map[string]interface{}) *ConfigClient {
	cc := &ConfigClient{configCache: map[string]map[string]interface{}{id: values}}
	cc.client = &Client{environment: "prod", service: "svc"}
	// Set up a no-op management so observeItemDeclaration calls don't crash.
	cc.management = &ConfigManagement{}
	return cc
}

func TestLiveConfigGetBoolReturnsValue(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"enabled": true})
	proxy := cc.cachedProxy("billing")
	if !proxy.GetBool("enabled", false) {
		t.Fatalf("expected true")
	}
}

func TestLiveConfigGetBoolReturnsDefaultOnMissing(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	proxy := cc.cachedProxy("billing")
	if !proxy.GetBool("missing", true) {
		t.Fatalf("expected default true")
	}
}

func TestLiveConfigGetBoolReturnsDefaultOnTypeMismatch(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"enabled": "yes"})
	proxy := cc.cachedProxy("billing")
	if proxy.GetBool("enabled", false) {
		t.Fatalf("expected default false on string-not-bool")
	}
}

func TestLiveConfigGetIntReturnsValue(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"max": 5})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetInt("max", 0); got != 5 {
		t.Fatalf("expected 5, got %d", got)
	}
}

func TestLiveConfigGetIntCoercesFloatWholeNumber(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"max": float64(5)})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetInt("max", 0); got != 5 {
		t.Fatalf("expected 5 from float64, got %d", got)
	}
}

func TestLiveConfigGetIntCoercesInt64(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"max": int64(5)})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetInt("max", 0); got != 5 {
		t.Fatalf("expected 5 from int64, got %d", got)
	}
}

func TestLiveConfigGetIntRejectsNonIntegerFloat(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"max": 1.5})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetInt("max", 99); got != 99 {
		t.Fatalf("expected default 99 for 1.5, got %d", got)
	}
}

func TestLiveConfigGetIntReturnsDefaultOnMissing(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetInt("missing", 99); got != 99 {
		t.Fatalf("expected 99, got %d", got)
	}
}

func TestLiveConfigGetIntReturnsDefaultOnTypeMismatch(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"max": "not a number"})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetInt("max", 99); got != 99 {
		t.Fatalf("expected 99 on type mismatch, got %d", got)
	}
}

func TestLiveConfigGetIntReturnsDefaultWhenCacheMissing(t *testing.T) {
	cc := &ConfigClient{client: &Client{environment: "prod"}, management: &ConfigManagement{}}
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetInt("k", 99); got != 99 {
		t.Fatalf("expected default when no cache, got %d", got)
	}
}

func TestLiveConfigGetFloatReturnsValue(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"ratio": 0.75})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetFloat("ratio", 0); got != 0.75 {
		t.Fatalf("expected 0.75, got %f", got)
	}
}

func TestLiveConfigGetFloatCoercesInt(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"ratio": 5})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetFloat("ratio", 0); got != 5.0 {
		t.Fatalf("expected 5.0, got %f", got)
	}
}

func TestLiveConfigGetFloatCoercesInt64(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"ratio": int64(5)})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetFloat("ratio", 0); got != 5.0 {
		t.Fatalf("expected 5.0 from int64, got %f", got)
	}
}

func TestLiveConfigGetFloatReturnsDefaultOnMissing(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetFloat("missing", 0.5); got != 0.5 {
		t.Fatalf("expected 0.5, got %f", got)
	}
}

func TestLiveConfigGetFloatReturnsDefaultOnTypeMismatch(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"ratio": "x"})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetFloat("ratio", 0.5); got != 0.5 {
		t.Fatalf("expected 0.5, got %f", got)
	}
}

func TestLiveConfigGetFloatReturnsDefaultWhenCacheMissing(t *testing.T) {
	cc := &ConfigClient{client: &Client{}, management: &ConfigManagement{}}
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetFloat("k", 0.5); got != 0.5 {
		t.Fatalf("expected 0.5, got %f", got)
	}
}

func TestLiveConfigGetStringReturnsValue(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"name": "billing"})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetString("name", ""); got != "billing" {
		t.Fatalf("expected billing, got %s", got)
	}
}

func TestLiveConfigGetStringReturnsDefaultOnMissing(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetString("name", "default"); got != "default" {
		t.Fatalf("expected default, got %s", got)
	}
}

func TestLiveConfigGetStringReturnsDefaultOnTypeMismatch(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"name": 42})
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetString("name", "default"); got != "default" {
		t.Fatalf("expected default, got %s", got)
	}
}

func TestLiveConfigGetStringReturnsDefaultWhenCacheMissing(t *testing.T) {
	cc := &ConfigClient{client: &Client{}, management: &ConfigManagement{}}
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetString("k", "x"); got != "x" {
		t.Fatalf("expected x, got %s", got)
	}
}

func TestLiveConfigGetJSONReturnsArbitraryValue(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"payload": map[string]interface{}{"a": 1}})
	proxy := cc.cachedProxy("billing")
	got := proxy.GetJSON("payload", nil)
	gotMap, ok := got.(map[string]interface{})
	if !ok || gotMap["a"] != 1 {
		t.Fatalf("expected {a:1}, got %v", got)
	}
}

func TestLiveConfigGetJSONReturnsDefaultOnMissing(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	proxy := cc.cachedProxy("billing")
	got := proxy.GetJSON("missing", map[string]interface{}{"fallback": true})
	if m, ok := got.(map[string]interface{}); !ok || m["fallback"] != true {
		t.Fatalf("expected fallback default, got %v", got)
	}
}

func TestLiveConfigGetJSONReturnsDefaultWhenCacheMissing(t *testing.T) {
	cc := &ConfigClient{client: &Client{}, management: &ConfigManagement{}}
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetJSON("k", "x"); got != "x" {
		t.Fatalf("expected x, got %v", got)
	}
}

func TestWithItemDescriptionPropagates(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{"max": 5})
	// addItem requires a prior declare, so register the config first.
	cc.management.RegisterConfig("billing", "svc", "prod", "", "", "")
	proxy := cc.cachedProxy("billing")
	proxy.GetInt("max", 5, WithItemDescription("hello"))
	batch := cc.management.buffer.drain()
	if len(batch) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(batch))
	}
	if got := batch[0].items["max"].description; got != "hello" {
		t.Fatalf("expected description 'hello', got %q", got)
	}
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

func TestGetOrCreateDoesNotErrorOnMissingCache(t *testing.T) {
	cc := newStubConfigClient("billing", map[string]interface{}{})
	cc.initOnce.Do(func() {}) // skip ensureInit network
	cc.initErr = nil
	if _, err := cc.GetOrCreate(context.Background(), "new-config",
		WithConfigParent("billing"),
		WithConfigName("New"),
		WithConfigDescription("desc"),
	); err != nil {
		t.Fatalf("GetOrCreate error: %v", err)
	}
	batch := cc.management.buffer.drain()
	if len(batch) != 1 || batch[0].id != "new-config" {
		t.Fatalf("buffer not populated: %+v", batch)
	}
	if batch[0].parent != "billing" {
		t.Fatalf("expected parent=billing, got %q", batch[0].parent)
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

func TestLiveConfigGetBoolReturnsDefaultWhenCacheMissing(t *testing.T) {
	cc := &ConfigClient{client: &Client{}, management: &ConfigManagement{}}
	proxy := cc.cachedProxy("billing")
	if got := proxy.GetBool("k", true); !got {
		t.Fatalf("expected default true, got %v", got)
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

func TestGetOrCreateReturnsEnsureInitError(t *testing.T) {
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
	if _, err := client.Config().GetOrCreate(context.Background(), "new"); err == nil {
		t.Fatalf("expected error from ensureInit failure")
	}
}

// Compile-time guard that the showcase-imported types are still exported
// under their expected names.
var _ ItemOption = WithItemDescription("x")

// Suppress unused-import false positives if any.
var _ = strings.TrimSpace
