package smplkit

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// AuditForwarders manages SIEM streaming destinations for the
// authenticated account. Pro tier only — methods return wrapped 402
// errors on lower tiers.
type AuditForwarders struct {
	gen        *genaudit.ClientWithResponses
	deliveries *AuditForwarderDeliveries
	actions    *AuditForwarderActions
}

// AuditForwarderDeliveries handles the per-forwarder delivery log and
// the per-delivery retry action.
type AuditForwarderDeliveries struct {
	gen     *genaudit.ClientWithResponses
	actions *AuditDeliveryActions
}

// AuditDeliveryActions exposes the single-delivery retry action.
type AuditDeliveryActions struct {
	gen *genaudit.ClientWithResponses
}

// AuditForwarderActions exposes forwarder-scoped actions like the bulk
// retry of failed deliveries.
type AuditForwarderActions struct {
	gen *genaudit.ClientWithResponses
}

// AuditFunctions exposes server-side function endpoints. Today this
// surface contains the test_forwarder/execute proxy.
type AuditFunctions struct {
	gen           *genaudit.ClientWithResponses
	testForwarder *AuditTestForwarder
}

// AuditTestForwarder is the namespace for the test_forwarder/execute
// proxy and any future test-forwarder-related actions.
type AuditTestForwarder struct {
	actions *AuditTestForwarderActions
}

// AuditTestForwarderActions exposes the Execute action.
type AuditTestForwarderActions struct {
	gen *genaudit.ClientWithResponses
}

// Deliveries returns the delivery log + retry namespace.
func (f *AuditForwarders) Deliveries() *AuditForwarderDeliveries {
	return f.deliveries
}

// Actions returns the forwarder-scoped actions namespace.
func (f *AuditForwarders) Actions() *AuditForwarderActions {
	return f.actions
}

// Actions returns the per-delivery actions namespace.
func (d *AuditForwarderDeliveries) Actions() *AuditDeliveryActions {
	return d.actions
}

// TestForwarder returns the test_forwarder namespace.
func (fn *AuditFunctions) TestForwarder() *AuditTestForwarder {
	return fn.testForwarder
}

// Actions returns the test_forwarder actions namespace.
func (t *AuditTestForwarder) Actions() *AuditTestForwarderActions {
	return t.actions
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
		return nil, fmt.Errorf(
			"audit Forwarders.Create failed: status=%d body=%s",
			resp.StatusCode(), string(resp.Body),
		)
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
		// Generated client typed FilterForwarderType as *string; convert
		// to drop the typed alias before taking its address.
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
		return nil, fmt.Errorf(
			"audit Forwarders.List failed: status=%d body=%s",
			resp.StatusCode(), string(resp.Body),
		)
	}
	body := resp.ApplicationvndApiJSON200
	page := &ListForwardersPage{
		Forwarders: make([]Forwarder, 0, len(body.Data)),
	}
	for _, r := range body.Data {
		page.Forwarders = append(page.Forwarders, forwarderFromResource(r))
	}
	page.NextCursor = nextCursorFromForwarderLinks(body.Links)
	return page, nil
}

// Get returns one forwarder by id.
func (f *AuditForwarders) Get(ctx context.Context, forwarderID uuid.UUID) (*Forwarder, error) {
	resp, err := f.gen.GetForwarderWithResponse(ctx, forwarderID)
	if err != nil {
		return nil, fmt.Errorf("audit Forwarders.Get: %w", err)
	}
	if resp.StatusCode() == 404 {
		return nil, fmt.Errorf("audit Forwarders.Get: forwarder not found (status=404)")
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, fmt.Errorf(
			"audit Forwarders.Get failed: status=%d body=%s",
			resp.StatusCode(), string(resp.Body),
		)
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
		return nil, fmt.Errorf(
			"audit Forwarders.Update failed: status=%d body=%s",
			resp.StatusCode(), string(resp.Body),
		)
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
		return fmt.Errorf(
			"audit Forwarders.Delete failed: status=%d body=%s",
			resp.StatusCode(), string(resp.Body),
		)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Deliveries
// ---------------------------------------------------------------------------

// List returns one page of deliveries for a forwarder.
func (d *AuditForwarderDeliveries) List(
	ctx context.Context, forwarderID uuid.UUID, input ListDeliveriesInput,
) (*ListDeliveriesPage, error) {
	params := &genaudit.ListForwarderDeliveriesParams{}
	if input.Status != "" {
		s := string(input.Status)
		params.FilterStatus = &s
	}
	if input.CreatedAtRange != "" {
		params.FilterCreatedAt = &input.CreatedAtRange
	}
	if input.PageSize > 0 {
		params.PageSize = &input.PageSize
	}
	if input.PageAfter != "" {
		params.PageAfter = &input.PageAfter
	}
	resp, err := d.gen.ListForwarderDeliveriesWithResponse(ctx, forwarderID, params)
	if err != nil {
		return nil, fmt.Errorf("audit Deliveries.List: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, fmt.Errorf(
			"audit Deliveries.List failed: status=%d body=%s",
			resp.StatusCode(), string(resp.Body),
		)
	}
	body := resp.ApplicationvndApiJSON200
	page := &ListDeliveriesPage{
		Deliveries: make([]ForwarderDelivery, 0, len(body.Data)),
	}
	for _, r := range body.Data {
		page.Deliveries = append(page.Deliveries, deliveryFromResource(r))
	}
	page.NextCursor = nextCursorFromForwarderLinks(body.Links)
	return page, nil
}

// Retry retries a single failed delivery. Records a new delivery row
// with attempt_number = prior + 1; the prior row is unchanged.
func (a *AuditDeliveryActions) Retry(
	ctx context.Context, forwarderID, deliveryID uuid.UUID,
) (*ForwarderDelivery, error) {
	resp, err := a.gen.RetryForwarderDeliveryWithResponse(
		ctx, forwarderID, deliveryID,
	)
	if err != nil {
		return nil, fmt.Errorf("audit Deliveries.Actions.Retry: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, fmt.Errorf(
			"audit Deliveries.Actions.Retry failed: status=%d body=%s",
			resp.StatusCode(), string(resp.Body),
		)
	}
	out := deliveryFromResource(resp.ApplicationvndApiJSON200.Data)
	return &out, nil
}

// RetryFailedDeliveries retries every failed delivery for a forwarder
// and returns counts.
func (a *AuditForwarderActions) RetryFailedDeliveries(
	ctx context.Context, forwarderID uuid.UUID,
) (*RetryFailedDeliveriesSummary, error) {
	resp, err := a.gen.RetryFailedForwarderDeliveriesWithResponse(ctx, forwarderID)
	if err != nil {
		return nil, fmt.Errorf("audit Forwarders.Actions.RetryFailedDeliveries: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, fmt.Errorf(
			"audit Forwarders.Actions.RetryFailedDeliveries failed: status=%d body=%s",
			resp.StatusCode(), string(resp.Body),
		)
	}
	body := resp.ApplicationvndApiJSON200
	return &RetryFailedDeliveriesSummary{
		Attempted: body.Attempted,
		Succeeded: body.Succeeded,
		Failed:    body.Failed,
	}, nil
}

// ---------------------------------------------------------------------------
// functions.test_forwarder.actions.Execute
// ---------------------------------------------------------------------------

// Execute proxies an HTTP request to a customer-supplied destination
// from inside the audit service, returning the response. The audit
// service applies its SSRF guard before resolving — private,
// loopback, link-local (incl. 169.254.169.254 IMDS), unique-local,
// and disallowed-port targets are rejected with Result.Succeeded=false
// and a descriptive Error.
func (a *AuditTestForwarderActions) Execute(
	ctx context.Context, input TestForwarderInput,
) (*TestForwarderResult, error) {
	body := genaudit.ExecuteTestForwarderJSONRequestBody{
		Url: input.URL,
	}
	if input.Method != "" {
		m := input.Method
		body.Method = &m
	}
	if len(input.Headers) > 0 {
		hh := make([]genaudit.HttpHeader, 0, len(input.Headers))
		for _, h := range input.Headers {
			hh = append(hh, genaudit.HttpHeader{Name: h.Name, Value: h.Value})
		}
		body.Headers = &hh
	}
	if input.Body != nil {
		body.Body = input.Body
	}
	if input.SuccessStatus != "" {
		s := input.SuccessStatus
		body.SuccessStatus = &s
	}
	if input.TimeoutMs != nil {
		body.TimeoutMs = input.TimeoutMs
	}
	resp, err := a.gen.ExecuteTestForwarderWithResponse(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("audit Functions.TestForwarder.Actions.Execute: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, fmt.Errorf(
			"audit Functions.TestForwarder.Actions.Execute failed: status=%d body=%s",
			resp.StatusCode(), string(resp.Body),
		)
	}
	body200 := resp.JSON200
	out := &TestForwarderResult{
		ResponseHeaders: map[string]string{},
	}
	if body200 != nil {
		out.Succeeded = body200.Succeeded
		out.ResponseStatus = body200.ResponseStatus
		if body200.ResponseBody != nil {
			out.ResponseBody = *body200.ResponseBody
		}
		out.LatencyMs = body200.LatencyMs
		out.Error = body200.Error
		if body200.ResponseHeaders != nil {
			for k, v := range *body200.ResponseHeaders {
				out.ResponseHeaders[k] = v
			}
		}
	}
	return out, nil
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
	if input.Data != nil {
		d := input.Data
		attrs.Data = &d
	}
	return genaudit.ForwarderResource{
		Id:         id,
		Type:       &rt,
		Attributes: attrs,
	}
}

func forwarderHttpToWire(h ForwarderHttp) genaudit.ForwarderHttp {
	out := genaudit.ForwarderHttp{
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
	id, _ := uuid.Parse(r.Id)
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
	if a.Data != nil {
		out.Data = *a.Data
	}
	return out
}

func forwarderHttpFromWire(h genaudit.ForwarderHttp) ForwarderHttp {
	out := ForwarderHttp{
		URL: h.Url,
	}
	if h.Method != nil {
		out.Method = *h.Method
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

func deliveryFromResource(r genaudit.ForwarderDeliveryResource) ForwarderDelivery {
	id, _ := uuid.Parse(r.Id)
	a := r.Attributes
	fid, _ := uuid.Parse(a.ForwarderId.String())
	eid, _ := uuid.Parse(a.EventId.String())
	out := ForwarderDelivery{
		ID:             id,
		ForwarderID:    fid,
		EventID:        eid,
		AttemptNumber:  a.AttemptNumber,
		Status:         ForwarderDeliveryStatus(a.Status),
		ResponseStatus: a.ResponseStatus,
		ResponseBody:   a.ResponseBody,
		LatencyMs:      a.LatencyMs,
		Error:          a.Error,
		CreatedAt:      a.CreatedAt,
	}
	if a.Request != nil {
		out.Request = *a.Request
	}
	return out
}

func nextCursorFromForwarderLinks(links *genaudit.ForwarderListLinks) string {
	if links == nil || links.Next == nil {
		return ""
	}
	next := *links.Next
	if i := strings.Index(next, "page[after]="); i >= 0 {
		token := next[i+len("page[after]="):]
		if amp := strings.Index(token, "&"); amp >= 0 {
			token = token[:amp]
		}
		return token
	}
	return ""
}
