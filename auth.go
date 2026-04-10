package smplkit

import "net/http"

// authTransport is an http.RoundTripper that handles authentication.
type authTransport struct {
	token string
	base  http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid mutating the caller's request.
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}
