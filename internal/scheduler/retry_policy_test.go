package scheduler

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	clock "k8s.io/utils/clock/testing"

	"github.com/armadaproject/armada/internal/common/armadacontext"
	"github.com/armadaproject/armada/internal/common/errormatch"
	protoutil "github.com/armadaproject/armada/internal/common/proto"
	"github.com/armadaproject/armada/internal/common/util"
	"github.com/armadaproject/armada/internal/leaderelection"
	schedulerconfig "github.com/armadaproject/armada/internal/scheduler/configuration"
	"github.com/armadaproject/armada/internal/scheduler/jobdb"
	"github.com/armadaproject/armada/internal/scheduler/pricing"
	"github.com/armadaproject/armada/internal/scheduler/retry"
	"github.com/armadaproject/armada/internal/scheduler/schedulerobjects"
	"github.com/armadaproject/armada/internal/scheduler/scheduling"
	schedulercontext "github.com/armadaproject/armada/internal/scheduler/scheduling/context"
	"github.com/armadaproject/armada/internal/scheduler/scheduling/runner"
	"github.com/armadaproject/armada/internal/scheduler/testfixtures"
	"github.com/armadaproject/armada/pkg/api"
	"github.com/armadaproject/armada/pkg/armadaevents"
)

// fakePolicyCache backs the retry-policy lookup with a fixed in-memory map.
// Tests use this to drive the engine's failure-handling path without going
// through the gRPC cache implementation.
type fakePolicyCache map[string]*retry.Policy

func (c fakePolicyCache) Get(name string) (*retry.Policy, bool) {
	if p, ok := c[name]; ok {
		return p, true
	}
	return nil, false
}

// makeRetryTestScheduler builds a Scheduler wired with the bare minimum
// dependencies needed to exercise the failure path through
// generateUpdateMessagesFromJob. The legacy maxAttemptedRuns and the queue
// cache are still needed even with the feature flag off, so we set them.
func makeRetryTestScheduler(t *testing.T, ffEnabled bool, policyCache retry.PolicyCache) *Scheduler {
	// GlobalMaxRetries 0 is the kill switch, so default to a cap high enough
	// that per-policy limits are the only gate in most tests.
	return makeRetryTestSchedulerWithGlobalMax(t, ffEnabled, policyCache, 10)
}

// makeRetryTestSchedulerWithGlobalMax is the same as makeRetryTestScheduler
// but lets the caller set the global retry cap, used to pin the contract
// that the cap counts failed runs only.
func makeRetryTestSchedulerWithGlobalMax(t *testing.T, ffEnabled bool, policyCache retry.PolicyCache, globalMaxRetries uint) *Scheduler {
	t.Helper()
	jobDb := testfixtures.NewJobDb(testfixtures.TestResourceListFactory)

	queueCache := &testQueueCache{queues: []*api.Queue{
		{Name: "testQueue", RetryPolicy: "test-policy"},
	}}

	sched, err := NewScheduler(
		jobDb,
		&testJobRepository{},
		&testExecutorRepository{},
		runner.NewSyncSchedulingRunner(&testSchedulingAlgo{}),
		leaderelection.NewStandaloneLeaderController(),
		newTestPublisher(),
		&testSubmitChecker{checkSuccess: true},
		&testGangValidator{validateSuccess: true},
		1*time.Second,
		5*time.Second,
		1*time.Hour,
		nil,
		maxNumberOfAttempts,
		nodeIdLabel,
		schedulerMetrics,
		pricing.NoopBidPriceProvider{},
		[]string{},
		queueCache,
		schedulerconfig.RetryPolicyConfig{Enabled: ffEnabled, GlobalMaxRetries: globalMaxRetries},
		policyCache,
	)
	require.NoError(t, err)
	sched.clock = clock.NewFakeClock(time.Now())
	return sched
}

// makeFailedJobForRetry builds a job with one failed run, ready for the
// failure-handling code path to evaluate.
func makeFailedJobForRetry(t *testing.T, sched *Scheduler) *jobdb.Job {
	t.Helper()
	jobId := util.NewULID()
	job := testfixtures.NewJob(
		jobId,
		"testJobset",
		"testQueue",
		uint32(10),
		toInternalSchedulingInfo(schedulingInfo),
		false, // queued
		1,     // queuedVersion
		false, false, false,
		1,    // created
		true, // validated
	)
	failedRun := sched.jobDb.CreateRun(
		uuid.NewString(),
		0, // index
		jobId,
		1, // creationTime
		"testExecutor",
		"testNodeId",
		"testNode",
		"testPool",
		nil,
		false, false, false, false, nil,
		false, // preempted
		false, // succeeded
		true,  // failed
		false, // cancelled
		nil, nil, nil, nil, nil,
		false, // returned (not lease-returned)
		false, // runAttempted
	)
	return job.WithUpdatedRun(failedRun)
}

// makePreemptRequestedJobForRetry builds a leased, preemptible job whose run
// has PreemptRequested=true and is not yet marked failed, mirroring the state
// the scheduler sees the cycle after `armadactl preempt job` lands.
func makePreemptRequestedJobForRetry(t *testing.T, sched *Scheduler) *jobdb.Job {
	t.Helper()
	jobId := util.NewULID()
	job := testfixtures.NewJob(
		jobId,
		"testJobset",
		"testQueue",
		uint32(10),
		toInternalSchedulingInfo(preemptibleSchedulingInfo),
		false, // queued
		1,     // queuedVersion
		false, false, false,
		1,    // created
		true, // validated
	)
	run := sched.jobDb.CreateRun(
		uuid.NewString(),
		0, // index
		jobId,
		1, // creationTime
		"testExecutor",
		"testNodeId",
		"testNode",
		"testPool",
		nil,
		true,  // leased
		false, // pending
		true,  // running
		true,  // preemptRequested
		nil,
		false, // preempted
		false, // succeeded
		false, // failed
		false, // cancelled
		nil, nil, nil, nil, nil,
		false, // returned
		false, // runAttempted
	)
	return job.WithUpdatedRun(run)
}

func containerErrorWithExitCode(code int32) *armadaevents.Error {
	return &armadaevents.Error{
		Reason: &armadaevents.Error_PodError{
			PodError: &armadaevents.PodError{
				ContainerErrors: []*armadaevents.ContainerError{
					{
						ExitCode: code,
						Message:  "exit",
					},
				},
				KubernetesReason: armadaevents.KubernetesReason_AppError,
			},
		},
	}
}

// categorizedError returns a container run error tagged with the given failure
// category, so tests can drive the category-only retry engine.
func categorizedError(category string) *armadaevents.Error {
	err := containerErrorWithExitCode(42)
	err.FailureCategory = category
	return err
}

// TestRetryPolicy_DefaultPolicyNameFallback pins that a job whose queue has no
// attached policy is evaluated against the configured default policy. This is
// the fleet-wide enablement path: one named default policy applies everywhere
// per-queue attachment is absent.
func TestRetryPolicy_DefaultPolicyNameFallback(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "default-policy",
		RetryLimit:    3,
		DefaultAction: api.RetryAction_RETRY_ACTION_RETRY,
	})
	require.NoError(t, err)

	sched := makeRetryTestScheduler(t, true, fakePolicyCache{"default-policy": policy})
	sched.retryPolicyConfig.DefaultPolicyName = "default-policy"

	job := makeFailedJobForRetry(t, sched)

	// An empty queue map means the queue has no attached policy, so resolution
	// must fall back to the default.
	shouldRetry, _, decided := sched.evaluateRetryPolicy(
		armadacontext.Background(), job, categorizedError("app-error"), map[string]string{})
	assert.True(t, decided, "default policy must produce a decision for an unattached queue")
	assert.True(t, shouldRetry, "default policy's Retry default action must apply")
}

func TestRetryPolicy_FFOn_RetryDecision(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    3,
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:     api.RetryAction_RETRY_ACTION_RETRY,
			OnCategory: "app-error",
		}},
	})
	require.NoError(t, err)

	sched := makeRetryTestScheduler(t, true, fakePolicyCache{"test-policy": policy})
	job := makeFailedJobForRetry(t, sched)

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	jobErrors := map[string]*armadaevents.Error{job.LatestRun().Id(): categorizedError("app-error")}
	queueRetryPolicies := map[string]string{"testQueue": "test-policy"}

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, jobErrors, queueRetryPolicies, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	hasRequeue, hasErrors := classifyEvents(events.Events)
	assert.True(t, hasRequeue, "FF on with matching retry rule must emit JobRequeued")
	assert.True(t, hasErrors, "FF on with retry decision must emit a non-terminal JobErrors so the api event stream surfaces the retry")
	assert.False(t, hasTerminalError(events.Events), "the emitted JobErrors must be non-terminal (Terminal=false) so it converts to JobFailedEvent{retryable=true}")

	// Simulate the next scheduling cycle creating a new run and assert the
	// run index advances. The scheduler-side WithNewRun derives index from
	// len(runsById), so the second run must be index 1.
	updatedJob := txn.GetById(job.Id())
	require.NotNil(t, updatedJob)
	relaunched := updatedJob.WithQueued(false).WithNewRun("testExecutor", "testNodeId", "testNode", "testPool", 0)
	assert.Equal(t, uint32(1), relaunched.LatestRun().Index(),
		"the executor pod-name suffix must change on retry: index 0 then index 1")
}

func TestRetryPolicy_FFOn_PolicyLimitCapsRetries(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    2,
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:     api.RetryAction_RETRY_ACTION_RETRY,
			OnCategory: "app-error",
		}},
	})
	require.NoError(t, err)

	sched := makeRetryTestScheduler(t, true, fakePolicyCache{"test-policy": policy})

	// retryLimit=2 means 2 retries are allowed. With three failed runs already
	// (initial + 2 retries), the policy cap must trip on the next evaluation.
	jobId := util.NewULID()
	job := testfixtures.NewJob(jobId, "testJobset", "testQueue", uint32(10), toInternalSchedulingInfo(schedulingInfo), false, 1, false, false, false, 1, true)
	for i := uint32(0); i < 3; i++ {
		failedRun := sched.jobDb.CreateRun(uuid.NewString(), i, jobId, 1, "testExecutor", "testNodeId", "testNode", "testPool", nil,
			false, false, false, false, nil, false, false, true, false, nil, nil, nil, nil, nil, false, false)
		job = job.WithUpdatedRun(failedRun)
	}
	require.Equal(t, uint32(3), job.FailureCount())

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	jobErrors := map[string]*armadaevents.Error{job.LatestRun().Id(): categorizedError("app-error")}
	queueRetryPolicies := map[string]string{"testQueue": "test-policy"}

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, jobErrors, queueRetryPolicies, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	hasRequeue, _ := classifyEvents(events.Events)
	assert.False(t, hasRequeue, "engine at retry limit must not emit JobRequeued")
	msg := terminalErrorMessage(events.Events)
	assert.Contains(t, msg, "Retry policy:",
		"terminal failure must surface the engine reason (operators rely on it for audit)")
	assert.Contains(t, msg, "policy retry limit",
		"reason must indicate the policy retry limit was hit")
}

func TestRetryPolicy_FFOn_TerminalFailPreservesFailureCategory(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    0, // unlimited at policy level
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:     api.RetryAction_RETRY_ACTION_FAIL,
			OnCategory: "ApplicationError",
		}},
	})
	require.NoError(t, err)

	sched := makeRetryTestScheduler(t, true, fakePolicyCache{"test-policy": policy})
	job := makeFailedJobForRetry(t, sched)

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	runError := containerErrorWithExitCode(42)
	runError.FailureCategory = "ApplicationError"
	runError.FailureSubcategory = "ExitCode42"
	jobErrors := map[string]*armadaevents.Error{job.LatestRun().Id(): runError}
	queueRetryPolicies := map[string]string{"testQueue": "test-policy"}

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, jobErrors, queueRetryPolicies, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	for _, e := range events.Events {
		if je := e.GetJobErrors(); je != nil {
			for _, errEv := range je.Errors {
				if !errEv.Terminal {
					continue
				}
				assert.Equal(t, "ApplicationError", errEv.FailureCategory,
					"policy-fail terminal error must carry the original FailureCategory")
				assert.Equal(t, "ExitCode42", errEv.FailureSubcategory,
					"policy-fail terminal error must carry the original FailureSubcategory")
				return
			}
		}
	}
	t.Fatal("expected a terminal JobErrors event")
}

func TestRetryPolicy_FFOn_MissingPolicyFallsThrough(t *testing.T) {
	sched := makeRetryTestScheduler(t, true, fakePolicyCache{}) // empty cache

	job := makeFailedJobForRetry(t, sched)
	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	jobErrors := map[string]*armadaevents.Error{job.LatestRun().Id(): categorizedError("app-error")}
	queueRetryPolicies := map[string]string{"testQueue": "test-policy"}

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, jobErrors, queueRetryPolicies, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	hasRequeue, hasErrors := classifyEvents(events.Events)
	assert.True(t, hasErrors, "missing policy must not crash; falls back to legacy terminal-failure path")
	assert.False(t, hasRequeue, "missing policy must not requeue; the legacy path terminally fails the job")
}

func classifyEvents(events []*armadaevents.EventSequence_Event) (hasRequeue bool, hasErrors bool) {
	for _, e := range events {
		if e.GetJobRequeued() != nil {
			hasRequeue = true
		}
		if e.GetJobErrors() != nil {
			hasErrors = true
		}
	}
	return
}

// hasTerminalError reports whether any emitted JobErrors carries a terminal
// Error. The engine retry path must emit only non-terminal errors so the
// api conversion stamps retryable=true on the resulting JobFailedEvent.
func hasTerminalError(events []*armadaevents.EventSequence_Event) bool {
	for _, e := range events {
		je := e.GetJobErrors()
		if je == nil {
			continue
		}
		for _, errEv := range je.Errors {
			if errEv.Terminal {
				return true
			}
		}
	}
	return false
}

func terminalErrorMessage(events []*armadaevents.EventSequence_Event) string {
	for _, e := range events {
		if je := e.GetJobErrors(); je != nil {
			for _, errEv := range je.Errors {
				if mre := errEv.GetMaxRunsExceeded(); mre != nil {
					return mre.Message
				}
			}
		}
	}
	return ""
}

// TestRetryPolicy_FFOn_GangJobSkipped pins gang skip: per-member retry would
// deadlock the QueuedGangIterator waiting for full cardinality or silently
// shrink the gang, so gang jobs always fall through to the legacy path.
func TestRetryPolicy_FFOn_GangJobSkipped(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    3,
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:     api.RetryAction_RETRY_ACTION_RETRY,
			OnCategory: "app-error",
		}},
	})
	require.NoError(t, err)

	sched := makeRetryTestScheduler(t, true, fakePolicyCache{"test-policy": policy})
	job := makeFailedJobForRetry(t, sched).WithGangInfo(jobdb.CreateGangInfo("gang-1", 3, ""))
	require.True(t, job.IsInGang(), "test fixture must produce a gang job")

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	jobErrors := map[string]*armadaevents.Error{job.LatestRun().Id(): categorizedError("app-error")}
	queueRetryPolicies := map[string]string{"testQueue": "test-policy"}

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, jobErrors, queueRetryPolicies, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	hasRequeue, hasErrors := classifyEvents(events.Events)
	assert.False(t, hasRequeue, "gang job must NOT be requeued by the engine")
	assert.True(t, hasErrors, "gang job must reach terminal failure via legacy path")
}

// TestRetryPolicy_FFOn_GlobalCapCountsAllRuns pins the global-cap contract:
// retriesUsed = TotalRuns-1 over ALL runs, so preempted runs consume global
// budget even though they do not consume the per-policy failure budget.
func TestRetryPolicy_FFOn_GlobalCapExcludesPreemptions(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    0, // no per-policy bound, global cap is the only gate
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:     api.RetryAction_RETRY_ACTION_RETRY,
			OnCategory: "app-error",
		}},
	})
	require.NoError(t, err)

	// Global cap of 2, three preempted runs plus one failed run. Preemptions
	// are the scheduler's own action and must not consume the global budget:
	// genuineRuns = 4 - 3 = 1, so the failed run is the job's first genuine
	// attempt and must still retry.
	sched := makeRetryTestSchedulerWithGlobalMax(t, true, fakePolicyCache{"test-policy": policy}, 2)

	jobId := util.NewULID()
	job := testfixtures.NewJob(
		jobId, "testJobset", "testQueue", uint32(10),
		toInternalSchedulingInfo(schedulingInfo),
		false, 1, false, false, false, 1, true,
	)
	// Add three preempted-but-not-failed runs, the scheduler-algo preemption
	// shape that creates new runs without burning policy-retry budget.
	for i := 0; i < 3; i++ {
		preemptedRun := sched.jobDb.CreateRun(
			uuid.NewString(), uint32(i), jobId, 1,
			"testExecutor", "testNodeId", "testNode", "testPool",
			nil, false, false, false, false, nil,
			true,  // preempted
			false, // succeeded
			false, // failed
			false, // cancelled
			nil, nil, nil, nil, nil,
			false, false,
		)
		job = job.WithUpdatedRun(preemptedRun)
	}
	// Add a single failed run that the engine will evaluate.
	failedRun := sched.jobDb.CreateRun(
		uuid.NewString(), 3, jobId, 1,
		"testExecutor", "testNodeId", "testNode", "testPool",
		nil, false, false, false, false, nil,
		false, // preempted
		false, // succeeded
		true,  // failed
		false, // cancelled
		nil, nil, nil, nil, nil,
		false, false,
	)
	job = job.WithUpdatedRun(failedRun)
	require.Equal(t, uint32(1), job.FailureCount(), "fixture must have exactly one failed run")
	require.Equal(t, 4, len(job.AllRuns()), "fixture must have four total runs")

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	jobErrors := map[string]*armadaevents.Error{job.LatestRun().Id(): categorizedError("app-error")}
	queueRetryPolicies := map[string]string{"testQueue": "test-policy"}

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, jobErrors, queueRetryPolicies, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	hasRequeue, _ := classifyEvents(events.Events)
	assert.True(t, hasRequeue,
		"preemptions must not consume the global cap: 3 preemptions + 1 failure with a cap of 2 must still retry")
}

// TestRetryPolicy_FFOn_GlobalMaxZeroDisablesRetries pins the kill switch: a
// GlobalMaxRetries of 0 means the engine never retries, even when a rule
// matches.
func TestRetryPolicy_FFOn_GlobalMaxZeroDisablesRetries(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    3,
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:     api.RetryAction_RETRY_ACTION_RETRY,
			OnCategory: "app-error",
		}},
	})
	require.NoError(t, err)

	sched := makeRetryTestSchedulerWithGlobalMax(t, true, fakePolicyCache{"test-policy": policy}, 0)
	job := makeFailedJobForRetry(t, sched)

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	jobErrors := map[string]*armadaevents.Error{job.LatestRun().Id(): categorizedError("app-error")}
	queueRetryPolicies := map[string]string{"testQueue": "test-policy"}

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, jobErrors, queueRetryPolicies, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	hasRequeue, _ := classifyEvents(events.Events)
	assert.False(t, hasRequeue, "GlobalMaxRetries 0 must never retry")
	assert.Contains(t, terminalErrorMessage(events.Events), "retries disabled",
		"terminal failure must surface the kill-switch reason")
}

// TestRetryPolicy_FFOn_GangSkipCounterOnlyWhenPolicyAttached pins the gang
// skip observability contract: the counter moves only when the gang job's
// queue actually has a retry policy attached, so operators are not alerted
// about queues that never opted into retry policies.
func TestRetryPolicy_FFOn_GangSkipCounterOnlyWhenPolicyAttached(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    3,
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:     api.RetryAction_RETRY_ACTION_RETRY,
			OnCategory: "app-error",
		}},
	})
	require.NoError(t, err)

	sched := makeRetryTestScheduler(t, true, fakePolicyCache{"test-policy": policy})
	counter := retryPolicyGangSkippedCounter

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()

	// No policy attached to the queue: the counter must not move.
	jobNoPolicy := makeFailedJobForRetry(t, sched).WithGangInfo(jobdb.CreateGangInfo("gang-1", 3, ""))
	require.NoError(t, txn.Upsert([]*jobdb.Job{jobNoPolicy}))
	before := testutil.ToFloat64(counter)
	_, err = sched.generateUpdateMessagesFromJob(
		armadacontext.Background(),
		jobNoPolicy,
		map[string]*armadaevents.Error{jobNoPolicy.LatestRun().Id(): categorizedError("app-error")},
		map[string]string{},
		txn,
	)
	require.NoError(t, err)
	assert.Equal(t, before, testutil.ToFloat64(counter),
		"gang skip counter must not move for queues without a retry policy")

	// Policy attached: exactly one increment for the skipped gang job.
	jobWithPolicy := makeFailedJobForRetry(t, sched).WithGangInfo(jobdb.CreateGangInfo("gang-2", 3, ""))
	require.NoError(t, txn.Upsert([]*jobdb.Job{jobWithPolicy}))
	_, err = sched.generateUpdateMessagesFromJob(
		armadacontext.Background(),
		jobWithPolicy,
		map[string]*armadaevents.Error{jobWithPolicy.LatestRun().Id(): categorizedError("app-error")},
		map[string]string{"testQueue": "test-policy"},
		txn,
	)
	require.NoError(t, err)
	assert.Equal(t, before+1, testutil.ToFloat64(counter),
		"gang skip counter must increment when the queue has a retry policy attached")
}

// makeRunningJobOnExecutor builds a leased, running, non-gang job on
// testExecutor, the state the lease-expiry sweep sees for jobs on an executor
// that stopped heartbeating.
func makeRunningJobOnExecutor(t *testing.T, sched *Scheduler) *jobdb.Job {
	t.Helper()
	jobId := util.NewULID()
	job := testfixtures.NewJob(
		jobId,
		"testJobset",
		"testQueue",
		uint32(10),
		toInternalSchedulingInfo(schedulingInfo),
		false, // queued
		1,     // queuedVersion
		false, false, false,
		1,    // created
		true, // validated
	)
	run := sched.jobDb.CreateRun(
		uuid.NewString(),
		0, // index
		jobId,
		1, // creationTime
		"testExecutor",
		"testNodeId",
		"testNode",
		"testPool",
		nil,
		true,  // leased
		false, // pending
		true,  // running
		false, // preemptRequested
		nil,
		false, // preempted
		false, // succeeded
		false, // failed
		false, // cancelled
		nil, nil, nil, nil, nil,
		false, // returned
		false, // runAttempted
	)
	return job.WithUpdatedRun(run)
}

func TestRetryPolicy_FFOn_LeaseExpiryRetriesWhenPolicyMatches(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    3,
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:        api.RetryAction_RETRY_ACTION_RETRY,
			OnCategory:    errormatch.CategoryInternal,
			OnSubcategory: errormatch.SubcategoryLeaseExpired,
		}},
	})
	require.NoError(t, err)

	sched := makeRetryTestScheduler(t, true, fakePolicyCache{"test-policy": policy})
	// testExecutor stopped heartbeating well past the 1h executorTimeout.
	sched.executorRepository = &testExecutorRepository{
		updateTimes: map[string]time.Time{"testExecutor": sched.clock.Now().Add(-2 * time.Hour)},
	}
	job := makeRunningJobOnExecutor(t, sched)

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	eventSequences, err := sched.expireJobsIfNecessary(armadacontext.Background(), txn)
	require.NoError(t, err)
	require.Len(t, eventSequences, 1)

	evs := eventSequences[0].Events
	require.Len(t, evs, 3, "lease-expiry retry must emit terminal JobRunErrors + non-terminal JobErrors + JobRequeued")

	jre := evs[0].GetJobRunErrors()
	require.NotNil(t, jre, "first event must be the terminal run-scoped JobRunErrors, which marks the run terminated in the DB so a returning executor cancels it")
	require.Len(t, jre.Errors, 1)
	assert.True(t, jre.Errors[0].Terminal, "the run error must be terminal")
	assert.NotNil(t, jre.Errors[0].GetLeaseExpired(), "the run error reason must be LeaseExpired")

	je := evs[1].GetJobErrors()
	require.NotNil(t, je, "second event must be JobErrors")
	require.Len(t, je.Errors, 1)
	assert.False(t, je.Errors[0].Terminal, "the JobErrors must be non-terminal so the api stream sees retryable=true")
	assert.NotNil(t, je.Errors[0].GetLeaseExpired(), "the error reason must be LeaseExpired")

	rq := evs[2].GetJobRequeued()
	require.NotNil(t, rq, "third event must be JobRequeued")
	assert.Equal(t, int32(2), rq.UpdateSequenceNumber, "JobRequeued must carry the bumped queued version")

	updated := txn.GetById(job.Id())
	require.NotNil(t, updated)
	assert.True(t, updated.Queued(), "job must be requeued instead of terminally failed")
	assert.False(t, updated.Failed(), "job must not be terminally failed on lease-expiry retry")
	assert.True(t, updated.LatestRun().Failed(), "the expired run must be marked failed")
}

func TestRetryPolicy_FFOn_LeaseExpiryTerminalWhenNoMatch(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    3,
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:     api.RetryAction_RETRY_ACTION_RETRY,
			OnCategory: "some-other-category",
		}},
	})
	require.NoError(t, err)

	sched := makeRetryTestScheduler(t, true, fakePolicyCache{"test-policy": policy})
	sched.executorRepository = &testExecutorRepository{
		updateTimes: map[string]time.Time{"testExecutor": sched.clock.Now().Add(-2 * time.Hour)},
	}
	job := makeRunningJobOnExecutor(t, sched)

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	eventSequences, err := sched.expireJobsIfNecessary(armadacontext.Background(), txn)
	require.NoError(t, err)
	require.Len(t, eventSequences, 1)

	// Exactly today's terminal shape: JobRunErrors + JobErrors, both terminal.
	expected := createEventsForFailedJob(
		job.Id(), job.LatestRun().Id(),
		&armadaevents.Error{
			Terminal:           true,
			FailureCategory:    errormatch.CategoryInternal,
			FailureSubcategory: errormatch.SubcategoryLeaseExpired,
			Reason: &armadaevents.Error_LeaseExpired{
				LeaseExpired: &armadaevents.LeaseExpired{},
			},
		},
		sched.clock.Now(),
	)
	assert.Equal(t, expected, eventSequences[0].Events,
		"a no-retry decision must produce exactly the legacy terminal failure events")

	updated := txn.GetById(job.Id())
	require.NotNil(t, updated)
	assert.True(t, updated.Failed(), "job must be terminally failed when the policy does not match")
}

// makeAttemptedFailedJobForRetry is makeFailedJobForRetry with
// runAttempted=true, so the node anti-affinity path applies on requeue.
func makeAttemptedFailedJobForRetry(t *testing.T, sched *Scheduler) *jobdb.Job {
	t.Helper()
	jobId := util.NewULID()
	job := testfixtures.NewJob(
		jobId,
		"testJobset",
		"testQueue",
		uint32(10),
		toInternalSchedulingInfo(schedulingInfo),
		false, // queued
		1,     // queuedVersion
		false, false, false,
		1,    // created
		true, // validated
	)
	failedRun := sched.jobDb.CreateRun(
		uuid.NewString(),
		0, // index
		jobId,
		1, // creationTime
		"testExecutor",
		"testNodeId",
		"testNode",
		"testPool",
		nil,
		false, false, false, false, nil,
		false, // preempted
		false, // succeeded
		true,  // failed
		false, // cancelled
		nil, nil, nil, nil, nil,
		false, // returned
		true,  // runAttempted
	)
	return job.WithUpdatedRun(failedRun)
}

// requeuedSchedulingInfo extracts the SchedulingInfo carried by the
// JobRequeued event, failing the test if no requeue event is present.
func requeuedSchedulingInfo(t *testing.T, events []*armadaevents.EventSequence_Event) *schedulerobjects.JobSchedulingInfo {
	t.Helper()
	for _, e := range events {
		if rq := e.GetJobRequeued(); rq != nil {
			return rq.SchedulingInfo
		}
	}
	t.Fatal("expected a JobRequeued event")
	return nil
}

func nodeAntiAffinityValues(si *schedulerobjects.JobSchedulingInfo) []string {
	req := si.GetPodRequirements()
	if req == nil || req.Affinity == nil || req.Affinity.NodeAffinity == nil ||
		req.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return nil
	}
	for _, term := range req.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		for _, me := range term.MatchExpressions {
			if me.Key == nodeIdLabel && me.Operator == v1.NodeSelectorOpNotIn {
				return me.Values
			}
		}
	}
	return nil
}

// TestRetryPolicy_FFOn_EngineRetryAddsNodeAntiAffinity pins that an
// engine-driven retry of an attempted run requeues with the anti-affinity for
// the failed node in the emitted scheduling info, like the legacy path does.
func TestRetryPolicy_FFOn_EngineRetryAddsNodeAntiAffinity(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    3,
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:     api.RetryAction_RETRY_ACTION_RETRY,
			OnCategory: "app-error",
		}},
	})
	require.NoError(t, err)

	sched := makeRetryTestScheduler(t, true, fakePolicyCache{"test-policy": policy})
	job := makeAttemptedFailedJobForRetry(t, sched)

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	jobErrors := map[string]*armadaevents.Error{job.LatestRun().Id(): categorizedError("app-error")}
	queueRetryPolicies := map[string]string{"testQueue": "test-policy"}

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, jobErrors, queueRetryPolicies, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	si := requeuedSchedulingInfo(t, events.Events)
	assert.Equal(t, []string{"testNode"}, nodeAntiAffinityValues(si),
		"the requeued scheduling info must carry a NotIn anti-affinity for the node the run failed on")
}

// TestRetryPolicy_FFOn_EngineRetryRequeuesWithoutAntiAffinityWhenUnschedulable
// pins the engine-path fallback: when adding the anti-affinity would make the
// job unschedulable (e.g. a single-node cluster), the retry still happens,
// just without the anti-affinity. The legacy path fails the job instead.
func TestRetryPolicy_FFOn_EngineRetryRequeuesWithoutAntiAffinityWhenUnschedulable(t *testing.T) {
	policy, err := retry.ConvertPolicy(&api.RetryPolicy{
		Name:          "test-policy",
		RetryLimit:    3,
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{{
			Action:     api.RetryAction_RETRY_ACTION_RETRY,
			OnCategory: "app-error",
		}},
	})
	require.NoError(t, err)

	sched := makeRetryTestScheduler(t, true, fakePolicyCache{"test-policy": policy})
	sched.submitChecker = &testSubmitChecker{checkSuccess: false}
	job := makeAttemptedFailedJobForRetry(t, sched)

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	jobErrors := map[string]*armadaevents.Error{job.LatestRun().Id(): categorizedError("app-error")}
	queueRetryPolicies := map[string]string{"testQueue": "test-policy"}

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, jobErrors, queueRetryPolicies, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	hasRequeue, _ := classifyEvents(events.Events)
	assert.True(t, hasRequeue, "engine retry must still requeue when the anti-affinity is unschedulable")
	si := requeuedSchedulingInfo(t, events.Events)
	assert.Empty(t, nodeAntiAffinityValues(si),
		"the fallback requeue must not carry the unschedulable anti-affinity")
}

// The three flag-off identity tests below prove, event by event, that with
// retryPolicy.enabled=false the scheduler produces exactly the pre-feature
// event sequences for a failed run, an API preemption, and an algo preemption.

func TestRetryPolicy_FFOff_FailedRunIdentity(t *testing.T) {
	sched := makeRetryTestScheduler(t, false, fakePolicyCache{})
	job := makeFailedJobForRetry(t, sched)

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	runError := containerErrorWithExitCode(42)
	jobErrors := map[string]*armadaevents.Error{job.LatestRun().Id(): runError}

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, jobErrors, nil, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	// Upstream emits exactly one JobErrors event carrying the run error
	// verbatim for a failed, non-returned run.
	expected := []*armadaevents.EventSequence_Event{
		{
			Created: protoutil.ToTimestamp(sched.clock.Now()),
			Event: &armadaevents.EventSequence_Event_JobErrors{
				JobErrors: &armadaevents.JobErrors{
					JobId:  job.Id(),
					Errors: []*armadaevents.Error{runError},
				},
			},
		},
	}
	assert.Equal(t, expected, events.Events)

	updated := txn.GetById(job.Id())
	require.NotNil(t, updated)
	assert.True(t, updated.Failed())
	assert.False(t, updated.Queued())
}

func TestRetryPolicy_FFOff_ApiPreemptionIdentity(t *testing.T) {
	sched := makeRetryTestScheduler(t, false, fakePolicyCache{})
	job := makePreemptRequestedJobForRetry(t, sched)

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	events, err := sched.generateUpdateMessagesFromJob(armadacontext.Background(), job, nil, nil, txn)
	require.NoError(t, err)
	require.NotNil(t, events)

	expected := createEventsForPreemptedJob(
		job.Id(), job.LatestRun().Id(), "",
		"Preempted - preemption requested via API",
		sched.clock.Now(),
	)
	assert.Equal(t, expected, events.Events)

	updated := txn.GetById(job.Id())
	require.NotNil(t, updated)
	assert.True(t, updated.Failed())
	assert.True(t, updated.LatestRun().Failed())
	assert.False(t, updated.LatestRun().Preempted(),
		"flag off must not pre-mark the run preempted; that lands via the ingester as before")
}

func TestRetryPolicy_FFOff_AlgoPreemptionIdentity(t *testing.T) {
	sched := makeRetryTestScheduler(t, false, fakePolicyCache{})

	jobId := util.NewULID()
	job := testfixtures.NewJob(
		jobId, "testJobset", "testQueue", uint32(10),
		toInternalSchedulingInfo(preemptibleSchedulingInfo),
		false, 1, false, false, false, 1, true,
	)
	run := sched.jobDb.CreateRun(
		uuid.NewString(), 0, jobId, 1,
		"testExecutor", "testNodeId", "testNode", "testPool", nil,
		true, false, true, false, nil,
		false, false, true, false, // failed=true (algo set)
		nil, nil, nil, nil, nil,
		false, false,
	)
	job = job.WithUpdatedRun(run).WithFailed(true)

	txn := sched.jobDb.WriteTxn()
	defer txn.Abort()
	require.NoError(t, txn.Upsert([]*jobdb.Job{job}))

	jctx := schedulercontext.JobSchedulingContextsFromJobs([]*jobdb.Job{job})[0]
	jctx.PreemptionDescription = "preempted by higher priority gang"
	result := &scheduling.SchedulerResult{
		PoolResults: []*scheduling.PoolSchedulingResult{
			{SchedulingResult: &scheduling.SchedulingResult{PreemptedJobs: []*schedulercontext.JobSchedulingContext{jctx}}},
		},
	}

	retried, err := sched.applyRetryPolicyToAlgoPreemptions(armadacontext.Background(), txn, result, nil)
	require.NoError(t, err)
	require.Empty(t, retried, "flag off must never override algo preemptions")

	eventSequences, err := EventsFromSchedulerResult(result, retried, sched.clock.Now())
	require.NoError(t, err)
	require.Len(t, eventSequences, 1)

	expected := createEventsForPreemptedJob(
		jobId, run.Id(), "",
		"preempted by higher priority gang",
		sched.clock.Now(),
	)
	assert.Equal(t, expected, eventSequences[0].Events)

	updated := txn.GetById(jobId)
	require.NotNil(t, updated)
	assert.True(t, updated.Failed(), "flag off must leave the algo's terminal fail untouched")
	assert.False(t, updated.LatestRun().Preempted(),
		"flag off must not mutate the run's preempted flag")
}
