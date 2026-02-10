package retry

import (
	"github.com/sirupsen/logrus"

	"github.com/armadaproject/armada/internal/scheduler/configuration"
	"github.com/armadaproject/armada/pkg/armadaevents"
)

// Engine evaluates retry policies for job failures.
type Engine struct {
	resolver *PolicyResolver
	matcher  *Matcher
	log      *logrus.Entry
}

// NewEngine creates a new retry policy Engine.
// Returns an error if regex patterns in the configuration fail to compile.
func NewEngine(config configuration.RetryPolicyConfig) (*Engine, error) {
	resolver, err := NewPolicyResolver(config)
	if err != nil {
		return nil, err
	}
	return &Engine{
		resolver: resolver,
		matcher:  NewMatcher(),
		log:      logrus.WithField("component", "RetryPolicyEngine"),
	}, nil
}

// Enabled returns true if the retry policy system is enabled.
func (e *Engine) Enabled() bool {
	return e.resolver.Enabled()
}

// GlobalMaxRetries returns the global maximum retries limit.
func (e *Engine) GlobalMaxRetries() uint {
	return e.resolver.GlobalMaxRetries()
}

// Result contains the result of evaluating a failure against the retry policy.
type Result struct {
	// Action to take (Fail or Retry).
	Action configuration.Action
	// ShouldRequeue is true if the job should be requeued.
	ShouldRequeue bool
	// IncrementFailureCount is true if the failure count should be incremented.
	IncrementFailureCount bool
}

// Evaluate evaluates the failure against the retry policy and returns the action to take.
func (e *Engine) Evaluate(
	queueRetryPolicy string,
	failureInfo *armadaevents.FailureInfo,
	currentFailureCount uint32,
	totalRuns uint,
) Result {
	if !e.Enabled() {
		// Feature disabled - fall back to pod check's decision
		return e.fallbackToPodCheck(failureInfo)
	}

	// Safety check: enforce global max retries
	if totalRuns >= e.GlobalMaxRetries() {
		e.log.WithFields(logrus.Fields{
			"totalRuns":        totalRuns,
			"globalMaxRetries": e.GlobalMaxRetries(),
		}).Debug("Global max retries reached")
		return Result{
			Action:                configuration.ActionFail,
			ShouldRequeue:         false,
			IncrementFailureCount: false,
		}
	}

	// Get the policy for this queue
	policy := e.resolver.GetPolicy(queueRetryPolicy)
	if policy == nil {
		return e.fallbackToPodCheck(failureInfo)
	}

	// Match failure against policy rules
	action := e.matcher.Match(failureInfo, policy.Rules)

	e.log.WithFields(logrus.Fields{
		"queueRetryPolicy":    queueRetryPolicy,
		"action":              action,
		"condition":           failureInfo.GetCondition(),
		"exitCode":            failureInfo.GetExitCode(),
		"terminationMessage":  failureInfo.GetTerminationMessage(),
		"currentFailureCount": currentFailureCount,
		"policyRetryLimit":    policy.RetryLimit,
	}).Info("Evaluated retry policy")

	return e.applyAction(action, policy, currentFailureCount)
}

// fallbackToPodCheck returns the action based on the pod check's decision.
// Used when retry policy is disabled or policy is not found.
func (e *Engine) fallbackToPodCheck(failureInfo *armadaevents.FailureInfo) Result {
	if failureInfo == nil || !failureInfo.PodCheckRetryable {
		return failResult()
	}
	return Result{
		Action:                configuration.ActionRetry,
		ShouldRequeue:         true,
		IncrementFailureCount: true,
	}
}

// applyAction determines the result based on the matched action.
func (e *Engine) applyAction(
	action configuration.Action,
	policy *configuration.Policy,
	currentFailureCount uint32,
) Result {
	if action != configuration.ActionRetry {
		return failResult()
	}

	// Retry: requeue if under limit, increment failure count
	if uint(currentFailureCount) >= policy.RetryLimit {
		e.log.WithFields(logrus.Fields{
			"currentFailureCount": currentFailureCount,
			"retryLimit":          policy.RetryLimit,
		}).Debug("Retry limit exceeded")
		return failResult()
	}

	return Result{
		Action:                configuration.ActionRetry,
		ShouldRequeue:         true,
		IncrementFailureCount: true,
	}
}

// failResult returns a Result indicating the job should fail without retry.
func failResult() Result {
	return Result{
		Action:                configuration.ActionFail,
		ShouldRequeue:         false,
		IncrementFailureCount: false,
	}
}
