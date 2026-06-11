package smplkit_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk/v3"
)

func TestSmplError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "base error without status",
			err:      &smplkit.Error{Message: "something failed"},
			expected: "something failed",
		},
		{
			name:     "base error with status",
			err:      &smplkit.Error{Message: "not found", StatusCode: 404},
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
		{"Error", &smplkit.Error{Message: "base"}},
		{"ConnectionError", &smplkit.ConnectionError{}},
		{"TimeoutError", &smplkit.TimeoutError{}},
		{"NotFoundError", &smplkit.NotFoundError{}},
		{"ConflictError", &smplkit.ConflictError{}},
		{"ValidationError", &smplkit.ValidationError{}},
		{"NotInstalledError", &smplkit.NotInstalledError{}},
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
		{"connection", &smplkit.ConnectionError{smplkit.Error{Message: "conn"}}},
		{"timeout", &smplkit.TimeoutError{smplkit.Error{Message: "timeout"}}},
		{"not found", &smplkit.NotFoundError{smplkit.Error{Message: "404"}}},
		{"conflict", &smplkit.ConflictError{smplkit.Error{Message: "409"}}},
		{"validation", &smplkit.ValidationError{smplkit.Error{Message: "422"}}},
		{"not installed", &smplkit.NotInstalledError{smplkit.Error{Message: "not installed"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var base *smplkit.Error
			require.True(t, errors.As(tt.err, &base), "errors.As should match Error")
		})
	}
}

func TestErrorsAs_SpecificTypes(t *testing.T) {
	err := &smplkit.NotFoundError{
		smplkit.Error{Message: "not found", StatusCode: 404, ResponseBody: `{"error":"not found"}`},
	}

	var notFound *smplkit.NotFoundError
	require.True(t, errors.As(err, &notFound))
	assert.Equal(t, 404, notFound.Base.StatusCode)
	assert.Equal(t, `{"error":"not found"}`, notFound.Base.ResponseBody)

	// Should not match other specific types.
	var conflict *smplkit.ConflictError
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
			err:      &smplkit.ConnectionError{Base: smplkit.Error{Message: "conn failed"}},
			expected: "conn failed",
		},
		{
			name:     "timeout error",
			err:      &smplkit.TimeoutError{Base: smplkit.Error{Message: "timed out"}},
			expected: "timed out",
		},
		{
			name:     "not found error",
			err:      &smplkit.NotFoundError{Base: smplkit.Error{Message: "missing", StatusCode: 404}},
			expected: "missing (status 404)",
		},
		{
			name:     "conflict error",
			err:      &smplkit.ConflictError{Base: smplkit.Error{Message: "conflict", StatusCode: 409}},
			expected: "conflict (status 409)",
		},
		{
			name:     "validation error",
			err:      &smplkit.ValidationError{Base: smplkit.Error{Message: "invalid", StatusCode: 422}},
			expected: "invalid (status 422)",
		},
		{
			name:     "not installed error",
			err:      &smplkit.NotInstalledError{Base: smplkit.Error{Message: "logging not installed"}},
			expected: "logging not installed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestErrorUnwrap(t *testing.T) {
	inner := smplkit.Error{Message: "inner", StatusCode: 500}
	err := &smplkit.ConnectionError{Base: inner}

	unwrapped := errors.Unwrap(err)
	require.NotNil(t, unwrapped)

	var base *smplkit.Error
	require.True(t, errors.As(unwrapped, &base))
	assert.Equal(t, "inner (status 500)", base.Error())
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

	// Should be ValidationError.
	var valErr *smplkit.ValidationError
	require.True(t, errors.As(err, &valErr), "expected ValidationError")

	// Message derived from first error's Detail.
	assert.Contains(t, valErr.Base.Message, "The 'name' field is required.")

	// Errors slice has 1 element.
	require.Len(t, valErr.Base.Errors, 1)
	assert.Equal(t, "400", valErr.Base.Errors[0].Status)
	assert.Equal(t, "Validation Error", valErr.Base.Errors[0].Title)
	assert.Equal(t, "The 'name' field is required.", valErr.Base.Errors[0].Detail)
	assert.Equal(t, "/data/attributes/name", valErr.Base.Errors[0].Source.Pointer)

	// StatusCode is 400.
	assert.Equal(t, 400, valErr.Base.StatusCode)

	// String representation includes JSON.
	errStr := err.Error()
	assert.Contains(t, errStr, `"status":"400"`)
	assert.Contains(t, errStr, `"pointer":"/data/attributes/name"`)
}

func TestCheckStatus_ExtractsCodeAndMeta(t *testing.T) {
	// Regression: parseJSONAPIErrors was dropping the JSON:API ``code``
	// and ``meta`` fields, so customers couldn't branch on
	// machine-readable codes like ``environment_unmanaged`` without
	// string-matching the human Detail.
	body := []byte(`{
		"errors": [{
			"status": "400",
			"code": "environment_unmanaged",
			"title": "Environment is unmanaged",
			"detail": "Promote it first.",
			"meta": {"environment": "staging", "count": 2, "is_default": false}
		}]
	}`)

	err := smplkit.CheckStatusForTest(400, body)
	require.Error(t, err)

	var valErr *smplkit.ValidationError
	require.True(t, errors.As(err, &valErr))
	require.Len(t, valErr.Base.Errors, 1)
	assert.Equal(t, "environment_unmanaged", valErr.Base.Errors[0].Code)
	assert.Equal(t, "staging", valErr.Base.Errors[0].Meta["environment"])
	// JSON numbers decode as float64 in Go by default.
	assert.Equal(t, float64(2), valErr.Base.Errors[0].Meta["count"])
	assert.Equal(t, false, valErr.Base.Errors[0].Meta["is_default"])

	// Error() now includes the code in the JSON dump.
	assert.Contains(t, err.Error(), `"code":"environment_unmanaged"`)
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

	var valErr *smplkit.ValidationError
	require.True(t, errors.As(err, &valErr))

	// Message has "(and 1 more error)".
	assert.Contains(t, valErr.Base.Message, "(and 1 more error)")

	// Errors slice has 2 elements.
	require.Len(t, valErr.Base.Errors, 2)
	assert.Equal(t, "The 'name' field is required.", valErr.Base.Errors[0].Detail)
	assert.Equal(t, "The 'id' field is required.", valErr.Base.Errors[1].Detail)

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

	var notFound *smplkit.NotFoundError
	require.True(t, errors.As(err, &notFound), "expected NotFoundError")
	assert.Contains(t, notFound.Base.Message, "Config with key 'nonexistent' not found.")
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

	var conflict *smplkit.ConflictError
	require.True(t, errors.As(err, &conflict), "expected ConflictError")
	assert.Contains(t, conflict.Base.Message, "A config with this key already exists.")
}

func TestCheckStatus_402Response(t *testing.T) {
	body := []byte(`{
		"errors": [{
			"status": "402",
			"title": "Payment Required",
			"detail": "This feature requires the Pro plan."
		}]
	}`)

	err := smplkit.CheckStatusForTest(402, body)
	require.Error(t, err)

	var pre *smplkit.PaymentRequiredError
	require.True(t, errors.As(err, &pre), "expected PaymentRequiredError")
	assert.Contains(t, pre.Base.Message, "This feature requires the Pro plan.")
	// Unwrap walks to the embedded base Error.
	var base *smplkit.Error
	require.True(t, errors.As(err, &base))
	if pre.Error() == "" {
		t.Fatal("Error() returned empty string")
	}
	if pre.Unwrap() == nil {
		t.Fatal("Unwrap() returned nil")
	}
}

func TestCheckStatus_NonJSON502(t *testing.T) {
	body := []byte(`<html>Bad Gateway</html>`)

	err := smplkit.CheckStatusForTest(502, body)
	require.Error(t, err)

	var base *smplkit.Error
	require.True(t, errors.As(err, &base))

	// Falls back to HTTP status code message.
	assert.Contains(t, base.Message, "HTTP 502")

	// Errors slice is empty (non-JSON body).
	assert.Empty(t, base.Errors)
}
