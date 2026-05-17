package smplkit

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ErrorSource identifies the source of a JSON:API error.
type ErrorSource struct {
	// Pointer is the JSON Pointer to the offending member in the
	// request body (e.g. "/data/attributes/name").
	Pointer string `json:"pointer,omitempty"`
}

// ApiErrorDetail holds a single JSON:API error object as surfaced by the
// smplkit platform's JSON:API responses. Mirrors Python's ApiErrorDetail.
type ApiErrorDetail struct {
	// Status is the HTTP status code as a string (e.g. "404").
	Status string `json:"status,omitempty"`
	// Title is a short, human-readable summary (e.g. "Not Found").
	Title string `json:"title,omitempty"`
	// Detail is a longer, human-readable explanation specific to this
	// occurrence.
	Detail string `json:"detail,omitempty"`
	// Source identifies the source of the error within the request.
	Source ErrorSource `json:"source,omitempty"`
}

// ErrorDetail is an alias for ApiErrorDetail kept for backward
// compatibility. New code should use ApiErrorDetail.
type ErrorDetail = ApiErrorDetail

// Error is the base error type for all smplkit SDK errors. The flat
// hierarchy mirrors the Python SDK: ConnectionError, TimeoutError,
// NotFoundError, ConflictError, and ValidationError are direct subtypes.
//
// To match any SDK error use errors.As against *Error:
//
//	var e *smplkit.Error
//	if errors.As(err, &e) { ... }
type Error struct {
	// Message is a human-readable summary of the error.
	Message string
	// StatusCode holds the HTTP status code if the error came from the API.
	StatusCode int
	// ResponseBody holds the raw response body if available.
	ResponseBody string
	// Errors holds parsed JSON:API error details when available.
	Errors []ApiErrorDetail
}

// Error implements the error interface.
func (e *Error) Error() string {
	if len(e.Errors) > 0 {
		var b strings.Builder
		b.WriteString(e.Message)
		if len(e.Errors) == 1 {
			b.WriteString("\nError: ")
			data, _ := json.Marshal(e.Errors[0])
			b.Write(data)
		} else {
			b.WriteString("\nErrors:")
			for i, ed := range e.Errors {
				data, _ := json.Marshal(ed)
				b.WriteString(fmt.Sprintf("\n  [%d] %s", i, string(data)))
			}
		}
		return b.String()
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s (status %d)", e.Message, e.StatusCode)
	}
	return e.Message
}

// ConnectionError is raised when a network request fails.
type ConnectionError struct {
	// Base is the embedded base Error carrying the HTTP status, message,
	// raw response body, and any parsed JSON:API error details.
	Base Error
}

// Error implements the error interface.
func (e *ConnectionError) Error() string { return e.Base.Error() }

// Unwrap returns the embedded Error so errors.Is/errors.As walk the chain.
func (e *ConnectionError) Unwrap() error { return &e.Base }

// TimeoutError is raised when an operation exceeds its timeout.
type TimeoutError struct {
	// Base is the embedded base Error carrying the HTTP status, message,
	// raw response body, and any parsed JSON:API error details.
	Base Error
}

// Error implements the error interface.
func (e *TimeoutError) Error() string { return e.Base.Error() }

// Unwrap returns the embedded Error so errors.Is/errors.As walk the chain.
func (e *TimeoutError) Unwrap() error { return &e.Base }

// NotFoundError is raised when a requested resource does not exist.
type NotFoundError struct {
	// Base is the embedded base Error carrying the HTTP status, message,
	// raw response body, and any parsed JSON:API error details.
	Base Error
}

// Error implements the error interface.
func (e *NotFoundError) Error() string { return e.Base.Error() }

// Unwrap returns the embedded Error so errors.Is/errors.As walk the chain.
func (e *NotFoundError) Unwrap() error { return &e.Base }

// ConflictError is raised when an operation conflicts with current resource state.
type ConflictError struct {
	// Base is the embedded base Error carrying the HTTP status, message,
	// raw response body, and any parsed JSON:API error details.
	Base Error
}

// Error implements the error interface.
func (e *ConflictError) Error() string { return e.Base.Error() }

// Unwrap returns the embedded Error so errors.Is/errors.As walk the chain.
func (e *ConflictError) Unwrap() error { return &e.Base }

// ValidationError is raised when the server rejects a request due to validation errors.
type ValidationError struct {
	// Base is the embedded base Error carrying the HTTP status, message,
	// raw response body, and any parsed JSON:API error details.
	Base Error
}

// Error implements the error interface.
func (e *ValidationError) Error() string { return e.Base.Error() }

// Unwrap returns the embedded Error so errors.Is/errors.As walk the chain.
func (e *ValidationError) Unwrap() error { return &e.Base }

// ─── Backward-compatibility aliases ──────────────────────────────────────────
//
// The original error types had a SmplXxxError name to match the Python
// SmplError convention. The cross-SDK overhaul drops the prefix, but we
// keep the old names as deprecated aliases so customer code that imported
// the old names continues to compile while they migrate.

// SmplError is a deprecated alias for Error.
//
// Deprecated: Use Error.
type SmplError = Error

// SmplConnectionError is a deprecated alias for ConnectionError.
//
// Deprecated: Use ConnectionError.
type SmplConnectionError = ConnectionError

// SmplTimeoutError is a deprecated alias for TimeoutError.
//
// Deprecated: Use TimeoutError.
type SmplTimeoutError = TimeoutError

// SmplNotFoundError is a deprecated alias for NotFoundError.
//
// Deprecated: Use NotFoundError.
type SmplNotFoundError = NotFoundError

// SmplConflictError is a deprecated alias for ConflictError.
//
// Deprecated: Use ConflictError.
type SmplConflictError = ConflictError

// SmplValidationError is a deprecated alias for ValidationError.
//
// Deprecated: Use ValidationError.
type SmplValidationError = ValidationError
