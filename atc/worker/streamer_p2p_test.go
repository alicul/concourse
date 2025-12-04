package worker_test

import (
	"context"
	"io"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// FakeP2PVolume implements runtime.P2PVolume for testing
type FakeP2PVolume struct {
	runtime.Volume
	handle         string
	workerName     string
	streamOutCalled bool
	streamP2POutCalled bool
	getStreamInP2PURLCalled bool
}

func (v *FakeP2PVolume) Handle() string { return v.handle }
func (v *FakeP2PVolume) DBVolume() db.CreatedVolume {
	fakeDBVolume := new(dbfakes.FakeCreatedVolume)
	fakeDBVolume.WorkerNameReturns(v.workerName)
	return fakeDBVolume
}
func (v *FakeP2PVolume) Source() string { return v.workerName }

func (v *FakeP2PVolume) StreamOut(ctx context.Context, path string, compression compression.Compression) (io.ReadCloser, error) {
	v.streamOutCalled = true
	return io.NopCloser(new(io.LimitedReader)), nil // Return something valid-ish
}

func (v *FakeP2PVolume) StreamIn(ctx context.Context, path string, compression compression.Compression, limitInMB float64, reader io.Reader) error {
	return nil
}

func (v *FakeP2PVolume) GetStreamInP2PURL(ctx context.Context, path string) (string, error) {
	v.getStreamInP2PURLCalled = true
	return "http://some-url", nil
}

func (v *FakeP2PVolume) StreamP2POut(ctx context.Context, path string, destURL string, compression compression.Compression) error {
	v.streamP2POutCalled = true
	return nil
}

// Dummy methods to satisfy interface
func (v *FakeP2PVolume) InitializeResourceCache(ctx context.Context, urc db.ResourceCache) (*db.UsedWorkerResourceCache, error) { return nil, nil }
func (v *FakeP2PVolume) InitializeStreamedResourceCache(ctx context.Context, urc db.ResourceCache, sourceWorkerResourceCacheID int) (*db.UsedWorkerResourceCache, error) { return nil, nil }
func (v *FakeP2PVolume) InitializeTaskCache(ctx context.Context, jobID int, stepName string, path string, privileged bool) error { return nil }


var _ = Describe("Streamer P2P Logic", func() {
	var (
		fakeWorkerFactory *dbfakes.FakeWorkerFactory
		fakeCacheFactory  *dbfakes.FakeResourceCacheFactory
		streamer          worker.Streamer
		ctx               context.Context
		srcVolume         *FakeP2PVolume
		dstVolume         *FakeP2PVolume
	)

	BeforeEach(func() {
		fakeWorkerFactory = new(dbfakes.FakeWorkerFactory)
		fakeCacheFactory = new(dbfakes.FakeResourceCacheFactory)
		streamer = worker.NewStreamer(
			fakeCacheFactory,
			fakeWorkerFactory,
			compression.NewGzipCompression(),
			0,
			worker.P2PConfig{Enabled: true},
		)
		ctx = context.Background()

		srcVolume = &FakeP2PVolume{handle: "src-handle", workerName: "src-worker"}
		dstVolume = &FakeP2PVolume{handle: "dst-handle", workerName: "dst-worker"}
	})

	Context("when both workers are on the same P2P network", func() {
		BeforeEach(func() {
			srcWorker := new(dbfakes.FakeWorker)
			srcWorker.NameReturns("src-worker")
			srcWorker.BaggageclaimP2PNetworkReturns("network-a")

			dstWorker := new(dbfakes.FakeWorker)
			dstWorker.NameReturns("dst-worker")
			dstWorker.BaggageclaimP2PNetworkReturns("network-a")

			fakeWorkerFactory.GetWorkerStub = func(name string) (db.Worker, bool, error) {
				if name == "src-worker" {
					return srcWorker, true, nil
				}
				if name == "dst-worker" {
					return dstWorker, true, nil
				}
				return nil, false, nil
			}
		})

		It("uses P2P streaming", func() {
			err := streamer.Stream(ctx, srcVolume, dstVolume)
			Expect(err).ToNot(HaveOccurred())
			Expect(srcVolume.streamP2POutCalled).To(BeTrue(), "should have called StreamP2POut")
			Expect(srcVolume.streamOutCalled).To(BeFalse(), "should NOT have called StreamOut (ATC streaming)")
		})
	})

	Context("when workers are on different P2P networks", func() {
		BeforeEach(func() {
			srcWorker := new(dbfakes.FakeWorker)
			srcWorker.NameReturns("src-worker")
			srcWorker.BaggageclaimP2PNetworkReturns("network-a")

			dstWorker := new(dbfakes.FakeWorker)
			dstWorker.NameReturns("dst-worker")
			dstWorker.BaggageclaimP2PNetworkReturns("network-b")

			fakeWorkerFactory.GetWorkerStub = func(name string) (db.Worker, bool, error) {
				if name == "src-worker" {
					return srcWorker, true, nil
				}
				if name == "dst-worker" {
					return dstWorker, true, nil
				}
				return nil, false, nil
			}
		})

		It("falls back to ATC streaming", func() {
			err := streamer.Stream(ctx, srcVolume, dstVolume)
			Expect(err).ToNot(HaveOccurred())
			Expect(srcVolume.streamP2POutCalled).To(BeFalse(), "should NOT have called StreamP2POut")
			Expect(srcVolume.streamOutCalled).To(BeTrue(), "should have called StreamOut (ATC streaming)")
		})
	})

	Context("when one worker has no P2P network", func() {
		BeforeEach(func() {
			srcWorker := new(dbfakes.FakeWorker)
			srcWorker.NameReturns("src-worker")
			srcWorker.BaggageclaimP2PNetworkReturns("network-a")

			dstWorker := new(dbfakes.FakeWorker)
			dstWorker.NameReturns("dst-worker")
			dstWorker.BaggageclaimP2PNetworkReturns("")

			fakeWorkerFactory.GetWorkerStub = func(name string) (db.Worker, bool, error) {
				if name == "src-worker" {
					return srcWorker, true, nil
				}
				if name == "dst-worker" {
					return dstWorker, true, nil
				}
				return nil, false, nil
			}
		})

		It("falls back to ATC streaming", func() {
			err := streamer.Stream(ctx, srcVolume, dstVolume)
			Expect(err).ToNot(HaveOccurred())
			Expect(srcVolume.streamP2POutCalled).To(BeFalse(), "should NOT have called StreamP2POut")
			Expect(srcVolume.streamOutCalled).To(BeTrue(), "should have called StreamOut (ATC streaming)")
		})
	})
})
