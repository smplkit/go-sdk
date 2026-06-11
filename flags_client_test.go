package smplkit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk/v3"
)

func sampleFlagListJSON(id, name, flagType string) string {
	return `{
		"data": [{
			"id": "` + id + `",
			"type": "flag",
			"attributes": {
				"id": "` + id + `",
				"name": "` + name + `",
				"type": "` + flagType + `",
				"default": true,
				"values": [{"name": "True", "value": true}, {"name": "False", "value": false}],
				"description": "A test flag",
				"environments": {},
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-06-15T12:00:00Z"
			}
		}]
	}`
}

func newExternalFlagsClient(t *testing.T, server *httptest.Server) *smplkit.FlagsClient {
	t.Helper()
	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	return client.Flags()
}

func TestClient_FlagsReturnsSubClient(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	flags := client.Flags()
	require.NotNil(t, flags)
	// Calling Flags() multiple times returns the same sub-client.
	assert.Same(t, flags, client.Flags())
}

func TestFlagsClient_Get_External(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Single-flag GET returns a single-object envelope.
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"feature-x","type":"flag","attributes":{"id":"feature-x","name":"Feature X","type":"BOOLEAN","default":true,"environments":{}}}}`))
	}))
	defer server.Close()

	flags := newExternalFlagsClient(t, server)
	flag, err := flags.Get(context.Background(), "feature-x")
	require.NoError(t, err)
	assert.Equal(t, "feature-x", flag.ID)
	assert.Equal(t, "BOOLEAN", flag.Type)
}

func TestFlagsClient_List_External(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListJSON("feature-x", "Feature X", "BOOLEAN")))
	}))
	defer server.Close()

	flags := newExternalFlagsClient(t, server)
	list, err := flags.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "feature-x", list[0].ID)
}

func TestFlagsClient_Delete_External(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	flags := newExternalFlagsClient(t, server)
	err := flags.Delete(context.Background(), "feature-x")
	assert.NoError(t, err)
}

func TestFlagsClient_Get_ByID_Error(t *testing.T) {
	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithHTTPClient(&http.Client{Transport: &failTransport{}}),
	)
	require.NoError(t, err)

	_, err = client.Flags().Get(context.Background(), "some-flag")
	assert.Error(t, err)
}

func TestFlagsClient_Delete_ByID_Error(t *testing.T) {
	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithHTTPClient(&http.Client{Transport: &failTransport{}}),
	)
	require.NoError(t, err)

	err = client.Flags().Delete(context.Background(), "some-flag")
	assert.Error(t, err)
}

func TestFlagsClient_NewBooleanFlag_AutoValues(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)

	// NewBooleanFlag auto-generates True/False values.
	flag := client.Flags().NewBooleanFlag("feature-x", false)
	assert.Equal(t, "feature-x", flag.ID)
	require.NotNil(t, flag.Values)
	assert.Len(t, *flag.Values, 2)
}

func TestFlagsClient_NewStringFlag_Unconstrained(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)

	// NewStringFlag without WithFlagValues creates an unconstrained flag (Values=nil).
	flag := client.Flags().NewStringFlag("greeting", "hello")
	assert.Equal(t, "greeting", flag.ID)
	assert.Equal(t, "hello", flag.Default)
	assert.Nil(t, flag.Values)
}

func TestFlagsClient_NewNumberFlag_Unconstrained(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)

	flag := client.Flags().NewNumberFlag("max-retries", 3.0)
	assert.Equal(t, "max-retries", flag.ID)
	assert.Nil(t, flag.Values)
}

func TestFlagsClient_NewJsonFlag_Unconstrained(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)

	flag := client.Flags().NewJsonFlag("config", map[string]interface{}{"key": "value"})
	assert.Equal(t, "config", flag.ID)
	assert.Nil(t, flag.Values)
}

func TestFlagsClient_NewStringFlag_Constrained(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)

	flag := client.Flags().NewStringFlag("theme", "light",
		smplkit.WithFlagValues([]smplkit.FlagValue{
			{Name: "Light", Value: "light"},
			{Name: "Dark", Value: "dark"},
		}),
	)
	assert.Equal(t, "theme", flag.ID)
	require.NotNil(t, flag.Values)
	assert.Len(t, *flag.Values, 2)
}

// --- Live runtime surface (external) ---

func TestFlagsClient_RuntimeAccessors(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	flags := client.Flags()

	assert.Equal(t, "disconnected", flags.ConnectionStatus())
	stats := flags.Stats()
	assert.Equal(t, 0, stats.CacheHits)
	assert.Equal(t, 0, stats.CacheMisses)

	// Listeners register without firing.
	flags.OnChange(func(*smplkit.FlagChangeEvent) {})
	flags.OnChangeKey("feature-x", func(*smplkit.FlagChangeEvent) {})
	flags.SetContextProvider(func(context.Context) []smplkit.Context { return nil })
}

func TestFlagsClient_TypedHandle_Default_Unreachable(t *testing.T) {
	// With an unreachable transport, init fails and Get returns the default.
	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithHTTPClient(&http.Client{Transport: &failTransport{}}),
	)
	require.NoError(t, err)
	flags := client.Flags()

	assert.Equal(t, false, flags.BooleanFlag("b", false).Get(context.Background()))
	assert.Equal(t, "light", flags.StringFlag("s", "light").Get(context.Background()))
	assert.Equal(t, 3.0, flags.NumberFlag("n", 3.0).Get(context.Background()))
	dflt := map[string]interface{}{"k": "v"}
	assert.Equal(t, dflt, flags.JsonFlag("j", dflt).Get(context.Background()))
}

func TestFlagsClient_Discovery_External(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)
	flags := client.Flags()

	assert.Equal(t, 0, flags.PendingCount())
	flags.RegisterFlag("dark-mode", "BOOLEAN", true)
	assert.Equal(t, 1, flags.PendingCount())
}

// --- Standalone NewFlagsClient (external) ---

func TestNewFlagsClient_Standalone_External(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleFlagListJSON("feature-x", "Feature X", "BOOLEAN")))
	}))
	defer server.Close()

	flags, err := smplkit.NewFlagsClient(
		smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "svc", DisableTelemetry: true},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)

	list, err := flags.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestNewFlagsClient_Standalone_ConfigError_External(t *testing.T) {
	_, err := smplkit.NewFlagsClient(smplkit.Config{Environment: "test"})
	assert.Error(t, err)
}

// --- Flag model tests ---

func TestFlag_AddRule_RequiresEnvironment(t *testing.T) {
	flag := &smplkit.Flag{
		ID: "test-flag",
	}

	rule := map[string]interface{}{
		"logic": map[string]interface{}{},
		"value": true,
		// Missing "environment" key.
	}

	err := flag.AddRule(rule)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "environment")
}

// --- Error classification tests ---

func TestFlagsClient_NetworkError(t *testing.T) {
	httpClient := &http.Client{
		Transport: &failTransport{},
	}

	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true}, smplkit.WithHTTPClient(httpClient))
	require.NoError(t, err)

	_, err = client.Flags().Get(context.Background(), "feature-x")
	assert.Error(t, err)

	var connErr *smplkit.ConnectionError
	assert.True(t, errors.As(err, &connErr))
}

type failTransport struct{}

func (t *failTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

// --- Factory method and Flag mutation tests ---

func TestNewBooleanFlag_Fields(t *testing.T) {
	client, err := smplkit.NewClient(smplkit.Config{APIKey: "sk_test_key", Environment: "test", Service: "test-service", DisableTelemetry: true})
	require.NoError(t, err)

	desc := "A feature flag"
	flag := client.Flags().NewBooleanFlag("feature-x", true,
		smplkit.WithFlagName("Feature X"),
		smplkit.WithFlagDescription(desc),
		smplkit.WithFlagValues([]smplkit.FlagValue{
			{Name: "True", Value: true},
			{Name: "False", Value: false},
		}),
	)
	assert.Equal(t, "feature-x", flag.ID)
	assert.Equal(t, "Feature X", flag.Name)
	assert.Equal(t, true, flag.Default)
	require.NotNil(t, flag.Values)
	assert.Len(t, *flag.Values, 2)
}

func TestFlag_MutateName(t *testing.T) {
	flag := &smplkit.Flag{
		ID:   "feature-x",
		Name: "Old Name",
	}
	flag.Name = "Updated Name"
	assert.Equal(t, "Updated Name", flag.Name)
}
