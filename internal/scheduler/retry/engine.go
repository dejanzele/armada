package retry

import (
	"fmt"

	"github.com/armadaproject/armada/pkg/armadaevents"
)

const (
	reasonMatchFail        = "matched rule: Fail"
	reasonMatchRetry       = "matched rule: Retry"
	reasonDefault          = "no rule matched, using default action"
	reasonRetriesDisabled  = "global max retries is 0, retries disabled"
	reasonNoErrorAvailable = "no error information available"
)

// Engine evaluates retry policies against job run errors to decide whether
// a job should be retried or permanently failed.
type Engine struct {
	globalMaxRetries uint
}

// NewEngine creates a retry engine with the given hard upper limit on retries.
func NewEngine(globalMaxRetries uint) *Engine {
	return &Engine{globalMaxRetries: globalMaxRetries}
}

// Counts holds the per-job run tallies the engine needs to enforce retry
// limits. All tallies include the run currently being evaluated.
type Counts struct {
	// Failures is the number of failed runs excluding preempted ones.
	Failures uint32
	// Preemptions is the number of preempted runs.
	Preemptions uint32
	// TotalRuns is the number of runs of any kind for the job, including
	// legacy lease returns that never consulted the retry engine.
	TotalRuns uint
}

// Evaluate applies the policy rules to runError and returns a retry decision.
//
// All limits count retries, not attempts: RetryLimit=3 allows 3 retries after
// the initial run, i.e. 4 attempts total. globalMaxRetries=0 disables all
// retries (kill switch). There is no unlimited setting for the global cap.
// RetryLimit=0 means no per-policy bound, so only the global cap applies.
//
// The global cap and the per-policy limit both exclude preemptions. A
// preemption is the scheduler's own action, not something the job did, so it
// must not consume either retry budget: otherwise a preemptible job in a
// contended cluster is terminally failed by the scheduler's own preemptions,
// denied genuine-failure retries it is well within its policy budget for. The
// per-policy limit picks the tally matching the error type (preemption retries
// consume Preemptions, genuine failures consume Failures); the global cap
// counts every non-preemption run.
//
// policy must not be nil. runError may be nil (treated as "no decision").
func (e *Engine) Evaluate(policy *Policy, runError *armadaevents.Error, counts Counts) Result {
	if runError == nil {
		return Result{ShouldRetry: false, Reason: reasonNoErrorAvailable}
	}

	if e.globalMaxRetries == 0 {
		return Result{ShouldRetry: false, Reason: reasonRetriesDisabled, Decision: DecisionFailGlobalLimit}
	}
	// Count only non-preemption runs against the global cap.
	genuineRuns := counts.TotalRuns
	if uint(counts.Preemptions) < genuineRuns {
		genuineRuns -= uint(counts.Preemptions)
	} else {
		genuineRuns = 0
	}
	retriesUsed := uint(0)
	if genuineRuns > 0 {
		retriesUsed = genuineRuns - 1
	}
	if retriesUsed >= e.globalMaxRetries {
		return Result{
			ShouldRetry: false,
			Reason:      fmt.Sprintf("global max retries exceeded (%d/%d)", retriesUsed, e.globalMaxRetries),
			Decision:    DecisionFailGlobalLimit,
		}
	}

	matched := matchRules(policy.Rules, matchInput{
		category:    extractCategory(runError),
		subcategory: extractSubcategory(runError),
	})

	action, reason := policy.DefaultAction, reasonDefault
	if matched != nil {
		action = matched.Action
		reason = reasonMatchRetry
		if action == ActionFail {
			reason = reasonMatchFail
		}
	}

	if action == ActionFail {
		decision := DecisionFailDefault
		if matched != nil {
			decision = DecisionFailRule
		}
		return Result{ShouldRetry: false, Reason: reason, Decision: decision}
	}

	// Preemptions and genuine failures draw from separate budgets: a job
	// preempted N times has not "failed" N times, so a preemption retry is
	// charged against the preemption tally only.
	tally := counts.Failures
	if _, isPreemption := runError.Reason.(*armadaevents.Error_JobRunPreemptedError); isPreemption {
		tally = counts.Preemptions
	}
	policyRetriesUsed := uint32(0)
	if tally > 0 {
		policyRetriesUsed = tally - 1
	}
	if policy.RetryLimit > 0 && policyRetriesUsed >= policy.RetryLimit {
		return Result{
			ShouldRetry: false,
			Reason:      fmt.Sprintf("policy retry limit exceeded (%d/%d)", policyRetriesUsed, policy.RetryLimit),
			Decision:    DecisionFailPolicyLimit,
		}
	}

	return Result{ShouldRetry: true, Reason: reason, Decision: DecisionRetry}
}
