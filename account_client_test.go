package smplkit

// Tests for the Smpl Account client: AccountClient, SettingsClient, and the
// AccountSettings active-record model.
//
// White-box (package smplkit) so the tests can construct a wired SettingsClient
// directly from a generated app transport and reach unexported ctors/helpers.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
)

// ─── harness ──────────────────────────────────────────────────────────────────

func accountTestServer(h func(http.ResponseWriter, *http.Request)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(h))
}

func accountTestGenApp(t *testing.T, serverURL string) genapp.ClientInterface {
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

func accountTestGenAppWithTransport(t *testing.T, transport http.RoundTripper) genapp.ClientInterface {
	t.Helper()
	httpClient := &http.Client{Transport: transport}
	gen, err := genapp.NewClient("http://account.test", genapp.WithHTTPClient(httpClient))
	require.NoError(t, err)
	return gen
}

func accountTestWriteJSON(w http.ResponseWriter, status int, payload string) {
	w.Header().Set("Content-Type", "application/vnd.api+json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, payload)
}

// accountTestBrokenBodyTransport returns a response whose body errors on Read.
type accountTestBrokenBodyTransport struct{ status int }

func (t *accountTestBrokenBodyTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(&accountTestErrReader{}),
		Header:     make(http.Header),
	}, nil
}

type accountTestErrReader struct{}

func (r *accountTestErrReader) Read(_ []byte) (int, error) {
	return 0, fmt.Errorf("simulated read error")
}

// accountTestErrTransport always returns a transport-level error.
type accountTestErrTransport struct{}

func (t *accountTestErrTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("dial failed")
}

func accountTestSettingsClient(t *testing.T, serverURL string) *SettingsClient {
	return &SettingsClient{appClient: accountTestGenApp(t, serverURL)}
}

// ─── AccountClient construction / accessors ──────────────────────────────────

func TestAccountClient_WiredCtor_Accessors(t *testing.T) {
	gen := accountTestGenApp(t, "http://account.test")
	a := newAccountClient(gen)
	require.NotNil(t, a.Settings())
	assert.NoError(t, a.Close())
}

func TestNewAccountClient_Standalone(t *testing.T) {
	srv := accountTestServer(func(w http.ResponseWriter, _ *http.Request) {
		accountTestWriteJSON(w, http.StatusOK, `{"environment_order":["production"]}`)
	})
	defer srv.Close()

	a, err := NewAccountClient(
		Config{APIKey: "sk_test", DisableTelemetry: true},
		withBaseURLOverride(srv.URL),
	)
	require.NoError(t, err)
	require.NotNil(t, a)

	settings, err := a.Settings().Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"production"}, settings.EnvironmentOrder())
	assert.NoError(t, a.Close())
}

func TestNewAccountClient_MissingAPIKey(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	_, err := NewAccountClient(Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No API key")
}

// ─── SettingsClient.Get ───────────────────────────────────────────────────────

func TestSettingsClient_Get(t *testing.T) {
	srv := accountTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		accountTestWriteJSON(w, http.StatusOK, `{"environment_order":["production","staging"],"extra":"keep"}`)
	})
	defer srv.Close()

	settings, err := accountTestSettingsClient(t, srv.URL).Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"production", "staging"}, settings.EnvironmentOrder())
	// Unknown keys preserved in Raw.
	assert.Equal(t, "keep", settings.Raw["extra"])
	assert.NotNil(t, settings.client)
}

func TestSettingsClient_Get_Errors(t *testing.T) {
	var connErr *ConnectionError

	c := &SettingsClient{appClient: accountTestGenAppWithTransport(t, &accountTestErrTransport{})}
	_, err := c.Get(context.Background())
	require.ErrorAs(t, err, &connErr)

	c = &SettingsClient{appClient: accountTestGenAppWithTransport(t, &accountTestBrokenBodyTransport{})}
	_, err = c.Get(context.Background())
	require.ErrorAs(t, err, &connErr)

	srv := accountTestServer(func(w http.ResponseWriter, _ *http.Request) {
		accountTestWriteJSON(w, http.StatusInternalServerError, `{"errors":[{"detail":"boom"}]}`)
	})
	defer srv.Close()
	_, err = accountTestSettingsClient(t, srv.URL).Get(context.Background())
	require.Error(t, err)

	bad := accountTestServer(func(w http.ResponseWriter, _ *http.Request) {
		accountTestWriteJSON(w, http.StatusOK, `not json`)
	})
	defer bad.Close()
	_, err = accountTestSettingsClient(t, bad.URL).Get(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse account settings")
}

// ─── AccountSettings.Save (SettingsClient.save) ──────────────────────────────

func TestAccountSettings_Save(t *testing.T) {
	var gotBody map[string]interface{}
	srv := accountTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &gotBody))
		accountTestWriteJSON(w, http.StatusOK, `{"environment_order":["production","staging"]}`)
	})
	defer srv.Close()

	c := accountTestSettingsClient(t, srv.URL)
	settings, err := func() (*AccountSettings, error) {
		// Seed via Get so the instance carries a client.
		getSrv := accountTestServer(func(w http.ResponseWriter, _ *http.Request) {
			accountTestWriteJSON(w, http.StatusOK, `{"environment_order":["production"]}`)
		})
		defer getSrv.Close()
		return accountTestSettingsClient(t, getSrv.URL).Get(context.Background())
	}()
	require.NoError(t, err)

	// Re-point the client at the PUT server and mutate.
	settings.client = c
	settings.SetEnvironmentOrder([]string{"production", "staging"})
	require.NoError(t, settings.Save(context.Background()))

	// Request body carried the mutated order.
	order, ok := gotBody["environment_order"].([]interface{})
	require.True(t, ok)
	assert.Len(t, order, 2)

	// Response applied back.
	assert.Equal(t, []string{"production", "staging"}, settings.EnvironmentOrder())
}

func TestAccountSettings_Save_NoClient(t *testing.T) {
	s := &AccountSettings{Raw: map[string]interface{}{}}
	err := s.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a client")
}

func TestAccountSettings_Save_Errors(t *testing.T) {
	var connErr *ConnectionError

	mk := func(transport http.RoundTripper) *AccountSettings {
		return &AccountSettings{
			Raw:    map[string]interface{}{"environment_order": []interface{}{"production"}},
			client: &SettingsClient{appClient: accountTestGenAppWithTransport(t, transport)},
		}
	}

	require.ErrorAs(t, mk(&accountTestErrTransport{}).Save(context.Background()), &connErr)
	require.ErrorAs(t, mk(&accountTestBrokenBodyTransport{}).Save(context.Background()), &connErr)

	// non-2xx.
	srv := accountTestServer(func(w http.ResponseWriter, _ *http.Request) {
		accountTestWriteJSON(w, http.StatusBadRequest, `{"errors":[{"detail":"bad"}]}`)
	})
	defer srv.Close()
	s := &AccountSettings{Raw: map[string]interface{}{}, client: accountTestSettingsClient(t, srv.URL)}
	require.Error(t, s.Save(context.Background()))

	// 2xx but unparseable response body.
	bad := accountTestServer(func(w http.ResponseWriter, _ *http.Request) {
		accountTestWriteJSON(w, http.StatusOK, `not json`)
	})
	defer bad.Close()
	s = &AccountSettings{Raw: map[string]interface{}{}, client: accountTestSettingsClient(t, bad.URL)}
	err := s.Save(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse account settings response")
}

// ─── AccountSettings model: EnvironmentOrder / SetEnvironmentOrder / Raw ──────

func TestAccountSettings_EnvironmentOrder_Variants(t *testing.T) {
	// missing key → nil
	s := &AccountSettings{Raw: map[string]interface{}{}}
	assert.Nil(t, s.EnvironmentOrder())

	// explicit nil value → nil
	s = &AccountSettings{Raw: map[string]interface{}{"environment_order": nil}}
	assert.Nil(t, s.EnvironmentOrder())

	// []string variant
	s = &AccountSettings{Raw: map[string]interface{}{"environment_order": []string{"a", "b"}}}
	assert.Equal(t, []string{"a", "b"}, s.EnvironmentOrder())

	// []interface{} variant with a non-string element (skipped)
	s = &AccountSettings{Raw: map[string]interface{}{"environment_order": []interface{}{"a", 42, "b"}}}
	assert.Equal(t, []string{"a", "b"}, s.EnvironmentOrder())

	// unexpected type → nil (falls through the switch)
	s = &AccountSettings{Raw: map[string]interface{}{"environment_order": "not-a-list"}}
	assert.Nil(t, s.EnvironmentOrder())
}

func TestAccountSettings_SetEnvironmentOrder(t *testing.T) {
	// nil Raw is lazily initialized.
	s := &AccountSettings{}
	s.SetEnvironmentOrder([]string{"production", "staging"})
	require.NotNil(t, s.Raw)
	stored, ok := s.Raw["environment_order"].([]interface{})
	require.True(t, ok)
	assert.Equal(t, []interface{}{"production", "staging"}, stored)
	// Round-trips through the getter.
	assert.Equal(t, []string{"production", "staging"}, s.EnvironmentOrder())
}

func TestAccountSettings_Apply(t *testing.T) {
	s := &AccountSettings{Raw: map[string]interface{}{"old": true}}
	s.apply(&AccountSettings{Raw: map[string]interface{}{"new": true}})
	assert.Equal(t, map[string]interface{}{"new": true}, s.Raw)
}
