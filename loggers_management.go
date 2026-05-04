package smplkit

import "context"

// LoggersManagement is the mgmt.loggers namespace — CRUD against single
// loggers (no log groups). Mirrors Python's mgmt.loggers (rule 2 of the
// cross-SDK overhaul, where loggers and log groups live in distinct flat
// namespaces).
//
// Obtain one via client.Manage().Loggers().
type LoggersManagement struct {
	logging *LoggingManagement
}

// New returns an unsaved Logger with the given ID. Call logger.Save(ctx)
// to persist. The display name defaults to id (rule 9: drop the name
// kwarg when name and id would normally be identical) and managed
// defaults to true (every customer using the management API to create a
// logger is doing so to manage it).
func (m *LoggersManagement) New(id string) *Logger {
	l := &Logger{
		ID:           id,
		Name:         id,
		Managed:      true,
		Environments: map[string]interface{}{},
		client:       m.logging,
	}
	return l
}

// Get retrieves a logger by ID. Returns NotFoundError if no match.
func (m *LoggersManagement) Get(ctx context.Context, id string) (*Logger, error) {
	return m.logging.Get(ctx, id)
}

// List returns all loggers for the account.
func (m *LoggersManagement) List(ctx context.Context) ([]*Logger, error) {
	return m.logging.List(ctx)
}

// Delete removes a logger by ID.
func (m *LoggersManagement) Delete(ctx context.Context, id string) error {
	return m.logging.Delete(ctx, id)
}

// Flush sends any pending logger discoveries to the server immediately.
// Discoveries are buffered by the runtime LoggingClient as adapter hooks
// fire and on explicit RegisterLogger calls; they are normally drained
// on a 5-second interval. Call this when you need them sent right away.
//
// Returns nil immediately when no runtime LoggingClient is attached
// (the management-only client built via NewManagementClient has no
// discovery buffer — call LoggingManagement.RegisterSources instead to
// post sources directly).
//
// Mirrors Python's client.manage.loggers.flush().
func (m *LoggersManagement) Flush(ctx context.Context) error {
	if m.logging == nil || m.logging.runtime == nil {
		return nil
	}
	return m.logging.runtime.Flush(ctx)
}

// LogGroupsManagement is the mgmt.log_groups namespace — CRUD against
// log groups. Split from the older combined LoggingManagement to keep
// loggers and log groups in distinct namespaces (rule 2).
//
// Obtain one via client.Manage().LogGroups().
type LogGroupsManagement struct {
	logging *LoggingManagement
}

// New returns an unsaved LogGroup with the given ID. Call group.Save(ctx)
// to persist.
func (m *LogGroupsManagement) New(id string, opts ...LogGroupOption) *LogGroup {
	return m.logging.NewGroup(id, opts...)
}

// Get retrieves a log group by ID.
func (m *LogGroupsManagement) Get(ctx context.Context, id string) (*LogGroup, error) {
	return m.logging.GetGroup(ctx, id)
}

// List returns all log groups for the account.
func (m *LogGroupsManagement) List(ctx context.Context) ([]*LogGroup, error) {
	return m.logging.ListGroups(ctx)
}

// Delete removes a log group by ID.
func (m *LogGroupsManagement) Delete(ctx context.Context, id string) error {
	return m.logging.DeleteGroup(ctx, id)
}
