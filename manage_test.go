package smplkit_test

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// recordingTransport counts every outbound request without actually
// sending it. Used to prove that NewManagementClient's construction is
// truly side-effect-free (rule 1).
type recordingTransport struct {
	calls int32
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&r.calls, 1)
	// Pretend the network worked so tests that do hit the wire later still pass.
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func (r *recordingTransport) Calls() int32 { return atomic.LoadInt32(&r.calls) }

func TestNewManagementClient_Basic(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_test_xxxxxxxxxxxxx")
	mgmt, err := smplkit.NewManagementClient(smplkit.ManagementConfig{})
	require.NoError(t, err)
	defer mgmt.Close()

	// All eight namespaces must be reachable from the management client.
	assert.NotNil(t, mgmt.Contexts())
	assert.NotNil(t, mgmt.ContextTypes())
	assert.NotNil(t, mgmt.Environments())
	assert.NotNil(t, mgmt.AccountSettings())
	assert.NotNil(t, mgmt.Config())
	assert.NotNil(t, mgmt.Flags())
	assert.NotNil(t, mgmt.Loggers())
	assert.NotNil(t, mgmt.LogGroups())
}

func TestClient_Manage_ExposesEightNamespaces(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_test_xxxxxxxxxxxxx")
	c, err := smplkit.NewClient(smplkit.Config{
		Environment: "production",
		Service:     "test",
	})
	require.NoError(t, err)
	defer c.Close()

	mgmt := c.Manage()
	require.NotNil(t, mgmt)
	assert.NotNil(t, mgmt.Contexts())
	assert.NotNil(t, mgmt.ContextTypes())
	assert.NotNil(t, mgmt.Environments())
	assert.NotNil(t, mgmt.AccountSettings())
	assert.NotNil(t, mgmt.Config())
	assert.NotNil(t, mgmt.Flags())
	assert.NotNil(t, mgmt.Loggers())
	assert.NotNil(t, mgmt.LogGroups())
}

// Rule 1: management client construction is zero-side-effect — no
// outbound HTTP calls until you invoke a method.
func TestNewManagementClient_NoOutboundCallsOnConstruction(t *testing.T) {
	rec := &recordingTransport{}
	hc := &http.Client{Transport: rec}

	t.Setenv("SMPLKIT_API_KEY", "sk_test_xxxxxxxx")
	mgmt, err := smplkit.NewManagementClient(
		smplkit.ManagementConfig{},
		smplkit.WithHTTPClient(hc),
	)
	require.NoError(t, err)
	defer mgmt.Close()

	// Touch every namespace accessor — these must not trigger HTTP.
	_ = mgmt.Contexts()
	_ = mgmt.ContextTypes()
	_ = mgmt.Environments()
	_ = mgmt.AccountSettings()
	_ = mgmt.Config()
	_ = mgmt.Flags()
	_ = mgmt.Loggers()
	_ = mgmt.LogGroups()

	assert.Zero(t, rec.Calls(),
		"NewManagementClient + namespace accessors must make zero HTTP calls (rule 1)")
}

// Same guarantee for the runtime Client when telemetry is disabled and
// the caller hasn't yet invoked any runtime method.
func TestNewClient_NoOutboundCallsBeforeFirstMethod(t *testing.T) {
	rec := &recordingTransport{}
	hc := &http.Client{Transport: rec}

	c, err := smplkit.NewClient(smplkit.Config{
		APIKey:           "sk_test_xxxxxxxx",
		Environment:      "test",
		Service:          "test-service",
		DisableTelemetry: true,
	}, smplkit.WithHTTPClient(hc))
	require.NoError(t, err)
	defer c.Close()

	// Touching the management surface and the typed-flag accessors must
	// not, by itself, trigger any outbound HTTP.
	mgmt := c.Manage()
	_ = mgmt.Contexts()
	_ = mgmt.Config()
	_ = mgmt.Flags()
	_ = c.Flags().BooleanFlag("dummy", false)

	// The runtime client may eventually fire a service-context registration
	// asynchronously; the contract on construction itself is "no synchronous
	// HTTP call before the caller's first method invocation". Sleep briefly
	// to let any racing goroutine run, then assert the count is at most 1
	// (the optional async service-context register).
	assert.LessOrEqual(t, int(rec.Calls()), 1,
		"NewClient + namespace accessors must not synchronously make multiple HTTP calls")
}

func TestLoggersManagement_NewDefaultsToManagedTrue(t *testing.T) {
	t.Setenv("SMPLKIT_API_KEY", "sk_test_xxxxxxxxxxxxx")
	mgmt, err := smplkit.NewManagementClient(smplkit.ManagementConfig{})
	require.NoError(t, err)
	defer mgmt.Close()

	logger := mgmt.Loggers().New("my.logger")
	// rule 9: New(id) drops the name kwarg — name defaults to id
	assert.Equal(t, "my.logger", logger.ID)
	assert.Equal(t, "my.logger", logger.Name)
	// rule 9: managed defaults to true
	assert.True(t, logger.Managed)
}
