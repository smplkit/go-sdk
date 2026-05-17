package smplkit

import (
	"context"
	"fmt"

	"github.com/google/uuid"

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
func (f *AuditForwarders) New(
	name string,
	forwarderType ForwarderType,
	configuration HttpConfiguration,
	opts ...ForwarderOption,
) *Forwarder {
	fwd := &Forwarder{
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

// WithForwarderTransform sets the optional JSONata template applied to
// each event before delivery.
func WithForwarderTransform(transform string) ForwarderOption {
	return func(fwd *Forwarder) {
		fwd.Transform = &transform
		tt := ForwarderTransformTypeJSONata
		fwd.TransformType = &tt
	}
}

// Save creates or updates this forwarder on the server. Upsert
// behavior keyed on CreatedAt: nil → create (POST), set → full-replace
// update (PUT). After the call, every field is refreshed from the
// server response (including newly-assigned ID, CreatedAt, UpdatedAt,
// Version). Header values must be plaintext: reads return them
// redacted, so a "<redacted>" round-tripped through Save would persist
// that literal.
func (fwd *Forwarder) Save(ctx context.Context) error {
	if fwd.client == nil {
		return &Error{Message: "forwarder was constructed without a client; cannot save"}
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

// Delete soft-deletes this forwarder on the server.
func (fwd *Forwarder) Delete(ctx context.Context) error {
	if fwd.client == nil || fwd.ID == uuid.Nil {
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
// Forwarder collection CRUD
// ---------------------------------------------------------------------------

// Create creates a new forwarder. Prefer the active-record flow
// (New().Save(ctx)) for new forwarders.
func (f *AuditForwarders) Create(ctx context.Context, input CreateForwarderInput) (*Forwarder, error) {
	body := genaudit.CreateForwarderApplicationVndAPIPlusJSONRequestBody{
		Data: forwarderResourceFromInput("", input),
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
func (f *AuditForwarders) Get(ctx context.Context, forwarderID uuid.UUID) (*Forwarder, error) {
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

// Update fully replaces a forwarder. Re-supply real header values —
// the GET path returns them redacted, and "<redacted>" sent back
// would persist that literal. Prefer the active-record flow
// (forwarder.Save(ctx)) for round-trips initiated from a fetched
// instance.
func (f *AuditForwarders) Update(
	ctx context.Context, forwarderID uuid.UUID, input UpdateForwarderInput,
) (*Forwarder, error) {
	body := genaudit.UpdateForwarderApplicationVndAPIPlusJSONRequestBody{
		Data: forwarderResourceFromInput(forwarderID.String(), input),
	}
	resp, err := f.gen.UpdateForwarderWithApplicationVndAPIPlusJSONBodyWithResponse(
		ctx, forwarderID, body,
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

// Delete soft-deletes a forwarder.
func (f *AuditForwarders) Delete(ctx context.Context, forwarderID uuid.UUID) error {
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
		Data: forwarderResourceFromForwarder("", fwd),
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
		Data: forwarderResourceFromForwarder(fwd.ID.String(), fwd),
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

func forwarderResourceFromInput(id string, input CreateForwarderInput) genaudit.ForwarderResource {
	rt := "forwarder"
	enabled := input.Enabled
	attrs := genaudit.Forwarder{
		Name:          input.Name,
		ForwarderType: input.ForwarderType,
		Enabled:       &enabled,
		Configuration: httpConfigurationToWire(input.Configuration),
	}
	if input.Description != "" {
		d := input.Description
		attrs.Description = &d
	}
	if input.Filter != nil {
		f := input.Filter
		attrs.Filter = &f
	}
	if input.Transform != "" {
		// Transform is a discriminated union; today JSONATA is the only
		// engine, so we set both the transform body and engine label.
		attrs.Transform = input.Transform
		tt := genaudit.JSONATA
		attrs.TransformType = &tt
	}
	var idPtr *string
	if id != "" {
		idPtr = &id
	}
	return genaudit.ForwarderResource{
		Id:         idPtr,
		Type:       &rt,
		Attributes: attrs,
	}
}

func forwarderResourceFromForwarder(id string, fwd *Forwarder) genaudit.ForwarderResource {
	rt := "forwarder"
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
	if fwd.Transform != nil && *fwd.Transform != "" {
		attrs.Transform = *fwd.Transform
		tt := genaudit.JSONATA
		if fwd.TransformType != nil {
			tt = *fwd.TransformType
		}
		attrs.TransformType = &tt
	}
	var idPtr *string
	if id != "" {
		idPtr = &id
	}
	return genaudit.ForwarderResource{
		Id:         idPtr,
		Type:       &rt,
		Attributes: attrs,
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
	var id uuid.UUID
	if r.Id != nil {
		id, _ = uuid.Parse(*r.Id)
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
	// Transform is a discriminated union; for the only supported engine
	// (JSONATA) the body is a string. Surface other shapes as nil rather
	// than panicking on the type assertion.
	if s, ok := a.Transform.(string); ok && s != "" {
		out.Transform = &s
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
	if h.Headers != nil {
		out.Headers = make([]HttpHeader, 0, len(*h.Headers))
		for _, hdr := range *h.Headers {
			out.Headers = append(out.Headers, HttpHeader{Name: hdr.Name, Value: hdr.Value})
		}
	}
	return out
}
