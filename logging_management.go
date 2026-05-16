package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	genlogging "github.com/smplkit/go-sdk/v3/internal/generated/logging"
)

// LoggingManagement provides CRUD operations for logger and log group
// resources. Obtain one via Client.Manage().Loggers() (for the
// canonical split surface) or LoggingClient.Management() (for the
// older combined surface).
//
// Owns its generated API client directly; the runtime back-reference
// is set only when wired into a runtime Client.
type LoggingManagement struct {
	gen     genlogging.ClientInterface
	runtime *LoggingClient

	// client is a backwards-compat alias for runtime.
	client *LoggingClient
}

// newLoggingManagement constructs a standalone LoggingManagement bound
// to the given generated client.
func newLoggingManagement(gen genlogging.ClientInterface) *LoggingManagement {
	return &LoggingManagement{gen: gen}
}

// attachRuntime links a runtime LoggingClient.
func (m *LoggingManagement) attachRuntime(c *LoggingClient) {
	m.runtime = c
	m.client = c
}

// New creates an unsaved Logger with the given ID. Call Save(ctx) to persist.
// If name is not provided via WithLoggerName, it is auto-generated from the ID.
func (m *LoggingManagement) New(id string, opts ...LoggerOption) *Logger {
	l := &Logger{
		ID:           id,
		Name:         keyToDisplayName(id),
		Managed:      true,
		Environments: map[string]interface{}{},
		client:       m,
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
		client:       m,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Get retrieves a logger by its ID.
func (m *LoggingManagement) Get(ctx context.Context, id string) (*Logger, error) {
	resp, err := m.gen.GetLogger(ctx, id)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{
			Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)},
		}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var result genlogging.LoggerResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("smplkit: failed to parse response: %w", err)
	}

	return resourceToLogger(result.Data, m), nil
}

// List returns one page of loggers for the account.
//
// Without options the server applies its defaults (page 1, page size
// 1000). Use [WithPageNumber] / [WithPageSize] to walk additional
// pages. The wrapper does not loop — callers that want every logger
// should iterate until a short page is returned.
func (m *LoggingManagement) List(ctx context.Context, opts ...ListOption) ([]*Logger, error) {
	o := resolveListOptions(opts)
	params := &genlogging.ListLoggersParams{
		PageNumber: o.pageNumber,
		PageSize:   o.pageSize,
	}
	resp, err := m.gen.ListLoggers(ctx, params)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{
			Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)},
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
		loggers[i] = resourceToLogger(result.Data[i], m)
	}
	return loggers, nil
}

// Delete removes a logger by its ID.
func (m *LoggingManagement) Delete(ctx context.Context, id string) error {
	resp, err := m.gen.DeleteLogger(ctx, id)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	return checkStatus(resp.StatusCode, body)
}

// updateLogger persists changes to an existing logger. Called from
// Logger.Save.
func (m *LoggingManagement) updateLogger(ctx context.Context, l *Logger) error {
	reqBody := genlogging.LoggerRequest{
		Data: genlogging.LoggerResource{
			Id:         &l.ID,
			Type:       genlogging.LoggerResourceTypeLogger,
			Attributes: buildLoggerAttributes(l),
		},
	}
	resp, err := m.gen.UpdateLoggerWithApplicationVndAPIPlusJSONBody(ctx, l.ID, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}
	var result genlogging.LoggerResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	l.apply(resourceToLogger(result.Data, m))
	return nil
}

// createGroup persists a new log group. Called from LogGroup.Save when
// CreatedAt is nil.
func (m *LoggingManagement) createGroup(ctx context.Context, g *LogGroup) error {
	reqBody := genlogging.LogGroupRequest{
		Data: genlogging.LogGroupResource{
			Id:         &g.ID,
			Type:       genlogging.LogGroupResourceTypeLogGroup,
			Attributes: buildLogGroupAttributes(g),
		},
	}
	resp, err := m.gen.CreateLogGroupWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}
	var result genlogging.LogGroupResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	g.apply(resourceToLogGroup(result.Data, m))
	return nil
}

// updateGroup persists changes to an existing log group. Called from
// LogGroup.Save when CreatedAt is set.
func (m *LoggingManagement) updateGroup(ctx context.Context, g *LogGroup) error {
	reqBody := genlogging.LogGroupRequest{
		Data: genlogging.LogGroupResource{
			Id:         &g.ID,
			Type:       genlogging.LogGroupResourceTypeLogGroup,
			Attributes: buildLogGroupAttributes(g),
		},
	}
	resp, err := m.gen.UpdateLogGroupWithApplicationVndAPIPlusJSONBody(ctx, g.ID, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}
	var result genlogging.LogGroupResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("smplkit: failed to parse response: %w", err)
	}
	g.apply(resourceToLogGroup(result.Data, m))
	return nil
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
	return nil, &NotFoundError{
		Base: Error{
			Message:    fmt.Sprintf("log group with id %q not found", id),
			StatusCode: 404,
		},
	}
}

// ListGroups returns one page of log groups for the account.
//
// Without options the server applies its defaults (page 1, page size
// 1000). Use [WithPageNumber] / [WithPageSize] to walk additional
// pages. The wrapper does not loop — callers that want every log
// group should iterate until a short page is returned.
func (m *LoggingManagement) ListGroups(ctx context.Context, opts ...ListOption) ([]*LogGroup, error) {
	o := resolveListOptions(opts)
	params := &genlogging.ListLogGroupsParams{
		PageNumber: o.pageNumber,
		PageSize:   o.pageSize,
	}
	resp, err := m.gen.ListLogGroups(ctx, params)
	if err != nil {
		return nil, classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{
			Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)},
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
		groups[i] = resourceToLogGroup(result.Data[i], m)
	}
	return groups, nil
}

// DeleteGroup removes a log group by its ID.
func (m *LoggingManagement) DeleteGroup(ctx context.Context, id string) error {
	resp, err := m.gen.DeleteLogGroup(ctx, id)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	return checkStatus(resp.StatusCode, body)
}

// RegisterSources registers a batch of logger sources observed in external services.
// This is useful for seeding source-discovery data without running the actual
// service process — e.g. for sample-data loading or cross-service migration.
func (m *LoggingManagement) RegisterSources(ctx context.Context, sources []LoggerSource) error {
	if len(sources) == 0 {
		return nil
	}
	items := make([]genlogging.LoggerBulkItem, len(sources))
	for i, s := range sources {
		item := genlogging.LoggerBulkItem{Id: s.ID}
		if s.Service != nil {
			item.Service = s.Service
		}
		if s.Environment != nil {
			item.Environment = s.Environment
		}
		if s.ResolvedLevel != nil {
			rl := string(*s.ResolvedLevel)
			item.ResolvedLevel = &rl
		}
		items[i] = item
	}
	reqBody := genlogging.LoggerBulkRequest{Loggers: items}
	resp, err := m.gen.BulkRegisterLoggersWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", err)}}
	}
	return checkStatus(resp.StatusCode, body)
}
