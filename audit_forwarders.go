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
// Forwarder CRUD
// ---------------------------------------------------------------------------

// Create creates a new forwarder.
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
	out := forwarderFromResource(resp.ApplicationvndApiJSON201.Data)
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
		page.Forwarders = append(page.Forwarders, forwarderFromResource(r))
	}
	return page, nil
}

// Get returns one forwarder by id.
func (f *AuditForwarders) Get(ctx context.Context, forwarderID uuid.UUID) (*Forwarder, error) {
	resp, err := f.gen.GetForwarderWithResponse(ctx, forwarderID)
	if err != nil {
		return nil, fmt.Errorf("audit Forwarders.Get: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, checkStatus(resp.StatusCode(), resp.Body)
	}
	out := forwarderFromResource(resp.ApplicationvndApiJSON200.Data)
	return &out, nil
}

// Update fully replaces a forwarder. Re-supply real header values —
// the GET path returns them redacted, and "<redacted>" sent back
// would persist that literal.
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
	out := forwarderFromResource(resp.ApplicationvndApiJSON200.Data)
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
// Helpers
// ---------------------------------------------------------------------------

func forwarderResourceFromInput(id string, input CreateForwarderInput) genaudit.ForwarderResource {
	rt := "forwarder"
	enabled := input.Enabled
	attrs := genaudit.Forwarder{
		Name:          input.Name,
		ForwarderType: input.ForwarderType,
		Enabled:       &enabled,
		Configuration: httpConfigurationToWire(input.HTTP),
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
		// The spec accepts arbitrary transform shapes via a discriminated
		// union; today JSONATA is the only engine, so we set both the
		// transform body (as a string) and the engine label.
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

func httpConfigurationToWire(h ForwarderHttp) genaudit.HttpConfiguration {
	out := genaudit.HttpConfiguration{
		Url: h.URL,
	}
	if h.Method != "" {
		m := genaudit.HttpConfigurationMethod(h.Method)
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

func forwarderFromResource(r genaudit.ForwarderResource) Forwarder {
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
		HTTP:          httpConfigurationFromWire(a.Configuration),
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
		DeletedAt:     a.DeletedAt,
		Version:       a.Version,
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

func httpConfigurationFromWire(h genaudit.HttpConfiguration) ForwarderHttp {
	out := ForwarderHttp{
		URL: h.Url,
	}
	if h.Method != nil {
		out.Method = string(*h.Method)
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
