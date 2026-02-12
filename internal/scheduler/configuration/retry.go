package configuration

import (
	"regexp"
	"slices"
)

// RetryPolicyConfig configures the retry policy system.
// When Enabled is false, the legacy MaxRetries behavior is used.
type RetryPolicyConfig struct {
	// Enabled toggles the retry policy system.
	// When false, existing MaxRetries behavior is preserved.
	Enabled bool

	// GlobalMaxRetries is the hard cap on total runs for any job.
	// This is a safety limit to prevent runaway retries.
	// Must be greater than 0 when Enabled is true.
	GlobalMaxRetries uint `validate:"required_if=Enabled true,gt=0"`

	// Default is the policy applied when a queue does not specify one.
	Default Policy

	// Policies are named policies that can be referenced by queues.
	Policies map[string]Policy
}

// Policy defines retry behavior for a set of queues.
type Policy struct {
	// RetryLimit is the maximum number of retries allowed.
	// Each Retry action increments the failure count toward this limit.
	RetryLimit uint

	// Rules are evaluated in order. First matching rule wins.
	// If no rule matches, the job fails (implicit Fail action).
	Rules []Rule
}

// Rule defines a condition-action pair for retry policy evaluation.
type Rule struct {
	// Action to take when this rule matches.
	Action Action

	// OnConditions matches failures by high-level condition.
	OnConditions []FailureCondition

	// OnExitCodes matches failures by container exit code.
	OnExitCodes *ExitCodeMatcher

	// OnTerminationMessage matches failures by regex on the container's termination message.
	// The termination message is the content written to /dev/termination-log.
	OnTerminationMessage *RegexMatcher
}

// Action defines what happens when a rule matches a failure.
type Action string

const (
	// ActionFail terminates the job immediately.
	// This is the default when no rule matches.
	ActionFail Action = "Fail"

	// ActionRetry requeues the job if under the retry limit.
	// Increments the failure count.
	ActionRetry Action = "Retry"
)

// FailureCondition categorizes job run failures.
type FailureCondition string

const (
	// ConditionPreempted indicates the pod was preempted.
	ConditionPreempted FailureCondition = "Preempted"

	// ConditionEvicted indicates the pod was evicted from its node.
	ConditionEvicted FailureCondition = "Evicted"

	// ConditionOOMKilled indicates the container was killed due to OOM.
	ConditionOOMKilled FailureCondition = "OOMKilled"

	// ConditionDeadlineExceeded indicates the pod exceeded activeDeadlineSeconds.
	ConditionDeadlineExceeded FailureCondition = "DeadlineExceeded"

	// ConditionUnschedulable indicates the pod could not be scheduled.
	ConditionUnschedulable FailureCondition = "Unschedulable"
)

// ExitCodeMatcher matches failures by container exit code.
type ExitCodeMatcher struct {
	// Operator is either "In" or "NotIn".
	Operator string `validate:"oneof=In NotIn"`

	// Values are the exit codes to match.
	Values []int32 `validate:"required,min=1"`
}

// Matches returns true if the exit code matches this matcher.
func (m *ExitCodeMatcher) Matches(exitCode int32) bool {
	contains := slices.Contains(m.Values, exitCode)
	if m.Operator == "In" {
		return contains
	}
	return !contains
}

// RegexMatcher matches strings using a regular expression pattern.
type RegexMatcher struct {
	// Pattern is the regular expression to match against.
	Pattern string `validate:"required"`

	// compiledRegex is the pre-compiled regex pattern.
	// This is populated by Compile() and used by Matches().
	compiledRegex *regexp.Regexp
}

// Compile pre-compiles the regex pattern. Should be called during configuration load.
// Returns an error if the pattern is invalid.
func (m *RegexMatcher) Compile() error {
	if m.Pattern == "" {
		return nil
	}
	re, err := regexp.Compile(m.Pattern)
	if err != nil {
		return err
	}
	m.compiledRegex = re
	return nil
}

// Matches returns true if the message matches the regex pattern.
// If the pattern was not pre-compiled, it compiles on demand (less efficient).
func (m *RegexMatcher) Matches(message string) bool {
	if message == "" {
		return false
	}
	if m.compiledRegex != nil {
		return m.compiledRegex.MatchString(message)
	}
	// Fallback: compile on demand if Compile() was not called
	re, err := regexp.Compile(m.Pattern)
	if err != nil {
		return false
	}
	return re.MatchString(message)
}
