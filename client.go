package smplkit

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/smplkit/go-sdk/v3/internal/debug"
	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
	genconfig "github.com/smplkit/go-sdk/v3/internal/generated/config"
	genflags "github.com/smplkit/go-sdk/v3/internal/generated/flags"
	genjobs "github.com/smplkit/go-sdk/v3/internal/generated/jobs"
	genlogging "github.com/smplkit/go-sdk/v3/internal/generated/logging"
)

// Periodic flush of all sub-client registration buffers (contexts, flags,
// loggers, configs). Threshold flushes still fire immediately when buffers
// fill up; this timer is the liveness guarantee for the tail.
const periodicFlushInterval = 60 * time.Second

// SmplClient is the canonical entry point for the smplkit SDK.
//
// Create one with NewClient and access sub-clients via accessor methods:
//
//	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_api_...", Environment: "production", Service: "my-service"})
//	billing, err := client.Config().Get(ctx, "billing")
//
// One client exposes config, flags, logging, audit, jobs, platform, and
// account.
type SmplClient struct {
	apiKey       string
	environment  string
	service      string
	appURL       string
	httpClient   *http.Client
	appGenerated genapp.ClientInterface

	// disableStreaming mirrors Config.DisableStreaming: the config / flags /
	// logging runtime surfaces fetch synchronously and never open the shared
	// WebSocket or spawn background goroutines.
	disableStreaming bool

	platform *PlatformClient
	account  *AccountClient
	config   *ConfigClient
	flags    *FlagsClient
	logging  *LoggingClient
	audit    *AuditClient
	jobs     *JobsClient

	// Shared context registration buffer — drained by client.Platform().
	// Contexts() and shared with the flags runtime's auto-registration path.
	contextBuf *contextRegistrationBuffer

	// ambientContexts is the per-client evaluation context stashed by
	// SetContext, guarded by ambientMu. The flags runtime reads it as a
	// fallback ambient source when no explicit contexts and no context
	// provider are supplied.
	ambientMu       sync.RWMutex
	ambientContexts []Context

	metrics *metricsReporter

	wsMu sync.Mutex
	ws   *sharedWebSocket

	// Deferred background machinery (periodic flush + service-context
	// registration) is started exactly once on the first config/flags/
	// logging operation, never at construction.
	startMu    sync.Mutex
	started    bool
	closed     bool
	flushTimer *time.Timer
}

// serviceURL builds a per-service URL from the client configuration.
// If baseURLOverride is set (test mode), all services share that URL.
// Otherwise the URL is {scheme}://{subdomain}.{baseDomain}.
func serviceURL(opts clientConfig, subdomain string, rc *resolvedConfig) string {
	if opts.baseURLOverride != "" {
		return opts.baseURLOverride
	}
	return fmt.Sprintf("%s://%s.%s", rc.scheme, subdomain, rc.baseDomain)
}

// NewClient creates a new smplkit API client.
//
// Configuration is resolved from four layers (later layers override earlier ones):
//  1. Defaults (scheme=https, baseDomain=smplkit.com)
//  2. Config file (~/.smplkit) with [common] and profile sections
//  3. Environment variables (SMPLKIT_API_KEY, SMPLKIT_ENVIRONMENT, etc.)
//  4. Explicit Config struct fields
//
// Use ClientOption functions to customize the timeout or HTTP client.
//
// Construction is side-effect-free: no background goroutines, no phone-home.
// The periodic registration-buffer flush and the service-context registration
// are deferred until the first config/flags/logging operation — so an
// audit-only or jobs-only customer pays zero goroutines and zero network at
// construction.
func NewClient(cfg Config, opts ...ClientOption) (*SmplClient, error) {
	rc, err := resolveConfig(cfg)
	if err != nil {
		return nil, err
	}

	if rc.debug {
		debug.Enable()
	}

	optCfg := defaultConfig()
	for _, opt := range opts {
		opt(&optCfg)
	}

	var httpClient *http.Client
	if optCfg.httpClient != nil {
		httpClient = optCfg.httpClient
	} else {
		httpClient = &http.Client{
			Timeout: optCfg.timeout,
		}
	}

	// Wrap the transport with auth.
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{
		token: rc.apiKey,
		base:  base,
	}

	configURL := serviceURL(optCfg, "config", rc)
	flagsURL := serviceURL(optCfg, "flags", rc)
	appURL := serviceURL(optCfg, "app", rc)
	logURL := serviceURL(optCfg, "logging", rc)
	auditURL := serviceURL(optCfg, "audit", rc)
	jobsURL := serviceURL(optCfg, "jobs", rc)

	// Capture extra headers once; the closures below close over this value.
	extraHeaders := rc.extraHeaders

	// Build the generated config client, passing the auth-wrapped httpClient
	// and request editors that inject extra headers (first) then SDK headers
	// (second, so SDK headers win on any collision).
	configExtraEditor := genconfig.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	})
	headerEditor := genconfig.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genConfigClient, _ := genconfig.NewClient(configURL,
		genconfig.WithHTTPClient(httpClient),
		configExtraEditor,
		headerEditor,
	)

	// Build the generated flags client with the same pattern.
	flagsExtraEditor := genflags.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	})
	flagsHeaderEditor := genflags.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genFlagsClient, _ := genflags.NewClient(flagsURL,
		genflags.WithHTTPClient(httpClient),
		flagsExtraEditor,
		flagsHeaderEditor,
	)

	// Build the generated app client for context operations.
	appExtraEditor := genapp.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	})
	appHeaderEditor := genapp.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genAppClient, _ := genapp.NewClient(appURL,
		genapp.WithHTTPClient(httpClient),
		appExtraEditor,
		appHeaderEditor,
	)

	// Build the generated logging client.
	loggingExtraEditor := genlogging.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	})
	loggingHeaderEditor := genlogging.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genLoggingClient, _ := genlogging.NewClient(logURL,
		genlogging.WithHTTPClient(httpClient),
		loggingExtraEditor,
		loggingHeaderEditor,
	)

	// Build the generated audit client. Environment scoping is body-driven
	// (ADR-055): the configured environment is stamped onto the event request
	// body when recording and supplied as the default filter[environment] on
	// the read / discovery surfaces — it no longer rides on a request header,
	// so a single transport backs the whole audit surface (runtime reads /
	// writes and account-wide forwarder CRUD alike).
	auditExtraEditor := genaudit.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	})
	auditHeaderEditor := genaudit.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genAuditRaw, _ := genaudit.NewClient(auditURL,
		genaudit.WithHTTPClient(httpClient),
		auditHeaderEditor,
		auditExtraEditor,
	)
	genAuditClient := &genaudit.ClientWithResponses{ClientInterface: genAuditRaw}

	// Build the generated jobs client.
	jobsExtraEditor := genjobs.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	})
	jobsHeaderEditor := genjobs.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genJobsRaw, _ := genjobs.NewClient(jobsURL,
		genjobs.WithHTTPClient(httpClient),
		jobsExtraEditor,
		jobsHeaderEditor,
	)
	genJobsClient := &genjobs.ClientWithResponses{ClientInterface: genJobsRaw}

	ctxBuf := newContextRegistrationBuffer()

	c := &SmplClient{
		apiKey:           rc.apiKey,
		environment:      rc.environment,
		service:          rc.service,
		appURL:           appURL,
		httpClient:       httpClient,
		appGenerated:     genAppClient,
		contextBuf:       ctxBuf,
		disableStreaming: rc.disableStreaming,
	}

	if !rc.disableTelemetry {
		c.metrics = newMetricsReporter(httpClient, appURL, rc.environment, rc.service, 0)
	}

	// Platform's cross-cutting CRUD on one client; it borrows the shared app
	// transport and owns the context-registration buffer. Built BEFORE flags
	// so the contexts seam below is available.
	c.platform = newPlatformClient(genAppClient, ctxBuf)
	// Account-level settings on one client; built from the shared app transport.
	c.account = newAccountClient(genAppClient)
	// Config's full surface on one client; borrows the shared config transport
	// and the parent's WebSocket.
	c.config = newConfigClient(c, genConfigClient, c.metrics)
	// Flags' full surface on one client; borrows the shared flags transport and
	// WebSocket. The contexts sub-client is the injection seam for
	// evaluation-context registration, wired to client.Platform().Contexts().
	c.flags = newFlagsClient(c, genFlagsClient, genAppClient, c.platform.contexts, c.metrics)
	// Logging's full surface on one client; borrows the shared logging
	// transport and WebSocket. The two management sub-clients live at
	// client.Logging().Loggers() / client.Logging().LogGroups().
	c.logging = newLoggingClient(c, genLoggingClient, c.metrics)
	// Audit's full surface on one client; environment scoping is body-driven
	// (ADR-055) — the configured environment is stamped on the event body and
	// defaulted onto filter[environment] for reads, so one gen client backs the
	// whole surface.
	c.audit = newAuditClient(genAuditClient, rc.environment, rc.disableEventBuffering)
	c.audit.client = c
	// Jobs reuses the shared jobs transport (single connection pool) so
	// client.Jobs() is one-stop; the configured environment defaults
	// environment-scoped writes/reads (one-off birth, run-now, runs filter).
	c.jobs = newJobsClient(genJobsClient, rc.environment)

	prefixLen := min(10, len(rc.apiKey))
	maskedKey := rc.apiKey[:prefixLen] + "..."
	debug.Debug("lifecycle", "SmplClient created (api_key=%s, environment=%s, service=%s)", maskedKey, rc.environment, rc.service)

	return c, nil
}

// Environment returns the resolved environment name.
func (c *SmplClient) Environment() string { return c.environment }

// Service returns the resolved service name.
func (c *SmplClient) Service() string { return c.service }

// Config returns the sub-client for the Smpl Config service.
func (c *SmplClient) Config() *ConfigClient { return c.config }

// Flags returns the sub-client for the Smpl Flags service.
func (c *SmplClient) Flags() *FlagsClient { return c.flags }

// Logging returns the sub-client for the Smpl Logging service.
func (c *SmplClient) Logging() *LoggingClient { return c.logging }

// Audit returns the sub-client for the Smpl Audit service.
//
// Use client.Audit().Events().Create(...) to record an event; the call is
// fire-and-forget — the SDK buffers the event in memory and a background
// goroutine flushes the buffer with retry on transient failures.
func (c *SmplClient) Audit() *AuditClient { return c.audit }

// Jobs returns the sub-client for the Smpl Jobs service.
func (c *SmplClient) Jobs() *JobsClient { return c.jobs }

// Platform returns the Smpl Platform sub-client — cross-cutting CRUD on
// environments, services, contexts, and context types.
func (c *SmplClient) Platform() *PlatformClient { return c.platform }

// Account returns the Smpl Account sub-client — account-level settings.
func (c *SmplClient) Account() *AccountClient { return c.account }

// ensureStarted starts the deferred background machinery exactly once.
//
// Idempotent and thread-safe (lock + flag); a no-op after Close. Triggered by
// the first config/flags/logging operation or WebSocket open — never at
// construction.
//
// With Config.DisableStreaming set there is no background machinery to start:
// no periodic flush timer is armed (buffers flush inline at their thresholds
// and on the first live call instead) and the service-context registration
// runs synchronously rather than on a goroutine.
func (c *SmplClient) ensureStarted() {
	c.startMu.Lock()
	if c.started || c.closed {
		c.startMu.Unlock()
		return
	}
	c.started = true
	c.startMu.Unlock()
	if c.disableStreaming {
		c.registerServiceContext(context.Background())
		return
	}
	c.schedulePeriodicFlush()
	go c.registerServiceContext(context.Background())
}

// schedulePeriodicFlush arms the periodic registration-buffer flush.
// Self-rescheduling via periodicFlush; a no-op after Close.
func (c *SmplClient) schedulePeriodicFlush() {
	c.startMu.Lock()
	if c.closed {
		c.startMu.Unlock()
		return
	}
	c.flushTimer = time.AfterFunc(periodicFlushInterval, c.periodicFlush)
	c.startMu.Unlock()
}

// periodicFlush drains every registration buffer (contexts, flags, loggers,
// configs) and reschedules itself. A no-op after Close.
func (c *SmplClient) periodicFlush() {
	if c.isClosed() {
		return
	}
	ctx := context.Background()
	_ = c.platform.contexts.Flush(ctx)
	c.flags.Flush(ctx)
	_ = c.logging.Loggers().Flush(ctx)
	_ = c.config.Flush(ctx)
	c.schedulePeriodicFlush()
}

func (c *SmplClient) isClosed() bool {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	return c.closed
}

// registerServiceContext sends environment and service context registrations
// to the app service. Only the values that are set are registered; if neither
// environment nor service was provided the POST is skipped entirely. Errors
// are logged but not returned.
func (c *SmplClient) registerServiceContext(ctx context.Context) {
	items := []genapp.ContextBulkItem{}
	if c.environment != "" {
		items = append(items, genapp.ContextBulkItem{Type: "environment", Key: c.environment})
	}
	if c.service != "" {
		svcAttrs := map[string]interface{}{"name": c.service}
		items = append(items, genapp.ContextBulkItem{Type: "service", Key: c.service, Attributes: &svcAttrs})
	}
	if len(items) == 0 {
		return
	}
	reqBody := genapp.ContextBulkRegister{Contexts: items}
	resp, err := c.appGenerated.BulkRegisterContextsWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		log.Printf("smplkit: failed to register service context: %s", err.Error())
		debug.Debug("api", "service context registration error details: %+v", err)
		return
	}
	resp.Body.Close()
}

// ensureWS returns the shared WebSocket connection.
func (c *SmplClient) ensureWS() *sharedWebSocket {
	c.ensureStarted()
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.ws == nil {
		c.ws = newSharedWebSocket(c.appURL, c.apiKey, c.metrics)
		c.ws.start()
	}
	return c.ws
}

// WaitUntilReady optionally pre-warms the SDK and blocks until the live
// socket is up.
//
// Eagerly connects config and flags — flushing discovery, pre-fetching all
// flags and configs into the local cache, opening the live-updates WebSocket
// — and waits for the handshake to complete. After this returns, flag.Get() /
// client.Config().Subscribe() hit cache (no first-request connect tax) and any
// OnChange listeners receive every server event from this point forward.
//
// Optional: config and flags connect lazily on first live use, so this is
// purely a pre-warm / WebSocket-ready barrier. Logging integration is not
// connected here — call client.Logging().Install() separately if you want it
// (it installs adapters and hooks into your application's logger, which should
// be opt-in).
//
// timeout=0 uses the SDK default. Context cancellation also returns. Returns
// a TimeoutError if the WebSocket fails to connect within timeout.
//
// With Config.DisableStreaming set there is no socket to wait for: the call
// still pre-warms the flag and config caches, then returns without opening a
// WebSocket.
func (c *SmplClient) WaitUntilReady(ctx context.Context, timeout time.Duration) error {
	if timeout == 0 {
		timeout = wsConnectTimeout
	}
	if err := c.flags.runtime.ensureInit(ctx); err != nil {
		return err
	}
	if err := c.config.ensureInit(ctx); err != nil {
		return err
	}
	if c.disableStreaming {
		return nil
	}
	return c.ensureWS().waitConnected(ctx, timeout)
}

// Close releases all resources held by the client and its sub-clients.
func (c *SmplClient) Close() error {
	debug.Debug("lifecycle", "SmplClient.Close() called")
	c.startMu.Lock()
	c.closed = true
	if c.flushTimer != nil {
		c.flushTimer.Stop()
		c.flushTimer = nil
	}
	c.startMu.Unlock()
	if c.logging != nil {
		c.logging.close()
	}
	if c.audit != nil {
		_ = c.audit.Close()
	}
	c.stopWS()
	if c.metrics != nil {
		c.metrics.Close()
	}
	return nil
}

// stopWS stops the shared WebSocket connection.
func (c *SmplClient) stopWS() {
	c.wsMu.Lock()
	ws := c.ws
	c.wsMu.Unlock()
	if ws != nil {
		ws.stop()
	}
}
