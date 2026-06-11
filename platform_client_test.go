package smplkit

// Tests for the Smpl Platform client: PlatformClient, EnvironmentsClient,
// ServicesClient, ContextTypesClient, ContextsClient, and their active-record
// models (Environment, Service, ContextType, ContextEntity).
//
// White-box (package smplkit) so the tests can construct wired sub-clients
// directly from a generated app transport and reach unexported ctors,
// helpers, and fields.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
)

// ─── harness ──────────────────────────────────────────────────────────────────

// platformTestServer spins up an httptest server running the given handler.
func platformTestServer(h func(http.ResponseWriter, *http.Request)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(h))
}

// platformTestGenApp builds a generated app client pointed at the server,
// wiring the same Accept / User-Agent header editor the production builder uses.
func platformTestGenApp(t *testing.T, serverURL string) genapp.ClientInterface {
	t.Helper()
	editor := genapp.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	gen, err := genapp.NewClient(serverURL, editor)
	require.NoError(t, err)
	return gen
}

// platformTestGenAppWithTransport builds a generated app client that routes
// through a custom RoundTripper (used to force io.ReadAll / connection errors).
func platformTestGenAppWithTransport(t *testing.T, transport http.RoundTripper) genapp.ClientInterface {
	t.Helper()
	httpClient := &http.Client{Transport: transport}
	gen, err := genapp.NewClient("http://platform.test", genapp.WithHTTPClient(httpClient))
	require.NoError(t, err)
	return gen
}

// platformTestWriteJSON writes a JSON body with the given status.
func platformTestWriteJSON(w http.ResponseWriter, status int, payload string) {
	w.Header().Set("Content-Type", "application/vnd.api+json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, payload)
}

// platformTestBrokenBodyTransport returns a response whose body errors on Read.
type platformTestBrokenBodyTransport struct{ status int }

func (t *platformTestBrokenBodyTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(&platformTestErrReader{}),
		Header:     make(http.Header),
	}, nil
}

// platformTestErrReader always fails on Read.
type platformTestErrReader struct{}

func (r *platformTestErrReader) Read(_ []byte) (int, error) {
	return 0, fmt.Errorf("simulated read error")
}

// platformTestErrTransport always returns a transport-level error so the
// generated client's Do returns (nil, err) and classifyError runs.
type platformTestErrTransport struct{}

func (t *platformTestErrTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("dial failed")
}

// platformTestEnvsClient builds an EnvironmentsClient pointed at the server.
func platformTestEnvsClient(t *testing.T, serverURL string) *EnvironmentsClient {
	return &EnvironmentsClient{appClient: platformTestGenApp(t, serverURL)}
}

func platformTestServicesClient(t *testing.T, serverURL string) *ServicesClient {
	return &ServicesClient{appClient: platformTestGenApp(t, serverURL)}
}

func platformTestContextTypesClient(t *testing.T, serverURL string) *ContextTypesClient {
	return &ContextTypesClient{appClient: platformTestGenApp(t, serverURL)}
}

func platformTestContextsClient(t *testing.T, serverURL string) *ContextsClient {
	return &ContextsClient{
		appClient:  platformTestGenApp(t, serverURL),
		contextBuf: newContextRegistrationBuffer(),
	}
}

// ─── PlatformClient construction / accessors ─────────────────────────────────

func TestPlatformClient_WiredCtor_Accessors(t *testing.T) {
	buf := newContextRegistrationBuffer()
	gen := platformTestGenApp(t, "http://platform.test")
	p := newPlatformClient(gen, buf)

	require.NotNil(t, p.Environments())
	require.NotNil(t, p.Services())
	require.NotNil(t, p.ContextTypes())
	require.NotNil(t, p.Contexts())
	// Contexts shares the supplied buffer.
	assert.Same(t, buf, p.contexts.contextBuf)
	assert.NoError(t, p.Close())
}

func TestNewPlatformClient_Standalone(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `{"data":[]}`)
	})
	defer srv.Close()

	p, err := NewPlatformClient(
		Config{APIKey: "sk_test", DisableTelemetry: true},
		withBaseURLOverride(srv.URL),
	)
	require.NoError(t, err)
	require.NotNil(t, p)

	envs, err := p.Environments().List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, envs)
	assert.NoError(t, p.Close())
}

func TestNewPlatformClient_MissingAPIKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	_, err := NewPlatformClient(Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No API key")
}

// ─── Environments: List ──────────────────────────────────────────────────────

func TestEnvironmentsClient_List(t *testing.T) {
	var gotQuery string
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		platformTestWriteJSON(w, http.StatusOK, `{"data":[
			{"type":"environment","id":"production","attributes":{"name":"Production","color":"#ef4444","classification":"STANDARD"}},
			{"type":"environment","id":"preview","attributes":{"name":"Preview","classification":"AD_HOC"}}
		]}`)
	})
	defer srv.Close()

	c := platformTestEnvsClient(t, srv.URL)
	envs, err := c.List(context.Background(), WithPageNumber(2), WithPageSize(50))
	require.NoError(t, err)
	require.Len(t, envs, 2)
	assert.Equal(t, "production", envs[0].ID)
	assert.Equal(t, "Production", envs[0].Name)
	assert.Equal(t, EnvironmentClassificationStandard, envs[0].Classification)
	assert.Equal(t, EnvironmentClassificationAdHoc, envs[1].Classification)
	assert.Contains(t, gotQuery, "page%5Bnumber%5D=2")
	assert.Contains(t, gotQuery, "page%5Bsize%5D=50")
}

func TestEnvironmentsClient_List_Errors(t *testing.T) {
	// classifyError path (transport error).
	c := &EnvironmentsClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	_, err := c.List(context.Background())
	var connErr *ConnectionError
	require.ErrorAs(t, err, &connErr)

	// io.ReadAll error path.
	c = &EnvironmentsClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{})}
	_, err = c.List(context.Background())
	require.ErrorAs(t, err, &connErr)

	// non-2xx → checkStatus.
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusInternalServerError, `{"errors":[{"detail":"boom"}]}`)
	})
	defer srv.Close()
	_, err = platformTestEnvsClient(t, srv.URL).List(context.Background())
	require.Error(t, err)

	// json unmarshal error.
	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `not json`)
	})
	defer bad.Close()
	_, err = platformTestEnvsClient(t, bad.URL).List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse environments")
}

// ─── Environments: Get ───────────────────────────────────────────────────────

func TestEnvironmentsClient_Get(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.True(t, strings.HasSuffix(r.URL.Path, "/production"))
		platformTestWriteJSON(w, http.StatusOK, `{"data":{"type":"environment","id":"production","attributes":{"name":"Production"}}}`)
	})
	defer srv.Close()

	env, err := platformTestEnvsClient(t, srv.URL).Get(context.Background(), "production")
	require.NoError(t, err)
	assert.Equal(t, "production", env.ID)
	assert.Equal(t, "Production", env.Name)
}

func TestEnvironmentsClient_Get_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &EnvironmentsClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	_, err := c.Get(context.Background(), "x")
	require.ErrorAs(t, err, &connErr)

	c = &EnvironmentsClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{})}
	_, err = c.Get(context.Background(), "x")
	require.ErrorAs(t, err, &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusNotFound, `{"errors":[{"detail":"nope"}]}`)
	})
	defer srv.Close()
	_, err = platformTestEnvsClient(t, srv.URL).Get(context.Background(), "x")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound)

	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `not json`)
	})
	defer bad.Close()
	_, err = platformTestEnvsClient(t, bad.URL).Get(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse environment")
}

// ─── Environments: Delete ────────────────────────────────────────────────────

func TestEnvironmentsClient_Delete(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()
	require.NoError(t, platformTestEnvsClient(t, srv.URL).Delete(context.Background(), "x"))
}

func TestEnvironmentsClient_Delete_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &EnvironmentsClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	require.ErrorAs(t, c.Delete(context.Background(), "x"), &connErr)

	c = &EnvironmentsClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{})}
	require.ErrorAs(t, c.Delete(context.Background(), "x"), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusConflict, `{"errors":[{"detail":"in use"}]}`)
	})
	defer srv.Close()
	require.Error(t, platformTestEnvsClient(t, srv.URL).Delete(context.Background(), "x"))
}

// ─── Environments: create / update via Save ──────────────────────────────────

func TestEnvironment_Save_Create(t *testing.T) {
	var body genapp.EnvironmentCreateRequest
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		platformTestWriteJSON(w, http.StatusCreated, `{"data":{"type":"environment","id":"staging","attributes":{"name":"Staging","color":"#00ff00","classification":"STANDARD","created_at":"2024-01-01T00:00:00Z"}}}`)
	})
	defer srv.Close()

	c := platformTestEnvsClient(t, srv.URL)
	env := c.New("staging", "Staging",
		WithEnvironmentColor("#00ff00"),
		WithEnvironmentClassification(EnvironmentClassificationStandard),
	)
	require.NoError(t, env.Save(context.Background()))
	// Request body carried the id + attributes.
	assert.Equal(t, "staging", body.Data.Id)
	assert.Equal(t, "Staging", body.Data.Attributes.Name)
	require.NotNil(t, body.Data.Attributes.Color)
	assert.Equal(t, "#00ff00", *body.Data.Attributes.Color)
	// Response applied: CreatedAt now set → next Save would PUT.
	require.NotNil(t, env.CreatedAt)
}

func TestEnvironment_Save_Update(t *testing.T) {
	var method string
	var reqBody genapp.EnvironmentRequest
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &reqBody))
		platformTestWriteJSON(w, http.StatusOK, `{"data":{"type":"environment","id":"production","attributes":{"name":"Renamed","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-02-01T00:00:00Z"}}}`)
	})
	defer srv.Close()

	c := platformTestEnvsClient(t, srv.URL)
	// Simulate an already-persisted env via the read path.
	created := platformTestParseEnv(t, c, `{"type":"environment","id":"production","attributes":{"name":"Production","created_at":"2024-01-01T00:00:00Z"}}`)
	created.Name = "Renamed"
	require.NoError(t, created.Save(context.Background()))
	assert.Equal(t, http.MethodPut, method)
	require.NotNil(t, reqBody.Data.Id)
	assert.Equal(t, "production", *reqBody.Data.Id)
	assert.Equal(t, "Renamed", created.Name)
}

func TestEnvironment_Save_NoClient(t *testing.T) {
	env := &Environment{ID: "x", Name: "X"}
	err := env.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a client")
}

func TestEnvironment_Create_Errors(t *testing.T) {
	var connErr *ConnectionError

	// transport error
	c := &EnvironmentsClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	require.ErrorAs(t, c.New("a", "A").Save(context.Background()), &connErr)

	// broken body
	c = &EnvironmentsClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{status: http.StatusCreated})}
	require.ErrorAs(t, c.New("a", "A").Save(context.Background()), &connErr)

	// non-2xx
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusBadRequest, `{"errors":[{"detail":"bad"}]}`)
	})
	defer srv.Close()
	require.Error(t, platformTestEnvsClient(t, srv.URL).New("a", "A").Save(context.Background()))

	// json unmarshal error
	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusCreated, `not json`)
	})
	defer bad.Close()
	err := platformTestEnvsClient(t, bad.URL).New("a", "A").Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse environment")
}

func TestEnvironment_Update_Errors(t *testing.T) {
	var connErr *ConnectionError

	mkPersisted := func(c *EnvironmentsClient) *Environment {
		return platformTestParseEnv(t, c, `{"type":"environment","id":"e","attributes":{"name":"E","created_at":"2024-01-01T00:00:00Z"}}`)
	}

	c := &EnvironmentsClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	require.ErrorAs(t, mkPersisted(c).Save(context.Background()), &connErr)

	c = &EnvironmentsClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{status: http.StatusOK})}
	require.ErrorAs(t, mkPersisted(c).Save(context.Background()), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusUnprocessableEntity, `{"errors":[{"detail":"bad"}]}`)
	})
	defer srv.Close()
	require.Error(t, mkPersisted(platformTestEnvsClient(t, srv.URL)).Save(context.Background()))

	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `not json`)
	})
	defer bad.Close()
	err := mkPersisted(platformTestEnvsClient(t, bad.URL)).Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse environment")
}

// ─── Environment model: TypedColor / SetTypedColor / Delete / classification ──

func TestEnvironment_TypedColor(t *testing.T) {
	c := platformTestEnvsClient(t, "http://platform.test")

	// nil color → zero
	env := c.New("a", "A")
	assert.True(t, env.TypedColor().IsZero())

	// empty string color → zero
	empty := ""
	env.Color = &empty
	assert.True(t, env.TypedColor().IsZero())

	// valid hex
	env = c.New("a", "A", WithEnvironmentColor("#ABCDEF"))
	col := env.TypedColor()
	assert.False(t, col.IsZero())
	assert.Equal(t, "#abcdef", col.Hex())

	// invalid hex on wire → zero (not an error)
	bad := "notacolor"
	env.Color = &bad
	assert.True(t, env.TypedColor().IsZero())
}

func TestEnvironment_SetTypedColor(t *testing.T) {
	env := &Environment{ID: "a", Name: "A"}

	col, err := NewColor("#123456")
	require.NoError(t, err)
	env.SetTypedColor(col)
	require.NotNil(t, env.Color)
	assert.Equal(t, "#123456", *env.Color)

	// zero clears
	env.SetTypedColor(Color{})
	assert.Nil(t, env.Color)
}

func TestEnvironment_Delete(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()
	c := platformTestEnvsClient(t, srv.URL)
	env := c.New("toast", "Toast")
	require.NoError(t, env.Delete(context.Background()))
}

func TestEnvironment_Delete_Guards(t *testing.T) {
	// nil client
	env := &Environment{ID: "x"}
	require.Error(t, env.Delete(context.Background()))

	// empty id
	c := platformTestEnvsClient(t, "http://platform.test")
	env = c.New("", "no id")
	require.Error(t, env.Delete(context.Background()))
	assert.Contains(t, env.Delete(context.Background()).Error(), "without a client or id")
}

func TestEnvironment_New_DefaultClassification(t *testing.T) {
	c := platformTestEnvsClient(t, "http://platform.test")
	env := c.New("a", "A")
	assert.Equal(t, EnvironmentClassificationStandard, env.Classification)
}

func TestResourceToEnvironment_NoClassification(t *testing.T) {
	// classification attribute absent → default STANDARD; nil id stays "".
	c := platformTestEnvsClient(t, "http://platform.test")
	env := resourceToEnvironment(genapp.EnvironmentResource{
		Attributes: genapp.Environment{Name: "X"},
	}, c)
	assert.Equal(t, "", env.ID)
	assert.Equal(t, EnvironmentClassificationStandard, env.Classification)
}

// platformTestParseEnv decodes a single environment resource body through the
// production read path so the resulting Environment carries a live client.
func platformTestParseEnv(t *testing.T, c *EnvironmentsClient, resourceJSON string) *Environment {
	t.Helper()
	var res genapp.EnvironmentResource
	require.NoError(t, json.Unmarshal([]byte(resourceJSON), &res))
	return resourceToEnvironment(res, c)
}

// ─── Services ─────────────────────────────────────────────────────────────────

func TestServicesClient_List(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `{"data":[{"type":"service","id":"svc-a","attributes":{"name":"Service A"}}]}`)
	})
	defer srv.Close()
	svcs, err := platformTestServicesClient(t, srv.URL).List(context.Background())
	require.NoError(t, err)
	require.Len(t, svcs, 1)
	assert.Equal(t, "svc-a", svcs[0].ID)
	assert.Equal(t, "Service A", svcs[0].Name)
}

func TestServicesClient_List_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &ServicesClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	_, err := c.List(context.Background())
	require.ErrorAs(t, err, &connErr)

	c = &ServicesClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{})}
	_, err = c.List(context.Background())
	require.ErrorAs(t, err, &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusInternalServerError, `{}`)
	})
	defer srv.Close()
	_, err = platformTestServicesClient(t, srv.URL).List(context.Background())
	require.Error(t, err)

	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `nope`)
	})
	defer bad.Close()
	_, err = platformTestServicesClient(t, bad.URL).List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse services")
}

func TestServicesClient_Get(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `{"data":{"type":"service","id":"svc-a","attributes":{"name":"Service A"}}}`)
	})
	defer srv.Close()
	svc, err := platformTestServicesClient(t, srv.URL).Get(context.Background(), "svc-a")
	require.NoError(t, err)
	assert.Equal(t, "svc-a", svc.ID)
}

func TestServicesClient_Get_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &ServicesClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	_, err := c.Get(context.Background(), "x")
	require.ErrorAs(t, err, &connErr)

	c = &ServicesClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{})}
	_, err = c.Get(context.Background(), "x")
	require.ErrorAs(t, err, &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusNotFound, `{}`)
	})
	defer srv.Close()
	_, err = platformTestServicesClient(t, srv.URL).Get(context.Background(), "x")
	require.Error(t, err)

	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `nope`)
	})
	defer bad.Close()
	_, err = platformTestServicesClient(t, bad.URL).Get(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse service")
}

func TestServicesClient_Delete(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()
	require.NoError(t, platformTestServicesClient(t, srv.URL).Delete(context.Background(), "x"))
}

func TestServicesClient_Delete_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &ServicesClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	require.ErrorAs(t, c.Delete(context.Background(), "x"), &connErr)

	c = &ServicesClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{})}
	require.ErrorAs(t, c.Delete(context.Background(), "x"), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusConflict, `{}`)
	})
	defer srv.Close()
	require.Error(t, platformTestServicesClient(t, srv.URL).Delete(context.Background(), "x"))
}

func TestService_Save_Create(t *testing.T) {
	var body genapp.ServiceCreateRequest
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		platformTestWriteJSON(w, http.StatusCreated, `{"data":{"type":"service","id":"svc","attributes":{"name":"Svc","created_at":"2024-01-01T00:00:00Z"}}}`)
	})
	defer srv.Close()

	svc := platformTestServicesClient(t, srv.URL).New("svc", "Svc")
	require.NoError(t, svc.Save(context.Background()))
	assert.Equal(t, "svc", body.Data.Id)
	assert.Equal(t, "Svc", body.Data.Attributes.Name)
	require.NotNil(t, svc.CreatedAt)
}

func TestService_Save_Update(t *testing.T) {
	var method string
	var reqBody genapp.ServiceRequest
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &reqBody))
		platformTestWriteJSON(w, http.StatusOK, `{"data":{"type":"service","id":"svc","attributes":{"name":"Renamed","created_at":"2024-01-01T00:00:00Z"}}}`)
	})
	defer srv.Close()

	c := platformTestServicesClient(t, srv.URL)
	svc := platformTestParseService(t, c, `{"type":"service","id":"svc","attributes":{"name":"Svc","created_at":"2024-01-01T00:00:00Z"}}`)
	svc.Name = "Renamed"
	require.NoError(t, svc.Save(context.Background()))
	assert.Equal(t, http.MethodPut, method)
	require.NotNil(t, reqBody.Data.Id)
	assert.Equal(t, "svc", *reqBody.Data.Id)
}

func TestService_Save_NoClient(t *testing.T) {
	svc := &Service{ID: "x", Name: "X"}
	err := svc.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a client")
}

func TestService_Create_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &ServicesClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	require.ErrorAs(t, c.New("a", "A").Save(context.Background()), &connErr)

	c = &ServicesClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{status: http.StatusCreated})}
	require.ErrorAs(t, c.New("a", "A").Save(context.Background()), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusBadRequest, `{}`)
	})
	defer srv.Close()
	require.Error(t, platformTestServicesClient(t, srv.URL).New("a", "A").Save(context.Background()))

	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusCreated, `nope`)
	})
	defer bad.Close()
	err := platformTestServicesClient(t, bad.URL).New("a", "A").Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse service")
}

func TestService_Update_Errors(t *testing.T) {
	var connErr *ConnectionError

	mk := func(c *ServicesClient) *Service {
		return platformTestParseService(t, c, `{"type":"service","id":"s","attributes":{"name":"S","created_at":"2024-01-01T00:00:00Z"}}`)
	}

	c := &ServicesClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	require.ErrorAs(t, mk(c).Save(context.Background()), &connErr)

	c = &ServicesClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{status: http.StatusOK})}
	require.ErrorAs(t, mk(c).Save(context.Background()), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusUnprocessableEntity, `{}`)
	})
	defer srv.Close()
	require.Error(t, mk(platformTestServicesClient(t, srv.URL)).Save(context.Background()))

	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `nope`)
	})
	defer bad.Close()
	err := mk(platformTestServicesClient(t, bad.URL)).Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse service")
}

func TestService_Delete(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()
	svc := platformTestServicesClient(t, srv.URL).New("svc", "Svc")
	require.NoError(t, svc.Delete(context.Background()))
}

func TestService_Delete_Guards(t *testing.T) {
	svc := &Service{ID: "x"}
	require.Error(t, svc.Delete(context.Background()))

	c := platformTestServicesClient(t, "http://platform.test")
	svc = c.New("", "no id")
	err := svc.Delete(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a client or id")
}

func TestResourceToService_NilID(t *testing.T) {
	c := platformTestServicesClient(t, "http://platform.test")
	svc := resourceToService(genapp.ServiceResource{Attributes: genapp.Service{Name: "X"}}, c)
	assert.Equal(t, "", svc.ID)
	assert.Equal(t, "X", svc.Name)
}

func platformTestParseService(t *testing.T, c *ServicesClient, resourceJSON string) *Service {
	t.Helper()
	var res genapp.ServiceResource
	require.NoError(t, json.Unmarshal([]byte(resourceJSON), &res))
	return resourceToService(res, c)
}

// ─── ContextTypes ────────────────────────────────────────────────────────────

func TestContextTypesClient_New_DefaultsAndOptions(t *testing.T) {
	c := platformTestContextTypesClient(t, "http://platform.test")

	// No name option → ID used as name.
	ct := c.New("user")
	assert.Equal(t, "user", ct.ID)
	assert.Equal(t, "user", ct.Name)
	assert.NotNil(t, ct.Attributes)

	// With name option.
	ct = c.New("account", WithContextTypeName("Account"))
	assert.Equal(t, "Account", ct.Name)
}

func TestContextTypesClient_List(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `{"data":[
			{"type":"context_type","id":"user","attributes":{"name":"User","attributes":{"plan":{"label":"Plan"},"raw":"not-a-map"}}}
		]}`)
	})
	defer srv.Close()
	types, err := platformTestContextTypesClient(t, srv.URL).List(context.Background())
	require.NoError(t, err)
	require.Len(t, types, 1)
	assert.Equal(t, "user", types[0].ID)
	// map value kept; non-map value coerced to empty map.
	assert.Equal(t, map[string]interface{}{"label": "Plan"}, types[0].Attributes["plan"])
	assert.Equal(t, map[string]interface{}{}, types[0].Attributes["raw"])
}

func TestContextTypesClient_List_NilAttributes(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `{"data":[{"type":"context_type","id":"user","attributes":{"name":"User"}}]}`)
	})
	defer srv.Close()
	types, err := platformTestContextTypesClient(t, srv.URL).List(context.Background())
	require.NoError(t, err)
	require.Len(t, types, 1)
	assert.Empty(t, types[0].Attributes)
}

func TestContextTypesClient_List_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &ContextTypesClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	_, err := c.List(context.Background())
	require.ErrorAs(t, err, &connErr)

	c = &ContextTypesClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{})}
	_, err = c.List(context.Background())
	require.ErrorAs(t, err, &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusInternalServerError, `{}`)
	})
	defer srv.Close()
	_, err = platformTestContextTypesClient(t, srv.URL).List(context.Background())
	require.Error(t, err)

	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `nope`)
	})
	defer bad.Close()
	_, err = platformTestContextTypesClient(t, bad.URL).List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse context types")
}

func TestContextTypesClient_Get(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `{"data":{"type":"context_type","id":"user","attributes":{"name":"User","attributes":{}}}}`)
	})
	defer srv.Close()
	ct, err := platformTestContextTypesClient(t, srv.URL).Get(context.Background(), "user")
	require.NoError(t, err)
	assert.Equal(t, "user", ct.ID)
	assert.NotNil(t, ct.client)
}

func TestContextTypesClient_Get_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &ContextTypesClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	_, err := c.Get(context.Background(), "x")
	require.ErrorAs(t, err, &connErr)

	c = &ContextTypesClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{})}
	_, err = c.Get(context.Background(), "x")
	require.ErrorAs(t, err, &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusNotFound, `{}`)
	})
	defer srv.Close()
	_, err = platformTestContextTypesClient(t, srv.URL).Get(context.Background(), "x")
	require.Error(t, err)

	// parse error inside parseContextTypeFromBody.
	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `nope`)
	})
	defer bad.Close()
	_, err = platformTestContextTypesClient(t, bad.URL).Get(context.Background(), "x")
	require.Error(t, err)
}

func TestContextTypesClient_Delete(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()
	require.NoError(t, platformTestContextTypesClient(t, srv.URL).Delete(context.Background(), "x"))
}

func TestContextTypesClient_Delete_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &ContextTypesClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	require.ErrorAs(t, c.Delete(context.Background(), "x"), &connErr)

	c = &ContextTypesClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{})}
	require.ErrorAs(t, c.Delete(context.Background(), "x"), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusConflict, `{}`)
	})
	defer srv.Close()
	require.Error(t, platformTestContextTypesClient(t, srv.URL).Delete(context.Background(), "x"))
}

func TestContextType_Save_Create(t *testing.T) {
	var body genapp.ContextTypeRequest
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		platformTestWriteJSON(w, http.StatusCreated, `{"data":{"type":"context_type","id":"user","attributes":{"name":"User","attributes":{"plan":{"label":"Plan"}},"created_at":"2024-01-01T00:00:00Z"}}}`)
	})
	defer srv.Close()

	c := platformTestContextTypesClient(t, srv.URL)
	ct := c.New("user", WithContextTypeName("User"))
	ct.AddAttribute("plan", map[string]interface{}{"label": "Plan"})
	require.NoError(t, ct.Save(context.Background()))
	require.NotNil(t, body.Data.Id)
	assert.Equal(t, "user", *body.Data.Id)
	require.NotNil(t, ct.CreatedAt)
}

func TestContextType_Save_Update(t *testing.T) {
	var method string
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		platformTestWriteJSON(w, http.StatusOK, `{"data":{"type":"context_type","id":"user","attributes":{"name":"User","attributes":{},"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-02-01T00:00:00Z"}}}`)
	})
	defer srv.Close()

	c := platformTestContextTypesClient(t, srv.URL)
	// Get one first to obtain a persisted (CreatedAt set) instance.
	getSrv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `{"data":{"type":"context_type","id":"user","attributes":{"name":"User","attributes":{},"created_at":"2024-01-01T00:00:00Z"}}}`)
	})
	defer getSrv.Close()
	persisted, err := platformTestContextTypesClient(t, getSrv.URL).Get(context.Background(), "user")
	require.NoError(t, err)
	require.NotNil(t, persisted.CreatedAt)

	// Re-point its client at the update server.
	persisted.client = c
	persisted.Name = "Renamed"
	require.NoError(t, persisted.Save(context.Background()))
	assert.Equal(t, http.MethodPut, method)
}

func TestContextType_Save_NoClient(t *testing.T) {
	ct := &ContextType{ID: "x", Name: "X"}
	err := ct.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a client")
}

func TestContextType_Create_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &ContextTypesClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	require.ErrorAs(t, c.New("a").Save(context.Background()), &connErr)

	c = &ContextTypesClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{status: http.StatusCreated})}
	require.ErrorAs(t, c.New("a").Save(context.Background()), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusBadRequest, `{}`)
	})
	defer srv.Close()
	require.Error(t, platformTestContextTypesClient(t, srv.URL).New("a").Save(context.Background()))

	// parse error after a 2xx body.
	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusCreated, `nope`)
	})
	defer bad.Close()
	require.Error(t, platformTestContextTypesClient(t, bad.URL).New("a").Save(context.Background()))
}

func TestContextType_Update_Errors(t *testing.T) {
	var connErr *ConnectionError

	mk := func(c *ContextTypesClient) *ContextType {
		ct := c.New("ct")
		ct.CreatedAt = platformTestTimePtr()
		return ct
	}

	c := &ContextTypesClient{appClient: platformTestGenAppWithTransport(t, &platformTestErrTransport{})}
	require.ErrorAs(t, mk(c).Save(context.Background()), &connErr)

	c = &ContextTypesClient{appClient: platformTestGenAppWithTransport(t, &platformTestBrokenBodyTransport{status: http.StatusOK})}
	require.ErrorAs(t, mk(c).Save(context.Background()), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusUnprocessableEntity, `{}`)
	})
	defer srv.Close()
	require.Error(t, mk(platformTestContextTypesClient(t, srv.URL)).Save(context.Background()))

	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `nope`)
	})
	defer bad.Close()
	require.Error(t, mk(platformTestContextTypesClient(t, bad.URL)).Save(context.Background()))
}

func TestContextType_Delete(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()
	ct := platformTestContextTypesClient(t, srv.URL).New("user")
	require.NoError(t, ct.Delete(context.Background()))
}

func TestContextType_Delete_Guards(t *testing.T) {
	ct := &ContextType{ID: "x"}
	require.Error(t, ct.Delete(context.Background()))

	c := platformTestContextTypesClient(t, "http://platform.test")
	ct = c.New("")
	err := ct.Delete(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a client or id")
}

func TestContextType_AttributeMutators(t *testing.T) {
	// AddAttribute with and without metadata, then on a nil map.
	ct := &ContextType{ID: "user", Name: "User"}
	ct.AddAttribute("plan", map[string]interface{}{"label": "Plan"})
	assert.Equal(t, map[string]interface{}{"label": "Plan"}, ct.Attributes["plan"])

	ct.AddAttribute("tier") // no meta → empty map
	assert.Equal(t, map[string]interface{}{}, ct.Attributes["tier"])

	// nil meta slice element → empty map branch.
	ct.AddAttribute("nilmeta", nil)
	assert.Equal(t, map[string]interface{}{}, ct.Attributes["nilmeta"])

	// UpdateAttribute replaces.
	ct.UpdateAttribute("plan", map[string]interface{}{"label": "Subscription"})
	assert.Equal(t, map[string]interface{}{"label": "Subscription"}, ct.Attributes["plan"])

	// RemoveAttribute.
	ct.RemoveAttribute("tier")
	_, ok := ct.Attributes["tier"]
	assert.False(t, ok)
}

func TestContextType_AddAttribute_NilMapInit(t *testing.T) {
	ct := &ContextType{ID: "user"} // Attributes nil
	ct.AddAttribute("a")
	assert.NotNil(t, ct.Attributes)
}

func TestContextType_UpdateAttribute_NilMapInit(t *testing.T) {
	ct := &ContextType{ID: "user"} // Attributes nil
	ct.UpdateAttribute("a", map[string]interface{}{"x": 1})
	assert.Equal(t, map[string]interface{}{"x": 1}, ct.Attributes["a"])
}

func TestResourceToContextType_NilID(t *testing.T) {
	c := platformTestContextTypesClient(t, "http://platform.test")
	ct := resourceToContextType(genapp.ContextTypeResource{
		Attributes: genapp.ContextType{Name: "X"},
	}, c)
	assert.Equal(t, "", ct.ID)
}

// ─── Contexts: Register / Flush / PendingCount ───────────────────────────────

func TestContextsClient_Register_QueueOnly(t *testing.T) {
	c := platformTestContextsClient(t, "http://platform.test")
	err := c.Register(context.Background(), []Context{
		NewContext("user", "u1", map[string]interface{}{"plan": "pro"}),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, c.PendingCount())
}

func TestContextsClient_Register_WithFlush(t *testing.T) {
	var body genapp.ContextBulkRegister
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		platformTestWriteJSON(w, http.StatusOK, `{}`)
	})
	defer srv.Close()

	c := platformTestContextsClient(t, srv.URL)
	err := c.Register(context.Background(), []Context{
		NewContext("user", "u1", map[string]interface{}{"plan": "pro"}),
	}, WithContextFlush())
	require.NoError(t, err)
	assert.Equal(t, 0, c.PendingCount())
	require.Len(t, body.Contexts, 1)
	assert.Equal(t, "user", body.Contexts[0].Type)
	assert.Equal(t, "u1", body.Contexts[0].Key)
	require.NotNil(t, body.Contexts[0].Attributes)
}

func TestContextsClient_Flush_Empty(t *testing.T) {
	c := platformTestContextsClient(t, "http://platform.test")
	// Nothing pending → no request, no error.
	require.NoError(t, c.Flush(context.Background()))
}

func TestContextsClient_Flush_NoAttributes(t *testing.T) {
	// Drain entries that lack an attributes map to hit the false branch of
	// the type-assert in flushBatch.
	var body genapp.ContextBulkRegister
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		platformTestWriteJSON(w, http.StatusOK, `{}`)
	})
	defer srv.Close()

	c := platformTestContextsClient(t, srv.URL)
	// Manually inject a pending entry without an "attributes" key.
	c.contextBuf.pending = append(c.contextBuf.pending, map[string]interface{}{
		"type": "user", "key": "u9",
	})
	require.NoError(t, c.Flush(context.Background()))
	require.Len(t, body.Contexts, 1)
	assert.Nil(t, body.Contexts[0].Attributes)
}

func TestContextsClient_Flush_Errors(t *testing.T) {
	var connErr *ConnectionError

	mk := func(transport http.RoundTripper) *ContextsClient {
		return &ContextsClient{
			appClient:  platformTestGenAppWithTransport(t, transport),
			contextBuf: newContextRegistrationBuffer(),
		}
	}

	c := mk(&platformTestErrTransport{})
	c.contextBuf.observe([]Context{NewContext("user", "u1", nil)})
	require.ErrorAs(t, c.Flush(context.Background()), &connErr)

	c = mk(&platformTestBrokenBodyTransport{})
	c.contextBuf.observe([]Context{NewContext("user", "u1", nil)})
	require.ErrorAs(t, c.Flush(context.Background()), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusInternalServerError, `{}`)
	})
	defer srv.Close()
	c = platformTestContextsClient(t, srv.URL)
	c.contextBuf.observe([]Context{NewContext("user", "u1", nil)})
	require.Error(t, c.Flush(context.Background()))
}

// ─── Contexts: List / Get / Delete ───────────────────────────────────────────

func TestContextsClient_List(t *testing.T) {
	var gotQuery string
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		platformTestWriteJSON(w, http.StatusOK, `{"data":[
			{"id":"user:u1","attributes":{"name":"Alice","attributes":{"plan":"pro"}}},
			{"id":"plainid","attributes":{}}
		]}`)
	})
	defer srv.Close()

	ents, err := platformTestContextsClient(t, srv.URL).List(context.Background(), "user")
	require.NoError(t, err)
	require.Len(t, ents, 2)
	assert.Equal(t, "user", ents[0].ContextType)
	assert.Equal(t, "u1", ents[0].Key)
	require.NotNil(t, ents[0].Name)
	assert.Equal(t, "Alice", *ents[0].Name)
	assert.Equal(t, "pro", ents[0].Attributes["plan"])
	// id without colon → only ContextType set.
	assert.Equal(t, "plainid", ents[1].ContextType)
	assert.Equal(t, "", ents[1].Key)
	assert.Contains(t, gotQuery, "filter%5Bcontext_type%5D=user")
}

func TestContextsClient_List_Errors(t *testing.T) {
	var connErr *ConnectionError

	mk := func(transport http.RoundTripper) *ContextsClient {
		return &ContextsClient{
			appClient:  platformTestGenAppWithTransport(t, transport),
			contextBuf: newContextRegistrationBuffer(),
		}
	}

	_, err := mk(&platformTestErrTransport{}).List(context.Background(), "user")
	require.ErrorAs(t, err, &connErr)

	_, err = mk(&platformTestBrokenBodyTransport{}).List(context.Background(), "user")
	require.ErrorAs(t, err, &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusInternalServerError, `{}`)
	})
	defer srv.Close()
	_, err = platformTestContextsClient(t, srv.URL).List(context.Background(), "user")
	require.Error(t, err)

	// outer unmarshal error.
	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `nope`)
	})
	defer bad.Close()
	_, err = platformTestContextsClient(t, bad.URL).List(context.Background(), "user")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse contexts")

	// per-item parse error (a data element that is not an object).
	itemBad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `{"data":[123]}`)
	})
	defer itemBad.Close()
	_, err = platformTestContextsClient(t, itemBad.URL).List(context.Background(), "user")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse context entity")
}

func TestContextsClient_Get_Composite(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.True(t, strings.HasSuffix(r.URL.Path, "/user:u1"))
		platformTestWriteJSON(w, http.StatusOK, `{"data":{"id":"user:u1","attributes":{"name":"Alice","attributes":{"plan":"pro"}}}}`)
	})
	defer srv.Close()

	// composite "type:key"
	ce, err := platformTestContextsClient(t, srv.URL).Get(context.Background(), "user:u1")
	require.NoError(t, err)
	assert.Equal(t, "user", ce.ContextType)
	assert.Equal(t, "u1", ce.Key)
}

func TestContextsClient_Get_TwoArgs(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.True(t, strings.HasSuffix(r.URL.Path, "/user:u1"))
		platformTestWriteJSON(w, http.StatusOK, `{"data":{"id":"user:u1","attributes":{}}}`)
	})
	defer srv.Close()

	ce, err := platformTestContextsClient(t, srv.URL).Get(context.Background(), "user", "u1")
	require.NoError(t, err)
	assert.Equal(t, "user", ce.ContextType)
	assert.Equal(t, "u1", ce.Key)
}

func TestContextsClient_Get_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := platformTestContextsClient(t, "http://platform.test")
	// composite parse error: single arg without colon.
	_, err := c.Get(context.Background(), "nocolon")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be 'type:key'")

	mk := func(transport http.RoundTripper) *ContextsClient {
		return &ContextsClient{appClient: platformTestGenAppWithTransport(t, transport), contextBuf: newContextRegistrationBuffer()}
	}

	_, err = mk(&platformTestErrTransport{}).Get(context.Background(), "user:u1")
	require.ErrorAs(t, err, &connErr)

	_, err = mk(&platformTestBrokenBodyTransport{}).Get(context.Background(), "user:u1")
	require.ErrorAs(t, err, &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusNotFound, `{}`)
	})
	defer srv.Close()
	_, err = platformTestContextsClient(t, srv.URL).Get(context.Background(), "user:u1")
	require.Error(t, err)

	// outer unmarshal error.
	bad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `nope`)
	})
	defer bad.Close()
	_, err = platformTestContextsClient(t, bad.URL).Get(context.Background(), "user:u1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse context")

	// inner entity parse error (data not an object).
	innerBad := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `{"data":123}`)
	})
	defer innerBad.Close()
	_, err = platformTestContextsClient(t, innerBad.URL).Get(context.Background(), "user:u1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse context entity")
}

func TestContextsClient_Delete(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.True(t, strings.HasSuffix(r.URL.Path, "/user:u1"))
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()
	require.NoError(t, platformTestContextsClient(t, srv.URL).Delete(context.Background(), "user", "u1"))
}

func TestContextsClient_Delete_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := platformTestContextsClient(t, "http://platform.test")
	// composite parse error.
	err := c.Delete(context.Background())
	require.Error(t, err)

	mk := func(transport http.RoundTripper) *ContextsClient {
		return &ContextsClient{appClient: platformTestGenAppWithTransport(t, transport), contextBuf: newContextRegistrationBuffer()}
	}

	require.ErrorAs(t, mk(&platformTestErrTransport{}).Delete(context.Background(), "user:u1"), &connErr)
	require.ErrorAs(t, mk(&platformTestBrokenBodyTransport{}).Delete(context.Background(), "user:u1"), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusConflict, `{}`)
	})
	defer srv.Close()
	require.Error(t, platformTestContextsClient(t, srv.URL).Delete(context.Background(), "user:u1"))
}

// ─── resolveContextID ────────────────────────────────────────────────────────

func TestResolveContextID(t *testing.T) {
	tests := []struct {
		name    string
		parts   []string
		want    string
		wantErr string
	}{
		{"composite ok", []string{"user:u1"}, "user:u1", ""},
		{"composite empty", []string{""}, "", "must be 'type:key'"},
		{"composite no colon", []string{"nocolon"}, "", "must be 'type:key'"},
		{"two args ok", []string{"user", "u1"}, "user:u1", ""},
		{"two args empty type", []string{"", "u1"}, "", "must not be empty"},
		{"two args empty key", []string{"user", ""}, "", "must not be empty"},
		{"zero args", nil, "", "one composite id or two"},
		{"three args", []string{"a", "b", "c"}, "", "one composite id or two"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveContextID(tc.parts)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestContainsColon(t *testing.T) {
	assert.True(t, containsColon("a:b"))
	assert.False(t, containsColon("ab"))
	assert.False(t, containsColon(""))
}

func TestIndexByte(t *testing.T) {
	assert.Equal(t, 1, indexByte("a:b", ':'))
	assert.Equal(t, -1, indexByte("abc", ':'))
}

// ─── ContextEntity model ──────────────────────────────────────────────────────

func TestContextEntity_CompositeID(t *testing.T) {
	ce := &ContextEntity{ContextType: "user", Key: "u1"}
	assert.Equal(t, "user:u1", ce.CompositeID())
}

func TestContextEntity_Save(t *testing.T) {
	var body genapp.ContextBulkRegister
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		platformTestWriteJSON(w, http.StatusOK, `{}`)
	})
	defer srv.Close()

	c := platformTestContextsClient(t, srv.URL)
	name := "Alice"
	ce := &ContextEntity{
		ContextType: "user",
		Key:         "u1",
		Name:        &name,
		Attributes:  map[string]interface{}{"plan": "pro"},
		client:      c,
	}
	require.NoError(t, ce.Save(context.Background()))
	require.Len(t, body.Contexts, 1)
	assert.Equal(t, "user", body.Contexts[0].Type)
	require.NotNil(t, body.Contexts[0].Attributes)
	attrs := *body.Contexts[0].Attributes
	assert.Equal(t, "pro", attrs["plan"])
	assert.Equal(t, "Alice", attrs["name"])
}

func TestContextEntity_Save_NoNameNoEmpty(t *testing.T) {
	// nil Name and empty Name pointer must NOT inject a "name" attribute.
	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusOK, `{}`)
	})
	defer srv.Close()

	c := platformTestContextsClient(t, srv.URL)
	ce := &ContextEntity{ContextType: "user", Key: "u1", Attributes: nil, client: c}
	require.NoError(t, ce.Save(context.Background()))

	empty := ""
	ce.Name = &empty
	require.NoError(t, ce.Save(context.Background()))
}

func TestContextEntity_Save_NoClient(t *testing.T) {
	ce := &ContextEntity{ContextType: "user", Key: "u1"}
	err := ce.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a client")
}

func TestContextEntity_Save_Errors(t *testing.T) {
	var connErr *ConnectionError

	mkEntity := func(transport http.RoundTripper) *ContextEntity {
		c := &ContextsClient{appClient: platformTestGenAppWithTransport(t, transport), contextBuf: newContextRegistrationBuffer()}
		return &ContextEntity{ContextType: "user", Key: "u1", client: c}
	}

	require.ErrorAs(t, mkEntity(&platformTestErrTransport{}).Save(context.Background()), &connErr)
	require.ErrorAs(t, mkEntity(&platformTestBrokenBodyTransport{}).Save(context.Background()), &connErr)

	srv := platformTestServer(func(w http.ResponseWriter, _ *http.Request) {
		platformTestWriteJSON(w, http.StatusInternalServerError, `{}`)
	})
	defer srv.Close()
	ce := &ContextEntity{ContextType: "user", Key: "u1", client: platformTestContextsClient(t, srv.URL)}
	require.Error(t, ce.Save(context.Background()))
}

func TestContextEntity_Delete(t *testing.T) {
	srv := platformTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.True(t, strings.HasSuffix(r.URL.Path, "/user:u1"))
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()
	ce := &ContextEntity{ContextType: "user", Key: "u1", client: platformTestContextsClient(t, srv.URL)}
	require.NoError(t, ce.Delete(context.Background()))
}

func TestContextEntity_Delete_NoClient(t *testing.T) {
	ce := &ContextEntity{ContextType: "user", Key: "u1"}
	err := ce.Delete(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a client")
}

// platformTestTimePtr returns a non-nil *time.Time so a model reads as persisted.
func platformTestTimePtr() *time.Time {
	t := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return &t
}
