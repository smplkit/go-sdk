package smplkit_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk"
)

func TestSmplError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "base error without status",
			err:      &smplkit.SmplError{Message: "something failed"},
			expected: "something failed",
		},
		{
			name:     "base error with status",
			err:      &smplkit.SmplError{Message: "not found", StatusCode: 404},
			expected: "not found (status 404)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestErrorTypes_ImplementError(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"SmplError", &smplkit.SmplError{Message: "base"}},
		{"SmplConnectionError", &smplkit.SmplConnectionError{}},
		{"SmplTimeoutError", &smplkit.SmplTimeoutError{}},
		{"SmplNotFoundError", &smplkit.SmplNotFoundError{}},
		{"SmplConflictError", &smplkit.SmplConflictError{}},
		{"SmplNotConnectedError", &smplkit.SmplNotConnectedError{}},
		{"SmplValidationError", &smplkit.SmplValidationError{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Implements(t, (*error)(nil), tt.err)
		})
	}
}

func TestErrorsAs_BaseError(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"connection", &smplkit.SmplConnectionError{smplkit.SmplError{Message: "conn"}}},
		{"timeout", &smplkit.SmplTimeoutError{smplkit.SmplError{Message: "timeout"}}},
		{"not found", &smplkit.SmplNotFoundError{smplkit.SmplError{Message: "404"}}},
		{"conflict", &smplkit.SmplConflictError{smplkit.SmplError{Message: "409"}}},
		{"validation", &smplkit.SmplValidationError{smplkit.SmplError{Message: "422"}}},
		{"not connected", &smplkit.SmplNotConnectedError{smplkit.SmplError{Message: "not connected"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var base *smplkit.SmplError
			require.True(t, errors.As(tt.err, &base), "errors.As should match SmplError")
		})
	}
}

func TestErrorsAs_SpecificTypes(t *testing.T) {
	err := &smplkit.SmplNotFoundError{
		smplkit.SmplError{Message: "not found", StatusCode: 404, ResponseBody: `{"error":"not found"}`},
	}

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound))
	assert.Equal(t, 404, notFound.StatusCode)
	assert.Equal(t, `{"error":"not found"}`, notFound.ResponseBody)

	// Should not match other specific types.
	var conflict *smplkit.SmplConflictError
	assert.False(t, errors.As(err, &conflict))
}

func TestSubtypeErrors_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "connection error",
			err:      &smplkit.SmplConnectionError{SmplError: smplkit.SmplError{Message: "conn failed"}},
			expected: "conn failed",
		},
		{
			name:     "timeout error",
			err:      &smplkit.SmplTimeoutError{SmplError: smplkit.SmplError{Message: "timed out"}},
			expected: "timed out",
		},
		{
			name:     "not found error",
			err:      &smplkit.SmplNotFoundError{SmplError: smplkit.SmplError{Message: "missing", StatusCode: 404}},
			expected: "missing (status 404)",
		},
		{
			name:     "conflict error",
			err:      &smplkit.SmplConflictError{SmplError: smplkit.SmplError{Message: "conflict", StatusCode: 409}},
			expected: "conflict (status 409)",
		},
		{
			name:     "validation error",
			err:      &smplkit.SmplValidationError{SmplError: smplkit.SmplError{Message: "invalid", StatusCode: 422}},
			expected: "invalid (status 422)",
		},
		{
			name:     "not connected error",
			err:      &smplkit.SmplNotConnectedError{SmplError: smplkit.SmplError{Message: "not connected"}},
			expected: "not connected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestErrorUnwrap(t *testing.T) {
	inner := smplkit.SmplError{Message: "inner", StatusCode: 500}
	err := &smplkit.SmplConnectionError{SmplError: inner}

	unwrapped := errors.Unwrap(err)
	require.NotNil(t, unwrapped)

	var base *smplkit.SmplError
	require.True(t, errors.As(unwrapped, &base))
	assert.Equal(t, "inner (status 500)", base.Error())
}

func TestSmplNotConnectedError_ErrorAndUnwrap(t *testing.T) {
	inner := smplkit.SmplError{Message: "not connected", StatusCode: 0}
	err := &smplkit.SmplNotConnectedError{SmplError: inner}

	assert.Equal(t, "not connected", err.Error())

	unwrapped := errors.Unwrap(err)
	require.NotNil(t, unwrapped)

	var base *smplkit.SmplError
	require.True(t, errors.As(unwrapped, &base))
	assert.Equal(t, "not connected", base.Error())
}

func TestCheckStatus_SingleError400(t *testing.T) {
	body := []byte(`{
		"errors": [{
			"status": "400",
			"title": "Validation Error",
			"detail": "The 'name' field is required.",
			"source": {"pointer": "/data/attributes/name"}
		}]
	}`)

	err := smplkit.CheckStatusForTest(400, body)
	require.Error(t, err)

	// Should be SmplValidationError.
	var valErr *smplkit.SmplValidationError
	require.True(t, errors.As(err, &valErr), "expected SmplValidationError")

	// Message derived from first error's Detail.
	assert.Contains(t, valErr.Message, "The 'name' field is required.")

	// Errors slice has 1 element.
	require.Len(t, valErr.Errors, 1)
	assert.Equal(t, "400", valErr.Errors[0].Status)
	assert.Equal(t, "Validation Error", valErr.Errors[0].Title)
	assert.Equal(t, "The 'name' field is required.", valErr.Errors[0].Detail)
	assert.Equal(t, "/data/attributes/name", valErr.Errors[0].Source.Pointer)

	// StatusCode is 400.
	assert.Equal(t, 400, valErr.StatusCode)

	// String representation includes JSON.
	errStr := err.Error()
	assert.Contains(t, errStr, `"status":"400"`)
	assert.Contains(t, errStr, `"pointer":"/data/attributes/name"`)
}

func TestCheckStatus_MultiError400(t *testing.T) {
	body := []byte(`{
		"errors": [
			{
				"status": "400",
				"title": "Validation Error",
				"detail": "The 'name' field is required.",
				"source": {"pointer": "/data/attributes/name"}
			},
			{
				"status": "400",
				"title": "Validation Error",
				"detail": "The 'id' field is required.",
				"source": {"pointer": "/data/id"}
			}
		]
	}`)

	err := smplkit.CheckStatusForTest(400, body)
	require.Error(t, err)

	var valErr *smplkit.SmplValidationError
	require.True(t, errors.As(err, &valErr))

	// Message has "(and 1 more error)".
	assert.Contains(t, valErr.Message, "(and 1 more error)")

	// Errors slice has 2 elements.
	require.Len(t, valErr.Errors, 2)
	assert.Equal(t, "The 'name' field is required.", valErr.Errors[0].Detail)
	assert.Equal(t, "The 'id' field is required.", valErr.Errors[1].Detail)

	// String representation includes both errors.
	errStr := err.Error()
	assert.Contains(t, errStr, "[0]")
	assert.Contains(t, errStr, "[1]")
}

func TestCheckStatus_404Response(t *testing.T) {
	body := []byte(`{
		"errors": [{
			"status": "404",
			"title": "Not Found",
			"detail": "Config with key 'nonexistent' not found."
		}]
	}`)

	err := smplkit.CheckStatusForTest(404, body)
	require.Error(t, err)

	var notFound *smplkit.SmplNotFoundError
	require.True(t, errors.As(err, &notFound), "expected SmplNotFoundError")
	assert.Contains(t, notFound.Message, "Config with key 'nonexistent' not found.")
}

func TestCheckStatus_409Response(t *testing.T) {
	body := []byte(`{
		"errors": [{
			"status": "409",
			"title": "Conflict",
			"detail": "A config with this key already exists."
		}]
	}`)

	err := smplkit.CheckStatusForTest(409, body)
	require.Error(t, err)

	var conflict *smplkit.SmplConflictError
	require.True(t, errors.As(err, &conflict), "expected SmplConflictError")
	assert.Contains(t, conflict.Message, "A config with this key already exists.")
}

func TestCheckStatus_NonJSON502(t *testing.T) {
	body := []byte(`<html>Bad Gateway</html>`)

	err := smplkit.CheckStatusForTest(502, body)
	require.Error(t, err)

	var base *smplkit.SmplError
	require.True(t, errors.As(err, &base))

	// Falls back to HTTP status code message.
	assert.Contains(t, base.Message, "HTTP 502")

	// Errors slice is empty (non-JSON body).
	assert.Empty(t, base.Errors)
}
