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
	headerEditor := func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	}
	genApp, _ := genapp.NewClient(appURL,
		genapp.WithHTTPClient(httpClient),
		genapp.WithRequestEditorFn(headerEditor),
	)
	return httpClient, genApp
}

// buildJobsGenClient constructs the generated jobs API client. No extra
// environment/service headers are injected — management callers authenticate
// via the API key only (set by authTransport on httpClient).
func buildJobsGenClient(optCfg clientConfig, rc *resolvedConfig, httpClient *http.Client) *genjobs.ClientWithResponses {
	jobsURL := serviceURL(optCfg, "jobs", rc)
	headerEditor := genjobs.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/vnd.api+json")
		req.Header.Set("User-Agent", userAgent)
		return nil
	})
	raw, _ := genjobs.NewClient(jobsURL, genjobs.WithHTTPClient(httpClient), headerEditor)
	return &genjobs.ClientWithResponses{ClientInterface: raw}
}
