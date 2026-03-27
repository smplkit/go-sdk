package smplkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
)

const userAgent = "smplkit-go-sdk/0.0.0"

// doRequest executes an HTTP request with standard headers and maps errors.
// It returns the raw response body, or a typed SDK error.
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("smplkit: failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	url := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("smplkit: failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/vnd.api+json")
	req.Header.Set("Accept", "application/vnd.api+json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}

	if err := checkStatus(resp.StatusCode, respBody); err != nil {
		return respBody, err
	}

	return respBody, nil
}

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

	// Check for network-level errors.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return &SmplTimeoutError{
				SmplError: SmplError{Message: fmt.Sprintf("request timed out: %s", err)},
			}
		}
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("connection error: %s", err)},
		}
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("connection error: %s", err)},
		}
	}

	return &SmplConnectionError{
		SmplError: SmplError{Message: fmt.Sprintf("request failed: %s", err)},
	}
}
