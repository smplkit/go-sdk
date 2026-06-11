package smplkit

// EnvironmentClassification indicates whether an environment participates
// in the canonical environment ordering.
//
// STANDARD environments are the customer's deploy targets — production,
// staging, development, etc. They participate in
// AccountSettings.EnvironmentOrder and appear in the standard Console
// environment columns.
//
// AD_HOC environments are transient targets (preview branches, individual
// developer sandboxes) that should not appear in the standard ordering.
type EnvironmentClassification string

// Supported EnvironmentClassification values, alphabetical by wire
// constant.
const (
	// EnvironmentClassificationAdHoc marks an environment as a transient,
	// non-ordered target (preview branches, developer sandboxes).
	EnvironmentClassificationAdHoc EnvironmentClassification = "AD_HOC"
	// EnvironmentClassificationStandard marks an environment as a canonical
	// deploy target — appears in AccountSettings.EnvironmentOrder.
	EnvironmentClassificationStandard EnvironmentClassification = "STANDARD"
)
