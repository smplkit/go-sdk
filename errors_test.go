package smplkit_test

import (
	"errors"
	"testing"

	"github.com/smplkit/go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestErrorUnwrap(t *testing.T) {
	inner := smplkit.SmplError{Message: "inner", StatusCode: 500}
	err := &smplkit.SmplConnectionError{SmplError: inner}

	unwrapped := errors.Unwrap(err)
	require.NotNil(t, unwrapped)

	var base *smplkit.SmplError
	require.True(t, errors.As(unwrapped, &base))
	assert.Equal(t, "inner (status 500)", base.Error())
}
