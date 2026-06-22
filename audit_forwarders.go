package smplkit

import (
	"context"
	"fmt"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// AuditForwarders manages SIEM streaming destinations for the
// authenticated account. Accessed via client.Audit().Forwarders().
//
// Forwarder CRUD is part of the single unified audit surface. It is
// account-wide and not environment-scoped; per-environment enablement
// lives in each forwarder's Environments map.
type AuditForwarders struct {
	gen *genaudit.ClientWithResponses
}

// ---------------------------------------------------------------------------
// Forwarder active-record surface
// ---------------------------------------------------------------------------

// New returns an unsaved Forwarder bound to this client. Call
// (*Forwarder).Save(ctx) to persist.
//
// id is the caller-supplied forwarder key — required at create time
// (the audit service does not auto-generate it). It is unique within the
// account and immutable for the lifetime of the forwarder; the audit
// service returns 409 if another live forwarder already uses it. Use a
// stable, human-readable identifier (e.g. "splunk-prod"); the key is what
// appears in every URL and audit-log line for this forwarder.
func (f *AuditForwarders) New(
	id string,
	name string,
	forwarderType ForwarderType,
	configuration HttpConfiguration,
	opts ...ForwarderOption,
) *Forwarder {
	fwd := &Forwarder{
		ID:            id,
		Name:          name,
		ForwarderType: forwarderType,
		Configuration: configuration,
		client:        f,
	}
	for _, opt := range opts {
		opt(fwd)
	}
	return fwd
}

// ForwarderOption configures an unsaved Forwarder returned by
// AuditForwarders.New.
type ForwarderOption func(*Forwarder)

// WithForwarderEnvironments sets the per-environment override map that
// drives enablement. A forwarder delivers in an environment only when
// that environment's entry has Enabled=true; each entry is a flat overlay that
// overrides only the leaves it sets (URL, Method, headers, …), inheriting the
// base configuration for the rest. Every referenced environment must exist and
// be managed for the account. Without this option the forwarder is created
// enabled nowhere. The entries are copied, so later mutations of the passed map
// do not affect the forwarder; mutate via forwarder.Environment(env) instead.
func WithForwarderEnvironments(environments map[string]ForwarderEnvironment) ForwarderOption {
	return func(fwd *Forwarder) {
		fwd.Environments = make(map[string]*ForwarderEnvironment, len(environments))
		for key, env := range environments {
			env := env
			fwd.Environments[key] = &env
		}
	}
}

// WithForwarderDescription sets the optional free-text description.
func WithForwarderDescription(description string) ForwarderOption {
	return func(fwd *Forwarder) { fwd.Description = &description }
}

// WithForwarderFilter sets the optional JSON Logic filter applied
// per event.
func WithForwarderFilter(filter map[string]interface{}) ForwarderOption {
	return func(fwd *Forwarder) { fwd.Filter = filter }
}

// WithForwarderTransform sets the optional template applied to each
// event before delivery, paired with the engine used to evaluate it.
// transform is intentionally untyped — today JSONATA carries a string
// expression, but the field shape is engine-defined and future engines
// may carry richer payloads. Both arguments must be supplied together;
// passing a non-nil transform without a transformType (or vice versa)
// is rejected server-side.
func WithForwarderTransform(transformType ForwarderTransformType, transform interface{}) ForwarderOption {
	return func(fwd *Forwarder) {
		fwd.Transform = transform
		tt := transformType
		fwd.TransformType = &tt
	}
}

// WithForwardSmplkitEvents opts this forwarder into receiving smplkit's
// own platform change events (flag, configuration, and similar changes
// smplkit records about your resources) in addition to your audit events.
// When true, each platform change event is delivered through every
// environment this forwarder is enabled in. Omitting this option leaves
// the field unset, which the server treats as false.
func WithForwardSmplkitEvents(forward bool) ForwarderOption {
	return func(fwd *Forwarder) { fwd.ForwardSmplkitEvents = &forward }
}

// Save creates or updates this forwarder on the server. Upsert
// behavior keyed on CreatedAt: nil → create (POST), set → full-replace
// update (PUT). After the call, every field is refreshed from the
// server response (including CreatedAt, UpdatedAt, Version). Header
// values are returned plaintext on reads, so a Get → mutate → Save
// round-trip preserves them.
func (fwd *Forwarder) Save(ctx context.Context) error {
	if fwd.client == nil {
		return &Error{Message: "forwarder was constructed without a client; cannot save"}
	}
	if err := fwd.validateTransform(); err != nil {
		return err
	}
	if fwd.CreatedAt == nil {
		updated, err := fwd.client.create(ctx, fwd)
		if err != nil {
			return err
		}
		fwd.apply(updated)
		return nil
	}
	updated, err := fwd.client.update(ctx, fwd)
	if err != nil {
		return err
	}
	fwd.apply(updated)
	return nil
}

// validateTransform enforces the two transform-related invariants
// before sending to the server:
//
//  1. Transform and TransformType must be set together (both or
//     neither). Setting one without the other is a configuration
//     error.
//  2. When TransformType is ForwarderTransformTypeJSONata, Transform
//     must be a string — JSONata expressions are strings. Future
//     engines may carry richer shapes; their own rules apply.
func (fwd *Forwarder) validateTransform() error {
	hasTransform := fwd.Transform != nil
	hasType := fwd.TransformType != nil
	switch {
	case hasTransform && !hasType:
		return &Error{Message: "forwarder Transform is set but TransformType is not; both must be specified together"}
	case hasType && !hasTransform:
		return &Error{Message: "forwarder TransformType is set but Transform is not; both must be specified together"}
	}
	if hasType && *fwd.TransformType == ForwarderTransformTypeJSONata {
		if _, ok := fwd.Transform.(string); !ok {
			return &Error{Message: "forwarder Transform must be a string when TransformType is JSONATA"}
		}
	}
	return nil
}

// Delete removes this forwarder on the server.
func (fwd *Forwarder) Delete(ctx context.Context) error {
	if fwd.client == nil || fwd.ID == "" {
		return &Error{Message: "forwarder was constructed without a client or id; cannot delete"}
	}
	return fwd.client.Delete(ctx, fwd.ID)
}

// Environment returns the per-environment override for environment — the
// single place to read or set what this forwarder overrides there (ADR-056).
//
// It returns the ForwarderEnvironment for environment, creating an empty one
// (and inserting it into Environments) on first access, so overrides can be set
// directly on the returned pointer:
//
//	forwarder.Environment("production").Enabled = true
//	forwarder.Environment("production").URL = "https://prod.siem.example.com/in"
//	forwarder.Environment("production").SetHeader("DD-API-KEY", "prod-secret")
//
// Only the leaves you set are sent on save; everything else inherits the base
// definition (the server resolves base ⊕ overrides on delivery).
func (fwd *Forwarder) Environment(environment string) *ForwarderEnvironment {
	if fwd.Environments == nil {
		fwd.Environments = map[string]*ForwarderEnvironment{}
	}
	env, ok := fwd.Environments[environment]
	if !ok {
		env = &ForwarderEnvironment{}
		fwd.Environments[environment] = env
	}
	return env
}

// Enabled reports the read-only roll-up: true when the forwarder is enabled in
// at least one environment. The audit API has no top-level enabled field, so
// the wrapper derives it from the Environments map (and it stays correct for
// in-memory edits made before Save). Enable per environment via
// forwarder.Environment(env).Enabled = true.
func (fwd *Forwarder) Enabled() bool {
	for _, env := range fwd.Environments {
		if env.Enabled {
			return true
		}
	}
	return false
}

func (fwd *Forwarder) apply(other *Forwarder) {
	fwd.ID = other.ID
	fwd.Name = other.Name
	fwd.Description = other.Description
	fwd.ForwarderType = other.ForwarderType
	fwd.Environments = other.Environments
	fwd.Filter = other.Filter
	fwd.Transform = other.Transform
	fwd.TransformType = other.TransformType
	fwd.ForwardSmplkitEvents = other.ForwardSmplkitEvents
	fwd.Configuration = other.Configuration
	fwd.CreatedAt = other.CreatedAt
	fwd.UpdatedAt = other.UpdatedAt
	fwd.DeletedAt = other.DeletedAt
	fwd.Version = other.Version
}

// ---------------------------------------------------------------------------
// Forwarder collection read surface
// ---------------------------------------------------------------------------

// List returns one page of forwarders. Offset pagination via PageNumber /
// PageSize.
func (f *AuditForwarders) List(ctx context.Context, input ListForwardersInput) (*ListForwardersPage, error) {
	params := &genaudit.ListForwardersParams{}
	if input.ForwarderType != "" {
		ft := string(input.ForwarderType)
		params.FilterForwarderType = &ft
	}
	if input.PageNumber > 0 {
		params.PageNumber = &input.PageNumber
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	if input.MetaTotal {
		mt := true
		params.MetaTotal = &mt
	}
	resp, err := f.gen.ListForwardersWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("audit Forwarders.List: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	body := resp.ApplicationvndApiJSON200
	page := &ListForwardersPage{
		Forwarders: make([]Forwarder, 0, len(body.Data)),
		Pagination: paginationFromMeta(body.Meta.Pagination),
	}
	for _, r := range body.Data {
		page.Forwarders = append(page.Forwarders, forwarderFromResource(r, f))
	}
	return page, nil
}

// Get returns one forwarder by id; returned instance is bound to this
// client so forwarder.Save(ctx) and forwarder.Delete(ctx) work.
func (f *AuditForwarders) Get(ctx context.Context, forwarderID string) (*Forwarder, error) {
	resp, err := f.gen.GetForwarderWithResponse(ctx, forwarderID)
	if err != nil {
		return nil, fmt.Errorf("audit Forwarders.Get: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	out := forwarderFromResource(resp.ApplicationvndApiJSON200.Data, f)
	return &out, nil
}

// Delete removes a forwarder by id.
func (f *AuditForwarders) Delete(ctx context.Context, forwarderID string) error {
	resp, err := f.gen.DeleteForwarderWithResponse(ctx, forwarderID)
	if err != nil {
		return fmt.Errorf("audit Forwarders.Delete: %w", err)
	}
	if resp.StatusCode() != 204 {
		return checkStatus(resp.StatusCode(), resp.Body)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// create posts a new forwarder and returns the server-authoritative
// response. Called by Forwarder.Save on unsaved instances.
func (f *AuditForwarders) create(ctx context.Context, fwd *Forwarder) (*Forwarder, error) {
	body := genaudit.CreateForwarderApplicationVndAPIPlusJSONRequestBody{
		Data: forwarderCreateResourceFromForwarder(fwd),
	}
	resp, err := f.gen.CreateForwarderWithApplicationVndAPIPlusJSONBodyWithResponse(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("audit Forwarders.Create: %w", err)
	}
	if resp.StatusCode() != 201 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	if resp.ApplicationvndApiJSON201 == nil {
		return nil, fmt.Errorf("audit Forwarders.Create: empty 201 body")
	}
	out := forwarderFromResource(resp.ApplicationvndApiJSON201.Data, f)
	return &out, nil
}

// update PUTs a full-replace and returns the server-authoritative
// response. Called by Forwarder.Save on saved instances.
func (f *AuditForwarders) update(ctx context.Context, fwd *Forwarder) (*Forwarder, error) {
	body := genaudit.UpdateForwarderApplicationVndAPIPlusJSONRequestBody{
		Data: forwarderResourceFromForwarder(fwd.ID, fwd),
	}
	resp, err := f.gen.UpdateForwarderWithApplicationVndAPIPlusJSONBodyWithResponse(
		ctx, fwd.ID, body,
	)
	if err != nil {
		return nil, fmt.Errorf("audit Forwarders.Update: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	out := forwarderFromResource(resp.ApplicationvndApiJSON200.Data, f)
	return &out, nil
}

// forwarderAttributes builds the shared Forwarder attribute payload.
//
// The base `enabled` is server-pinned false (ADR-055) — the wrapper never
// sends it. Enablement travels entirely through the `environments` map.
func forwarderAttributes(fwd *Forwarder) genaudit.Forwarder {
	attrs := genaudit.Forwarder{
		Name:          fwd.Name,
		ForwarderType: genaudit.ForwarderType(fwd.ForwarderType),
		Configuration: httpConfigurationToWire(fwd.Configuration),
	}
	if len(fwd.Environments) > 0 {
		envs := environmentsToWire(fwd.Environments)
		attrs.Environments = &envs
	}
	if fwd.Description != nil {
		attrs.Description = fwd.Description
	}
	if fwd.Filter != nil {
		f := fwd.Filter
		attrs.Filter = &f
	}
	// Transform body is engine-defined; pass through whatever the
	// caller set. TransformType must be set whenever Transform is set
	// — the server enforces that, so we just forward both.
	if fwd.Transform != nil {
		attrs.Transform = fwd.Transform
	}
	if fwd.TransformType != nil {
		tt := genaudit.ForwarderTransformType(*fwd.TransformType)
		attrs.TransformType = &tt
	}
	if fwd.ForwardSmplkitEvents != nil {
		v := *fwd.ForwardSmplkitEvents
		attrs.ForwardSmplkitEvents = &v
	}
	return attrs
}

func forwarderResourceFromForwarder(id string, fwd *Forwarder) genaudit.ForwarderResource {
	rt := "forwarder"
	var idPtr *string
	if id != "" {
		idPtr = &id
	}
	return genaudit.ForwarderResource{
		Id:         idPtr,
		Type:       &rt,
		Attributes: forwarderAttributes(fwd),
	}
}

func forwarderCreateResourceFromForwarder(fwd *Forwarder) genaudit.ForwarderCreateResource {
	rt := genaudit.ForwarderCreateResourceTypeForwarder
	return genaudit.ForwarderCreateResource{
		Id:         fwd.ID,
		Type:       &rt,
		Attributes: forwarderAttributes(fwd),
	}
}

// environmentsToWire converts the wrapper per-environment override map to the
// flat, sparse leaf-path overlay the audit API expects (ADR-056): each
// environment emits "enabled" plus only the leaves it overrides, with every
// header addressed individually as headers.<name>.
func environmentsToWire(envs map[string]*ForwarderEnvironment) map[string]map[string]interface{} {
	out := make(map[string]map[string]interface{}, len(envs))
	for key, env := range envs {
		overlay := map[string]interface{}{"enabled": env.Enabled}
		if env.URL != "" {
			overlay["url"] = env.URL
		}
		if env.Method != "" {
			overlay["method"] = string(env.Method)
		}
		if env.SuccessStatus != "" {
			overlay["success_status"] = env.SuccessStatus
		}
		if env.TlsVerify != nil {
			overlay["tls_verify"] = *env.TlsVerify
		}
		if env.CaCert != nil {
			overlay["ca_cert"] = *env.CaCert
		}
		for name, value := range env.Headers {
			overlay["headers."+name] = value
		}
		out[key] = overlay
	}
	return out
}

// environmentsFromWire parses the flat leaf-path overlay the audit API returns
// (ADR-056) back into the wrapper shape. Header leaves arrive as headers.<name>
// (split on the first dot, so a dotted header name is preserved); unknown leaves
// are ignored for forward compatibility. Reads are pure override — an unset
// leaf stays the wrapper's zero value, never the base.
func environmentsFromWire(envs map[string]map[string]interface{}) map[string]*ForwarderEnvironment {
	out := make(map[string]*ForwarderEnvironment, len(envs))
	for key, raw := range envs {
		out[key] = &ForwarderEnvironment{
			Enabled:       overlayBool(raw, "enabled"),
			URL:           overlayString(raw, "url"),
			Method:        HttpMethod(overlayString(raw, "method")),
			SuccessStatus: overlayString(raw, "success_status"),
			TlsVerify:     overlayBoolPtr(raw, "tls_verify"),
			CaCert:        overlayStringPtr(raw, "ca_cert"),
			Headers:       overlayHeaders(raw),
		}
	}
	return out
}

func httpConfigurationToWire(h HttpConfiguration) genaudit.ForwarderHttpConfiguration {
	out := genaudit.ForwarderHttpConfiguration{
		Url: h.URL,
	}
	if h.Method != "" {
		m := genaudit.ForwarderHttpConfigurationMethod(h.Method)
		out.Method = &m
	}
	if h.SuccessStatus != "" {
		s := h.SuccessStatus
		out.SuccessStatus = &s
	}
	if h.TlsVerify != nil {
		v := *h.TlsVerify
		out.TlsVerify = &v
	}
	if h.CaCert != nil {
		c := *h.CaCert
		out.CaCert = &c
	}
	if len(h.Headers) > 0 {
		headers := make(map[string]string, len(h.Headers))
		for name, value := range h.Headers {
			headers[name] = value
		}
		out.Headers = &headers
	}
	return out
}

func forwarderFromResource(r genaudit.ForwarderResource, client *AuditForwarders) Forwarder {
	id := ""
	if r.Id != nil {
		id = *r.Id
	}
	a := r.Attributes
	out := Forwarder{
		ID:                   id,
		Name:                 a.Name,
		Description:          a.Description,
		ForwarderType:        ForwarderType(a.ForwarderType),
		Configuration:        httpConfigurationFromWire(a.Configuration),
		ForwardSmplkitEvents: a.ForwardSmplkitEvents,
		CreatedAt:            a.CreatedAt,
		UpdatedAt:            a.UpdatedAt,
		DeletedAt:            a.DeletedAt,
		Version:              a.Version,
		client:               client,
	}
	if a.TransformType != nil {
		tt := ForwarderTransformType(*a.TransformType)
		out.TransformType = &tt
	}
	if a.Environments != nil {
		out.Environments = environmentsFromWire(*a.Environments)
	}
	if a.Filter != nil {
		out.Filter = *a.Filter
	}
	// Transform body is engine-defined; pass the server-returned value
	// through verbatim. The Go zero value (nil interface{}) signals
	// "no transform"; any other value — string, map, slice — is
	// surfaced as-is.
	if a.Transform != nil {
		out.Transform = a.Transform
	}
	return out
}

func httpConfigurationFromWire(h genaudit.ForwarderHttpConfiguration) HttpConfiguration {
	out := HttpConfiguration{
		URL: h.Url,
	}
	if h.Method != nil {
		out.Method = HttpMethod(*h.Method)
	}
	if h.SuccessStatus != nil {
		out.SuccessStatus = *h.SuccessStatus
	}
	if h.TlsVerify != nil {
		v := *h.TlsVerify
		out.TlsVerify = &v
	}
	if h.CaCert != nil {
		c := *h.CaCert
		out.CaCert = &c
	}
	if h.Headers != nil {
		out.Headers = make(map[string]string, len(*h.Headers))
		for name, value := range *h.Headers {
			out.Headers[name] = value
		}
	}
	return out
}
