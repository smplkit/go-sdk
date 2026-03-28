package smplkit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

const userAgent = "smplkit-go-sdk/0.0.0"

// checkStatus maps HTTP error status codes to typed SDK errors.
func checkStatus(code int, body []byte) error {
	msg := string(body)
	switch code {
	case http.StatusNotFound:
		return &SmplNotFoundError{
			SmplError: SmplError{Message: msg, StatusCode: code, ResponseBody: msg},
		}
	case http.StatusConflict:
		return &SmplConflictError{
			SmplError: SmplError{Message: msg, StatusCode: code, ResponseBody: msg},
		}
	case http.StatusUnprocessableEntity:
		return &SmplValidationError{
			SmplError: SmplError{Message: msg, StatusCode: code, ResponseBody: msg},
		}
	}
	if code >= 400 {
		return &SmplError{Message: msg, StatusCode: code, ResponseBody: msg}
	}
	return nil
}

// classifyError converts standard library errors into typed SDK errors.
func classifyError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &SmplTimeoutError{
			SmplError: SmplError{Message: fmt.Sprintf("request timed out: %s", err)},
		}
	}
	if errors.Is(err, context.Canceled) {
		return &SmplTimeoutError{
			SmplError: SmplError{Message: fmt.Sprintf("request canceled: %s", err)},
		}
	}

	// Check for network-level timeout errors (e.g. http.Client.Timeout).
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &SmplTimeoutError{
			SmplError: SmplError{Message: fmt.Sprintf("request timed out: %s", err)},
		}
	}

	// All remaining errors are connection failures.
	return &SmplConnectionError{
		SmplError: SmplError{Message: fmt.Sprintf("connection error: %s", err)},
	}
}
