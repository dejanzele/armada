package retrypolicy

import (
	"fmt"
	"regexp"

	"github.com/armadaproject/armada/pkg/api"
)

// Policy names follow RFC 1123 label rules, like queue names. Names are used
// as stable references from queues and may end up in Kubernetes labels, so
// anything looser risks breaking downstream consumers.
var policyNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

const maxPolicyNameLength = 63

// ValidatePolicy performs structural validation of a retry policy at write
// time. The scheduler-side cache loader silently drops policies it cannot
// compile, so rejecting malformed policies here is the only way the operator
// gets immediate feedback instead of a policy that never takes effect.
func ValidatePolicy(p *api.RetryPolicy) error {
	if p == nil {
		return fmt.Errorf("retry policy must not be nil")
	}
	if p.Name == "" {
		return fmt.Errorf("retry policy name must not be empty")
	}
	if len(p.Name) > maxPolicyNameLength {
		return fmt.Errorf("retry policy name %q must be at most %d characters", p.Name, maxPolicyNameLength)
	}
	if !policyNamePattern.MatchString(p.Name) {
		return fmt.Errorf(
			"retry policy name %q is invalid: must consist of lowercase alphanumeric characters or '-', and must start and end with an alphanumeric character",
			p.Name,
		)
	}
	for i, rule := range p.Rules {
		if err := validateRule(rule); err != nil {
			return fmt.Errorf("retry policy %q rule %d: %w", p.Name, i, err)
		}
	}
	// The scheduler's policy loader rejects an unspecified default action, so
	// reject it here too: accepting a policy at write time that the scheduler
	// would silently drop is exactly the confusion write-time validation exists
	// to prevent.
	if p.DefaultAction == api.RetryAction_RETRY_ACTION_UNSPECIFIED {
		return fmt.Errorf("retry policy %q must set a default action (Fail or Retry)", p.Name)
	}
	return nil
}

func validateRule(r *api.RetryRule) error {
	if r == nil {
		return fmt.Errorf("rule must not be nil")
	}
	if r.Action == api.RetryAction_RETRY_ACTION_UNSPECIFIED {
		return fmt.Errorf("action must be specified")
	}
	// on_subcategory only narrows an on_category match, so it does not count
	// as a matcher on its own. This mirrors the scheduler engine's rule
	// compilation in internal/scheduler/retry.
	if len(r.OnConditions) == 0 && r.OnExitCodes == nil && r.OnTerminationMessagePattern == "" && r.OnCategory == "" {
		return fmt.Errorf("at least one matcher must be set (on_conditions, on_exit_codes, on_termination_message_pattern, or on_category)")
	}
	if r.OnSubcategory != "" && r.OnCategory == "" {
		return fmt.Errorf("on_subcategory requires on_category to be set")
	}
	if r.OnExitCodes != nil {
		if r.OnExitCodes.Operator == api.ExitCodeOperator_EXIT_CODE_OPERATOR_UNSPECIFIED {
			return fmt.Errorf("on_exit_codes operator must be specified")
		}
		if len(r.OnExitCodes.Values) == 0 {
			return fmt.Errorf("on_exit_codes values must not be empty")
		}
	}
	if r.OnTerminationMessagePattern != "" {
		if _, err := regexp.Compile(r.OnTerminationMessagePattern); err != nil {
			return fmt.Errorf("on_termination_message_pattern is not a valid regular expression: %w", err)
		}
	}
	return nil
}
