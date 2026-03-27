package smplkit

import "fmt"

// SmplError is the base error type for all smplkit SDK errors.
// All specific error types embed SmplError, so errors.As(err, &SmplError{})
// will match any SDK error.
type SmplError struct {
	Message      string
	StatusCode   int
	ResponseBody string
}

// Error implements the error interface.
func (e *SmplError) Error() string {
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

// SmplValidationError is raised when the server rejects a request due to validation errors (HTTP 422).
type SmplValidationError struct {
	SmplError
}

// Error implements the error interface.
func (e *SmplValidationError) Error() string { return e.SmplError.Error() }

// Unwrap returns the embedded SmplError for errors.Is/errors.As support.
func (e *SmplValidationError) Unwrap() error { return &e.SmplError }
