package smplkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	rtdebug "runtime/debug"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSDKModulePathMatchesGoMod guards the sdkModulePath constant against
// drift: the User-Agent version lookup keys on the module path recorded in
// consumer build metadata, which is whatever go.mod declares.
func TestSDKModulePathMatchesGoMod(t *testing.T) {
	data, err := os.ReadFile("go.mod")
	require.NoError(t, err)
	modLine := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "module ") {
			modLine = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			break
		}
	}
	require.NotEmpty(t, modLine, "go.mod has no module directive")
	assert.Equal(t, sdkModulePath, modLine)
}

// TestSDKVersion covers every resolution shape: missing build info, the SDK
// as a dependency (plain, replaced by a module, replaced by a directory,
// devel-versioned), and the SDK as the main module (its own test binaries).
func TestSDKVersion(t *testing.T) {
	dep := func(m rtdebug.Module) *rtdebug.BuildInfo {
		return &rtdebug.BuildInfo{
			Main: rtdebug.Module{Path: "example.com/consumer", Version: "(devel)"},
			Deps: []*rtdebug.Module{
				{Path: "github.com/google/uuid", Version: "v1.6.0"},
				&m,
			},
		}
	}

	cases := []struct {
		name string
		bi   *rtdebug.BuildInfo
		ok   bool
		want string
	}{
		{"no build info", nil, false, "(devel)"},
		{"nil build info despite ok", nil, true, "(devel)"},
		{
			"consumer build: dep version",
			dep(rtdebug.Module{Path: sdkModulePath, Version: "v3.4.5"}),
			true,
			"v3.4.5",
		},
		{
			"consumer build: module replace wins",
			dep(rtdebug.Module{Path: sdkModulePath, Version: "v3.4.5",
				Replace: &rtdebug.Module{Path: "example.com/fork", Version: "v3.4.6"}}),
			true,
			"v3.4.6",
		},
		{
			"consumer build: directory replace has no version",
			dep(rtdebug.Module{Path: sdkModulePath, Version: "v3.4.5",
				Replace: &rtdebug.Module{Path: "../go-sdk"}}),
			true,
			"(devel)",
		},
		{
			"consumer build: devel-versioned dep",
			dep(rtdebug.Module{Path: sdkModulePath, Version: "(devel)"}),
			true,
			"(devel)",
		},
		{
			"main module with stamped version",
			&rtdebug.BuildInfo{Main: rtdebug.Module{Path: sdkModulePath, Version: "v3.9.9"}},
			true,
			"v3.9.9",
		},
		{
			"main module without version",
			&rtdebug.BuildInfo{Main: rtdebug.Module{Path: sdkModulePath, Version: "(devel)"}},
			true,
			"(devel)",
		},
		{
			"unrelated main module, SDK absent",
			&rtdebug.BuildInfo{Main: rtdebug.Module{Path: "example.com/other", Version: "v1.0.0"}},
			true,
			"(devel)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sdkVersion(tc.bi, tc.ok))
		})
	}
}

// TestUserAgent_Format pins the package-level User-Agent shape: the SDK
// identifier, a slash, and a non-empty version with no whitespace. In this
// repo's own test binaries the version resolves via the main-module branch.
func TestUserAgent_Format(t *testing.T) {
	assert.Regexp(t, regexp.MustCompile(`^smplkit-sdk-go/\S+$`), userAgent)
}

func TestSetDefaultUserAgent(t *testing.T) {
	h := http.Header{}
	setDefaultUserAgent(h)
	assert.Equal(t, userAgent, h.Get("User-Agent"))

	// A caller-supplied value is left alone — including one that arrived
	// under a non-canonical casing, as the extra-header merge produces.
	h = http.Header{}
	h.Set("user-agent", "my-app/1.0")
	setDefaultUserAgent(h)
	assert.Equal(t, "my-app/1.0", h.Get("User-Agent"))
}

func TestCallerUserAgent(t *testing.T) {
	assert.Equal(t, "", callerUserAgent(nil))
	assert.Equal(t, "", callerUserAgent(map[string]string{"X-Custom": "v"}))
	assert.Equal(t, "a/1", callerUserAgent(map[string]string{"User-Agent": "a/1"}))
	assert.Equal(t, "b/2", callerUserAgent(map[string]string{"user-agent": "b/2"}))
	assert.Equal(t, "c/3", callerUserAgent(map[string]string{"USER-AGENT": "c/3"}))
}

// uaCaptureServer records the User-Agent of every request by path.
func uaCaptureServer(t *testing.T) (*httptest.Server, func() map[string]string) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]string{} // request path -> User-Agent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get("User-Agent")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(listBody))
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]string, len(seen))
		for k, v := range seen {
			out[k] = v
		}
		return out
	}
}

// driveAllServices issues one list call against every backend service so
// each sub-client's header editors run.
func driveAllServices(t *testing.T, c *SmplClient) {
	t.Helper()
	ctx := context.Background()
	_, _ = c.Config().List(ctx)                                          // config service
	_, _ = c.Flags().List(ctx)                                           // flags service
	_, _ = c.Platform().Environments().List(ctx)                         // app service
	_, _ = c.Logging().Loggers().List(ctx)                               // logging service
	_, _ = c.Audit().ResourceTypes().List(ctx, ListResourceTypesInput{}) // audit service
	_, _ = c.Jobs().List(ctx, ListJobsInput{})                           // jobs service
}

// TestNewClient_DefaultUserAgentPerService proves the smplkit-sdk-go default
// User-Agent travels on requests to all six backend services when the caller
// supplies none.
func TestNewClient_DefaultUserAgentPerService(t *testing.T) {
	server, headers := uaCaptureServer(t)

	c, err := NewClient(
		Config{APIKey: "sk_test", Environment: "test", Service: "svc", DisableTelemetry: true},
		withBaseURLOverride(server.URL),
	)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	driveAllServices(t, c)

	seen := headers()
	require.NotEmpty(t, seen, "expected at least one recorded request")
	for path, ua := range seen {
		assert.Regexpf(t, `^smplkit-sdk-go/\S+$`, ua, "unexpected User-Agent on %s", path)
	}
}

// TestNewClient_CallerUserAgentWinsPerService proves a User-Agent supplied
// through the extra-headers surface — under any casing — replaces the SDK
// default on requests to all six backend services.
func TestNewClient_CallerUserAgentWinsPerService(t *testing.T) {
	server, headers := uaCaptureServer(t)

	c, err := NewClient(
		Config{
			APIKey:           "sk_test",
			Environment:      "test",
			Service:          "svc",
			DisableTelemetry: true,
			ExtraHeaders:     map[string]string{"uSeR-aGeNt": "corp-agent/7"},
		},
		withBaseURLOverride(server.URL),
	)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	driveAllServices(t, c)

	seen := headers()
	require.NotEmpty(t, seen, "expected at least one recorded request")
	for path, ua := range seen {
		assert.Equalf(t, "corp-agent/7", ua, "caller User-Agent lost on %s", path)
	}
}

// TestEnsureWS_CallerUserAgentWiring proves the caller-supplied User-Agent
// reaches the WebSocket handshake configuration on every construction shape
// that can own a socket (SmplClient plus the three standalone live clients).
// The suite-wide wsLaunch no-op keeps the sockets from dialing.
func TestEnsureWS_CallerUserAgentWiring(t *testing.T) {
	server, _ := uaCaptureServer(t)
	cfg := Config{
		APIKey:           "sk_test",
		Environment:      "test",
		Service:          "svc",
		DisableTelemetry: true,
		ExtraHeaders:     map[string]string{"user-agent": "corp-agent/7"},
	}

	t.Run("smplclient", func(t *testing.T) {
		c, err := NewClient(cfg, withBaseURLOverride(server.URL))
		require.NoError(t, err)
		defer func() { _ = c.Close() }()
		assert.Equal(t, "corp-agent/7", c.ensureWS().callerUA)
	})

	t.Run("config standalone", func(t *testing.T) {
		c, err := NewConfigClient(cfg, withBaseURLOverride(server.URL))
		require.NoError(t, err)
		assert.Equal(t, "corp-agent/7", c.ensureWS().callerUA)
	})

	t.Run("flags standalone", func(t *testing.T) {
		c, err := NewFlagsClient(cfg, withBaseURLOverride(server.URL))
		require.NoError(t, err)
		assert.Equal(t, "corp-agent/7", c.ensureWS().callerUA)
	})

	t.Run("logging standalone", func(t *testing.T) {
		c, err := NewLoggingClient(cfg, withBaseURLOverride(server.URL))
		require.NoError(t, err)
		assert.Equal(t, "corp-agent/7", c.ensureWS().callerUA)
	})
}
