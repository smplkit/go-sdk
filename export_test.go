package smplkit

import "github.com/smplkit/go-sdk/logging/adapters"

// CheckStatusForTest exposes checkStatus for use in external tests.
var CheckStatusForTest = checkStatus

// WithBaseURL is a test-only option that routes all four service clients to the
// same base URL. Use WithBaseDomain and WithScheme for production configuration.
var WithBaseURL = withBaseURLOverride

// TestDiscoveredLogger is an alias for adapters.DiscoveredLogger for use in tests.
type TestDiscoveredLogger = adapters.DiscoveredLogger

// TestLoggingAdapter is an alias for adapters.LoggingAdapter for use in tests.
type TestLoggingAdapter = adapters.LoggingAdapter
