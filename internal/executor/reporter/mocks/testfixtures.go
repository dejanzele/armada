package mocks

import (
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"

	"github.com/armadaproject/armada/internal/executor/reporter"
)

type FakeEventReporter struct {
	// ReceivedEvents may be read or reset directly by tests that drive the
	// reporter synchronously. Tests that report from another goroutine (e.g.
	// informer callbacks) must use GetReceivedEvents to avoid data races.
	ReceivedEvents []reporter.EventMessage
	ErrorOnReport  bool

	mutex sync.Mutex
}

func NewFakeEventReporter() *FakeEventReporter {
	return &FakeEventReporter{}
}

func (f *FakeEventReporter) Report(events []reporter.EventMessage) error {
	if f.ErrorOnReport {
		return fmt.Errorf("failed to report events")
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.ReceivedEvents = append(f.ReceivedEvents, events...)
	return nil
}

// GetReceivedEvents returns a snapshot of the events reported so far.
func (f *FakeEventReporter) GetReceivedEvents() []reporter.EventMessage {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	result := make([]reporter.EventMessage, len(f.ReceivedEvents))
	copy(result, f.ReceivedEvents)
	return result
}

func (f *FakeEventReporter) QueueEvent(event reporter.EventMessage, callback func(error)) {
	e := f.Report([]reporter.EventMessage{event})
	callback(e)
}

func (f *FakeEventReporter) HasPendingEvents(pod *v1.Pod) bool {
	return false
}
