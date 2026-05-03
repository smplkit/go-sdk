package smplkit

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
	genlogging "github.com/smplkit/go-sdk/v3/internal/generated/logging"
)

// errTransport returns a transport-level error on every request, used to cover
// the classifyError(err) paths where the HTTP round-trip itself fails.
type errTransport struct{}

func (t *errTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("connection refused")
}

// newManagementTestClientWithTransport builds a ManagementClient backed by a
// custom HTTP transport. This is used to exercise io.ReadAll error paths.
func newManagementTestClientWithTransport(t *testing.T, transport http.RoundTripper) *ManagementClient {
	t.Helper()
	httpClient := &http.Client{Transport: transport}

	appHeaderEditor := genapp.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genAppClient, _ := genapp.NewClient("http://localhost:1",
		genapp.WithHTTPClient(httpClient),
		appHeaderEditor,
	)

	c := &Client{
		apiKey:       "sk_test",
		httpClient:   httpClient,
		appGenerated: genAppClient,
	}
	ctxBuf := newContextRegistrationBuffer()
	mgmt := &ManagementClient{
		client:     c,
		appClient:  genAppClient,
		contextBuf: ctxBuf,
	}
	c.management = mgmt
	return mgmt
}

func newLoggingManagementWithTransport(t *testing.T, transport http.RoundTripper) *LoggingManagement {
	t.Helper()
	httpClient := &http.Client{Transport: transport}

	logHeaderEditor := genlogging.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	genLogClient, _ := genlogging.NewClient("http://localhost:1",
		genlogging.WithHTTPClient(httpClient),
		logHeaderEditor,
	)

	lm := newLoggingManagement(genLogClient)
	return lm
}

// ── EnvironmentsManagement io.ReadAll error paths ─────────────────────────────

func TestEnvironmentsManagement_List_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	_, err := mgmt.Environments().List(context.Background())
	assert.Error(t, err)
}

func TestEnvironmentsManagement_Get_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	_, err := mgmt.Environments().Get(context.Background(), "production")
	assert.Error(t, err)
}

func TestEnvironmentsManagement_Delete_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	err := mgmt.Environments().Delete(context.Background(), "production")
	assert.Error(t, err)
}

func TestEnvironmentsManagement_Create_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	e := &Environment{ID: "test", Name: "Test", client: mgmt.Environments()}
	err := mgmt.Environments().create(context.Background(), e)
	assert.Error(t, err)
}

func TestEnvironmentsManagement_Update_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	e := &Environment{ID: "test", Name: "Test", client: mgmt.Environments()}
	err := mgmt.Environments().update(context.Background(), e)
	assert.Error(t, err)
}

// ── ContextTypesManagement io.ReadAll error paths ─────────────────────────────

func TestContextTypesManagement_List_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	_, err := mgmt.ContextTypes().List(context.Background())
	assert.Error(t, err)
}

func TestContextTypesManagement_Get_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	_, err := mgmt.ContextTypes().Get(context.Background(), "user")
	assert.Error(t, err)
}

func TestContextTypesManagement_Delete_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	err := mgmt.ContextTypes().Delete(context.Background(), "user")
	assert.Error(t, err)
}

func TestContextTypesManagement_Create_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	ct := &ContextType{ID: "user", Name: "User", Attributes: map[string]map[string]interface{}{}, client: mgmt.ContextTypes()}
	err := mgmt.ContextTypes().create(context.Background(), ct)
	assert.Error(t, err)
}

func TestContextTypesManagement_Update_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	ct := &ContextType{ID: "user", Name: "User", Attributes: map[string]map[string]interface{}{}, client: mgmt.ContextTypes()}
	err := mgmt.ContextTypes().update(context.Background(), ct)
	assert.Error(t, err)
}

// ── ContextsManagement io.ReadAll error paths ─────────────────────────────────

func TestContextsManagement_FlushBatch_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	err := mgmt.Contexts().flushBatch(context.Background(), []map[string]interface{}{
		{"type": "user", "key": "u1"},
	})
	assert.Error(t, err)
}

func TestContextsManagement_List_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	_, err := mgmt.Contexts().List(context.Background(), "user")
	assert.Error(t, err)
}

func TestContextsManagement_Get_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	_, err := mgmt.Contexts().Get(context.Background(), "user:u1")
	assert.Error(t, err)
}

func TestContextsManagement_Delete_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	err := mgmt.Contexts().Delete(context.Background(), "user:u1")
	assert.Error(t, err)
}

func TestContextsManagement_SaveEntity_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	name := "Alice"
	ce := &ContextEntity{
		ContextType: "user",
		Key:         "u-1",
		Name:        &name,
		Attributes:  map[string]interface{}{"plan": "ent"},
		client:      mgmt.Contexts(),
	}
	err := ce.Save(context.Background())
	assert.Error(t, err)
}

// ── AccountSettingsManagement io.ReadAll error paths ─────────────────────────

func TestAccountSettingsManagement_Get_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	_, err := mgmt.AccountSettings().Get(context.Background())
	assert.Error(t, err)
}

func TestAccountSettingsManagement_Save_ReadBodyError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &brokenBodyTransport{})
	s := &AccountSettings{Raw: map[string]interface{}{"k": "v"}, client: mgmt.AccountSettings()}
	err := mgmt.AccountSettings().save(context.Background(), s)
	assert.Error(t, err)
}

// ── LoggingManagement RegisterSources io.ReadAll error path ──────────────────

func TestLoggingManagement_RegisterSources_ReadBodyError(t *testing.T) {
	lm := newLoggingManagementWithTransport(t, &brokenBodyTransport{})
	err := lm.RegisterSources(context.Background(), []LoggerSource{
		NewLoggerSource("my.logger"),
	})
	assert.Error(t, err)
}

// ── parseContextTypeRaw: unmarshal error path ─────────────────────────────────

func TestParseContextTypeRaw_UnmarshalError(t *testing.T) {
	_, err := parseContextTypeRaw([]byte(`{not json`))
	assert.Error(t, err)
}

// ── FlagsManagement.ListContextTypes: unmarshal error path ───────────────────

func TestFlagsManagement_ListContextTypes_ReadBodyError(t *testing.T) {
	fc := newFlagsClientWithTransport(t, &brokenBodyTransport{})
	_, err := fc.Management().ListContextTypes(context.Background())
	assert.Error(t, err)
}

// ── resolveContextID edge cases ───────────────────────────────────────────────

func TestResolveContextID_EmptyString(t *testing.T) {
	_, err := resolveContextID([]string{""})
	assert.Error(t, err)
}

func TestResolveContextID_EmptyType(t *testing.T) {
	_, err := resolveContextID([]string{"", "key"})
	assert.Error(t, err)
}

func TestResolveContextID_ZeroArgs(t *testing.T) {
	_, err := resolveContextID([]string{})
	assert.Error(t, err)
}

// ── indexByte ─────────────────────────────────────────────────────────────────

func TestIndexByte_NoMatch(t *testing.T) {
	assert.Equal(t, -1, indexByte("nocolon", ':'))
}

func TestIndexByte_Match(t *testing.T) {
	assert.Equal(t, 4, indexByte("user:key", ':'))
}

// ── Transport-error paths (classifyError branch) ──────────────────────────────
// These tests exercise the `if err != nil { return classifyError(err) }` blocks
// that fire when the HTTP round-trip itself fails at the transport layer.

func TestEnvironmentsManagement_List_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	_, err := mgmt.Environments().List(context.Background())
	assert.Error(t, err)
}

func TestEnvironmentsManagement_Get_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	_, err := mgmt.Environments().Get(context.Background(), "production")
	assert.Error(t, err)
}

func TestEnvironmentsManagement_Delete_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	err := mgmt.Environments().Delete(context.Background(), "production")
	assert.Error(t, err)
}

func TestEnvironmentsManagement_Create_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	e := &Environment{ID: "test", Name: "Test", client: mgmt.Environments()}
	err := mgmt.Environments().create(context.Background(), e)
	assert.Error(t, err)
}

func TestEnvironmentsManagement_Update_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	e := &Environment{ID: "test", Name: "Test", client: mgmt.Environments()}
	err := mgmt.Environments().update(context.Background(), e)
	assert.Error(t, err)
}

func TestContextTypesManagement_List_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	_, err := mgmt.ContextTypes().List(context.Background())
	assert.Error(t, err)
}

func TestContextTypesManagement_Get_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	_, err := mgmt.ContextTypes().Get(context.Background(), "user")
	assert.Error(t, err)
}

func TestContextTypesManagement_Delete_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	err := mgmt.ContextTypes().Delete(context.Background(), "user")
	assert.Error(t, err)
}

func TestContextTypesManagement_Create_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	ct := &ContextType{ID: "user", Name: "User", Attributes: map[string]map[string]interface{}{}, client: mgmt.ContextTypes()}
	err := mgmt.ContextTypes().create(context.Background(), ct)
	assert.Error(t, err)
}

func TestContextTypesManagement_Update_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	ct := &ContextType{ID: "user", Name: "User", Attributes: map[string]map[string]interface{}{}, client: mgmt.ContextTypes()}
	err := mgmt.ContextTypes().update(context.Background(), ct)
	assert.Error(t, err)
}

func TestContextsManagement_FlushBatch_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	err := mgmt.Contexts().flushBatch(context.Background(), []map[string]interface{}{
		{"type": "user", "key": "u1"},
	})
	assert.Error(t, err)
}

func TestContextsManagement_List_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	_, err := mgmt.Contexts().List(context.Background(), "user")
	assert.Error(t, err)
}

func TestContextsManagement_Get_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	_, err := mgmt.Contexts().Get(context.Background(), "user:u1")
	assert.Error(t, err)
}

func TestContextsManagement_Delete_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	err := mgmt.Contexts().Delete(context.Background(), "user:u1")
	assert.Error(t, err)
}

func TestAccountSettingsManagement_Get_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	_, err := mgmt.AccountSettings().Get(context.Background())
	assert.Error(t, err)
}

func TestAccountSettingsManagement_Save_TransportError(t *testing.T) {
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	s := &AccountSettings{Raw: map[string]interface{}{"k": "v"}, client: mgmt.AccountSettings()}
	err := mgmt.AccountSettings().save(context.Background(), s)
	assert.Error(t, err)
}

func TestAccountSettingsManagement_Save_MarshalError(t *testing.T) {
	// json.Marshal fails when Raw contains a non-serializable value.
	mgmt := newManagementTestClientWithTransport(t, &errTransport{})
	s := &AccountSettings{Raw: map[string]interface{}{"k": make(chan int)}, client: mgmt.AccountSettings()}
	err := mgmt.AccountSettings().save(context.Background(), s)
	assert.Error(t, err)
}

func TestLoggingManagement_RegisterSources_TransportError(t *testing.T) {
	lm := newLoggingManagementWithTransport(t, &errTransport{})
	err := lm.RegisterSources(context.Background(), []LoggerSource{
		NewLoggerSource("my.logger"),
	})
	assert.Error(t, err)
}
