package smplkit

import (
	"net/http"
	rtdebug "runtime/debug"
)

// sdkModulePath mirrors the module directive in go.mod. It is the key used
// to locate this SDK in a consumer binary's build metadata. A guard test
// keeps the constant and go.mod in sync.
const sdkModulePath = "github.com/smplkit/go-sdk/v3"

// devVersionMarker is the User-Agent version used when no release version
// can be resolved from build metadata — this module's own test binaries, a
// consumer using a directory replace, or a binary built without module
// support.
const devVersionMarker = "(devel)"

// sdkVersion resolves this SDK's version from the binary's build metadata.
//
// In a consumer build the SDK appears in the dependency list, and the
// version recorded there (honoring any replace directive) is the published
// module version, which is derived from the release git tag. When this
// module is itself the main module — its own test binaries — there is no
// dependency entry and the main-module version is used when one is stamped.
// Every unresolvable shape falls back to devVersionMarker.
func sdkVersion(bi *rtdebug.BuildInfo, ok bool) string {
	if !ok || bi == nil {
		return devVersionMarker
	}
	for _, dep := range bi.Deps {
		if dep.Path != sdkModulePath {
			continue
		}
		m := dep
		if m.Replace != nil {
			// A replace directive is what actually got built. A module
			// replace carries its own version; a directory replace has none.
			m = m.Replace
		}
		if m.Version != "" && m.Version != devVersionMarker {
			return m.Version
		}
		return devVersionMarker
	}
	if bi.Main.Path == sdkModulePath && bi.Main.Version != "" && bi.Main.Version != devVersionMarker {
		return bi.Main.Version
	}
	return devVersionMarker
}

// userAgent is the SDK-identifying User-Agent sent by default on every
// outbound request (HTTP and WebSocket handshake). Resolved exactly once at
// package initialization; the version comes from build metadata, never from
// a hand-maintained constant.
var userAgent = func() string {
	bi, ok := rtdebug.ReadBuildInfo()
	return "smplkit-sdk-go/" + sdkVersion(bi, ok)
}()

// setDefaultUserAgent stamps the SDK's default User-Agent on h unless the
// caller already supplied one. Caller-supplied values arrive through
// Config.ExtraHeaders, whose merge uses http.Header.Set — that canonicalizes
// the key, so a caller-provided "user-agent" or "USER-AGENT" is visible to
// the Get check here regardless of casing and wins over the default.
func setDefaultUserAgent(h http.Header) {
	if h.Get("User-Agent") == "" {
		h.Set("User-Agent", userAgent)
	}
}

// callerUserAgent returns the User-Agent the caller supplied through the
// extra-headers surface, matching the key case-insensitively, or "" when
// none was supplied. Used where headers are not merged through http.Header
// (the WebSocket handshake), so the caller's value can win there too.
func callerUserAgent(extraHeaders map[string]string) string {
	for k, v := range extraHeaders {
		if http.CanonicalHeaderKey(k) == "User-Agent" {
			return v
		}
	}
	return ""
}
