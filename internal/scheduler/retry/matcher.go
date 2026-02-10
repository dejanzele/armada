package retry

import (
	"slices"

	"github.com/armadaproject/armada/internal/scheduler/configuration"
	"github.com/armadaproject/armada/pkg/armadaevents"
)

// Matcher evaluates failure info against policy rules.
type Matcher struct{}

// NewMatcher creates a new Matcher.
func NewMatcher() *Matcher {
	return &Matcher{}
}

// Match evaluates the failure info against the policy rules and returns the action.
// Rules are evaluated in order; the first matching rule wins.
// If no rule matches, ActionFail is returned (implicit fail).
func (m *Matcher) Match(failureInfo *armadaevents.FailureInfo, rules []configuration.Rule) configuration.Action {
	if failureInfo == nil {
		return configuration.ActionFail
	}

	for _, rule := range rules {
		if matchesRule(failureInfo, rule) {
			return rule.Action
		}
	}

	return configuration.ActionFail
}

// matchesRule returns true if the failure info matches the given rule.
// Rules can match on conditions, exit codes, or termination messages (mutually exclusive).
func matchesRule(info *armadaevents.FailureInfo, rule configuration.Rule) bool {
	if len(rule.OnConditions) > 0 {
		return matchesCondition(info.Condition, rule.OnConditions)
	}
	if rule.OnExitCodes != nil {
		return matchesExitCode(info.ExitCode, rule.OnExitCodes)
	}
	if rule.OnTerminationMessage != nil {
		return matchesTerminationMessage(info.TerminationMessage, rule.OnTerminationMessage)
	}
	return false
}

// matchesCondition checks if the failure condition matches any of the rule conditions.
func matchesCondition(cond armadaevents.FailureCondition, ruleConditions []configuration.FailureCondition) bool {
	return slices.Contains(ruleConditions, mapProtoCondition(cond))
}

// matchesExitCode checks if the exit code matches the exit code matcher.
// Exit code 0 is treated as "not set" in proto3, so it never matches.
func matchesExitCode(exitCode int32, matcher *configuration.ExitCodeMatcher) bool {
	if exitCode <= 0 {
		return false
	}
	return matcher.Matches(exitCode)
}

// matchesTerminationMessage checks if the termination message matches the regex pattern.
// The regex should be pre-compiled via RegexMatcher.Compile() during configuration load.
func matchesTerminationMessage(message string, matcher *configuration.RegexMatcher) bool {
	return matcher.Matches(message)
}

// mapProtoCondition maps the proto FailureCondition to the config FailureCondition.
func mapProtoCondition(cond armadaevents.FailureCondition) configuration.FailureCondition {
	switch cond {
	case armadaevents.FailureCondition_FAILURE_CONDITION_PREEMPTED:
		return configuration.ConditionPreempted
	case armadaevents.FailureCondition_FAILURE_CONDITION_EVICTED:
		return configuration.ConditionEvicted
	case armadaevents.FailureCondition_FAILURE_CONDITION_OOM_KILLED:
		return configuration.ConditionOOMKilled
	case armadaevents.FailureCondition_FAILURE_CONDITION_DEADLINE_EXCEEDED:
		return configuration.ConditionDeadlineExceeded
	case armadaevents.FailureCondition_FAILURE_CONDITION_UNSCHEDULABLE:
		return configuration.ConditionUnschedulable
	default:
		return ""
	}
}
