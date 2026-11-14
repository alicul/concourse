package worker

import (
	"context"
	"os"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/v8/worker/baggageclaim"
)

// ImageSweeper lets go of the images that a runtime keeps unpacked and that
// no volume on the worker holds any more.
type ImageSweeper func(ctx context.Context) error

// VolumeSweeperOpt configures a volume sweeper.
type VolumeSweeperOpt func(*volumeSweeper)

// WithImageSweeper makes the volume sweeper sweep the runtime's images each
// time it has swept the volumes.
func WithImageSweeper(sweepImages ImageSweeper) VolumeSweeperOpt {
	return func(s *volumeSweeper) {
		s.sweepImages = sweepImages
	}
}

// volumeSweeper is an ifrit.Runner that periodically reports and
// garbage-collects a worker's volumes and, given WithImageSweeper, then lets
// go of the runtime's images that no volume holds any more.
type volumeSweeper struct {
	logger             lager.Logger
	interval           time.Duration
	tsaClient          TSAClient
	baggageclaimClient baggageclaim.Client
	maxInFlight        uint16
	sweepImages        ImageSweeper
}

func NewVolumeSweeper(
	logger lager.Logger,
	sweepInterval time.Duration,
	tsaClient TSAClient,
	bcClient baggageclaim.Client,
	maxInFlight uint16,
	opts ...VolumeSweeperOpt,
) *volumeSweeper {
	sweeper := &volumeSweeper{
		logger:             logger,
		interval:           sweepInterval,
		tsaClient:          tsaClient,
		baggageclaimClient: bcClient,
		maxInFlight:        maxInFlight,
	}
	for _, opt := range opts {
		opt(sweeper)
	}
	return sweeper
}

func (sweeper *volumeSweeper) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	timer := time.NewTicker(sweeper.interval)

	close(ready)

	for {
		select {
		case <-timer.C:
			sweeper.sweep(sweeper.logger.Session("tick"))

		case sig := <-signals:
			sweeper.logger.Info("sweep-cancelled-by-signal", lager.Data{"signal": sig})
			return nil
		}
	}
}

func (sweeper *volumeSweeper) sweep(logger lager.Logger) {
	ctx := lagerctx.NewContext(context.Background(), logger)

	volumes, err := sweeper.baggageclaimClient.ListVolumes(lagerctx.NewContext(ctx, logger.Session("list-volumes")), baggageclaim.VolumeProperties{})
	if err != nil {
		logger.Error("failed-to-list-volumes", err)
	} else {
		handles := []string{}
		for _, volume := range volumes {
			handles = append(handles, volume.Handle())
		}

		err := sweeper.tsaClient.ReportVolumes(ctx, handles)
		if err != nil {
			logger.Error("failed-to-report-volumes", err)
		}
	}

	volumeHandles, err := sweeper.tsaClient.VolumesToDestroy(ctx)
	if err != nil {
		logger.Error("failed-to-get-volumes-to-destroy", err)
	} else {
		var wg sync.WaitGroup
		maxInFlight := make(chan int, sweeper.maxInFlight)

		for _, handle := range volumeHandles {
			maxInFlight <- 1
			wg.Add(1)

			go func(handle string) {
				err := sweeper.baggageclaimClient.DestroyVolume(lagerctx.NewContext(ctx, logger.Session("destroy-volumes")), handle)
				if err != nil {
					logger.WithData(lager.Data{"handle": handle}).Error("failed-to-destroy-volume", err)
				}

				<-maxInFlight
				wg.Done()
			}(handle)
		}
		wg.Wait()
	}

	err = sweeper.baggageclaimClient.CleanupOrphanedVolumes(ctx)
	if err != nil {
		logger.Error("failed-to-cleanup-orphaned-volumes", err)
	}

	if sweeper.sweepImages != nil {
		// after the volumes, so that an image is let go of in the same sweep
		// as the last volume that held it
		err := sweeper.sweepImages(ctx)
		if err != nil {
			logger.Error("failed-to-sweep-images", err)
		}
	}
}
