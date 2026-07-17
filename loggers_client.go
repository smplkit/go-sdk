package smplkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"

	genlogging "github.com/smplkit/go-sdk/v3/internal/generated/logging"
)

// LoggersClient is the surface for client.Logging().Loggers() — logger
// CRUD plus the discovery buffer. The buffer is owned by the fused
// LoggingClient and shared here so discovery (driven by
// LoggingClient.Install) and explicit Register drain through one queue.
//
// Obtain one via client.Logging().Loggers().
type LoggersClient struct {
	gen    genlogging.ClientInterface
	buffer *loggerRegistrationBuffer

	// metrics records discovery counts on flush; nil when unset.
	metrics *metricsReporter

	// disableStreaming mirrors Config.DisableStreaming: threshold flushes
	// run inline instead of on a background goroutine.
	disableStreaming bool
}

// New returns a new unsaved Logger. Call logger.Save(ctx) to persist.
//
// id is the identifier for the logger (its normalized name); the display
// name defaults to id. managed defaults to true (WithLoggerManaged
// overrides it): when true, smplkit controls this logger's level at
// runtime; set false to register the logger for visibility without taking
// over its level.
func (m *LoggersClient) New(id string, opts ...LoggerOption) *Logger {
	l := &Logger{
		ID:           id,
		Name:         id,
		Managed:      true,
		Environments: map[string]interface{}{},
		client:       m,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Register queues one or more logger sources for registration with the
// server. Sources are buffered locally and coalesced into the shared
// discovery buffer, then sent in a batch.
//
// sources are the logger sources to queue. flush, when true, sends the
// buffered batch immediately instead of waiting for it to fill.
func (m *LoggersClient) Register(ctx context.Context, sources []LoggerSource, flush bool) error {
	for _, src := range sources {
		resolved := ""
		if src.ResolvedLevel != nil {
			resolved = string(*src.ResolvedLevel)
		}
		// The explicit (configured) level defaults to the resolved level when
		// the caller did not set one, so an inherited source still registers a
		// usable level.
		explicit := resolved
		if src.Level != nil {
			explicit = string(*src.Level)
		}
		service := ""
		if src.Service != nil {
			service = *src.Service
		}
		environment := ""
		if src.Environment != nil {
			environment = *src.Environment
		}
		m.buffer.add(NormalizeLoggerName(src.ID), explicit, resolved, service, environment)
	}
	if flush {
		return m.Flush(ctx)
	}
	if m.buffer.pendingCount() >= loggerBatchFlushSize {
		if m.disableStreaming {
			// Stateless mode: flush inline rather than spawning.
			m.thresholdFlush()
		} else {
			go m.thresholdFlush()
		}
	}
	return nil
}

// thresholdFlush flushes once the buffer crosses the batch threshold,
// logging (not returning) any error. Runs on a background goroutine
// normally, inline when Config.DisableStreaming is set.
func (m *LoggersClient) thresholdFlush() {
	if err := m.Flush(context.Background()); err != nil {
		log.Printf("smplkit: logger registration flush failed: %s", err.Error())
	}
}

// Flush drains the buffer and POSTs pending logger sources to the bulk
// endpoint. Returns nil immediately when there is nothing to flush.
func (m *LoggersClient) Flush(ctx context.Context) error {
	batch := m.buffer.drain()
	if len(batch) == 0 {
		return nil
	}
	items := make([]genlogging.LoggerBulkItem, 0, len(batch))
	for _, entry := range batch {
		item := genlogging.LoggerBulkItem{Id: entry.key}
		if entry.level != "" {
			item.Level = &entry.level
		}
		if entry.resolvedLevel != "" {
			item.ResolvedLevel = &entry.resolvedLevel
		}
		if entry.service != "" {
			item.Service = &entry.service
		}
		if entry.environment != "" {
			item.Environment = &entry.environment
		}
		items = append(items, item)
	}
	reqBody := genlogging.LoggerBulkRequest{Loggers: items}
	resp, err := m.gen.BulkRegisterLoggersWithApplicationVndAPIPlusJSONBody(ctx, reqBody)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return &ConnectionError{Base: Error{Message: fmt.Sprintf("failed to read response body: %s", readErr)}}
	}
	if err := checkStatus(resp.StatusCode, body); err != nil {
		return err
	}
	if m.metrics != nil {
		m.metrics.Record("logging.loggers_discovered", len(batch), "loggers", nil)
	}
	return nil
}

// FlushSync is an alias of Flush for the periodic-flush path.
func (m *LoggersClient) FlushSync(ctx context.Context) error {
	return m.Flush(ctx)
}

// PendingCount returns the number of sources queued and awaiting flush.
func (m *LoggersClient) PendingCount() int {
	return m.buffer.pendingCount()
}

// Get fetches the editable Logger resource by id. It returns a
// *NotFoundError when no logger with that id exists.
func (m *LoggersClient) Get(ctx context.Context, id string) (*Logger, error) {
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

// List lists loggers for the authenticated account.
//
// Without options the server applies its defaults (page 1, page size
// 1000). Use [WithPageNumber] / [WithPageSize] to walk additional
// pages. The wrapper does not loop — callers that want every logger
// should iterate until a short page is returned.
func (m *LoggersClient) List(ctx context.Context, opts ...ListOption) ([]*Logger, error) {
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

// Delete deletes a logger by id.
func (m *LoggersClient) Delete(ctx context.Context, id string) error {
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

// RegisterSources registers a batch of logger sources observed in external
// services. This is useful for seeding source-discovery data without running
// the actual service process — e.g. for sample-data loading or cross-service
// migration.
func (m *LoggersClient) RegisterSources(ctx context.Context, sources []LoggerSource) error {
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
		if s.Level != nil {
			lv := string(*s.Level)
			item.Level = &lv
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

// saveLogger persists changes to an existing logger. Called from
// Logger.Save. PUT has upsert semantics: the server creates the logger
// if it does not exist.
func (m *LoggersClient) saveLogger(ctx context.Context, l *Logger) error {
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

// LogGroupsClient is the surface for client.Logging().LogGroups() — log
// group CRUD.
//
// Obtain one via client.Logging().LogGroups().
type LogGroupsClient struct {
	gen genlogging.ClientInterface
}

// New returns a new unsaved LogGroup. Call group.Save(ctx) to persist.
func (m *LogGroupsClient) New(id string, opts ...LogGroupOption) *LogGroup {
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

// Get fetches the editable LogGroup resource by id. It returns a
// *NotFoundError when no log group with that id exists.
func (m *LogGroupsClient) Get(ctx context.Context, id string) (*LogGroup, error) {
	groups, err := m.List(ctx)
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

// List lists log groups for the authenticated account.
//
// Without options the server applies its defaults (page 1, page size
// 1000). Use [WithPageNumber] / [WithPageSize] to walk additional
// pages. The wrapper does not loop — callers that want every log group
// should iterate until a short page is returned.
func (m *LogGroupsClient) List(ctx context.Context, opts ...ListOption) ([]*LogGroup, error) {
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

// Delete deletes a log group by id.
func (m *LogGroupsClient) Delete(ctx context.Context, id string) error {
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

// saveGroup persists a log group. Called from LogGroup.Save: creates when
// CreatedAt is nil, updates otherwise.
func (m *LogGroupsClient) saveGroup(ctx context.Context, g *LogGroup) error {
	if g.CreatedAt == nil {
		return m.createGroup(ctx, g)
	}
	return m.updateGroup(ctx, g)
}

// createGroup persists a new log group.
func (m *LogGroupsClient) createGroup(ctx context.Context, g *LogGroup) error {
	reqBody := genlogging.LogGroupCreateRequest{
		Data: genlogging.LogGroupCreateResource{
			Id:         g.ID,
			Type:       genlogging.LogGroupCreateResourceTypeLogGroup,
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

// updateGroup persists changes to an existing log group.
func (m *LogGroupsClient) updateGroup(ctx context.Context, g *LogGroup) error {
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
