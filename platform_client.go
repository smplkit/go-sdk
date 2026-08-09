package smplkit

// The Smpl Platform client — cross-cutting CRUD on client.Platform().
//
// PlatformClient groups the account-wide configuration resources that aren't
// owned by a single product, mirroring the product UI's Platform area:
//
//   - Platform().Environments() — environment CRUD
//   - Platform().Services()     — service CRUD
//   - Platform().Contexts()     — evaluation-context registration + read/delete
//   - Platform().ContextTypes() — context-type CRUD
//
// All four are pure CRUD — no Install gate. Every sub-client speaks to the
// app service, so the client needs exactly one app transport (plus the
// context-registration buffer that Contexts drains).
//
// The client supports two construction shapes:
//
//   - Wired into SmplClient — borrows the parent's app transport and an
//     externally-supplied context buffer. This is the common path;
//     client.Flags() borrows client.Platform().Contexts() as its
//     evaluation-context registration seam.
//   - Standalone — NewPlatformClient(...) builds and owns its own app
//     transport and buffer.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
)

// PlatformClient is the Smpl Platform client.
//
// Groups the account-wide CRUD resources that aren't owned by a single
// product, reachable as client.Platform() (SmplClient) or constructed
// directly:
//
//	platform, err := smplkit.NewPlatformClient(smplkit.Config{APIKey: "sk_..."})
//	prod := platform.Environments().New("production", "Production")
//	prod.Save(ctx)
//	svcs, err := platform.Services().List(ctx)
//
// Sub-clients: Environments, Services, Contexts, ContextTypes. Pure CRUD —
// no Install required.
type PlatformClient struct {
	appClient  genapp.ClientInterface
	contextBuf *contextRegistrationBuffer

	environments *EnvironmentsClient
	services     *ServicesClient
	contextTypes *ContextTypesClient
	contexts     *ContextsClient
}

// newPlatformClient wires a PlatformClient onto a pre-built app transport and
// a shared context-registration buffer (the wired path used by SmplClient).
func newPlatformClient(appClient genapp.ClientInterface, buf *contextRegistrationBuffer) *PlatformClient {
	p := &PlatformClient{appClient: appClient, contextBuf: buf}
	p.environments = &EnvironmentsClient{appClient: appClient}
	p.services = &ServicesClient{appClient: appClient}
	p.contextTypes = &ContextTypesClient{appClient: appClient}
	p.contexts = &ContextsClient{appClient: appClient, contextBuf: buf}
	return p
}

// NewPlatformClient creates a standalone Smpl Platform client that owns its
// own app transport and context-registration buffer.
//
// Construction has zero side effects: no service registration, no metrics, no
// event stream — just CRUD against the app service.
func NewPlatformClient(cfg Config, opts ...ClientOption) (*PlatformClient, error) {
	rc, err := resolveConfig(cfg)
	if err != nil {
		return nil, err
	}
	optCfg := defaultConfig()
	for _, opt := range opts {
		opt(&optCfg)
	}
	_, genApp := buildGenClients(optCfg, rc)
	return newPlatformClient(genApp, newContextRegistrationBuffer()), nil
}

// Environments returns the sub-client for environment CRUD operations.
func (p *PlatformClient) Environments() *EnvironmentsClient { return p.environments }

// Services returns the sub-client for service CRUD operations.
func (p *PlatformClient) Services() *ServicesClient { return p.services }

// ContextTypes returns the sub-client for context-type CRUD operations.
func (p *PlatformClient) ContextTypes() *ContextTypesClient { return p.contextTypes }

// Contexts returns the sub-client for context registration and read/delete operations.
func (p *PlatformClient) Contexts() *ContextsClient { return p.contexts }

// Close releases resources held by a standalone PlatformClient. A wired
// client borrows the parent's app transport and closes nothing; the
// underlying http.Client is pooled by net/http, so this is a no-op kept for
// symmetry and a clean defer point.
func (p *PlatformClient) Close() error { return nil }

// ── Environments ─────────────────────────────────────────────────────────────

// EnvironmentsClient provides CRUD operations for environment resources
// (client.Platform().Environments()).
type EnvironmentsClient struct {
	appClient genapp.ClientInterface
}

// New returns an unsaved Environment. Call env.Save(ctx) to persist.
//
// id is a stable, human-readable identifier, e.g. "production". name is
// the display name shown in the Console. The options refine the
// environment: WithEnvironmentColor sets the accent color as a Color or
// CSS hex string (defaults to no color), and WithEnvironmentClassification
// controls whether the environment participates in the standard
// environment ordering (defaults to STANDARD).
func (m *EnvironmentsClient) New(id string, name string, opts ...EnvironmentOption) *Environment {
	e := &Environment{
		ID:             id,
		Name:           name,
		Classification: EnvironmentClassificationStandard,
		client:         m,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// List returns one page of environments for the account.
//
// Without options the server applies its defaults (page 1, page size
// 1000). Use [WithPageNumber] / [WithPageSize] to walk additional pages.
func (m *EnvironmentsClient) List(ctx context.Context, opts ...ListOption) ([]*Environment, error) {
	o := resolveListOptions(opts)
	params := &genapp.ListEnvironmentsParams{
		PageNumber: o.pageNumber,
		PageSize:   o.pageSize,
	}
	resp, err := m.appClient.ListEnvironments(ctx, params)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genapp.EnvironmentListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse environments: %w", err)
	}

	envs := make([]*Environment, len(result.Data))
	for i, r := range result.Data {
		envs[i] = resourceToEnvironment(r, m)
	}
	return envs, nil
}

// Get retrieves a single environment by ID. id is the identifier of the
// environment to fetch. Returns a NotFoundError when no environment with
// that id exists.
func (m *EnvironmentsClient) Get(ctx context.Context, id string) (*Environment, error) {
	resp, err := m.appClient.GetEnvironment(ctx, id)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genapp.EnvironmentResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse environment: %w", err)
	}
	return resourceToEnvironment(result.Data, m), nil
}

// Delete removes an environment by ID. id is the identifier of the
// environment to delete.
func (m *EnvironmentsClient) Delete(ctx context.Context, id string) error {
	resp, err := m.appClient.DeleteEnvironment(ctx, id, nil)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	return checkStatus(resp.StatusCode, body)
}

// create sends a POST to create the environment; updates e with the server response.
func (m *EnvironmentsClient) create(ctx context.Context, e *Environment) error {
	reqBody := environmentToCreateRequest(e)
	resp, err := m.appClient.CreateEnvironmentWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var result genapp.EnvironmentResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse environment: %w", err)
	}
	e.apply(resourceToEnvironment(result.Data, m))
	return nil
}

// update sends a PUT to update the environment; updates e with the server response.
func (m *EnvironmentsClient) update(ctx context.Context, e *Environment) error {
	reqBody := environmentToRequest(e)
	resp, err := m.appClient.UpdateEnvironmentWithApplicationVndAPIPlusJSONBody(ctx, e.ID, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var result genapp.EnvironmentResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse environment: %w", err)
	}
	e.apply(resourceToEnvironment(result.Data, m))
	return nil
}

func environmentAttributes(e *Environment) genapp.Environment {
	cls := genapp.EnvironmentClassification(e.Classification)
	return genapp.Environment{
		Name:           e.Name,
		Color:          e.Color,
		Classification: &cls,
	}
}

func environmentToRequest(e *Environment) genapp.EnvironmentRequest {
	id := e.ID
	return genapp.EnvironmentRequest{
		Data: genapp.EnvironmentResource{
			Type:       genapp.EnvironmentResourceTypeEnvironment,
			Id:         &id,
			Attributes: environmentAttributes(e),
		},
	}
}

// environmentToCreateRequest builds the EnvironmentCreateRequest envelope.
// The create envelope requires a non-nullable string id (vs the update
// envelope, which optionally echoes the id since the id lives in the
// URL path).
func environmentToCreateRequest(e *Environment) genapp.EnvironmentCreateRequest {
	return genapp.EnvironmentCreateRequest{
		Data: genapp.EnvironmentCreateResource{
			Type:       genapp.EnvironmentCreateResourceTypeEnvironment,
			Id:         e.ID,
			Attributes: environmentAttributes(e),
		},
	}
}

func resourceToEnvironment(r genapp.EnvironmentResource, m *EnvironmentsClient) *Environment {
	e := &Environment{
		Name:           r.Attributes.Name,
		Color:          r.Attributes.Color,
		Classification: EnvironmentClassificationStandard,
		CreatedAt:      r.Attributes.CreatedAt,
		UpdatedAt:      r.Attributes.UpdatedAt,
		client:         m,
	}
	if r.Id != nil {
		e.ID = *r.Id
	}
	if r.Attributes.Classification != nil {
		if *r.Attributes.Classification == genapp.ADHOC {
			e.Classification = EnvironmentClassificationAdHoc
		}
	}
	return e
}

// ── Services ─────────────────────────────────────────────────────────────────

// ServicesClient provides CRUD operations for service resources
// (client.Platform().Services()).
type ServicesClient struct {
	appClient genapp.ClientInterface
}

// New returns an unsaved Service. Call svc.Save(ctx) to persist.
//
// id is a stable, human-readable identifier for the service. name is the
// display name shown in the Console.
func (m *ServicesClient) New(id string, name string) *Service {
	return &Service{
		ID:     id,
		Name:   name,
		client: m,
	}
}

// List returns one page of services for the account.
//
// Without options the server applies its defaults (page 1, page size
// 1000). Use [WithPageNumber] / [WithPageSize] to walk additional pages.
func (m *ServicesClient) List(ctx context.Context, opts ...ListOption) ([]*Service, error) {
	o := resolveListOptions(opts)
	params := &genapp.ListServicesParams{
		PageNumber: o.pageNumber,
		PageSize:   o.pageSize,
	}
	resp, err := m.appClient.ListServices(ctx, params)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genapp.ServiceListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse services: %w", err)
	}

	svcs := make([]*Service, len(result.Data))
	for i, r := range result.Data {
		svcs[i] = resourceToService(r, m)
	}
	return svcs, nil
}

// Get retrieves a single service by ID. id is the identifier of the
// service to fetch. Returns a NotFoundError when no service with that id
// exists.
func (m *ServicesClient) Get(ctx context.Context, id string) (*Service, error) {
	resp, err := m.appClient.GetService(ctx, id)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genapp.ServiceResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse service: %w", err)
	}
	return resourceToService(result.Data, m), nil
}

// Delete removes a service by ID. id is the identifier of the service to
// delete.
func (m *ServicesClient) Delete(ctx context.Context, id string) error {
	resp, err := m.appClient.DeleteService(ctx, id)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	return checkStatus(resp.StatusCode, body)
}

// create sends a POST to create the service; updates s with the server response.
func (m *ServicesClient) create(ctx context.Context, s *Service) error {
	reqBody := serviceToCreateRequest(s)
	resp, err := m.appClient.CreateServiceWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var result genapp.ServiceResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse service: %w", err)
	}
	s.apply(resourceToService(result.Data, m))
	return nil
}

// update sends a PUT to update the service; updates s with the server response.
func (m *ServicesClient) update(ctx context.Context, s *Service) error {
	reqBody := serviceToRequest(s)
	resp, err := m.appClient.UpdateServiceWithApplicationVndAPIPlusJSONBody(ctx, s.ID, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var result genapp.ServiceResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse service: %w", err)
	}
	s.apply(resourceToService(result.Data, m))
	return nil
}

func serviceAttributes(s *Service) genapp.Service {
	return genapp.Service{
		Name: s.Name,
	}
}

func serviceToRequest(s *Service) genapp.ServiceRequest {
	id := s.ID
	return genapp.ServiceRequest{
		Data: genapp.ServiceResource{
			Type:       genapp.ServiceResourceTypeService,
			Id:         &id,
			Attributes: serviceAttributes(s),
		},
	}
}

// serviceToCreateRequest builds the ServiceCreateRequest envelope.
// The create envelope requires a non-nullable string id (vs the update
// envelope, which optionally echoes the id since the id lives in the
// URL path).
func serviceToCreateRequest(s *Service) genapp.ServiceCreateRequest {
	return genapp.ServiceCreateRequest{
		Data: genapp.ServiceCreateResource{
			Type:       genapp.ServiceCreateResourceTypeService,
			Id:         s.ID,
			Attributes: serviceAttributes(s),
		},
	}
}

func resourceToService(r genapp.ServiceResource, m *ServicesClient) *Service {
	s := &Service{
		Name:      r.Attributes.Name,
		CreatedAt: r.Attributes.CreatedAt,
		UpdatedAt: r.Attributes.UpdatedAt,
		client:    m,
	}
	if r.Id != nil {
		s.ID = *r.Id
	}
	return s
}

// ── ContextTypes ─────────────────────────────────────────────────────────────

// ContextTypesClient provides CRUD operations for context-type resources
// (client.Platform().ContextTypes()).
type ContextTypesClient struct {
	appClient genapp.ClientInterface
}

// New returns an unsaved ContextType. Call ct.Save(ctx) to persist.
//
// id is a stable, human-readable identifier, e.g. "user". If no
// WithContextTypeName option is provided the ID is used as the display
// name. A new context type starts with no declared known-attribute slots;
// declare them via AddAttribute, each keyed by attribute name with a
// metadata map per slot, before saving.
func (m *ContextTypesClient) New(id string, opts ...ContextTypeOption) *ContextType {
	ct := &ContextType{
		ID:         id,
		Name:       id,
		Attributes: make(map[string]map[string]interface{}),
		client:     m,
	}
	for _, opt := range opts {
		opt(ct)
	}
	return ct
}

// List returns one page of context types for the account.
//
// Without options the server applies its defaults (page 1, page size
// 1000). Use [WithPageNumber] / [WithPageSize] to walk additional pages.
func (m *ContextTypesClient) List(ctx context.Context, opts ...ListOption) ([]*ContextType, error) {
	o := resolveListOptions(opts)
	params := &genapp.ListContextTypesParams{
		PageNumber: o.pageNumber,
		PageSize:   o.pageSize,
	}
	resp, err := m.appClient.ListContextTypes(ctx, params)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genapp.ContextTypeListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse context types: %w", err)
	}

	types := make([]*ContextType, len(result.Data))
	for i, r := range result.Data {
		types[i] = resourceToContextType(r, m)
	}
	return types, nil
}

// Get retrieves a single context type by ID. id is the identifier of the
// context type to fetch. Returns a NotFoundError when no context type with
// that id exists.
func (m *ContextTypesClient) Get(ctx context.Context, id string) (*ContextType, error) {
	resp, err := m.appClient.GetContextType(ctx, id)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}
	return parseContextTypeFromBody(body, m)
}

// Delete removes a context type by ID. id is the identifier of the context
// type to delete.
func (m *ContextTypesClient) Delete(ctx context.Context, id string) error {
	resp, err := m.appClient.DeleteContextType(ctx, id)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	return checkStatus(resp.StatusCode, body)
}

func (m *ContextTypesClient) create(ctx context.Context, ct *ContextType) error {
	reqBody := contextTypeToRequest(ct)
	resp, err := m.appClient.CreateContextTypeWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}
	updated, err := parseContextTypeFromBody(body, m)
	if err != nil {
		return err
	}
	ct.apply(updated)
	return nil
}

func (m *ContextTypesClient) update(ctx context.Context, ct *ContextType) error {
	reqBody := contextTypeToRequest(ct)
	resp, err := m.appClient.UpdateContextTypeWithApplicationVndAPIPlusJSONBody(ctx, ct.ID, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}
	updated, err := parseContextTypeFromBody(body, m)
	if err != nil {
		return err
	}
	ct.apply(updated)
	return nil
}

func contextTypeToRequest(ct *ContextType) genapp.ContextTypeRequest {
	attrMeta := map[string]interface{}{}
	for k, v := range ct.Attributes {
		attrMeta[k] = v
	}
	attrs := genapp.ContextType{
		Name:       ct.Name,
		Attributes: &attrMeta,
	}
	id := ct.ID
	return genapp.ContextTypeRequest{
		Data: genapp.ContextTypeResource{
			Type:       "context_type",
			Id:         &id,
			Attributes: attrs,
		},
	}
}

func resourceToContextType(r genapp.ContextTypeResource, m *ContextTypesClient) *ContextType {
	typedAttrs := make(map[string]map[string]interface{})
	if r.Attributes.Attributes != nil {
		for k, v := range *r.Attributes.Attributes {
			if vm, ok := v.(map[string]interface{}); ok {
				typedAttrs[k] = vm
			} else {
				typedAttrs[k] = map[string]interface{}{}
			}
		}
	}
	ct := &ContextType{
		Name:       r.Attributes.Name,
		Attributes: typedAttrs,
		client:     m,
	}
	if r.Id != nil {
		ct.ID = *r.Id
	}
	return ct
}

func parseContextTypeFromBody(body []byte, m *ContextTypesClient) (*ContextType, error) {
	ct, err := parseContextType(body)
	if err != nil {
		return nil, err
	}
	ct.client = m
	return ct, nil
}

// ── Contexts ──────────────────────────────────────────────────────────────────

// ContextsRegisterOption configures a Register call.
type ContextsRegisterOption func(*contextsRegisterOpts)

type contextsRegisterOpts struct {
	flush bool
}

// WithContextFlush causes Register to immediately flush the buffer to the server.
// Without this option contexts are queued for the next background flush.
func WithContextFlush() ContextsRegisterOption {
	return func(o *contextsRegisterOpts) { o.flush = true }
}

// ContextsClient provides context registration, listing, and deletion
// (client.Platform().Contexts()).
type ContextsClient struct {
	appClient  genapp.ClientInterface
	contextBuf *contextRegistrationBuffer
}

// Register buffers contexts for registration with the server.
//
// contexts are the contexts to buffer for registration. Buffered contexts
// are sent in batches: a background flush kicks in once enough have
// accumulated, and any remainder is sent on the next explicit flush. Pass
// WithContextFlush() to send everything buffered right away with an
// immediate synchronous flush after queuing.
func (m *ContextsClient) Register(ctx context.Context, contexts []Context, opts ...ContextsRegisterOption) error {
	o := &contextsRegisterOpts{}
	for _, opt := range opts {
		opt(o)
	}
	m.contextBuf.observe(contexts)
	if o.flush {
		return m.Flush(ctx)
	}
	return nil
}

// Flush sends any pending context observations to the server immediately.
func (m *ContextsClient) Flush(ctx context.Context) error {
	batch := m.contextBuf.drain()
	if len(batch) == 0 {
		return nil
	}
	return m.flushBatch(ctx, batch)
}

// PendingCount reports the number of observations queued and awaiting flush.
func (m *ContextsClient) PendingCount() int {
	return m.contextBuf.pendingCount()
}

func (m *ContextsClient) flushBatch(ctx context.Context, batch []map[string]interface{}) error {
	items := make([]genapp.ContextBulkItem, 0, len(batch))
	for _, entry := range batch {
		t, _ := entry["type"].(string)
		k, _ := entry["key"].(string)
		item := genapp.ContextBulkItem{Type: t, Key: k}
		if attrs, ok := entry["attributes"].(map[string]interface{}); ok {
			item.Attributes = &attrs
		}
		items = append(items, item)
	}
	reqBody := genapp.ContextBulkRegister{Contexts: items}
	resp, err := m.appClient.BulkRegisterContextsWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	return checkStatus(resp.StatusCode, body)
}

// List returns one page of context instances of the given context type.
//
// contextType is the context type to list, e.g. "user". Without options
// the server applies its defaults (page 1, page size 1000). Use
// [WithPageNumber] / [WithPageSize] to walk additional pages.
func (m *ContextsClient) List(ctx context.Context, contextType string, opts ...ListOption) ([]*ContextEntity, error) {
	o := resolveListOptions(opts)
	params := &genapp.ListContextsParams{
		FilterContextType: &contextType,
		PageNumber:        o.pageNumber,
		PageSize:          o.pageSize,
	}
	resp, err := m.appClient.ListContexts(ctx, params)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse contexts: %w", err)
	}

	entities := make([]*ContextEntity, 0, len(result.Data))
	for _, raw := range result.Data {
		ce, err := parseContextEntityRawWithClient(raw, m)
		if err != nil {
			return nil, err
		}
		entities = append(entities, ce)
	}
	return entities, nil
}

// Get retrieves a single context by its composite "type:key" id, or by separate
// type and key arguments:
//
//	client.Platform().Contexts().Get(ctx, "user:usr_123")
//	client.Platform().Contexts().Get(ctx, "user", "usr_123")
//
// parts identifies the context: pass one argument carrying the composite
// "type:key" id, or two arguments giving the type and key separately. Any
// other number of arguments is an error. Returns a NotFoundError when no
// context with that id exists.
func (m *ContextsClient) Get(ctx context.Context, parts ...string) (*ContextEntity, error) {
	composite, err := resolveContextID(parts)
	if err != nil {
		return nil, err
	}
	resp, err := m.appClient.GetContext(ctx, composite)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse context: %w", err)
	}
	return parseContextEntityRawWithClient(result.Data, m)
}

// saveEntity is the active-record save path for a ContextEntity. It uses
// the upsert-style bulk-register endpoint so creation and update share
// one wire call.
func (m *ContextsClient) saveEntity(ctx context.Context, ce *ContextEntity) error {
	attrs := make(map[string]interface{}, len(ce.Attributes))
	for k, v := range ce.Attributes {
		attrs[k] = v
	}
	if ce.Name != nil && *ce.Name != "" {
		attrs["name"] = *ce.Name
	}
	item := genapp.ContextBulkItem{
		Type:       ce.ContextType,
		Key:        ce.Key,
		Attributes: &attrs,
	}
	body := genapp.ContextBulkRegister{Contexts: []genapp.ContextBulkItem{item}}
	resp, err := m.appClient.BulkRegisterContextsWithApplicationVndAPIPlusJSONBody(ctx, body)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	return checkStatus(resp.StatusCode, respBody)
}

// Delete removes a context by its composite "type:key" id, or by separate type
// and key arguments.
//
// parts identifies the context: pass one argument carrying the composite
// "type:key" id, or two arguments giving the type and key separately. Any
// other number of arguments is an error.
func (m *ContextsClient) Delete(ctx context.Context, parts ...string) error {
	composite, err := resolveContextID(parts)
	if err != nil {
		return err
	}
	resp, err := m.appClient.DeleteContext(ctx, composite)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	return checkStatus(resp.StatusCode, body)
}

// resolveContextID accepts either ["type:key"] or ["type", "key"].
func resolveContextID(parts []string) (string, error) {
	switch len(parts) {
	case 1:
		id := parts[0]
		if id == "" || !containsColon(id) {
			return "", fmt.Errorf("smplkit: context id must be 'type:key' (got %q); pass type and key as separate args", id)
		}
		return id, nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", fmt.Errorf("smplkit: context type and key must not be empty")
		}
		return parts[0] + ":" + parts[1], nil
	default:
		return "", fmt.Errorf("smplkit: Get/Delete accepts one composite id or two (type, key) arguments")
	}
}

func containsColon(s string) bool {
	for _, c := range s {
		if c == ':' {
			return true
		}
	}
	return false
}

func parseContextEntityRawWithClient(raw json.RawMessage, client *ContextsClient) (*ContextEntity, error) {
	var data struct {
		ID         string `json:"id"`
		Attributes struct {
			Name       *string                `json:"name"`
			Attributes map[string]interface{} `json:"attributes"`
			CreatedAt  *string                `json:"created_at"`
			UpdatedAt  *string                `json:"updated_at"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse context entity: %w", err)
	}

	ce := &ContextEntity{
		Attributes: make(map[string]interface{}),
		client:     client,
	}
	compositeID := data.ID
	if i := indexByte(compositeID, ':'); i >= 0 {
		ce.ContextType = compositeID[:i]
		ce.Key = compositeID[i+1:]
	} else {
		ce.ContextType = compositeID
	}
	ce.Name = data.Attributes.Name
	if data.Attributes.Attributes != nil {
		ce.Attributes = data.Attributes.Attributes
	}
	return ce, nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
