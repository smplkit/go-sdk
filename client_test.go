package smplkit_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
)

func TestNewClient_Defaults(t *testing.T) {
	client := smplkit.NewClient("sk_test_key")
	require.NotNil(t, client)
	require.NotNil(t, client.Config())
}

func TestNewClient_WithBaseURL(t *testing.T) {
	client := smplkit.NewClient("sk_test_key", smplkit.WithBaseURL("https://custom.example.com"))
	require.NotNil(t, client)
}

func TestNewClient_WithTimeout(t *testing.T) {
	client := smplkit.NewClient("sk_test_key", smplkit.WithTimeout(5*time.Second))
	require.NotNil(t, client)
}

func TestNewClient_WithHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 10 * time.Second}
	client := smplkit.NewClient("sk_test_key", smplkit.WithHTTPClient(custom))
	require.NotNil(t, client)
}

func TestNewClient_MultipleOptions(t *testing.T) {
	client := smplkit.NewClient("sk_test_key",
		smplkit.WithBaseURL("https://custom.example.com"),
		smplkit.WithTimeout(10*time.Second),
	)
	require.NotNil(t, client)
}

func TestClient_ConfigReturnsSubClient(t *testing.T) {
	client := smplkit.NewClient("sk_test_key")
	cfg := client.Config()
	require.NotNil(t, cfg)
	// Calling Config() multiple times returns the same sub-client.
	assert.Same(t, cfg, client.Config())
}
