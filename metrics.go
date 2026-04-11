package smplkit

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultFlushInterval = 60 * time.Second
	metricsEndpoint      = "/api/v1/metrics/bulk"
	metricsContentType   = "application/vnd.api+json"
)

// counter is a mutable accumulator for a single metric series.
type counter struct {
	value       int
	unit        string
	windowStart time.Time
}

// metricsReporter accumulates usage metrics in memory and periodically
// flushes them to the app service. All failures are swallowed — telemetry
// never throws, blocks, or degrades the caller's application.
type metricsReporter struct {
	httpClient    *http.Client
	appBaseURL    string
	environment   string
	service       string
	flushInterval time.Duration

	mu       sync.Mutex
	counters map[string]*counter
	gauges   map[string]*counter
	ticker   *time.Ticker
	done     chan struct{}
	closed   bool
}

// newMetricsReporter creates a new reporter. Pass flushInterval <= 0 for default (60s).
func newMetricsReporter(httpClient *http.Client, appBaseURL, environment, service string, flushInterval time.Duration) *metricsReporter {
	if flushInterval <= 0 {
		flushInterval = defaultFlushInterval
	}
	return &metricsReporter{
		httpClient:    httpClient,
		appBaseURL:    appBaseURL,
		environment:   environment,
		service:       service,
		flushInterval: flushInterval,
		counters:      make(map[string]*counter),
		gauges:        make(map[string]*counter),
	}
}

// makeKey builds a deterministic map key from metric name and merged dimensions.
func (r *metricsReporter) makeKey(name string, dimensions map[string]string) string {
	merged := map[string]string{
		"environment": r.environment,
		"service":     r.service,
	}
	for k, v := range dimensions {
		merged[k] = v
	}

	// Sort dimension pairs for deterministic key.
	pairs := make([]string, 0, len(merged))
	for k, v := range merged {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return name + "\x00" + strings.Join(pairs, "\x00")
}

// parseDimensions extracts the dimension map from a key produced by makeKey.
func parseDimensions(key string) (string, map[string]string) {
	parts := strings.Split(key, "\x00")
	name := parts[0]
	dims := make(map[string]string, len(parts)-1)
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		idx := strings.Index(p, "=")
		if idx >= 0 {
			dims[p[:idx]] = p[idx+1:]
		}
	}
	return name, dims
}

// Record increments a counter metric by value (default 1).
func (r *metricsReporter) Record(name string, value int, unit string, dimensions map[string]string) {
	if value == 0 {
		value = 1
	}
	key := r.makeKey(name, dimensions)
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.counters[key]
	if !ok {
		c = &counter{unit: unit, windowStart: time.Now().UTC()}
		r.counters[key] = c
	}
	c.value += value
	if c.unit == "" && unit != "" {
		c.unit = unit
	}
	r.maybeStartTicker()
}

// RecordGauge sets a gauge metric (replaces rather than accumulates).
func (r *metricsReporter) RecordGauge(name string, value int, unit string, dimensions map[string]string) {
	key := r.makeKey(name, dimensions)
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.gauges[key]
	if !ok {
		g = &counter{unit: unit, windowStart: time.Now().UTC()}
		r.gauges[key] = g
	}
	g.value = value
	if g.unit == "" && unit != "" {
		g.unit = unit
	}
	r.maybeStartTicker()
}

// Flush synchronously flushes accumulated metrics.
func (r *metricsReporter) Flush() {
	r.flush()
}

// Close stops the ticker and flushes one final time. Idempotent.
func (r *metricsReporter) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	if r.ticker != nil {
		r.ticker.Stop()
		close(r.done)
		r.ticker = nil
	}
	r.mu.Unlock()
	r.flush()
}

// maybeStartTicker starts the periodic flush ticker if not already running.
// Must be called while r.mu is held.
func (r *metricsReporter) maybeStartTicker() {
	if r.ticker == nil && !r.closed {
		r.ticker = time.NewTicker(r.flushInterval)
		r.done = make(chan struct{})
		go r.tickLoop(r.ticker, r.done)
	}
}

func (r *metricsReporter) tickLoop(ticker *time.Ticker, done chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			r.flush()
		}
	}
}

func (r *metricsReporter) flush() {
	r.mu.Lock()
	counters := r.counters
	gauges := r.gauges
	r.counters = make(map[string]*counter)
	r.gauges = make(map[string]*counter)
	r.mu.Unlock()

	if len(counters) == 0 && len(gauges) == 0 {
		return
	}

	payload := r.buildPayload(counters, gauges)

	func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("smplkit: metrics flush panic: %v", rec)
			}
		}()

		body, err := json.Marshal(payload)
		if err != nil {
			log.Printf("smplkit: metrics flush marshal failed: %v", err)
			return
		}

		url := strings.TrimRight(r.appBaseURL, "/") + metricsEndpoint
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			log.Printf("smplkit: metrics flush request build failed: %v", err)
			return
		}
		req.Header.Set("Content-Type", metricsContentType)
		req.Header.Set("User-Agent", userAgent)

		resp, err := r.httpClient.Do(req)
		if err != nil {
			log.Printf("smplkit: metrics flush failed: %v", err)
			return
		}
		resp.Body.Close()
	}()
}

func (r *metricsReporter) buildPayload(counters, gauges map[string]*counter) map[string]interface{} {
	data := make([]interface{}, 0, len(counters)+len(gauges))
	for key, c := range counters {
		data = append(data, r.entry(key, c))
	}
	for key, g := range gauges {
		data = append(data, r.entry(key, g))
	}
	return map[string]interface{}{"data": data}
}

func (r *metricsReporter) entry(key string, c *counter) map[string]interface{} {
	name, dims := parseDimensions(key)
	attrs := map[string]interface{}{
		"name":           name,
		"value":          c.value,
		"period_seconds": int(r.flushInterval.Seconds()),
		"dimensions":     dims,
		"recorded_at":    c.windowStart.Format(time.RFC3339Nano),
	}
	if c.unit != "" {
		attrs["unit"] = c.unit
	} else {
		attrs["unit"] = nil
	}
	return map[string]interface{}{
		"type":       "metric",
		"attributes": attrs,
	}
}
