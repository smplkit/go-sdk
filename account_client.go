package smplkit

// The Smpl Account client — account-level settings on client.Account().
//
// AccountClient exposes the authenticated account's own configuration,
// mirroring the product UI's Account area:
//
//   - Account().Settings() — get/save the account settings object
//
// The client supports two construction shapes:
//
//   - Wired into SmplClient — built from the app base URL and api key the
//     top-level client has already resolved. This is the common path.
//   - Standalone — NewAccountClient(...) resolves the app base URL itself.
//     There are no pooled transports to tear down, so Close is a no-op kept
//     for interface symmetry.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	genapp "github.com/smplkit/go-sdk/v3/internal/generated/app"
)

// AccountClient is the Smpl Account client.
//
// Exposes the authenticated account's own configuration, reachable as
// client.Account() (SmplClient) or constructed directly:
//
//	account, err := smplkit.NewAccountClient(smplkit.Config{APIKey: "sk_..."})
//	settings, err := account.Settings().Get(ctx)
//	settings.SetEnvironmentOrder([]string{"production", "staging"})
//	settings.Save(ctx)
//
// Sub-client: Settings (get/save). Pure CRUD — no Install required.
type AccountClient struct {
	settings *SettingsClient
}

// newAccountClient wires an AccountClient onto a pre-built app transport (the
// wired path used by SmplClient).
func newAccountClient(appClient genapp.ClientInterface) *AccountClient {
	return &AccountClient{settings: &SettingsClient{appClient: appClient}}
}

// NewAccountClient creates a standalone Smpl Account client that resolves and
// owns its own app transport.
func NewAccountClient(cfg Config, opts ...ClientOption) (*AccountClient, error) {
	rc, err := resolveStandaloneConfig(cfg)
	if err != nil {
		return nil, err
	}
	optCfg := defaultConfig()
	for _, opt := range opts {
		opt(&optCfg)
	}
	_, genApp := buildGenClients(optCfg, rc)
	return newAccountClient(genApp), nil
}

// Settings returns the sub-client for account settings get/save.
func (a *AccountClient) Settings() *SettingsClient { return a.settings }

// Close is a no-op — the underlying http.Client is pooled by net/http. Kept
// for interface symmetry and a clean defer point.
func (a *AccountClient) Close() error { return nil }

// SettingsClient provides get/save for account-level settings
// (client.Account().Settings()).
type SettingsClient struct {
	appClient genapp.ClientInterface
}

// Get retrieves the current account settings.
func (m *SettingsClient) Get(ctx context.Context) (*AccountSettings, error) {
	resp, err := m.appClient.GetAccountSettings(ctx)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse account settings: %w", err)
	}
	return &AccountSettings{Raw: raw, client: m}, nil
}

// save writes the settings back to the server and updates s in place.
func (m *SettingsClient) save(ctx context.Context, s *AccountSettings) error {
	// The spec now declares an explicit request body for put_account_settings,
	// so the generator only emits the typed/-WithBody variants. Hand the raw
	// map directly to the typed call — the generated client marshals it as
	// application/vnd.api+json under the hood.
	resp, err := m.appClient.PutAccountSettingsWithApplicationVndAPIPlusJSONBody(ctx, s.Raw)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("smplkit: failed to parse account settings response: %w", err)
	}
	s.apply(&AccountSettings{Raw: raw})
	return nil
}
