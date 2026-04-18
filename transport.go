package smplkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
)

const userAgent = "smplkit-go-sdk/0.0.0"

// parseJSONAPIErrors extracts error details from a response body.
func parseJSONAPIErrors(body []byte) ([]ErrorDetail, string) {
	var envelope struct {
		Errors []ErrorDetail `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Errors) == 0 {
		return nil, ""
	}

	first := envelope.Errors[0]
	msg := first.Detail
	if msg == "" {
		msg = first.Title
	}
	if msg == "" {
		msg = first.Status
	}
	if msg == "" {
		msg = "An API error occurred"
	}
	if len(envelope.Errors) > 1 {
		msg = fmt.Sprintf("%s (and %d more error", msg, len(envelope.Errors)-1)
		if len(envelope.Errors)-1 > 1 {
			msg += "s"
		}
		msg += ")"
	}
	return envelope.Errors, msg
}

// checkStatus returns a typed SDK error for non-success status codes, or nil.
func checkStatus(code int, body []byte) error {
	if code < 400 {
		return nil
	}

	raw := string(body)
	details, msg := parseJSONAPIErrors(body)
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", code)
	}

	base := SmplError{
		Message:      msg,
		StatusCode:   code,
		ResponseBody: raw,
		Errors:       details,
	}

	switch code {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return &SmplValidationError{SmplError: base}
	case http.StatusNotFound:
		return &SmplNotFoundError{SmplError: base}
	case http.StatusConflict:
		return &SmplConflictError{SmplError: base}
	default:
		return &base
	}
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
	// If the error is a *url.Error, extract the URL for a cleaner message.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("Cannot connect to %s: %s", urlErr.URL, urlErr.Err)},
		}
	}
	return &SmplConnectionError{
		SmplError: SmplError{Message: fmt.Sprintf("connection error: %s", err)},
	}
}
