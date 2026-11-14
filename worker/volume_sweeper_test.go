package worker_test

import (
	"context"
	"errors"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/v8/worker"
	"github.com/concourse/concourse/v8/worker/baggageclaim/baggageclaimfakes"
	"github.com/concourse/concourse/v8/worker/workerfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Volume Sweeper", func() {
	const (
		sweepInterval = 1 * time.Second
		maxInFlight   = uint16(1)
	)

	var (
		testLogger *lagertest.TestLogger

		fakeTSAClient *workerfakes.FakeTSAClient
		fakeBcClient  *baggageclaimfakes.FakeClient
		sweeperOpts   []worker.VolumeSweeperOpt

		osSignal chan os.Signal
		exited   chan struct{}
	)

	BeforeEach(func() {
		testLogger = lagertest.NewTestLogger("volume-sweeper")
		osSignal = make(chan os.Signal)
		exited = make(chan struct{})
		sweeperOpts = nil

		fakeTSAClient = new(workerfakes.FakeTSAClient)
		fakeTSAClient.ReportVolumesReturns(nil)
		fakeTSAClient.VolumesToDestroyReturns([]string{}, nil)

		fakeBcClient = new(baggageclaimfakes.FakeClient)
		fakeBcClient.ListVolumesReturns(nil, nil)
	})

	JustBeforeEach(func() {
		sweeper := worker.NewVolumeSweeper(testLogger, sweepInterval, fakeTSAClient, fakeBcClient, maxInFlight, sweeperOpts...)
		go func() {
			_ = sweeper.Run(osSignal, make(chan struct{}))
			close(exited)
		}()
	})

	AfterEach(func() {
		close(osSignal)
		<-exited
	})

	It("calls CleanupOrphanedVolumes on each tick", func() {
		Eventually(fakeBcClient.CleanupOrphanedVolumesCallCount).Should(BeNumerically(">=", 1))
	})

	Context("when CleanupOrphanedVolumes returns an error", func() {
		BeforeEach(func() {
			fakeBcClient.CleanupOrphanedVolumesReturns(errors.New("cleanup-failed"))
		})

		It("logs the error but continues sweeping", func() {
			// Wait for at least 2 ticks to confirm it keeps running
			Eventually(fakeBcClient.CleanupOrphanedVolumesCallCount).Should(BeNumerically(">=", 2))
			logs := testLogger.Logs()
			Expect(len(logs)).To(BeNumerically(">=", 2))
			Expect(logs[0].LogLevel).To(Equal(lager.ERROR))
			Expect(logs[0].Data["error"]).To(Equal("cleanup-failed"))
		})
	})

	Context("when the runtime has images to sweep", func() {
		var volumesDestroyed chan int

		BeforeEach(func() {
			volumesDestroyed = make(chan int, 1)

			fakeTSAClient.VolumesToDestroyReturns([]string{"destroyed"}, nil)

			sweeperOpts = []worker.VolumeSweeperOpt{
				worker.WithImageSweeper(func(context.Context) error {
					// never blocks the sweeper: only the first sweep is recorded
					select {
					case volumesDestroyed <- fakeBcClient.DestroyVolumeCallCount():
					default:
					}

					return errors.New("sweep-failed")
				}),
			}
		})

		It("sweeps them once the volumes are destroyed", func() {
			var destroyed int
			Eventually(volumesDestroyed).Should(Receive(&destroyed))
			Expect(destroyed).To(Equal(1))
		})

		It("logs when sweeping them fails", func() {
			Eventually(func() []string {
				var failures []string
				for _, log := range testLogger.Logs() {
					if log.Message == "volume-sweeper.tick.failed-to-sweep-images" {
						failures = append(failures, log.Data["error"].(string))
					}
				}
				return failures
			}).Should(ContainElement("sweep-failed"))
		})
	})
})
