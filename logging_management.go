package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	genlogging "github.com/smplkit/go-sdk/internal/generated/logging"
)

// LoggingManagement provides CRUD operations for logger and log group resources.
// Obtain one via LoggingClient.Management().
type LoggingManagement struct {
	client *LoggingClient
}

// New creates an unsaved Logger with the given ID. Call Save(ctx) to persist.
// If name is not provided via WithLoggerName, it is auto-generated from the ID.
func (m *LoggingManagement) New(id string, opts ...LoggerOption) *Logger {
	l := &Logger{
		ID:           id,
		Name:         keyToDisplayName(id),
		Managed:      true,
		Environments: map[string]interface{}{},
		client:       m.client,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// NewGroup creates an unsaved LogGroup with the given ID. Call Save(ctx) to persist.
func (m *LoggingManagement) NewGroup(id string, opts ...LogGroupOption) *LogGroup {
	g := &LogGroup{
		ID:           id,
		Name:         keyToDisplayName(id),
		Environments: map[string]interface{}{},
		client:       m.client,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Get retrieves a logger by its ID.
func (m *LoggingManagement) Get(ctx context.Context, id string) (*Logger, error) {
	resp, err := m.client.generated.GetLogger(ctx, id)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genlogging.LoggerResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	return resourceToLogger(result.Data, m.client), nil
}

// List returns all loggers for the account.
func (m *LoggingManagement) List(ctx context.Context) ([]*Logger, error) {
	resp, err := m.client.generated.ListLoggers(ctx, nil)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genlogging.LoggerListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	loggers := make([]*Logger, len(result.Data))
	for i := range result.Data {
		loggers[i] = resourceToLogger(result.Data[i], m.client)
	}
	return loggers, nil
}

// Delete removes a logger by its ID.
func (m *LoggingManagement) Delete(ctx context.Context, id string) error {
	return m.client.deleteLoggerByID(ctx, id)
}

// GetGroup retrieves a log group by its ID.
func (m *LoggingManagement) GetGroup(ctx context.Context, id string) (*LogGroup, error) {
	groups, err := m.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.ID == id {
			return g, nil
		}
	}
	return nil, &SmplNotFoundError{
		SmplError: SmplError{
			Message:    fmt.Sprintf("log group with id %q not found", id),
			StatusCode: 404,
		},
	}
}

// ListGroups returns all log groups for the account.
func (m *LoggingManagement) ListGroups(ctx context.Context) ([]*LogGroup, error) {
	resp, err := m.client.generated.ListLogGroups(ctx)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SmplConnectionError{
			SmplError: SmplError{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genlogging.LogGroupListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	groups := make([]*LogGroup, len(result.Data))
	for i := range result.Data {
		groups[i] = resourceToLogGroup(result.Data[i], m.client)
	}
	return groups, nil
}

// DeleteGroup removes a log group by its ID.
func (m *LoggingManagement) DeleteGroup(ctx context.Context, id string) error {
	return m.client.deleteGroupByID(ctx, id)
}
