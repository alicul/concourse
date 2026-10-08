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
	Describe("volume streaming", func() {
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

		// eventsByName waits for exactly count events and groups them by name,
		// in emission order.
		eventsByName := func(count int) map[string][]metric.Event {
			Eventually(emitter.EmitCallCount).Should(Equal(count))
			Consistently(emitter.EmitCallCount).Should(Equal(count))
			events := map[string][]metric.Event{}
			for i := 0; i < count; i++ {
				_, event := emitter.EmitArgsForCall(i)
				events[event.Name] = append(events[event.Name], event)
			}
			return events
		}

		DescribeTable("emits duration, bytes and per-worker events with bounded attributes", func(route string, streamErr error, status string) {
			metric.VolumeStreaming{
				Route:     route,
				Duration:  1500 * time.Millisecond,
				Err:       streamErr,
				Bytes:     4096,
				SrcWorker: "worker-a",
				DstWorker: "worker-b",
				SrcGroup:  "zone-a",
				DstGroup:  "zone-b",
			}.Emit(testLogger)

			events := eventsByName(4)
			routeLabels := map[string]string{"route": route, "status": status, "src_group": "zone-a", "dst_group": "zone-b"}

			Expect(events["volume streaming duration"]).To(HaveLen(1))
			Expect(events["volume streaming duration"][0].Value).To(Equal(1.5))
			Expect(events["volume streaming duration"][0].Attributes).To(Equal(routeLabels))

			Expect(events["volume streaming bytes"]).To(HaveLen(1))
			Expect(events["volume streaming bytes"][0].Value).To(Equal(4096.0))
			Expect(events["volume streaming bytes"][0].Attributes).To(Equal(routeLabels))

			// One event per side, never both worker names on one event.
			Expect(events["volume streaming worker"]).To(HaveLen(2))
			Expect(events["volume streaming worker"][0].Value).To(Equal(4096.0))
			Expect(events["volume streaming worker"][0].Attributes).To(Equal(map[string]string{"worker": "worker-a", "direction": "sent", "status": status}))
			Expect(events["volume streaming worker"][1].Value).To(Equal(4096.0))
			Expect(events["volume streaming worker"][1].Attributes).To(Equal(map[string]string{"worker": "worker-b", "direction": "received", "status": status}))

			Expect(events).NotTo(HaveKey("volume streaming unmeasured"))
		},
			Entry("successful p2p", "p2p", nil, "success"),
			Entry("disabled p2p", "atc_disabled", nil, "success"),
			Entry("unsupported p2p", "atc_unsupported", nil, "success"),
			Entry("group mismatch", "atc_group_mismatch", nil, "success"),
			Entry("fallback", "atc_fallback", nil, "success"),
			Entry("failed fallback", "atc_fallback", errors.New("volume-specific stream failure"), "error"),
		)

		It("reports an unknown byte count as unmeasured and zero bytes per worker", func() {
			metric.VolumeStreaming{
				Route:     "p2p",
				Duration:  time.Second,
				Bytes:     -1,
				SrcWorker: "worker-a",
				DstWorker: "worker-b",
			}.Emit(testLogger)

			events := eventsByName(4)
			Expect(events).NotTo(HaveKey("volume streaming bytes"))
			Expect(events["volume streaming unmeasured"]).To(HaveLen(1))
			Expect(events["volume streaming unmeasured"][0].Value).To(Equal(1.0))
			Expect(events["volume streaming unmeasured"][0].Attributes).To(Equal(map[string]string{"route": "p2p"}))
			Expect(events["volume streaming worker"]).To(HaveLen(2))
			for _, event := range events["volume streaming worker"] {
				Expect(event.Value).To(BeZero())
				Expect(event.Attributes["status"]).To(Equal("success"))
			}
		})

		It("labels ungrouped workers with a sentinel and skips a nameless source", func() {
			metric.VolumeStreaming{
				Route:     "atc_unsupported",
				Duration:  time.Second,
				Bytes:     10,
				DstWorker: "worker-b",
				SrcGroup:  metric.UnknownStreamingGroup,
			}.Emit(testLogger)

			events := eventsByName(3)
			Expect(events["volume streaming duration"][0].Attributes).To(Equal(map[string]string{
				"route": "atc_unsupported", "status": "success", "src_group": "unknown", "dst_group": "ungrouped",
			}))
			Expect(events["volume streaming worker"]).To(HaveLen(1))
			Expect(events["volume streaming worker"][0].Attributes["worker"]).To(Equal("worker-b"))
			Expect(events["volume streaming worker"][0].Attributes["direction"]).To(Equal("received"))
		})
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
