package smplkit

import (
	"context"
	"net/http"

	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
	genjobs "github.com/smplkit/go-sdk/v3/internal/generated/jobs"
)

// buildGenClients builds the auth-wrapped HTTP client and the generated app
// API client shared by the standalone platform, account, and jobs clients.
// It wires the auth + SDK-header transport once; the HTTP client is returned
// so the jobs client can hang its own generated client off the same pool.
func buildGenClients(optCfg clientConfig, rc *resolvedConfig) (*http.Client, genapp.ClientInterface) {
	httpClient := optCfg.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: optCfg.timeout}
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &authTransport{token: rc.apiKey, base: base}

	appURL := serviceURL(optCfg, "app", rc)
	// Extra headers first, then SDK headers (so the SDK Accept wins on a
	// collision; the SDK User-Agent is only a default the caller may override).
	extraHeaders := rc.extraHeaders
	extraEditor := func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	}
	headerEditor := func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		setDefaultUserAgent(req.Header)
		return nil
	}
	genApp, _ := genapp.NewClient(appURL,
		genapp.WithHTTPClient(httpClient),
		genapp.WithRequestEditorFn(extraEditor),
		genapp.WithRequestEditorFn(headerEditor),
	)
	return httpClient, genApp
}

// buildJobsGenClient constructs the generated jobs API client. No
// environment/service headers are injected — management callers authenticate
// via the API key only (set by authTransport on httpClient) — but caller
// ExtraHeaders are honored, added before SDK headers so the SDK Accept wins
// on a collision (the SDK User-Agent is only a default the caller may
// override).
func buildJobsGenClient(optCfg clientConfig, rc *resolvedConfig, httpClient *http.Client) *genjobs.ClientWithResponses {
	jobsURL := serviceURL(optCfg, "jobs", rc)
	extraHeaders := rc.extraHeaders
	extraEditor := genjobs.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return nil
	})
	headerEditor := genjobs.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		setDefaultUserAgent(req.Header)
		return nil
	})
	raw, _ := genjobs.NewClient(jobsURL, genjobs.WithHTTPClient(httpClient), extraEditor, headerEditor)
	return &genjobs.ClientWithResponses{ClientInterface: raw}
}
