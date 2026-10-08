package worker_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync/atomic"
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

// reportedP2PBytes is what the fake source worker reports for a direct transfer.
const reportedP2PBytes int64 = 1234

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
		unmeasuredP2P                 bool
		urlErr, p2pErr, outErr, inErr error
		waitForCancellation, canceled bool
		cacheErr                      bool
		wantRoute, wantStatus         string
		wantErr                       error
	}{
		{name: "matching groups", srcGroup: "private-a", dstGroup: "private-a", wantRoute: "p2p", wantStatus: "success"},
		{name: "empty groups match", wantRoute: "p2p", wantStatus: "success"},
		{name: "P2P from a worker that reports no byte count", unmeasuredP2P: true, wantRoute: "p2p", wantStatus: "success"},
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
			content := runtimetest.VolumeContent{"file": {Data: []byte("some content to stream")}}
			src := &metricTestVolume{Volume: runtimetest.NewVolume("source-handle").WithContent(content), outErr: tc.outErr}
			dst := &metricTestVolume{Volume: runtimetest.NewVolume("destination-handle"), inErr: tc.inErr}
			src.DBVolume_.WorkerNameReturns("source-worker")
			dst.DBVolume_.WorkerNameReturns("destination-worker")
			src.DBVolume_.P2PStreamingGroupReturns(tc.srcGroup)
			dst.DBVolume_.P2PStreamingGroupReturns(tc.dstGroup)
			p2pBytes := reportedP2PBytes
			if tc.unmeasuredP2P {
				p2pBytes = -1
			}
			p2pSrc := &metricTestP2PVolume{metricTestVolume: src, p2pErr: tc.p2pErr, p2pBytes: p2pBytes, waitForCancellation: tc.waitForCancellation}
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

			// A sentinel on the same emission queue makes the exact set deterministic.
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
			byName := map[string][]metric.Event{}
			for _, event := range emitted {
				byName[event.Name] = append(byName[event.Name], event)
			}

			srcGroup := groupLabel(tc.srcGroup)
			if tc.artifact {
				srcGroup = "unknown"
			}
			routeLabels := map[string]string{"route": tc.wantRoute, "status": tc.wantStatus, "src_group": srcGroup, "dst_group": groupLabel(tc.dstGroup)}

			durations := byName["volume streaming duration"]
			if len(durations) != 1 {
				t.Fatalf("emitted %d duration events, want exactly one: %#v", len(durations), emitted)
			}
			if !reflect.DeepEqual(durations[0].Attributes, routeLabels) {
				t.Errorf("duration labels = %v, want exactly %v", durations[0].Attributes, routeLabels)
			}
			// Includes both attempts when P2P fails, measured in seconds.
			minimumDuration := src.transferDuration + dst.transferDuration
			if durations[0].Value <= 0 || durations[0].Value < minimumDuration.Seconds() || durations[0].Value > elapsed.Seconds() {
				t.Errorf("duration = %g seconds, want between %g and %g", durations[0].Value, minimumDuration.Seconds(), elapsed.Seconds())
			}

			// Bytes are what moved on the final route: the source worker's report
			// for P2P, what the web node relayed otherwise, nothing on failure.
			var wantBytes int64
			switch {
			case tc.wantRoute == "p2p":
				wantBytes = p2pBytes
			case tc.wantStatus == "success":
				wantBytes = dst.bytesIn.Load()
			}
			if wantBytes < 0 {
				unmeasured := byName["volume streaming unmeasured"]
				if len(unmeasured) != 1 || unmeasured[0].Value != 1 || !reflect.DeepEqual(unmeasured[0].Attributes, map[string]string{"route": tc.wantRoute}) {
					t.Errorf("unmeasured events = %#v, want one for route %s", unmeasured, tc.wantRoute)
				}
				if got := byName["volume streaming bytes"]; len(got) != 0 {
					t.Errorf("bytes events = %#v, want none for an unknown count", got)
				}
			} else {
				bytesEvents := byName["volume streaming bytes"]
				if len(bytesEvents) != 1 {
					t.Fatalf("emitted %d bytes events, want exactly one: %#v", len(bytesEvents), emitted)
				}
				if bytesEvents[0].Value != float64(wantBytes) {
					t.Errorf("bytes = %g, want %d", bytesEvents[0].Value, wantBytes)
				}
				if tc.wantStatus == "success" && wantBytes <= 0 {
					t.Errorf("successful transfer reported %d bytes", wantBytes)
				}
				if !reflect.DeepEqual(bytesEvents[0].Attributes, routeLabels) {
					t.Errorf("bytes labels = %v, want exactly %v", bytesEvents[0].Attributes, routeLabels)
				}
				if got := byName["volume streaming unmeasured"]; len(got) != 0 {
					t.Errorf("unmeasured events = %#v, want none", got)
				}
			}

			// One event per worker side; a non-volume source names no worker.
			wantWorkers := []map[string]string{{"worker": "destination-worker", "direction": "received", "status": tc.wantStatus}}
			if !tc.artifact {
				wantWorkers = append([]map[string]string{{"worker": "source-worker", "direction": "sent", "status": tc.wantStatus}}, wantWorkers...)
			}
			workerEvents := byName["volume streaming worker"]
			if len(workerEvents) != len(wantWorkers) {
				t.Fatalf("emitted %d worker events, want %d: %#v", len(workerEvents), len(wantWorkers), workerEvents)
			}
			for i, want := range wantWorkers {
				if !reflect.DeepEqual(workerEvents[i].Attributes, want) {
					t.Errorf("worker event %d labels = %v, want %v", i, workerEvents[i].Attributes, want)
				}
				if workerEvents[i].Value != float64(max(wantBytes, 0)) {
					t.Errorf("worker event %d bytes = %g, want %d", i, workerEvents[i].Value, max(wantBytes, 0))
				}
			}
			if extra := len(emitted) - len(durations) - len(byName["volume streaming bytes"]) - len(byName["volume streaming unmeasured"]) - len(workerEvents); extra != 0 {
				t.Errorf("%d unexpected events: %#v", extra, emitted)
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

// groupLabel mirrors the sentinel the metric event uses for ungrouped workers.
func groupLabel(group string) string {
	if group == "" {
		return "ungrouped"
	}
	return group
}

type metricTestVolume struct {
	*runtimetest.Volume
	outErr, inErr     error
	outCalls, inCalls int
	bytesIn           atomic.Int64
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

// StreamIn counts the compressed bytes it consumes so tests can compare them
// with what the streamer reports for the ATC route.
func (v *metricTestVolume) StreamIn(ctx context.Context, path string, c compression.Compression, limit float64, reader io.Reader) error {
	start := time.Now()
	defer func() { v.transferDuration += time.Since(start) }()
	v.inCalls++
	if v.inErr != nil {
		return v.inErr
	}
	counted := &countingReader{Reader: reader, n: &v.bytesIn}
	return v.Volume.StreamIn(ctx, path, c, limit, counted)
}

type countingReader struct {
	io.Reader
	n *atomic.Int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n.Add(int64(n))
	return n, err
}

type metricTestP2PVolume struct {
	*metricTestVolume
	urlErr, p2pErr      error
	p2pBytes            int64
	waitForCancellation bool
	p2pCalls, urlCalls  int
}

func (v *metricTestP2PVolume) GetStreamInP2PURL(context.Context, string) (string, error) {
	v.urlCalls++
	return "http://destination-worker/volumes/destination-handle/stream-in", v.urlErr
}

// StreamP2POut reports p2pBytes on success, like a worker's byte trailer, and
// nothing on failure.
func (v *metricTestP2PVolume) StreamP2POut(ctx context.Context, _, _ string, _ compression.Compression) (int64, error) {
	start := time.Now()
	defer func() { v.transferDuration += time.Since(start) }()
	v.p2pCalls++
	if v.waitForCancellation {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	if v.p2pErr != nil {
		return 0, v.p2pErr
	}
	return v.p2pBytes, nil
}
