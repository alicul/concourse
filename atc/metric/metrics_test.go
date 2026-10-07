package metric_test

import (
	"errors"
	"time"

	"github.com/concourse/concourse/v8/atc/db"
	"github.com/concourse/concourse/v8/atc/metric"
	"github.com/concourse/concourse/v8/atc/metric/metricfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Metrics", func() {
	Describe("volume streaming duration", func() {
		var emitter *metricfakes.FakeEmitter

		BeforeEach(func() {
			previousMonitor := metric.Metrics
			DeferCleanup(func() { metric.Metrics = previousMonitor })
			metric.Metrics = metric.NewMonitor()
			emitter = new(metricfakes.FakeEmitter)
			emitterFactory := new(metricfakes.FakeEmitterFactory)
			emitterFactory.IsConfiguredReturns(true)
			emitterFactory.NewEmitterReturns(emitter, nil)
			metric.Metrics.RegisterEmitter(emitterFactory)
			Expect(metric.Metrics.Initialize(testLogger, "test", map[string]string{}, 10)).To(Succeed())
		})

		DescribeTable("emits seconds with only route and status attributes", func(route string, streamErr error, status string) {
			metric.VolumeStreamingDuration{
				Route:    route,
				Duration: 1500 * time.Millisecond,
				Err:      streamErr,
			}.Emit(testLogger)

			Eventually(emitter.EmitCallCount).Should(Equal(1))
			_, event := emitter.EmitArgsForCall(0)
			Expect(event.Name).To(Equal("volume streaming duration"))
			Expect(event.Value).To(Equal(1.5))
			Expect(event.Attributes).To(Equal(map[string]string{"route": route, "status": status}))
		},
			Entry("successful p2p", "p2p", nil, "success"),
			Entry("disabled p2p", "atc_disabled", nil, "success"),
			Entry("unsupported p2p", "atc_unsupported", nil, "success"),
			Entry("group mismatch", "atc_group_mismatch", nil, "success"),
			Entry("fallback", "atc_fallback", nil, "success"),
			Entry("failed p2p", "p2p", errors.New("worker-specific connection failure"), "error"),
			Entry("failed fallback", "atc_fallback", errors.New("volume-specific stream failure"), "error"),
		)
	})

	Describe("worker state metric", func() {
		var (
			emitter *smartFakeEmitter
			monitor *metric.Monitor
		)

		BeforeEach(func() {
			emitter = new(smartFakeEmitter)
			monitor = metric.NewMonitor()

			emitterFactory := new(metricfakes.FakeEmitterFactory)
			emitterFactory.IsConfiguredReturns(true)
			emitterFactory.NewEmitterReturns(emitter, nil)

			monitor.RegisterEmitter(emitterFactory)
			monitor.Initialize(testLogger, "test", map[string]string{}, 1000)
		})

		It("emits a value for every state", func() {
			givenNoWorkers().Emit(testLogger, monitor)

			waitForEvents(emitter)

			for _, state := range db.AllWorkerStates() {
				event := emitter.eventWithState(state)
				Expect(event.Value).To(Equal(float64(0)))
			}
		})

		It("correctly emits the number of running workers", func() {
			givenOneWorkerWithState(db.WorkerStateRunning).
				Emit(testLogger, monitor)

			waitForEvents(emitter)

			event := emitter.eventWithState(db.WorkerStateRunning)
			Expect(event.Value).To(Equal(float64(1)))
		})
	})
})

type smartFakeEmitter struct {
	metricfakes.FakeEmitter
}

func (fakeEmitter *smartFakeEmitter) eventWithState(state db.WorkerState) *metric.Event {
	for i := 0; i < fakeEmitter.EmitCallCount(); i++ {
		_, event := fakeEmitter.EmitArgsForCall(i)
		if event.Attributes["state"] == string(state) {
			return &event
		}
	}
	return nil
}

func givenNoWorkers() metric.WorkersState {
	return metric.WorkersState{
		WorkerStateByName: make(map[string]db.WorkerState),
	}
}

func givenOneWorkerWithState(state db.WorkerState) metric.WorkersState {
	workersState := givenNoWorkers()
	workersState.WorkerStateByName["my-worker"] = state
	return workersState
}

func waitForEvents(fakeEmitter *smartFakeEmitter) {
	numberOfWorkerStates := len(db.AllWorkerStates())
	Eventually(fakeEmitter.EmitCallCount).Should(Equal(numberOfWorkerStates))
}
