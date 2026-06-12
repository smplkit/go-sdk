package smplkit

import (
	"github.com/smplkit/go-sdk/v3/internal/debug"
	"github.com/smplkit/go-sdk/v3/logging/adapters"
)

// IsDebugEnabled reports whether the internal debug package has debug output
// enabled. Used by tests to verify that Config{Debug: true} activates the
// debug facility.
var IsDebugEnabled = debug.IsEnabled

// SetDebugEnabled sets the process-global debug-output flag. Tests that
// construct a Config{Debug: true} client use it (via t.Cleanup) to restore the
// flag so debug output does not leak into unrelated tests.
var SetDebugEnabled = debug.SetEnabled

// CheckStatusForTest exposes checkStatus for use in external tests.
var CheckStatusForTest = checkStatus

// ClassifyErrorForTest exposes classifyError for use in external tests.
var ClassifyErrorForTest = classifyError

// WithBaseURL is a test-only option that routes all four service clients to the
// same base URL. Use Config.BaseDomain and Config.Scheme for production configuration.
var WithBaseURL = withBaseURLOverride

// TestDiscoveredLogger is an alias for adapters.DiscoveredLogger for use in tests.
type TestDiscoveredLogger = adapters.DiscoveredLogger

// TestLoggingAdapter is an alias for adapters.LoggingAdapter for use in tests.
type TestLoggingAdapter = adapters.LoggingAdapter
