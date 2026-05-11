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

// List returns one page of forwarders.
func (f *AuditForwarders) List(ctx context.Context, input ListForwardersInput) (*ListForwardersPage, error) {
	params := &genaudit.ListForwardersParams{}
	if input.ForwarderType != "" {
		ft := string(input.ForwarderType)
		params.FilterForwarderType = &ft
	}
	if input.Enabled != nil {
		params.FilterEnabled = input.Enabled
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	if input.PageAfter != "" {
		params.PageAfter = &input.PageAfter
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
	}
	for _, r := range body.Data {
		page.Forwarders = append(page.Forwarders, forwarderFromResource(r))
	}
	if body.Links != nil && body.Links.Next != nil {
		page.NextCursor = extractNextCursor(body.Links.Next)
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
		Http:          forwarderHttpToWire(input.HTTP),
	}
	if input.Filter != nil {
		f := input.Filter
		attrs.Filter = &f
	}
	if input.Transform != "" {
		t := input.Transform
		attrs.Transform = &t
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

func forwarderHttpToWire(h ForwarderHttp) genaudit.ForwarderHttp {
	out := genaudit.ForwarderHttp{
		Url: h.URL,
	}
	if h.Method != "" {
		m := genaudit.ForwarderHttpMethod(h.Method)
		out.Method = &m
	}
	if h.SuccessStatus != "" {
		s := h.SuccessStatus
		out.SuccessStatus = &s
	}
	if h.Body != nil {
		out.Body = h.Body
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
		ForwarderType: a.ForwarderType,
		HTTP:          forwarderHttpFromWire(a.Http),
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
		DeletedAt:     a.DeletedAt,
		Version:       a.Version,
	}
	if a.Slug != nil {
		out.Slug = *a.Slug
	}
	if a.Enabled != nil {
		out.Enabled = *a.Enabled
	}
	if a.Filter != nil {
		out.Filter = *a.Filter
	}
	if a.Transform != nil {
		out.Transform = a.Transform
	}
	return out
}

func forwarderHttpFromWire(h genaudit.ForwarderHttp) ForwarderHttp {
	out := ForwarderHttp{
		URL: h.Url,
	}
	if h.Method != nil {
		out.Method = string(*h.Method)
	}
	if h.SuccessStatus != nil {
		out.SuccessStatus = *h.SuccessStatus
	}
	if h.Body != nil {
		out.Body = h.Body
	}
	if h.Headers != nil {
		out.Headers = make([]HttpHeader, 0, len(*h.Headers))
		for _, hdr := range *h.Headers {
			out.Headers = append(out.Headers, HttpHeader{Name: hdr.Name, Value: hdr.Value})
		}
	}
	return out
}
