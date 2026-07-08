package retry

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/armadaproject/armada/pkg/armadaevents"
)

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

func TestEngine_Evaluate(t *testing.T) {
	tests := map[string]struct {
		globalMax uint
		policy    *Policy
		runError  *armadaevents.Error
		counts    Counts
		expected  Result
	}{
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

func TestValidatePolicy(t *testing.T) {
	tests := map[string]struct {
		policy      Policy
		expectError string
	}{
		"valid policy with Retry default": {
			policy: Policy{
				Name:          "test",
				DefaultAction: ActionRetry,
			},
		},
		"valid policy with OnCategory rule": {
			policy: Policy{
				Name:          "test",
				DefaultAction: ActionFail,
				Rules: []Rule{
					{Action: ActionRetry, OnCategory: "transient"},
				},
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
