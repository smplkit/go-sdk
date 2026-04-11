package smplkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeReporter(t *testing.T, opts ...func(*metricsReporter)) *metricsReporter {
	t.Helper()
	r := newMetricsReporter(http.DefaultClient, "https://app.smplkit.com", "test", "test-service", 60*time.Second)
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// captureFlush installs an httptest server and returns the reporter plus a
// function that reads the captured request body from the last flush.
func captureFlush(t *testing.T) (*metricsReporter, func() map[string]interface{}) {
	t.Helper()
	var mu sync.Mutex
	var lastBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		lastBody = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	r := newMetricsReporter(server.Client(), server.URL, "test", "test-service", 60*time.Second)
	// Override httpClient to hit the test server (authTransport not needed for test).
	r.httpClient = server.Client()
	r.appBaseURL = server.URL

	return r, func() map[string]interface{} {
		mu.Lock()
		defer mu.Unlock()
		if lastBody == nil {
			return nil
		}
		var payload map[string]interface{}
		_ = json.Unmarshal(lastBody, &payload)
		return payload
	}
}

// ===================================================================
// counter defaults
// ===================================================================

func TestCounter_Defaults(t *testing.T) {
	c := &counter{}
	assert.Equal(t, 0, c.value)
	assert.Equal(t, "", c.unit)
	assert.True(t, c.windowStart.IsZero())
}

func TestCounter_CustomValues(t *testing.T) {
	c := &counter{value: 5, unit: "evaluations"}
	assert.Equal(t, 5, c.value)
	assert.Equal(t, "evaluations", c.unit)
}

// ===================================================================
// metricsReporter — accumulation
// ===================================================================

func TestMetricsReporter_RecordAccumulatesValues(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "evaluations", nil)
	r.Record("flags.evaluations", 1, "evaluations", nil)
	r.Record("flags.evaluations", 1, "evaluations", nil)

	r.mu.Lock()
	assert.Len(t, r.counters, 1)
	for _, c := range r.counters {
		assert.Equal(t, 3, c.value)
	}
	r.mu.Unlock()
}

func TestMetricsReporter_RecordWithCustomValue(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.Record("logging.loggers_discovered", 10, "loggers", nil)

	r.mu.Lock()
	for _, c := range r.counters {
		assert.Equal(t, 10, c.value)
	}
	r.mu.Unlock()
}

func TestMetricsReporter_DifferentDimensionsSeparateCounters(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", map[string]string{"flag_id": "checkout-v2"})
	r.Record("flags.evaluations", 1, "", map[string]string{"flag_id": "dark-mode"})

	r.mu.Lock()
	assert.Len(t, r.counters, 2)
	r.mu.Unlock()
}

func TestMetricsReporter_SameDimensionsAccumulate(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", map[string]string{"flag_id": "checkout-v2"})
	r.Record("flags.evaluations", 1, "", map[string]string{"flag_id": "checkout-v2"})

	r.mu.Lock()
	assert.Len(t, r.counters, 1)
	for _, c := range r.counters {
		assert.Equal(t, 2, c.value)
	}
	r.mu.Unlock()
}

func TestMetricsReporter_BaseDimensionsInjected(t *testing.T) {
	r := newMetricsReporter(http.DefaultClient, "https://app.smplkit.com", "prod", "user-svc", 60*time.Second)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", map[string]string{"flag_id": "x"})

	r.mu.Lock()
	for key := range r.counters {
		_, dims := parseDimensions(key)
		assert.Equal(t, "prod", dims["environment"])
		assert.Equal(t, "user-svc", dims["service"])
		assert.Equal(t, "x", dims["flag_id"])
	}
	r.mu.Unlock()
}

func TestMetricsReporter_UnitFirstWriteWins(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "evaluations", nil)
	r.Record("flags.evaluations", 1, "different", nil)

	r.mu.Lock()
	for _, c := range r.counters {
		assert.Equal(t, "evaluations", c.unit)
	}
	r.mu.Unlock()
}

func TestMetricsReporter_UnitSetOnFirstNonEmpty(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", nil)
	r.Record("flags.evaluations", 1, "evaluations", nil)

	r.mu.Lock()
	for _, c := range r.counters {
		assert.Equal(t, "evaluations", c.unit)
	}
	r.mu.Unlock()
}

// ===================================================================
// metricsReporter — gauge
// ===================================================================

func TestMetricsReporter_GaugeReplacesValue(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.RecordGauge("platform.websocket_connections", 1, "connections", nil)
	r.RecordGauge("platform.websocket_connections", 0, "connections", nil)

	r.mu.Lock()
	for _, g := range r.gauges {
		assert.Equal(t, 0, g.value)
	}
	r.mu.Unlock()
}

func TestMetricsReporter_GaugeSeparateFromCounters(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", nil)
	r.RecordGauge("platform.websocket_connections", 1, "", nil)

	r.mu.Lock()
	assert.Len(t, r.counters, 1)
	assert.Len(t, r.gauges, 1)
	r.mu.Unlock()
}

func TestMetricsReporter_GaugeUnitFirstWriteWins(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.RecordGauge("platform.websocket_connections", 1, "connections", nil)
	r.RecordGauge("platform.websocket_connections", 0, "other", nil)

	r.mu.Lock()
	for _, g := range r.gauges {
		assert.Equal(t, "connections", g.unit)
	}
	r.mu.Unlock()
}

func TestMetricsReporter_GaugeUnitSetOnFirstNonEmpty(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.RecordGauge("platform.websocket_connections", 1, "", nil)
	r.RecordGauge("platform.websocket_connections", 0, "connections", nil)

	r.mu.Lock()
	for _, g := range r.gauges {
		assert.Equal(t, "connections", g.unit)
	}
	r.mu.Unlock()
}

// ===================================================================
// metricsReporter — flush
// ===================================================================

func TestMetricsReporter_FlushSendsHTTPPost(t *testing.T) {
	r, getPayload := captureFlush(t)
	defer r.Close()

	r.Record("flags.evaluations", 3, "evaluations", map[string]string{"flag_id": "x"})
	r.flush()

	payload := getPayload()
	require.NotNil(t, payload)

	data := payload["data"].([]interface{})
	assert.Len(t, data, 1)

	entry := data[0].(map[string]interface{})
	assert.Equal(t, "metric", entry["type"])

	attrs := entry["attributes"].(map[string]interface{})
	assert.Equal(t, "flags.evaluations", attrs["name"])
	assert.Equal(t, float64(3), attrs["value"])
	assert.Equal(t, "evaluations", attrs["unit"])
	assert.Equal(t, float64(60), attrs["period_seconds"])
	assert.NotEmpty(t, attrs["recorded_at"])

	dims := attrs["dimensions"].(map[string]interface{})
	assert.Equal(t, "test", dims["environment"])
	assert.Equal(t, "test-service", dims["service"])
	assert.Equal(t, "x", dims["flag_id"])
}

func TestMetricsReporter_FlushIncludesGauges(t *testing.T) {
	r, getPayload := captureFlush(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "evaluations", nil)
	r.RecordGauge("platform.websocket_connections", 1, "connections", nil)
	r.flush()

	payload := getPayload()
	require.NotNil(t, payload)

	data := payload["data"].([]interface{})
	assert.Len(t, data, 2)

	names := make(map[string]bool)
	for _, d := range data {
		entry := d.(map[string]interface{})
		attrs := entry["attributes"].(map[string]interface{})
		names[attrs["name"].(string)] = true
	}
	assert.True(t, names["flags.evaluations"])
	assert.True(t, names["platform.websocket_connections"])
}

func TestMetricsReporter_FlushResetsCounters(t *testing.T) {
	r, _ := captureFlush(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", nil)
	r.RecordGauge("platform.websocket_connections", 1, "", nil)
	r.flush()

	r.mu.Lock()
	assert.Empty(t, r.counters)
	assert.Empty(t, r.gauges)
	r.mu.Unlock()
}

func TestMetricsReporter_FlushEmptySendsNoHTTP(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := newMetricsReporter(server.Client(), server.URL, "test", "test-service", 60*time.Second)
	defer r.Close()

	r.flush()
	assert.Equal(t, 0, requestCount)
}

func TestMetricsReporter_FlushAfterFlushSendsNoHTTP(t *testing.T) {
	var mu sync.Mutex
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := newMetricsReporter(server.Client(), server.URL, "test", "test-service", 60*time.Second)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", nil)
	r.flush()

	mu.Lock()
	assert.Equal(t, 1, requestCount)
	mu.Unlock()

	r.flush()

	mu.Lock()
	assert.Equal(t, 1, requestCount)
	mu.Unlock()
}

func TestMetricsReporter_FlushHTTPErrorSwallowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	r := newMetricsReporter(server.Client(), server.URL, "test", "test-service", 60*time.Second)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", nil)
	r.flush() // Should not panic

	r.mu.Lock()
	assert.Empty(t, r.counters) // Data is discarded after failed flush
	r.mu.Unlock()
}

func TestMetricsReporter_FlushPublicMethod(t *testing.T) {
	r, getPayload := captureFlush(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", nil)
	r.Flush()

	assert.NotNil(t, getPayload())
}

// ===================================================================
// metricsReporter — ticker
// ===================================================================

func TestMetricsReporter_TickerStartsLazily(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.mu.Lock()
	assert.Nil(t, r.ticker)
	r.mu.Unlock()

	r.Record("flags.evaluations", 1, "", nil)

	r.mu.Lock()
	assert.NotNil(t, r.ticker)
	r.mu.Unlock()
}

func TestMetricsReporter_TickerNotStartedWhenNoRecords(t *testing.T) {
	r := makeReporter(t)
	r.Close()

	r.mu.Lock()
	assert.Nil(t, r.ticker)
	r.mu.Unlock()
}

func TestMetricsReporter_TickerNotStartedAfterClose(t *testing.T) {
	r := makeReporter(t)
	r.Close()

	r.Record("flags.evaluations", 1, "", nil)

	r.mu.Lock()
	assert.Nil(t, r.ticker)
	r.mu.Unlock()
}

// ===================================================================
// metricsReporter — close
// ===================================================================

func TestMetricsReporter_CloseFlushes(t *testing.T) {
	r, getPayload := captureFlush(t)

	r.Record("flags.evaluations", 1, "", nil)
	r.Close()

	assert.NotNil(t, getPayload())
	r.mu.Lock()
	assert.Empty(t, r.counters)
	r.mu.Unlock()
}

func TestMetricsReporter_CloseCancelsTicker(t *testing.T) {
	r := makeReporter(t)

	r.Record("flags.evaluations", 1, "", nil)
	r.mu.Lock()
	assert.NotNil(t, r.ticker)
	r.mu.Unlock()

	r.Close()

	r.mu.Lock()
	assert.Nil(t, r.ticker)
	r.mu.Unlock()
}

func TestMetricsReporter_CloseIdempotent(t *testing.T) {
	var mu sync.Mutex
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := newMetricsReporter(server.Client(), server.URL, "test", "test-service", 60*time.Second)

	r.Record("flags.evaluations", 1, "", nil)
	r.Close()
	r.Close() // Should not panic or double-flush

	mu.Lock()
	assert.Equal(t, 1, requestCount)
	mu.Unlock()
}

// ===================================================================
// metricsReporter — thread safety
// ===================================================================

func TestMetricsReporter_ConcurrentRecords(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Record("flags.evaluations", 1, "", nil)
			}
		}()
	}
	wg.Wait()

	r.mu.Lock()
	total := 0
	for _, c := range r.counters {
		total += c.value
	}
	r.mu.Unlock()
	assert.Equal(t, 1000, total)
}

// ===================================================================
// metricsReporter — periodic flush
// ===================================================================

func TestMetricsReporter_PeriodicFlush(t *testing.T) {
	var mu sync.Mutex
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := newMetricsReporter(server.Client(), server.URL, "test", "test-service", 100*time.Millisecond)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", nil)

	// Wait for at least one periodic flush.
	time.Sleep(250 * time.Millisecond)

	mu.Lock()
	assert.GreaterOrEqual(t, requestCount, 1)
	mu.Unlock()
}

// ===================================================================
// parseDimensions
// ===================================================================

func TestParseDimensions(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	key := r.makeKey("flags.evaluations", map[string]string{"flag_id": "checkout-v2"})
	name, dims := parseDimensions(key)

	assert.Equal(t, "flags.evaluations", name)
	assert.Equal(t, "test", dims["environment"])
	assert.Equal(t, "test-service", dims["service"])
	assert.Equal(t, "checkout-v2", dims["flag_id"])
}

func TestMakeKey_Deterministic(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	dims1 := map[string]string{"flag_id": "x", "extra": "y"}
	dims2 := map[string]string{"extra": "y", "flag_id": "x"}

	key1 := r.makeKey("test", dims1)
	key2 := r.makeKey("test", dims2)
	assert.Equal(t, key1, key2)
}

// ===================================================================
// Payload format
// ===================================================================

func TestPayloadFormat_JSONAPIStructure(t *testing.T) {
	r, getPayload := captureFlush(t)
	defer r.Close()

	r.Record("flags.evaluations", 42, "evaluations", map[string]string{"flag_id": "x"})
	r.RecordGauge("platform.websocket_connections", 1, "connections", nil)
	r.flush()

	payload := getPayload()
	require.NotNil(t, payload)

	data, ok := payload["data"].([]interface{})
	require.True(t, ok)
	assert.Len(t, data, 2)

	for _, d := range data {
		entry := d.(map[string]interface{})
		assert.Equal(t, "metric", entry["type"])

		attrs := entry["attributes"].(map[string]interface{})
		assert.Contains(t, attrs, "name")
		assert.Contains(t, attrs, "value")
		assert.Contains(t, attrs, "unit")
		assert.Contains(t, attrs, "period_seconds")
		assert.Contains(t, attrs, "dimensions")
		assert.Contains(t, attrs, "recorded_at")

		_, ok := attrs["dimensions"].(map[string]interface{})
		assert.True(t, ok)
	}
}

// ===================================================================
// DisableTelemetry option
// ===================================================================

func TestDisableTelemetry_Option(t *testing.T) {
	cfg := defaultConfig()
	DisableTelemetry()(&cfg)
	assert.True(t, cfg.disableTelemetry)
}

// ===================================================================
// WebSocket metrics instrumentation
// ===================================================================

func TestWebSocket_MetricsOnConnect(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	ws := newSharedWebSocket("https://app.smplkit.com", "test", r)
	assert.Equal(t, r, ws.metrics)
}

func TestWebSocket_NoMetrics(t *testing.T) {
	ws := newSharedWebSocket("https://app.smplkit.com", "test", nil)
	assert.Nil(t, ws.metrics)
}

// ===================================================================
// metricsReporter — null unit handling
// ===================================================================

func TestMetricsReporter_NullUnitInPayload(t *testing.T) {
	r, getPayload := captureFlush(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "", nil) // No unit
	r.flush()

	payload := getPayload()
	require.NotNil(t, payload)

	data := payload["data"].([]interface{})
	entry := data[0].(map[string]interface{})
	attrs := entry["attributes"].(map[string]interface{})
	assert.Nil(t, attrs["unit"])
}

func TestMetricsReporter_NonNullUnitInPayload(t *testing.T) {
	r, getPayload := captureFlush(t)
	defer r.Close()

	r.Record("flags.evaluations", 1, "evaluations", nil)
	r.flush()

	payload := getPayload()
	require.NotNil(t, payload)

	data := payload["data"].([]interface{})
	entry := data[0].(map[string]interface{})
	attrs := entry["attributes"].(map[string]interface{})
	assert.Equal(t, "evaluations", attrs["unit"])
}

// ===================================================================
// Integration: Client creates/closes metrics
// ===================================================================

func TestClient_MetricsEnabledByDefault(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_api_test")
	t.Setenv("SMPLKIT_ENVIRONMENT", "test")
	t.Setenv("SMPLKIT_SERVICE", "test-service")

	client, err := NewClient("", "", "")
	require.NoError(t, err)
	assert.NotNil(t, client.metrics)
	client.Close()
}

func TestClient_MetricsDisabled(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_api_test")
	t.Setenv("SMPLKIT_ENVIRONMENT", "test")
	t.Setenv("SMPLKIT_SERVICE", "test-service")

	client, err := NewClient("", "", "", DisableTelemetry())
	require.NoError(t, err)
	assert.Nil(t, client.metrics)
	client.Close()
}

// ===================================================================
// Integration: FlagsRuntime instrumentation
// ===================================================================

func TestFlagsRuntime_EvaluationRecordsMetrics(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	client := &Client{
		environment: "test",
		service:     "test-service",
		metrics:     r,
	}
	fc := &FlagsClient{client: client}
	rt := newFlagsRuntime(fc)

	// Populate flag store.
	rt.mu.Lock()
	rt.environment = "test"
	rt.flagStore["checkout-v2"] = map[string]interface{}{
		"key":          "checkout-v2",
		"type":         "boolean",
		"default":      false,
		"environments": map[string]interface{}{},
	}
	rt.mu.Unlock()

	// Mark init complete.
	rt.initOnce.Do(func() {})

	// First call: cache miss.
	rt.evaluateHandle(context.Background(), "checkout-v2", false, nil)

	r.mu.Lock()
	counterNames := make(map[string]int)
	for key, c := range r.counters {
		name, _ := parseDimensions(key)
		counterNames[name] += c.value
	}
	r.mu.Unlock()

	assert.Equal(t, 1, counterNames["flags.evaluations"])
	assert.Equal(t, 1, counterNames["flags.cache_misses"])
	assert.Equal(t, 0, counterNames["flags.cache_hits"])
}

func TestFlagsRuntime_CacheHitRecordsMetrics(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	client := &Client{
		environment: "test",
		service:     "test-service",
		metrics:     r,
	}
	fc := &FlagsClient{client: client}
	rt := newFlagsRuntime(fc)

	rt.mu.Lock()
	rt.environment = "test"
	rt.flagStore["checkout-v2"] = map[string]interface{}{
		"key":          "checkout-v2",
		"type":         "boolean",
		"default":      false,
		"environments": map[string]interface{}{},
	}
	rt.mu.Unlock()

	rt.initOnce.Do(func() {})

	// First call: cache miss. Second call: cache hit.
	rt.evaluateHandle(context.Background(), "checkout-v2", false, nil)
	rt.evaluateHandle(context.Background(), "checkout-v2", false, nil)

	r.mu.Lock()
	counterNames := make(map[string]int)
	for key, c := range r.counters {
		name, _ := parseDimensions(key)
		counterNames[name] += c.value
	}
	r.mu.Unlock()

	assert.Equal(t, 2, counterNames["flags.evaluations"])
	assert.Equal(t, 1, counterNames["flags.cache_misses"])
	assert.Equal(t, 1, counterNames["flags.cache_hits"])
}

func TestFlagsRuntime_NoMetricsWhenDisabled(t *testing.T) {
	client := &Client{
		environment: "test",
		service:     "test-service",
		metrics:     nil,
	}
	fc := &FlagsClient{client: client}
	rt := newFlagsRuntime(fc)

	rt.mu.Lock()
	rt.environment = "test"
	rt.flagStore["checkout-v2"] = map[string]interface{}{
		"key":          "checkout-v2",
		"type":         "boolean",
		"default":      false,
		"environments": map[string]interface{}{},
	}
	rt.mu.Unlock()

	rt.initOnce.Do(func() {})

	// Should not panic with nil metrics.
	rt.evaluateHandle(context.Background(), "checkout-v2", false, nil)
}

// ===================================================================
// Integration: ConfigClient instrumentation
// ===================================================================

func TestConfigClient_ResolveRecordsMetric(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	client := &Client{
		environment: "test",
		service:     "test-service",
		metrics:     r,
	}
	cc := &ConfigClient{client: client}
	cc.configCache = map[string]map[string]interface{}{
		"my-config": {"host": "localhost"},
	}
	cc.initOnce.Do(func() {}) // Mark init complete.

	result, err := cc.Resolve(context.Background(), "my-config")
	require.NoError(t, err)
	assert.Equal(t, "localhost", result["host"])

	r.mu.Lock()
	var found bool
	for key := range r.counters {
		name, dims := parseDimensions(key)
		if name == "config.resolutions" {
			assert.Equal(t, "my-config", dims["config_id"])
			found = true
		}
	}
	r.mu.Unlock()
	assert.True(t, found, "expected config.resolutions metric")
}

func TestConfigClient_ResolveNoMetricsWhenDisabled(t *testing.T) {
	client := &Client{
		environment: "test",
		service:     "test-service",
		metrics:     nil,
	}
	cc := &ConfigClient{client: client}
	cc.configCache = map[string]map[string]interface{}{
		"my-config": {"host": "localhost"},
	}
	cc.initOnce.Do(func() {})

	result, err := cc.Resolve(context.Background(), "my-config")
	require.NoError(t, err)
	assert.Equal(t, "localhost", result["host"])
}

func TestConfigClient_ChangeListenersRecordMetric(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	client := &Client{
		environment: "test",
		service:     "test-service",
		metrics:     r,
	}
	cc := &ConfigClient{client: client}

	oldCache := map[string]map[string]interface{}{
		"my-config": {"host": "old"},
	}
	newCache := map[string]map[string]interface{}{
		"my-config": {"host": "new"},
	}

	// Register a listener so diffAndFire fires.
	cc.OnChange(func(e *ConfigChangeEvent) {})
	cc.diffAndFire(oldCache, newCache, "manual")

	r.mu.Lock()
	var found bool
	for key := range r.counters {
		name, dims := parseDimensions(key)
		if name == "config.changes" {
			assert.Equal(t, "my-config", dims["config_id"])
			found = true
		}
	}
	r.mu.Unlock()
	assert.True(t, found, "expected config.changes metric")
}

func TestConfigClient_NoChangeNoMetric(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	client := &Client{
		environment: "test",
		service:     "test-service",
		metrics:     r,
	}
	cc := &ConfigClient{client: client}

	sameCache := map[string]map[string]interface{}{
		"my-config": {"host": "same"},
	}

	cc.OnChange(func(e *ConfigChangeEvent) {})
	cc.diffAndFire(sameCache, sameCache, "manual")

	r.mu.Lock()
	assert.Empty(t, r.counters)
	r.mu.Unlock()
}

// ===================================================================
// metricsReporter — window_start
// ===================================================================

func TestMetricsReporter_WindowStartSet(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	before := time.Now().UTC()
	r.Record("flags.evaluations", 1, "", nil)
	after := time.Now().UTC()

	r.mu.Lock()
	for _, c := range r.counters {
		assert.False(t, c.windowStart.Before(before))
		assert.False(t, c.windowStart.After(after))
	}
	r.mu.Unlock()
}

// ===================================================================
// metricsReporter — default flush interval
// ===================================================================

func TestMetricsReporter_DefaultFlushInterval(t *testing.T) {
	r := newMetricsReporter(http.DefaultClient, "https://app.smplkit.com", "test", "test-service", 0)
	defer r.Close()
	assert.Equal(t, defaultFlushInterval, r.flushInterval)
}

// ===================================================================
// metricsReporter — RecordGauge with value 0 at start
// ===================================================================

func TestMetricsReporter_RecordGaugeZero(t *testing.T) {
	r := makeReporter(t)
	defer r.Close()

	r.RecordGauge("platform.websocket_connections", 0, "connections", nil)

	r.mu.Lock()
	assert.Len(t, r.gauges, 1)
	for _, g := range r.gauges {
		assert.Equal(t, 0, g.value)
	}
	r.mu.Unlock()
}
