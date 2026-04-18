package smplkit_test

import (
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
)

func TestParseJSONAPIErrors_FallbackToTitle(t *testing.T) {
	// When Detail is empty, message should fall back to Title.
	body := []byte(`{
		"errors": [{
			"status": "422",
			"title": "Validation Error"
		}]
	}`)

	err := smplkit.CheckStatusForTest(422, body)
	require.Error(t, err)

	var valErr *smplkit.SmplValidationError
	require.True(t, errors.As(err, &valErr))
	assert.Equal(t, "Validation Error", valErr.Message)
}

func TestParseJSONAPIErrors_FallbackToStatus(t *testing.T) {
	// When both Detail and Title are empty, message should fall back to Status.
	body := []byte(`{
		"errors": [{
			"status": "500"
		}]
	}`)

	err := smplkit.CheckStatusForTest(500, body)
	require.Error(t, err)

	var base *smplkit.SmplError
	require.True(t, errors.As(err, &base))
	assert.Equal(t, "500", base.Message)
}

func TestParseJSONAPIErrors_FallbackToDefault(t *testing.T) {
	// When Detail, Title, and Status are all empty, message should be the default.
	body := []byte(`{
		"errors": [{}]
	}`)

	err := smplkit.CheckStatusForTest(500, body)
	require.Error(t, err)

	var base *smplkit.SmplError
	require.True(t, errors.As(err, &base))
	assert.Equal(t, "An API error occurred", base.Message)
}

func TestClassifyError_URLErrorIncludesURL(t *testing.T) {
	urlErr := &url.Error{
		Op:  "Get",
		URL: "http://config.localhost/api/v1/configs",
		Err: fmt.Errorf("dial tcp: lookup config.localhost: no such host"),
	}
	err := smplkit.ClassifyErrorForTest(urlErr)
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr), "expected SmplConnectionError, got %T: %v", err, err)
	assert.Contains(t, connErr.Message, "http://config.localhost/api/v1/configs")
	assert.Contains(t, connErr.Message, "no such host")
}

func TestClassifyError_NonURLErrorFallback(t *testing.T) {
	plain := fmt.Errorf("some generic error")
	err := smplkit.ClassifyErrorForTest(plain)
	require.Error(t, err)

	var connErr *smplkit.SmplConnectionError
	require.True(t, errors.As(err, &connErr))
	assert.Contains(t, connErr.Message, "some generic error")
}

func TestParseJSONAPIErrors_PluralMoreErrors(t *testing.T) {
	// When there are 3+ errors, the suffix should use plural "errors".
	body := []byte(`{
		"errors": [
			{"detail": "First problem"},
			{"detail": "Second problem"},
			{"detail": "Third problem"}
		]
	}`)

	err := smplkit.CheckStatusForTest(400, body)
	require.Error(t, err)

	var valErr *smplkit.SmplValidationError
	require.True(t, errors.As(err, &valErr))
	assert.Contains(t, valErr.Message, "(and 2 more errors)")
	require.Len(t, valErr.Errors, 3)
}
