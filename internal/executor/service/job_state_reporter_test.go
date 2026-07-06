package service

import (
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/armadaproject/armada/internal/executor/categorizer"
	fakecontext "github.com/armadaproject/armada/internal/executor/context/fake"
	"github.com/armadaproject/armada/internal/executor/domain"
	"github.com/armadaproject/armada/internal/executor/reporter"
	"github.com/armadaproject/armada/internal/executor/reporter/mocks"
	"github.com/armadaproject/armada/internal/executor/util"
	"github.com/armadaproject/armada/pkg/armadaevents"
)

func TestRequiresIngressToBeReported_FalseWhenIngressHasBeenReported(t *testing.T) {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				domain.HasIngress:      "true",
				domain.IngressReported: time.Now().String(),
			},
		},
	}
	assert.False(t, requiresIngressToBeReported(pod))
}

func TestRequiresIngressToBeReported_FalseWhenNonIngressPod(t *testing.T) {
	pod := &v1.Pod{}
	assert.False(t, requiresIngressToBeReported(pod))
}

func TestRequiresIngressToBeReported_TrueWhenHasIngressButNotIngressReportedAnnotation(t *testing.T) {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{domain.HasIngress: "true"},
		},
	}
	assert.True(t, requiresIngressToBeReported(pod))
}

func TestJobStateReporter_HandlesPodAddEvents(t *testing.T) {
	type podAddEventTest struct {
		pod              *v1.Pod
		expectEvent      bool
		expectAnnotation bool
		expectedType     reflect.Type
	}

	tests := []podAddEventTest{
		{pod: &v1.Pod{Status: v1.PodStatus{Phase: v1.PodPending}}, expectAnnotation: false, expectEvent: false},
		{pod: makeTestPod(v1.PodStatus{Phase: v1.PodPending}), expectEvent: true, expectAnnotation: false, expectedType: reflect.TypeOf(&armadaevents.EventSequence_Event_JobRunAssigned{})},
		{pod: makeTestPod(v1.PodStatus{Phase: v1.PodRunning}), expectEvent: true, expectAnnotation: true, expectedType: reflect.TypeOf(&armadaevents.EventSequence_Event_JobRunRunning{})},
		{pod: makeTestPod(v1.PodStatus{Phase: v1.PodSucceeded}), expectEvent: true, expectAnnotation: true, expectedType: reflect.TypeOf(&armadaevents.EventSequence_Event_JobRunSucceeded{})},
		{pod: makeTestPod(v1.PodStatus{Phase: v1.PodFailed}), expectEvent: true, expectAnnotation: true, expectedType: reflect.TypeOf(&armadaevents.EventSequence_Event_JobRunErrors{})},
	}

	for _, test := range tests {
		_, _, eventReporter, fakeClusterContext := setUpJobStateReporterTest(t)

		addPod(t, fakeClusterContext, test.pod)
		fakeClusterContext.SimulatePodAddEvent(test.pod)
		time.Sleep(time.Millisecond * 100) // Give time for async routine to process message

		if test.expectEvent {
			assertExpectedEvents(t, test.pod, eventReporter.GetReceivedEvents(), test.expectedType)
		} else {
			assert.Len(t, eventReporter.GetReceivedEvents(), 0)
		}

		if test.expectAnnotation {
			assertExpectedAnnotations(t, test.pod, fakeClusterContext)
		} else {
			assert.Equal(t, 0, fakeClusterContext.CountPodsWithAddedAnnotations())
		}
	}
}

func TestJobStateReporter_HandlesPodUpdateEvents(t *testing.T) {
	type podAddEventTest struct {
		before       v1.PodPhase
		after        v1.PodPhase
		expectEvent  bool
		expectedType reflect.Type
	}

	tests := []podAddEventTest{
		{before: v1.PodPending, after: v1.PodPending, expectEvent: false}, // No update sent if pod doesn't change phase
		{before: v1.PodPending, after: v1.PodRunning, expectEvent: true, expectedType: reflect.TypeOf(&armadaevents.EventSequence_Event_JobRunRunning{})},
		{before: v1.PodPending, after: v1.PodFailed, expectEvent: true, expectedType: reflect.TypeOf(&armadaevents.EventSequence_Event_JobRunErrors{})},
		{before: v1.PodPending, after: v1.PodSucceeded, expectEvent: true, expectedType: reflect.TypeOf(&armadaevents.EventSequence_Event_JobRunSucceeded{})},

		{before: v1.PodRunning, after: v1.PodRunning, expectEvent: false}, // No update sent if pod doesn't change phase
		{before: v1.PodRunning, after: v1.PodFailed, expectEvent: true, expectedType: reflect.TypeOf(&armadaevents.EventSequence_Event_JobRunErrors{})},
		{before: v1.PodRunning, after: v1.PodSucceeded, expectEvent: true, expectedType: reflect.TypeOf(&armadaevents.EventSequence_Event_JobRunSucceeded{})},

		{before: v1.PodFailed, after: v1.PodFailed, expectEvent: false},       // No update sent if pod doesn't change phase
		{before: v1.PodSucceeded, after: v1.PodSucceeded, expectEvent: false}, // No update sent if pod doesn't change phase
	}

	for _, test := range tests {
		_, _, eventReporter, fakeClusterContext := setUpJobStateReporterTest(t)

		before := makeTestPod(v1.PodStatus{Phase: test.before})
		after := copyWithUpdatedPhase(before, test.after)

		addPod(t, fakeClusterContext, before)
		fakeClusterContext.SimulateUpdateAddEvent(before, after)
		time.Sleep(time.Millisecond * 100) // Give time for async routine to process message

		if test.expectEvent {
			assertExpectedEvents(t, before, eventReporter.GetReceivedEvents(), test.expectedType)
			assertExpectedAnnotations(t, after, fakeClusterContext)
		} else {
			assert.Len(t, eventReporter.GetReceivedEvents(), 0)
			assert.Equal(t, 0, fakeClusterContext.CountPodsWithAddedAnnotations())
		}
	}
}

func TestJobStateReporter_HandlesPodUpdateEvents_IgnoreUnmanagedPods(t *testing.T) {
	_, _, eventReporter, fakeClusterContext := setUpJobStateReporterTest(t)

	before := &v1.Pod{Status: v1.PodStatus{Phase: v1.PodPending}}
	after := &v1.Pod{Status: v1.PodStatus{Phase: v1.PodRunning}}

	fakeClusterContext.SimulateUpdateAddEvent(before, after)
	time.Sleep(time.Millisecond * 100) // Give time for async routine to process message

	assert.Len(t, eventReporter.GetReceivedEvents(), 0)
}

func TestJobStateReporter_PodFailed_EmitsEventWhenIssueHandlerErrors(t *testing.T) {
	_, _, eventReporter, fakeClusterContext := setUpJobStateReporterTestWithIssueHandler(
		t, &stubIssueHandler{detectAndRegisterFailedPodIssueError: fmt.Errorf("error")},
	)

	pod := makeFailedPodWithExitCode(t, 42)
	addPod(t, fakeClusterContext, pod)
	fakeClusterContext.SimulatePodAddEvent(pod)
	time.Sleep(time.Millisecond * 100)

	require.Len(t, eventReporter.GetReceivedEvents(), 1, "a handler error must not swallow the terminal failure")
	assertExpectedEvents(t, pod, eventReporter.GetReceivedEvents(), reflect.TypeOf(&armadaevents.EventSequence_Event_JobRunErrors{}))
}

func setUpJobStateReporterTest(t *testing.T) (*JobStateReporter, *stubIssueHandler, *mocks.FakeEventReporter, *fakecontext.SyncFakeClusterContext) {
	return setUpJobStateReporterTestWithIssueHandler(t, &stubIssueHandler{})
}

func setUpJobStateReporterTestWithIssueHandler(
	t *testing.T,
	issueHandler *stubIssueHandler,
) (*JobStateReporter, *stubIssueHandler, *mocks.FakeEventReporter, *fakecontext.SyncFakeClusterContext) {
	fakeClusterContext := fakecontext.NewSyncFakeClusterContext()
	eventReporter := mocks.NewFakeEventReporter()
	jobStateReporter, err := NewJobStateReporter(fakeClusterContext, eventReporter, issueHandler)
	require.NoError(t, err)
	return jobStateReporter, issueHandler, eventReporter, fakeClusterContext
}

func makeFailedPodWithExitCode(t *testing.T, exitCode int32) *v1.Pod {
	t.Helper()
	pod := makeTestPod(v1.PodStatus{
		Phase: v1.PodFailed,
		ContainerStatuses: []v1.ContainerStatus{
			{
				Name: "main",
				State: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{ExitCode: exitCode, Reason: "Error"},
				},
			},
		},
	})
	return pod
}

// The metric counter itself is tested in the metrics package.
// These tests cover the emission gating that governs when the counter is
// advanced: a JobFailedEvent must be emitted (not a ReturnLease or a no-op).
// Classification happens once, inside the issue handler. The reporter builds
// the Failed event from the result the handler returns.

func TestJobStateReporter_PodFailed_EmitsFailedEventFromHandlerClassification(t *testing.T) {
	stub := &stubIssueHandler{
		classifyResult: categorizer.ClassifyResult{Category: "jsr-emit-cat", Subcategory: "jsr-emit-sub"},
	}
	_, _, eventReporter, fakeClusterContext := setUpJobStateReporterTestWithIssueHandler(t, stub)

	pod := makeFailedPodWithExitCode(t, 42)
	addPod(t, fakeClusterContext, pod)
	fakeClusterContext.SimulatePodAddEvent(pod)
	time.Sleep(time.Millisecond * 100)

	require.Len(t, eventReporter.GetReceivedEvents(), 1)
	failedEvent, ok := eventReporter.GetReceivedEvents()[0].Event.Events[0].Event.(*armadaevents.EventSequence_Event_JobRunErrors)
	require.True(t, ok)
	assert.Equal(t, "jsr-emit-cat", failedEvent.JobRunErrors.Errors[0].GetFailureCategory())
	assert.Equal(t, "jsr-emit-sub", failedEvent.JobRunErrors.Errors[0].GetFailureSubcategory())
	assert.Equal(t, int32(1), stub.detectCalls.Load(), "classification must happen exactly once, in the issue handler")
}

func TestJobStateReporter_PodFailed_SuppressesEmissionWhenRetryableIssueRegistered(t *testing.T) {
	_, _, eventReporter, fakeClusterContext := setUpJobStateReporterTestWithIssueHandler(
		t,
		&stubIssueHandler{detectAndRegisterFailedPodIssueResult: true},
	)

	pod := makeFailedPodWithExitCode(t, 42)
	addPod(t, fakeClusterContext, pod)
	fakeClusterContext.SimulatePodAddEvent(pod)
	time.Sleep(time.Millisecond * 100)

	assert.Len(t, eventReporter.GetReceivedEvents(), 0, "retryable issue path emits ReturnLease, not JobFailed")
}

func TestJobStateReporter_PodFailed_SuppressesEmissionWhenIssueAlreadyExists(t *testing.T) {
	pod := makeFailedPodWithExitCode(t, 42)
	stub := &stubIssueHandler{runIdsWithIssues: map[string]bool{util.ExtractJobRunId(pod): true}}
	_, _, eventReporter, fakeClusterContext := setUpJobStateReporterTestWithIssueHandler(t, stub)

	addPod(t, fakeClusterContext, pod)
	fakeClusterContext.SimulatePodAddEvent(pod)
	time.Sleep(time.Millisecond * 100)

	assert.Len(t, eventReporter.GetReceivedEvents(), 0, "run already owned by issue handler - no direct emission from this path")
	assert.Equal(t, int32(0), stub.detectCalls.Load(), "no detection attempt when the run already has an issue")
}

func TestJobStateReporter_PodFailed_DropsEventWhenReporterErrors(t *testing.T) {
	_, _, eventReporter, fakeClusterContext := setUpJobStateReporterTestWithIssueHandler(t, &stubIssueHandler{})
	eventReporter.ErrorOnReport = true

	pod := makeFailedPodWithExitCode(t, 42)
	addPod(t, fakeClusterContext, pod)
	fakeClusterContext.SimulatePodAddEvent(pod)
	time.Sleep(time.Millisecond * 100)

	assert.Len(t, eventReporter.GetReceivedEvents(), 0, "event is dropped when reporter errors - counter increment is gated behind successful emission")
}

func TestJobStateReporter_PodFailed_EmitsEventWithEmptyCategoryWhenClassificationDisabled(t *testing.T) {
	_, _, eventReporter, fakeClusterContext := setUpJobStateReporterTestWithIssueHandler(t, &stubIssueHandler{})

	pod := makeFailedPodWithExitCode(t, 42)
	addPod(t, fakeClusterContext, pod)
	fakeClusterContext.SimulatePodAddEvent(pod)
	time.Sleep(time.Millisecond * 100)

	require.Len(t, eventReporter.GetReceivedEvents(), 1, "event still emitted with empty category/subcategory when classification is disabled")
	failedEvent, ok := eventReporter.GetReceivedEvents()[0].Event.Events[0].Event.(*armadaevents.EventSequence_Event_JobRunErrors)
	require.True(t, ok)
	assert.Empty(t, failedEvent.JobRunErrors.Errors[0].GetFailureCategory())
	assert.Empty(t, failedEvent.JobRunErrors.Errors[0].GetFailureSubcategory())
}

func assertExpectedEvents(t *testing.T, pod *v1.Pod, messages []reporter.EventMessage, expectedType reflect.Type) {
	assert.Len(t, messages, 1)
	assert.Len(t, messages[0].Event.Events, 1)

	assert.Equal(t, util.ExtractJobRunId(pod), messages[0].JobRunId)
	assert.Equal(t, util.ExtractQueue(pod), messages[0].Event.Queue)
	assert.Equal(t, util.ExtractJobSet(pod), messages[0].Event.JobSetName)

	event := messages[0].Event.Events[0]
	resultType := reflect.TypeOf(event.Event)
	assert.Equal(t, expectedType, resultType)
}

func assertExpectedAnnotations(t *testing.T, pod *v1.Pod, clusterContext *fakecontext.SyncFakeClusterContext) {
	jobAnnotations := clusterContext.GetAddedAnnotations(util.ExtractJobId(pod))
	assert.Len(t, jobAnnotations, 1)
	_, exists := jobAnnotations[string(pod.Status.Phase)]
	assert.True(t, exists)
}

type stubIssueHandler struct {
	runIdsWithIssues                      map[string]bool
	detectAndRegisterFailedPodIssueResult bool
	classifyResult                        categorizer.ClassifyResult
	detectAndRegisterFailedPodIssueError  error
	// detectCalls counts DetectAndRegisterFailedPodIssue invocations. Atomic
	// because reportCurrentStatus runs on a goroutine spawned by the informer.
	detectCalls atomic.Int32
}

func (s *stubIssueHandler) HasIssue(runId string) bool {
	_, exists := s.runIdsWithIssues[runId]
	return exists
}

func (s *stubIssueHandler) DetectAndRegisterFailedPodIssue(pod *v1.Pod) (bool, categorizer.ClassifyResult, error) {
	s.detectCalls.Add(1)
	return s.detectAndRegisterFailedPodIssueResult, s.classifyResult, s.detectAndRegisterFailedPodIssueError
}

func copyWithUpdatedPhase(pod *v1.Pod, newPhase v1.PodPhase) *v1.Pod {
	result := pod.DeepCopy()
	result.Status.Phase = newPhase
	return result
}
