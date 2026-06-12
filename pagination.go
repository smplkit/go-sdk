package smplkit

// fetchAllPageSize is the page size the runtime requests when walking every
// page of a list endpoint. Matches the server's maximum page size — bigger
// pages mean fewer round trips during startup and refresh.
const fetchAllPageSize = 1000

// ListOption configures a paginated list call. Pass options to any list
// method to request a specific page or page size:
//
//	loggers, err := client.Logging().Loggers().List(ctx, smplkit.WithPageSize(50))
//
// Omit all options to let the server apply its defaults (page 1, page
// size 1000). The wrapper does not loop on the caller's behalf — see
// each list method's docs for the slice it returns.
type ListOption func(*listOptions)

type listOptions struct {
	pageNumber *int
	pageSize   *int
}

// WithPageNumber requests a specific 1-based page from a list endpoint.
// Values < 1 are rejected by the server with a 400.
func WithPageNumber(n int) ListOption {
	return func(o *listOptions) { o.pageNumber = &n }
}

// WithPageSize requests a specific page size from a list endpoint. The
// server caps this at 1000; values outside [1, 1000] are rejected with
// a 400.
func WithPageSize(n int) ListOption {
	return func(o *listOptions) { o.pageSize = &n }
}

// resolveListOptions collapses the variadic options into a single struct.
func resolveListOptions(opts []ListOption) listOptions {
	var o listOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}
