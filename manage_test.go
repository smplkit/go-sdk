package smplkit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
)

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
