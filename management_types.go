package smplkit

// EnvironmentClassification indicates whether an environment participates
// in the canonical environment ordering.
//
// STANDARD environments are the customer's deploy targets — production,
// staging, development, etc. They participate in AccountSettings.EnvironmentOrder
// and appear in the standard Console environment columns.
//
// AD_HOC environments are transient targets (preview branches, individual
// developer sandboxes) that should not appear in the standard ordering.
type EnvironmentClassification string

const (
	// EnvironmentClassificationStandard marks an environment as a canonical deploy target.
	EnvironmentClassificationStandard EnvironmentClassification = "STANDARD"
	// EnvironmentClassificationAdHoc marks an environment as a transient, non-ordered target.
	EnvironmentClassificationAdHoc EnvironmentClassification = "AD_HOC"
)
