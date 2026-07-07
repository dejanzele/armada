package retry

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/armadaproject/armada/internal/common/errormatch"
	"github.com/armadaproject/armada/pkg/armadaevents"
)

func makeOOMError() *armadaevents.Error {
	return &armadaevents.Error{
		Reason: &armadaevents.Error_PodError{
			PodError: &armadaevents.PodError{
				KubernetesReason: armadaevents.KubernetesReason_OOM,
				ContainerErrors:  []*armadaevents.ContainerError{{ExitCode: 137, Message: "OOMKilled"}},
			},
		},
		FailureCategory: "infrastructure",
	}
}

func makeAppError(exitCode int32, message string) *armadaevents.Error {
	return &armadaevents.Error{
		Reason: &armadaevents.Error_PodError{
			PodError: &armadaevents.PodError{
				KubernetesReason: armadaevents.KubernetesReason_AppError,
				ContainerErrors:  []*armadaevents.ContainerError{{ExitCode: exitCode, Message: message}},
			},
		},
	}
}

func compilePolicy(t *testing.T, p *Policy) *Policy {
	t.Helper()
	require.NoError(t, CompileRules(p.Rules))
	return p
}

func TestEngine_Evaluate(t *testing.T) {
	tests := map[string]struct {
		globalMax uint
		policy    *Policy
		runError  *armadaevents.Error
		counts    Counts
		expected  Result
	}{
		"condition match OOMKilled, action Fail": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{Action: ActionFail, OnConditions: []string{errormatch.ConditionOOMKilled}},
				},
			},
			runError: makeOOMError(),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "matched rule: Fail"},
		},
		"condition match Evicted, action Retry": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{Action: ActionRetry, OnConditions: []string{errormatch.ConditionEvicted}},
				},
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_PodError{
					PodError: &armadaevents.PodError{KubernetesReason: armadaevents.KubernetesReason_Evicted},
				},
			},
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "matched rule: Retry"},
		},
		"condition match DeadlineExceeded": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{Action: ActionFail, OnConditions: []string{errormatch.ConditionDeadlineExceeded}},
				},
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_PodError{
					PodError: &armadaevents.PodError{KubernetesReason: armadaevents.KubernetesReason_DeadlineExceeded},
				},
			},
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "matched rule: Fail"},
		},
		"condition match Preempted": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{Action: ActionRetry, OnConditions: []string{errormatch.ConditionPreempted}},
				},
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_JobRunPreemptedError{
					JobRunPreemptedError: &armadaevents.JobRunPreemptedError{},
				},
			},
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "matched rule: Retry"},
		},
		"condition match LeaseReturned": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{Action: ActionRetry, OnConditions: []string{errormatch.ConditionLeaseReturned}},
				},
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_PodLeaseReturned{
					PodLeaseReturned: &armadaevents.PodLeaseReturned{},
				},
			},
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "matched rule: Retry"},
		},
		"condition match LeaseExpired": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{Action: ActionRetry, OnConditions: []string{errormatch.ConditionLeaseExpired}},
				},
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_LeaseExpired{
					LeaseExpired: &armadaevents.LeaseExpired{},
				},
			},
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "matched rule: Retry"},
		},
		"condition match AppError": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{Action: ActionFail, OnConditions: []string{errormatch.ConditionAppError}},
				},
			},
			runError: makeAppError(1, "crash"),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "matched rule: Fail"},
		},
		"exit code In match": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{
						Action:      ActionFail,
						OnExitCodes: &errormatch.ExitCodeMatcher{Operator: errormatch.ExitCodeOperatorIn, Values: []int32{42, 43}},
					},
				},
			},
			runError: makeAppError(42, ""),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "matched rule: Fail"},
		},
		"exit code NotIn match": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{
						Action:      ActionRetry,
						OnExitCodes: &errormatch.ExitCodeMatcher{Operator: errormatch.ExitCodeOperatorNotIn, Values: []int32{42}},
					},
				},
			},
			runError: makeAppError(1, ""),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "matched rule: Retry"},
		},
		"termination message regex match": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{
						Action:               ActionFail,
						OnTerminationMessage: &errormatch.RegexMatcher{Pattern: "(?i)cuda.*error"},
					},
				},
			},
			runError: makeAppError(1, "CUDA memory error on device 0"),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "matched rule: Fail"},
		},
		"termination message regex no match": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{
						Action:               ActionFail,
						OnTerminationMessage: &errormatch.RegexMatcher{Pattern: "(?i)cuda.*error"},
					},
				},
			},
			runError: makeAppError(1, "segfault"),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "no rule matched, using default action"},
		},
		"category match (any subcategory)": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{Action: ActionFail, OnCategory: "gpu"},
				},
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_PodError{
					PodError: &armadaevents.PodError{KubernetesReason: armadaevents.KubernetesReason_AppError},
				},
				FailureCategory:    "gpu",
				FailureSubcategory: "transient",
			},
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "matched rule: Fail"},
		},
		"category match with subcategory match": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{Action: ActionFail, OnCategory: "gpu", OnSubcategory: "transient"},
				},
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_PodError{
					PodError: &armadaevents.PodError{KubernetesReason: armadaevents.KubernetesReason_AppError},
				},
				FailureCategory:    "gpu",
				FailureSubcategory: "transient",
			},
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "matched rule: Fail"},
		},
		"category match but subcategory mismatch": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{Action: ActionFail, OnCategory: "gpu", OnSubcategory: "permanent"},
				},
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_PodError{
					PodError: &armadaevents.PodError{KubernetesReason: armadaevents.KubernetesReason_AppError},
				},
				FailureCategory:    "gpu",
				FailureSubcategory: "transient",
			},
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "no rule matched, using default action"},
		},
		"category mismatch": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{Action: ActionFail, OnCategory: "network"},
				},
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_PodError{
					PodError: &armadaevents.PodError{KubernetesReason: armadaevents.KubernetesReason_AppError},
				},
				FailureCategory:    "gpu",
				FailureSubcategory: "transient",
			},
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "no rule matched, using default action"},
		},
		"first match wins": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{Action: ActionRetry, OnConditions: []string{errormatch.ConditionAppError}},
					{Action: ActionFail, OnConditions: []string{errormatch.ConditionAppError}},
				},
			},
			runError: makeAppError(1, "crash"),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "matched rule: Retry"},
		},
		"no match returns DefaultAction Fail": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{Action: ActionRetry, OnConditions: []string{errormatch.ConditionOOMKilled}},
				},
			},
			runError: makeAppError(1, ""),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "no rule matched, using default action"},
		},
		"no match returns DefaultAction Retry": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{Action: ActionFail, OnConditions: []string{errormatch.ConditionOOMKilled}},
				},
			},
			runError: makeAppError(1, ""),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "no rule matched, using default action"},
		},
		"global cap exceeded": {
			globalMax: 5,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
			},
			runError: makeAppError(1, "crash"),
			// TotalRuns=6 means 5 retries have already happened (initial run
			// plus 5 re-leases). At globalMax=5 the cap is now reached.
			counts:   Counts{TotalRuns: 6},
			expected: Result{ShouldRetry: false, Reason: "global max retries exceeded (5/5)"},
		},
		"retry limit exceeded": {
			globalMax: 100,
			policy: &Policy{
				Name:          "test",
				RetryLimit:    3,
				DefaultAction: ActionRetry,
			},
			runError: makeAppError(1, "crash"),
			// Failures=4 means 3 retries have already happened (initial
			// failure plus 3 retry failures). At retryLimit=3 the cap is now
			// reached.
			counts:   Counts{Failures: 4, TotalRuns: 4},
			expected: Result{ShouldRetry: false, Reason: "policy retry limit exceeded (3/3)"},
		},
		"retry limit 0 means unlimited within global cap": {
			globalMax: 100,
			policy: &Policy{
				Name:          "test",
				RetryLimit:    0,
				DefaultAction: ActionRetry,
			},
			runError: makeAppError(1, "crash"),
			counts:   Counts{Failures: 50, TotalRuns: 50},
			expected: Result{ShouldRetry: true, Reason: "no rule matched, using default action"},
		},
		"nil error returns fail": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
			},
			runError: nil,
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "no error information available"},
		},
		"exit code match reads from ContainerError": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{
						Action:      ActionFail,
						OnExitCodes: &errormatch.ExitCodeMatcher{Operator: errormatch.ExitCodeOperatorIn, Values: []int32{42}},
					},
				},
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_PodError{
					PodError: &armadaevents.PodError{
						KubernetesReason: armadaevents.KubernetesReason_AppError,
						ContainerErrors:  []*armadaevents.ContainerError{{ExitCode: 42, Message: "custom exit"}},
					},
				},
			},
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "matched rule: Fail"},
		},
		"AND logic, all fields must match": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{
						Action:       ActionFail,
						OnConditions: []string{errormatch.ConditionAppError},
						OnExitCodes:  &errormatch.ExitCodeMatcher{Operator: errormatch.ExitCodeOperatorIn, Values: []int32{42}},
					},
				},
			},
			// Condition matches but exit code does not
			runError: makeAppError(1, ""),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: true, Reason: "no rule matched, using default action"},
		},
		"AND logic, both fields match": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
				Rules: []Rule{
					{
						Action:       ActionFail,
						OnConditions: []string{errormatch.ConditionAppError},
						OnExitCodes:  &errormatch.ExitCodeMatcher{Operator: errormatch.ExitCodeOperatorIn, Values: []int32{42}},
					},
				},
			},
			runError: makeAppError(42, ""),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "matched rule: Fail"},
		},
		"empty rules returns DefaultAction": {
			globalMax: 10,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules:         []Rule{},
			},
			runError: makeAppError(1, "crash"),
			counts:   Counts{TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "no rule matched, using default action"},
		},
		"globalMaxRetries 0 disables retries": {
			globalMax: 0,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
			},
			runError: makeAppError(1, "crash"),
			counts:   Counts{Failures: 1, TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "global max retries is 0, retries disabled"},
		},
		"globalMaxRetries 0 disables retries even with matching Retry rule": {
			globalMax: 0,
			policy: &Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{Action: ActionRetry, OnConditions: []string{errormatch.ConditionAppError}},
				},
			},
			runError: makeAppError(1, "crash"),
			counts:   Counts{Failures: 1, TotalRuns: 1},
			expected: Result{ShouldRetry: false, Reason: "global max retries is 0, retries disabled"},
		},
		"preemption error consumes Preemptions tally, not Failures": {
			globalMax: 100,
			policy: &Policy{
				Name:          "test",
				RetryLimit:    2,
				DefaultAction: ActionRetry,
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_JobRunPreemptedError{
					JobRunPreemptedError: &armadaevents.JobRunPreemptedError{},
				},
			},
			// Failures=3 is over the limit, but a preemption error is charged
			// against Preemptions=1 (0 preemption retries used), so it retries.
			counts:   Counts{Failures: 3, Preemptions: 1, TotalRuns: 4},
			expected: Result{ShouldRetry: true, Reason: "no rule matched, using default action"},
		},
		"preemption error over Preemptions limit does not retry": {
			globalMax: 100,
			policy: &Policy{
				Name:          "test",
				RetryLimit:    2,
				DefaultAction: ActionRetry,
			},
			runError: &armadaevents.Error{
				Reason: &armadaevents.Error_JobRunPreemptedError{
					JobRunPreemptedError: &armadaevents.JobRunPreemptedError{},
				},
			},
			counts:   Counts{Failures: 1, Preemptions: 3, TotalRuns: 4},
			expected: Result{ShouldRetry: false, Reason: "policy retry limit exceeded (2/2)"},
		},
		"failure error consumes Failures tally, not Preemptions": {
			globalMax: 100,
			policy: &Policy{
				Name:          "test",
				RetryLimit:    2,
				DefaultAction: ActionRetry,
			},
			runError: makeAppError(1, "crash"),
			// Preemptions=3 is over the limit, but a genuine failure is
			// charged against Failures=1 (0 failure retries used).
			counts:   Counts{Failures: 1, Preemptions: 3, TotalRuns: 4},
			expected: Result{ShouldRetry: true, Reason: "no rule matched, using default action"},
		},
		"failure error over Failures limit does not retry": {
			globalMax: 100,
			policy: &Policy{
				Name:          "test",
				RetryLimit:    2,
				DefaultAction: ActionRetry,
			},
			runError: makeAppError(1, "crash"),
			counts:   Counts{Failures: 3, Preemptions: 1, TotalRuns: 4},
			expected: Result{ShouldRetry: false, Reason: "policy retry limit exceeded (2/2)"},
		},
		"preemptions do not consume the global cap": {
			globalMax: 3,
			policy: &Policy{
				Name:          "test",
				RetryLimit:    0,
				DefaultAction: ActionRetry,
			},
			runError: makeAppError(1, "crash"),
			// A heavily-preempted job (4 preemptions) failing genuinely on
			// only its 2nd real attempt must still retry: the scheduler's own
			// preemptions must not exhaust the global budget. genuineRuns =
			// TotalRuns - Preemptions = 6 - 4 = 2, so retriesUsed = 1 < 3.
			counts:   Counts{Failures: 1, Preemptions: 4, TotalRuns: 6},
			expected: Result{ShouldRetry: true, Reason: "no rule matched, using default action"},
		},
		"global cap still trips on genuine failures": {
			globalMax: 3,
			policy: &Policy{
				Name:          "test",
				RetryLimit:    0,
				DefaultAction: ActionRetry,
			},
			runError: makeAppError(1, "crash"),
			// Four non-preemption runs -> retriesUsed = 3 >= 3.
			counts:   Counts{Failures: 3, Preemptions: 0, TotalRuns: 4},
			expected: Result{ShouldRetry: false, Reason: "global max retries exceeded (3/3)"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tc.policy = compilePolicy(t, tc.policy)
			engine := NewEngine(tc.globalMax)
			result := engine.Evaluate(tc.policy, tc.runError, tc.counts)
			if tc.expected.Decision == "" {
				tc.expected.Decision = expectedDecision(tc.expected)
			}
			assert.Equal(t, tc.expected, result)
		})
	}
}

// expectedDecision maps a Reason to the Decision that must accompany it, so
// every table case pins the typed decision without repeating it. A case can
// still set Decision explicitly to override the mapping.
func expectedDecision(r Result) Decision {
	switch {
	case r.Reason == reasonNoErrorAvailable:
		return ""
	case r.Reason == reasonRetriesDisabled:
		return DecisionFailGlobalLimit
	case strings.HasPrefix(r.Reason, "global max retries exceeded"):
		return DecisionFailGlobalLimit
	case strings.HasPrefix(r.Reason, "policy retry limit exceeded"):
		return DecisionFailPolicyLimit
	case r.Reason == reasonMatchFail:
		return DecisionFailRule
	case r.ShouldRetry:
		return DecisionRetry
	default:
		return DecisionFailDefault
	}
}

func TestCompileRules_InvalidRegex(t *testing.T) {
	rules := []Rule{
		{
			Action:               ActionFail,
			OnTerminationMessage: &errormatch.RegexMatcher{Pattern: "[invalid"},
		},
	}
	err := CompileRules(rules)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to compile termination message pattern")
}

func TestValidatePolicy(t *testing.T) {
	tests := map[string]struct {
		policy      Policy
		expectError string
	}{
		"valid policy with Fail default": {
			policy: Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{Action: ActionRetry, OnConditions: []string{"OOMKilled"}},
				},
			},
		},
		"valid policy with Retry default": {
			policy: Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
			},
		},
		"empty DefaultAction rejected": {
			policy:      Policy{Name: "test", DefaultAction: ""},
			expectError: "DefaultAction must be",
		},
		"unknown DefaultAction rejected": {
			policy:      Policy{Name: "test", DefaultAction: "Skip"},
			expectError: "DefaultAction must be",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidatePolicy(tc.policy)
			if tc.expectError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCompileRules_Validation(t *testing.T) {
	tests := map[string]struct {
		rules       []Rule
		expectError string
	}{
		"empty termination message pattern": {
			rules: []Rule{
				{
					Action:               ActionFail,
					OnTerminationMessage: &errormatch.RegexMatcher{Pattern: ""},
				},
			},
			expectError: "rule 0: OnTerminationMessage pattern must not be empty",
		},
		"empty rule with no match fields": {
			rules: []Rule{
				{Action: ActionFail},
			},
			expectError: "rule 0: must have at least one match field",
		},
		"invalid exit code operator": {
			rules: []Rule{
				{
					Action:      ActionFail,
					OnExitCodes: &errormatch.ExitCodeMatcher{Operator: "BadOp", Values: []int32{1}},
				},
			},
			expectError: "rule 0: OnExitCodes operator must be",
		},
		"empty exit code values": {
			rules: []Rule{
				{
					Action:      ActionFail,
					OnExitCodes: &errormatch.ExitCodeMatcher{Operator: errormatch.ExitCodeOperatorIn, Values: []int32{}},
				},
			},
			expectError: "rule 0: OnExitCodes values must not be empty",
		},
		"empty Action rejected": {
			rules: []Rule{
				{OnConditions: []string{errormatch.ConditionOOMKilled}},
			},
			expectError: `rule 0: Action must be "Fail" or "Retry", got ""`,
		},
		"unknown Action rejected": {
			rules: []Rule{
				{Action: "Skip", OnConditions: []string{errormatch.ConditionOOMKilled}},
			},
			expectError: `rule 0: Action must be "Fail" or "Retry", got "Skip"`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := CompileRules(tc.rules)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.expectError)
		})
	}
}

// TestMatchRule_UncompiledPatternFailsClosed ensures that a Rule with a regex
// matcher set but the corresponding compiled pattern nil (CompileRules was not
// called) does not vacuously match every error.
func TestMatchRule_UncompiledPatternFailsClosed(t *testing.T) {
	rule := &Rule{
		Action:               ActionFail,
		OnTerminationMessage: &errormatch.RegexMatcher{Pattern: ".*"},
	}
	assert.False(t, matchRule(rule, matchInput{isContainerFailure: true, terminationMessage: "anything"}),
		"uncompiled OnTerminationMessage must not vacuously match")
}

// TestEvaluate_OnTerminationMessageIgnoredForNonContainerFailures pins the
// isContainerFailure guard in matchRule: a wildcard OnTerminationMessage
// pattern must not fire for lease returns or preemptions. See the matchInput
// doc comment for why.
func TestEvaluate_OnTerminationMessageIgnoredForNonContainerFailures(t *testing.T) {
	tests := map[string]*armadaevents.Error{
		"PodLeaseReturned":     {Reason: &armadaevents.Error_PodLeaseReturned{PodLeaseReturned: &armadaevents.PodLeaseReturned{}}},
		"JobRunPreemptedError": {Reason: &armadaevents.Error_JobRunPreemptedError{JobRunPreemptedError: &armadaevents.JobRunPreemptedError{}}},
	}

	for name, runErr := range tests {
		t.Run(name, func(t *testing.T) {
			engine := NewEngine(10)
			policy := &Policy{
				Name:          "p",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{Action: ActionRetry, OnTerminationMessage: &errormatch.RegexMatcher{Pattern: ".*"}},
				},
			}
			require.NoError(t, CompileRules(policy.Rules))

			result := engine.Evaluate(policy, runErr, Counts{TotalRuns: 1})
			assert.False(t, result.ShouldRetry,
				"wildcard OnTerminationMessage must not match non-container failures; otherwise lease-return/preemption silently triggers retries")
			assert.Equal(t, "no rule matched, using default action", result.Reason)
		})
	}
}
