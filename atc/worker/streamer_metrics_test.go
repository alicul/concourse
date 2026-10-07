package worker_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/v8/atc"
	"github.com/concourse/concourse/v8/atc/compression"
	"github.com/concourse/concourse/v8/atc/db/dbfakes"
	"github.com/concourse/concourse/v8/atc/metric"
	"github.com/concourse/concourse/v8/atc/metric/metricfakes"
	"github.com/concourse/concourse/v8/atc/runtime"
	"github.com/concourse/concourse/v8/atc/runtime/runtimetest"
	"github.com/concourse/concourse/v8/atc/worker"
)

func TestStreamerMetrics(t *testing.T) {
	previousMonitor := metric.Metrics
	previousCacheSetting := atc.EnableCacheStreamedVolumes
	t.Cleanup(func() {
		metric.Metrics = previousMonitor
		atc.EnableCacheStreamedVolumes = previousCacheSetting
	})

	events := make(chan metric.Event, 100)
	emitter := new(metricfakes.FakeEmitter)
	emitter.EmitStub = func(_ lager.Logger, event metric.Event) { events <- event }
	factory := new(metricfakes.FakeEmitterFactory)
	factory.IsConfiguredReturns(true)
	factory.NewEmitterReturns(emitter, nil)
	metric.Metrics = metric.NewMonitor()
	metric.Metrics.RegisterEmitter(factory)
	logger := lager.NewLogger("streamer-metrics-test")
	if err := metric.Metrics.Initialize(logger, "test", map[string]string{}, 100); err != nil {
		t.Fatal(err)
	}

	transferErr := errors.New("transfer failed")
	cacheErr := errors.New("resource cache lookup failed")
	for _, tc := range []struct {
		name                          string
		srcGroup, dstGroup            string
		disabled, srcNoP2P, dstNoP2P  bool
		artifact                      bool
		urlErr, p2pErr, outErr, inErr error
		waitForCancellation, canceled bool
		cacheErr                      bool
		wantRoute, wantStatus         string
		wantErr                       error
	}{
		{name: "matching groups", srcGroup: "private-a", dstGroup: "private-a", wantRoute: "p2p", wantStatus: "success"},
		{name: "empty groups match", wantRoute: "p2p", wantStatus: "success"},
		{name: "different groups", srcGroup: "private-a", dstGroup: "private-b", wantRoute: "atc_group_mismatch", wantStatus: "success"},
		{name: "one empty group", srcGroup: "private-a", wantRoute: "atc_group_mismatch", wantStatus: "success"},
		{name: "groups are case sensitive", srcGroup: "private-a", dstGroup: "PRIVATE-A", wantRoute: "atc_group_mismatch", wantStatus: "success"},
		{name: "disabled takes precedence", disabled: true, srcNoP2P: true, srcGroup: "private-a", dstGroup: "private-b", wantRoute: "atc_disabled", wantStatus: "success"},
		{name: "unsupported source takes precedence over groups", srcNoP2P: true, srcGroup: "private-a", dstGroup: "private-b", wantRoute: "atc_unsupported", wantStatus: "success"},
		{name: "unsupported destination", dstNoP2P: true, wantRoute: "atc_unsupported", wantStatus: "success"},
		{name: "non-volume artifact", artifact: true, wantRoute: "atc_unsupported", wantStatus: "success"},
		{name: "P2P URL lookup fails", urlErr: transferErr, wantRoute: "atc_fallback", wantStatus: "success"},
		{name: "failed P2P falls back successfully", p2pErr: transferErr, wantRoute: "atc_fallback", wantStatus: "success"},
		{name: "failed P2P and failed fallback", p2pErr: transferErr, inErr: transferErr, wantRoute: "atc_fallback", wantStatus: "error", wantErr: transferErr},
		{name: "ATC stream out fails", disabled: true, outErr: transferErr, wantRoute: "atc_disabled", wantStatus: "error", wantErr: transferErr},
		{name: "ATC stream in fails", srcGroup: "private-a", inErr: transferErr, wantRoute: "atc_group_mismatch", wantStatus: "error", wantErr: transferErr},
		{name: "P2P timeout falls back successfully", waitForCancellation: true, wantRoute: "atc_fallback", wantStatus: "success"},
		{name: "cancellation fails fallback", waitForCancellation: true, canceled: true, wantRoute: "atc_fallback", wantStatus: "error", wantErr: context.Canceled},
		{name: "cache lookup error follows successful transfer", cacheErr: true, wantRoute: "p2p", wantStatus: "success", wantErr: cacheErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &metricTestVolume{Volume: runtimetest.NewVolume("source-handle"), outErr: tc.outErr}
			dst := &metricTestVolume{Volume: runtimetest.NewVolume("destination-handle"), inErr: tc.inErr}
			src.DBVolume_.WorkerNameReturns("source-worker")
			dst.DBVolume_.WorkerNameReturns("destination-worker")
			src.DBVolume_.P2PStreamingGroupReturns(tc.srcGroup)
			dst.DBVolume_.P2PStreamingGroupReturns(tc.dstGroup)
			p2pSrc := &metricTestP2PVolume{metricTestVolume: src, p2pErr: tc.p2pErr, waitForCancellation: tc.waitForCancellation}
			p2pDst := &metricTestP2PVolume{metricTestVolume: dst, urlErr: tc.urlErr}
			var source runtime.Artifact = p2pSrc
			if tc.srcNoP2P {
				source = src
			}
			if tc.artifact {
				// Expose only the Artifact interface, as with an uploaded input.
				source = struct{ runtime.Artifact }{src}
			}
			var destination runtime.Volume = p2pDst
			if tc.dstNoP2P {
				destination = dst
			}

			atc.EnableCacheStreamedVolumes = tc.cacheErr
			cacheFactory := new(dbfakes.FakeResourceCacheFactory)
			if tc.cacheErr {
				src.DBVolume_.GetResourceCacheIDReturns(1)
				cacheFactory.FindResourceCacheByIDReturns(nil, false, cacheErr)
			}
			streamer := worker.NewStreamer(cacheFactory, compression.NewGzipCompression(), 0, worker.P2PConfig{
				Enabled: !tc.disabled,
				Timeout: 5 * time.Millisecond,
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			start := time.Now()
			err := streamer.Stream(ctx, source, destination)
			elapsed := time.Since(start)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Stream error = %v, want %v", err, tc.wantErr)
			}

			// A sentinel on the same emission queue makes the exact count deterministic.
			metric.BuildCollectorDuration{}.Emit(logger)
			var emitted []metric.Event
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
		readEvents:
			for {
				select {
				case event := <-events:
					if event.Name == "gc: build collector duration (ms)" {
						break readEvents
					}
					emitted = append(emitted, event)
				case <-deadline.C:
					t.Fatal("timed out waiting for metric events")
				}
			}
			if len(emitted) != 1 {
				t.Fatalf("emitted %d events, want exactly one: %#v", len(emitted), emitted)
			}
			event := emitted[0]
			if event.Name != "volume streaming duration" {
				t.Fatalf("event name = %q", event.Name)
			}
			wantLabels := map[string]string{"route": tc.wantRoute, "status": tc.wantStatus}
			if !reflect.DeepEqual(event.Attributes, wantLabels) {
				t.Errorf("event labels = %v, want exactly %v", event.Attributes, wantLabels)
			}
			// Includes both attempts when P2P fails, measured in seconds.
			minimumDuration := src.transferDuration + dst.transferDuration
			if event.Value <= 0 || event.Value < minimumDuration.Seconds() || event.Value > elapsed.Seconds() {
				t.Errorf("duration = %g seconds, want between %g and %g", event.Value, minimumDuration.Seconds(), elapsed.Seconds())
			}

			wantP2P, wantURL, wantOut, wantIn := 0, 0, 1, 1
			if tc.wantRoute == "p2p" || tc.wantRoute == "atc_fallback" {
				wantP2P, wantURL = 1, 1
			}
			if tc.urlErr != nil {
				wantP2P = 0
			}
			if tc.wantRoute == "p2p" {
				wantOut, wantIn = 0, 0
			} else if tc.outErr != nil || tc.canceled {
				wantIn = 0
			}
			if p2pSrc.p2pCalls != wantP2P || p2pDst.urlCalls != wantURL || src.outCalls != wantOut || dst.inCalls != wantIn {
				t.Errorf("calls (P2P, URL, out, in) = (%d, %d, %d, %d), want (%d, %d, %d, %d)", p2pSrc.p2pCalls, p2pDst.urlCalls, src.outCalls, dst.inCalls, wantP2P, wantURL, wantOut, wantIn)
			}
			wantFallbacks := float64(0)
			if tc.wantRoute == "atc_fallback" {
				wantFallbacks = 1
			}
			if got := metric.Metrics.VolumesStreamedViaFallback.Delta(); got != wantFallbacks {
				t.Errorf("fallback counter = %g, want %g", got, wantFallbacks)
			}
		})
	}
}

type metricTestVolume struct {
	*runtimetest.Volume
	outErr, inErr     error
	outCalls, inCalls int
	transferDuration  time.Duration
}

func (v *metricTestVolume) StreamOut(ctx context.Context, path string, c compression.Compression) (io.ReadCloser, error) {
	start := time.Now()
	defer func() { v.transferDuration += time.Since(start) }()
	v.outCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if v.outErr != nil {
		return nil, v.outErr
	}
	return v.Volume.StreamOut(ctx, path, c)
}

func (v *metricTestVolume) StreamIn(ctx context.Context, path string, c compression.Compression, limit float64, reader io.Reader) error {
	start := time.Now()
	defer func() { v.transferDuration += time.Since(start) }()
	v.inCalls++
	if v.inErr != nil {
		return v.inErr
	}
	return v.Volume.StreamIn(ctx, path, c, limit, reader)
}

type metricTestP2PVolume struct {
	*metricTestVolume
	urlErr, p2pErr      error
	waitForCancellation bool
	p2pCalls, urlCalls  int
}

func (v *metricTestP2PVolume) GetStreamInP2PURL(context.Context, string) (string, error) {
	v.urlCalls++
	return "http://destination-worker/volumes/destination-handle/stream-in", v.urlErr
}

func (v *metricTestP2PVolume) StreamP2POut(ctx context.Context, _, _ string, _ compression.Compression) error {
	start := time.Now()
	defer func() { v.transferDuration += time.Since(start) }()
	v.p2pCalls++
	if v.waitForCancellation {
		<-ctx.Done()
		return ctx.Err()
	}
	return v.p2pErr
}
