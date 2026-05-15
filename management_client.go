package smplkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
)

// AuditManagement is the mgmt.audit.* surface — forwarder CRUD.
// Obtained via ManagementClient.Audit().
type AuditManagement struct {
	forwarders *AuditForwarders
}

// Forwarders returns the SIEM forwarder CRUD sub-client.
func (a *AuditManagement) Forwarders() *AuditForwarders {
	return a.forwarders
}

// ManagementClient is the management-plane sub-client. Obtain one via
// Client.Manage() (or via NewManagementClient for a standalone management
// client with zero construction side effects — no service registration,
// no metrics, no websocket).
//
// The flat namespaces mirror the Python SDK's SmplManagementClient:
//
//	mgmt.Contexts()         // context entity CRUD
//	mgmt.ContextTypes()     // context-type schemas
//	mgmt.Environments()     // environments
//	mgmt.AccountSettings()  // account-level settings
//	mgmt.Config()           // config CRUD (was client.Config().Management())
//	mgmt.Flags()            // flag CRUD (was client.Flags().Management())
//	mgmt.Loggers()          // logger CRUD (split from the old logging mgmt)
//	mgmt.LogGroups()        // log-group CRUD (split from the old logging mgmt)
//	mgmt.Audit()            // audit forwarder CRUD
type ManagementClient struct {
	client     *Client
	appClient  genapp.ClientInterface
	contextBuf *contextRegistrationBuffer
	standalone bool

	environments    *EnvironmentsManagement
	contextTypes    *ContextTypesManagement
	contexts        *ContextsManagement
	accountSettings *AccountSettingsManagement

	// Per-domain management surfaces, owned directly (rule 1):
	// neither requires the runtime Client to operate.
	configMgmt    *ConfigManagement
	flagsMgmt     *FlagsManagement
	loggingMgmt   *LoggingManagement // legacy combined surface
	loggersMgmt   *LoggersManagement
	logGroupsMgmt *LogGroupsManagement
	auditMgmt     *AuditManagement
}

// Environments returns the sub-client for environment CRUD operations.
func (m *ManagementClient) Environments() *EnvironmentsManagement {
	if m.environments == nil {
		m.environments = &EnvironmentsManagement{client: m}
	}
	return m.environments
}

// ContextTypes returns the sub-client for context type CRUD operations.
func (m *ManagementClient) ContextTypes() *ContextTypesManagement {
	if m.contextTypes == nil {
		m.contextTypes = &ContextTypesManagement{client: m}
	}
	return m.contextTypes
}

// Contexts returns the sub-client for context registration and read/delete operations.
func (m *ManagementClient) Contexts() *ContextsManagement {
	if m.contexts == nil {
		m.contexts = &ContextsManagement{client: m}
	}
	return m.contexts
}

// AccountSettings returns the sub-client for account settings get/save.
func (m *ManagementClient) AccountSettings() *AccountSettingsManagement {
	if m.accountSettings == nil {
		m.accountSettings = &AccountSettingsManagement{client: m}
	}
	return m.accountSettings
}

// ── Environments ─────────────────────────────────────────────────────────────

// EnvironmentsManagement provides CRUD operations for environment resources.
// Obtain one via ManagementClient.Environments().
type EnvironmentsManagement struct {
	client *ManagementClient
}

// New returns an unsaved Environment. Call env.Save(ctx) to persist.
func (m *EnvironmentsManagement) New(id string, name string, opts ...EnvironmentOption) *Environment {
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

// List returns all environments for the account.
func (m *EnvironmentsManagement) List(ctx context.Context) ([]*Environment, error) {
	resp, err := m.client.appClient.ListEnvironments(ctx, nil)
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

// Get retrieves a single environment by ID.
func (m *EnvironmentsManagement) Get(ctx context.Context, id string) (*Environment, error) {
	resp, err := m.client.appClient.GetEnvironment(ctx, id)
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

// Delete removes an environment by ID.
func (m *EnvironmentsManagement) Delete(ctx context.Context, id string) error {
	resp, err := m.client.appClient.DeleteEnvironment(ctx, id, nil)
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
func (m *EnvironmentsManagement) create(ctx context.Context, e *Environment) error {
	reqBody := environmentToRequest(e)
	resp, err := m.client.appClient.CreateEnvironmentWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
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
func (m *EnvironmentsManagement) update(ctx context.Context, e *Environment) error {
	reqBody := environmentToRequest(e)
	resp, err := m.client.appClient.UpdateEnvironmentWithApplicationVndAPIPlusJSONBody(ctx, e.ID, reqBody)
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

func environmentToRequest(e *Environment) genapp.EnvironmentRequest {
	cls := genapp.EnvironmentClassification(e.Classification)
	attrs := genapp.Environment{
		Name:           e.Name,
		Color:          e.Color,
		Classification: &cls,
	}
	id := e.ID
	return genapp.EnvironmentRequest{
		Data: genapp.EnvironmentResource{
			Type:       genapp.EnvironmentResourceTypeEnvironment,
			Id:         &id,
			Attributes: attrs,
		},
	}
}

func resourceToEnvironment(r genapp.EnvironmentResource, m *EnvironmentsManagement) *Environment {
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

// ── ContextTypes ─────────────────────────────────────────────────────────────

// ContextTypesManagement provides CRUD operations for context type resources.
// Obtain one via ManagementClient.ContextTypes().
type ContextTypesManagement struct {
	client *ManagementClient
}

// New returns an unsaved ContextType. Call ct.Save(ctx) to persist.
// If no name option is provided the ID is used as the display name.
func (m *ContextTypesManagement) New(id string, opts ...ContextTypeOption) *ContextType {
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

// List returns all context types for the account.
func (m *ContextTypesManagement) List(ctx context.Context) ([]*ContextType, error) {
	resp, err := m.client.appClient.ListContextTypes(ctx, nil)
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

// Get retrieves a single context type by ID.
func (m *ContextTypesManagement) Get(ctx context.Context, id string) (*ContextType, error) {
	resp, err := m.client.appClient.GetContextType(ctx, id)
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

// Delete removes a context type by ID.
func (m *ContextTypesManagement) Delete(ctx context.Context, id string) error {
	resp, err := m.client.appClient.DeleteContextType(ctx, id)
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

func (m *ContextTypesManagement) create(ctx context.Context, ct *ContextType) error {
	reqBody := contextTypeToRequest(ct)
	resp, err := m.client.appClient.CreateContextTypeWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
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

func (m *ContextTypesManagement) update(ctx context.Context, ct *ContextType) error {
	reqBody := contextTypeToRequest(ct)
	resp, err := m.client.appClient.UpdateContextTypeWithApplicationVndAPIPlusJSONBody(ctx, ct.ID, reqBody)
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

func resourceToContextType(r genapp.ContextTypeResource, m *ContextTypesManagement) *ContextType {
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

func parseContextTypeFromBody(body []byte, m *ContextTypesManagement) (*ContextType, error) {
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

// ContextsManagement provides context registration, listing, and deletion.
// Obtain one via ManagementClient.Contexts().
type ContextsManagement struct {
	client *ManagementClient
}

// Register buffers contexts for registration with the server.
// By default contexts are queued for background flush; use WithContextFlush()
// to perform an immediate synchronous flush after queuing.
func (m *ContextsManagement) Register(ctx context.Context, contexts []Context, opts ...ContextsRegisterOption) error {
	o := &contextsRegisterOpts{}
	for _, opt := range opts {
		opt(o)
	}
	m.client.contextBuf.observe(contexts)
	if o.flush {
		return m.Flush(ctx)
	}
	return nil
}

// Flush sends any pending context observations to the server immediately.
func (m *ContextsManagement) Flush(ctx context.Context) error {
	batch := m.client.contextBuf.drain()
	if len(batch) == 0 {
		return nil
	}
	return m.flushBatch(ctx, batch)
}

func (m *ContextsManagement) flushBatch(ctx context.Context, batch []map[string]interface{}) error {
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
	resp, err := m.client.appClient.BulkRegisterContextsWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
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

// List returns all context instances of the given context type.
func (m *ContextsManagement) List(ctx context.Context, contextType string) ([]*ContextEntity, error) {
	params := &genapp.ListContextsParams{FilterContextType: &contextType}
	resp, err := m.client.appClient.ListContexts(ctx, params)
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
//	management.Contexts().Get(ctx, "user:usr_123")
//	management.Contexts().Get(ctx, "user", "usr_123")
func (m *ContextsManagement) Get(ctx context.Context, parts ...string) (*ContextEntity, error) {
	composite, err := resolveContextID(parts)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.appClient.GetContext(ctx, composite)
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
func (m *ContextsManagement) saveEntity(ctx context.Context, ce *ContextEntity) error {
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
	resp, err := m.client.appClient.BulkRegisterContextsWithApplicationVndAPIPlusJSONBody(ctx, body)
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
func (m *ContextsManagement) Delete(ctx context.Context, parts ...string) error {
	composite, err := resolveContextID(parts)
	if err != nil {
		return err
	}
	resp, err := m.client.appClient.DeleteContext(ctx, composite)
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

func parseContextEntityRawWithClient(raw json.RawMessage, client *ContextsManagement) (*ContextEntity, error) {
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

// ── AccountSettings ───────────────────────────────────────────────────────────

// AccountSettingsManagement provides get/save for account-level settings.
// Obtain one via ManagementClient.AccountSettings().
type AccountSettingsManagement struct {
	client *ManagementClient
}

// Get retrieves the current account settings.
func (m *AccountSettingsManagement) Get(ctx context.Context) (*AccountSettings, error) {
	resp, err := m.client.appClient.GetAccountSettings(ctx)
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

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse account settings: %w", err)
	}
	return &AccountSettings{Raw: raw, client: m}, nil
}

// save writes the settings back to the server and updates s in place.
func (m *AccountSettingsManagement) save(ctx context.Context, s *AccountSettings) error {
	bodyBytes, err := json.Marshal(s.Raw)
	if err != nil {
		return fmt.Errorf("smplkit: failed to marshal account settings: %w", err)
	}

	bodyBuf := bytes.NewReader(bodyBytes)
	bodyEditor := genapp.RequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Body = io.NopCloser(bodyBuf)
		req.ContentLength = int64(len(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		return nil
	})

	resp, err := m.client.appClient.PutAccountSettings(ctx, bodyEditor)
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

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("smplkit: failed to parse account settings response: %w", err)
	}
	s.apply(&AccountSettings{Raw: raw})
	return nil
}
