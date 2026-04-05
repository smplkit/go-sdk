package smplkit

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ErrorSource identifies the source of a JSON:API error.
type ErrorSource struct {
	Pointer string `json:"pointer,omitempty"`
}

// ErrorDetail holds a single JSON:API error object.
type ErrorDetail struct {
	Status string      `json:"status,omitempty"`
	Title  string      `json:"title,omitempty"`
	Detail string      `json:"detail,omitempty"`
	Source ErrorSource `json:"source,omitempty"`
}

// SmplError is the base error type for all smplkit SDK errors.
// All specific error types embed SmplError, so errors.As(err, &SmplError{})
// will match any SDK error.
type SmplError struct {
	Message      string
	StatusCode   int
	ResponseBody string
	Errors       []ErrorDetail
}

// Error implements the error interface.
// When Errors contains parsed JSON:API details, the output includes each error
// serialized as JSON for debugging.
func (e *SmplError) Error() string {
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

// SmplConnectionError is raised when a network request fails.
type SmplConnectionError struct {
	SmplError
}

// Error implements the error interface.
func (e *SmplConnectionError) Error() string { return e.SmplError.Error() }

// Unwrap returns the embedded SmplError for errors.Is/errors.As support.
func (e *SmplConnectionError) Unwrap() error { return &e.SmplError }

// SmplTimeoutError is raised when an operation exceeds its timeout.
type SmplTimeoutError struct {
	SmplError
}

// Error implements the error interface.
func (e *SmplTimeoutError) Error() string { return e.SmplError.Error() }

// Unwrap returns the embedded SmplError for errors.Is/errors.As support.
func (e *SmplTimeoutError) Unwrap() error { return &e.SmplError }

// SmplNotFoundError is raised when a requested resource does not exist (HTTP 404).
type SmplNotFoundError struct {
	SmplError
}

// Error implements the error interface.
func (e *SmplNotFoundError) Error() string { return e.SmplError.Error() }

// Unwrap returns the embedded SmplError for errors.Is/errors.As support.
func (e *SmplNotFoundError) Unwrap() error { return &e.SmplError }

// SmplConflictError is raised when an operation conflicts with current state (HTTP 409).
type SmplConflictError struct {
	SmplError
}

// Error implements the error interface.
func (e *SmplConflictError) Error() string { return e.SmplError.Error() }

// Unwrap returns the embedded SmplError for errors.Is/errors.As support.
func (e *SmplConflictError) Unwrap() error { return &e.SmplError }

// SmplNotConnectedError is raised when a method requiring Connect() is called
// before the client is connected.
type SmplNotConnectedError struct {
	SmplError
}

// Error implements the error interface.
func (e *SmplNotConnectedError) Error() string { return e.SmplError.Error() }

// Unwrap returns the embedded SmplError for errors.Is/errors.As support.
func (e *SmplNotConnectedError) Unwrap() error { return &e.SmplError }

// ErrNotConnected is a convenience sentinel for SmplNotConnectedError checks.
var ErrNotConnected = &SmplNotConnectedError{SmplError{Message: "SmplClient is not connected. Call client.Connect() first."}}

// SmplValidationError is raised when the server rejects a request due to validation errors (HTTP 422).
type SmplValidationError struct {
	SmplError
}

// Error implements the error interface.
func (e *SmplValidationError) Error() string { return e.SmplError.Error() }

// Unwrap returns the embedded SmplError for errors.Is/errors.As support.
func (e *SmplValidationError) Unwrap() error { return &e.SmplError }
