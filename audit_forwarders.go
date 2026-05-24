package smplkit

import (
	"context"
	"fmt"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// AuditForwarders manages SIEM streaming destinations for the
// authenticated account. Accessed via Client.Manage().Audit().Forwarders().
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
// (the audit service does not auto-generate it). Use a stable,
// human-readable identifier (e.g. "splunk-prod"); the key is what
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
		Enabled:       true,
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

// WithForwarderEnabled overrides the default Enabled=true.
func WithForwarderEnabled(enabled bool) ForwarderOption {
	return func(fwd *Forwarder) { fwd.Enabled = enabled }
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

// Save creates or updates this forwarder on the server. Upsert
// behavior keyed on CreatedAt: nil → create (POST), set → full-replace
// update (PUT). After the call, every field is refreshed from the
// server response (including CreatedAt, UpdatedAt, Version). Header
// values must be plaintext: reads return them redacted, so a
// "<redacted>" round-tripped through Save would persist that literal.
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

// Delete soft-deletes this forwarder on the server.
func (fwd *Forwarder) Delete(ctx context.Context) error {
	if fwd.client == nil || fwd.ID == "" {
		return &Error{Message: "forwarder was constructed without a client or id; cannot delete"}
	}
	return fwd.client.Delete(ctx, fwd.ID)
}

func (fwd *Forwarder) apply(other *Forwarder) {
	fwd.ID = other.ID
	fwd.Name = other.Name
	fwd.Description = other.Description
	fwd.ForwarderType = other.ForwarderType
	fwd.Enabled = other.Enabled
	fwd.Filter = other.Filter
	fwd.Transform = other.Transform
	fwd.TransformType = other.TransformType
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
// PageSize (ADR-014).
func (f *AuditForwarders) List(ctx context.Context, input ListForwardersInput) (*ListForwardersPage, error) {
	params := &genaudit.ListForwardersParams{}
	if input.ForwarderType != "" {
		ft := string(input.ForwarderType)
		params.FilterForwarderType = &ft
	}
	if input.Enabled != nil {
		params.FilterEnabled = input.Enabled
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

// Delete soft-deletes a forwarder.
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
func forwarderAttributes(fwd *Forwarder) genaudit.Forwarder {
	enabled := fwd.Enabled
	attrs := genaudit.Forwarder{
		Name:          fwd.Name,
		ForwarderType: fwd.ForwarderType,
		Enabled:       &enabled,
		Configuration: httpConfigurationToWire(fwd.Configuration),
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
		tt := *fwd.TransformType
		attrs.TransformType = &tt
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

func httpConfigurationToWire(h HttpConfiguration) genaudit.HttpConfiguration {
	out := genaudit.HttpConfiguration{
		Url: h.URL,
	}
	if h.Method != "" {
		m := h.Method
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
		hh := make([]genaudit.HttpHeader, 0, len(h.Headers))
		for _, hdr := range h.Headers {
			hh = append(hh, genaudit.HttpHeader{Name: hdr.Name, Value: hdr.Value})
		}
		out.Headers = &hh
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
		ID:            id,
		Name:          a.Name,
		Description:   a.Description,
		ForwarderType: a.ForwarderType,
		Configuration: httpConfigurationFromWire(a.Configuration),
		TransformType: a.TransformType,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
		DeletedAt:     a.DeletedAt,
		Version:       a.Version,
		client:        client,
	}
	if a.Enabled != nil {
		out.Enabled = *a.Enabled
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

func httpConfigurationFromWire(h genaudit.HttpConfiguration) HttpConfiguration {
	out := HttpConfiguration{
		URL: h.Url,
	}
	if h.Method != nil {
		out.Method = *h.Method
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
		out.Headers = make([]HttpHeader, 0, len(*h.Headers))
		for _, hdr := range *h.Headers {
			out.Headers = append(out.Headers, HttpHeader{Name: hdr.Name, Value: hdr.Value})
		}
	}
	return out
}
