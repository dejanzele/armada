package configuration

// RetryPolicyConfig controls the scheduler's retry policy behavior.
type RetryPolicyConfig struct {
	// Enabled controls whether the retry policy engine is active.
	Enabled bool `yaml:"enabled"`
	// GlobalMaxRetries is the maximum number of retries per job across all
	// policies. It is counted in retries (not runs): the initial run does
	// not consume budget, only subsequent re-leases do. A value of 0 is the
	// kill switch: no job is ever retried by the policy engine. There is no
	// unlimited setting for the global cap.
	GlobalMaxRetries uint `yaml:"globalMaxRetries"`
	// DefaultPolicyName is the retry policy applied to jobs whose queue has no
	// policy of its own. It lets an operator turn on retry policies fleet-wide
	// with a single named policy before per-queue attachment is configured.
	// Required when Enabled is true; otherwise no job would ever be retried.
	DefaultPolicyName string `yaml:"defaultPolicyName"`
}
