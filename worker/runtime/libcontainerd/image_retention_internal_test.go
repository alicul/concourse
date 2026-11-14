package libcontainerd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

// testImageStore is an image store that treats labels the way containerd's
// does: an Update names the labels it touches, a label updated to an empty
// value is removed, and labels are only ever updated one by one.
type testImageStore struct {
	lock   sync.Mutex
	images map[string]images.Image

	// called, when set, at the start of the named operation on an image
	before func(operation string, name string)
}

func newTestImageStore() *testImageStore {
	return &testImageStore{images: map[string]images.Image{}}
}

func cloneImage(image images.Image) images.Image {
	image.Labels = maps.Clone(image.Labels)
	return image
}

func (s *testImageStore) hook(operation string, name string) {
	if s.before != nil {
		s.before(operation, name)
	}
}

// labels returns the labels of a stored image, or nil when it isn't stored.
func (s *testImageStore) labels(name string) map[string]string {
	s.lock.Lock()
	defer s.lock.Unlock()

	image, stored := s.images[name]
	if !stored {
		return nil
	}

	return maps.Clone(image.Labels)
}

// remove takes an image out of the store behind the back of the code under test.
func (s *testImageStore) remove(name string) {
	s.lock.Lock()
	defer s.lock.Unlock()

	delete(s.images, name)
}

func (s *testImageStore) Get(_ context.Context, name string) (images.Image, error) {
	s.hook("get", name)

	s.lock.Lock()
	defer s.lock.Unlock()

	image, stored := s.images[name]
	if !stored {
		return images.Image{}, errdefs.ErrNotFound
	}

	return cloneImage(image), nil
}

func (s *testImageStore) List(_ context.Context, _ ...string) ([]images.Image, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	var list []images.Image
	for _, image := range s.images {
		list = append(list, cloneImage(image))
	}

	return list, nil
}

func (s *testImageStore) Create(_ context.Context, image images.Image) (images.Image, error) {
	s.hook("create", image.Name)

	s.lock.Lock()
	defer s.lock.Unlock()

	if _, stored := s.images[image.Name]; stored {
		return images.Image{}, errdefs.ErrAlreadyExists
	}

	image = cloneImage(image)
	maps.DeleteFunc(image.Labels, func(_, value string) bool { return value == "" })
	if image.Labels == nil {
		image.Labels = map[string]string{}
	}
	s.images[image.Name] = image

	return cloneImage(image), nil
}

func (s *testImageStore) Update(_ context.Context, image images.Image, fieldpaths ...string) (images.Image, error) {
	s.hook("update", image.Name)

	s.lock.Lock()
	defer s.lock.Unlock()

	stored, found := s.images[image.Name]
	if !found {
		return images.Image{}, errdefs.ErrNotFound
	}

	if len(fieldpaths) == 0 {
		return images.Image{}, fmt.Errorf("an update of the whole image would overwrite labels: %w", errdefs.ErrInvalidArgument)
	}

	for _, fieldpath := range fieldpaths {
		label, ok := strings.CutPrefix(fieldpath, "labels.")
		if !ok {
			return images.Image{}, fmt.Errorf("fieldpath %q: %w", fieldpath, errdefs.ErrInvalidArgument)
		}

		if image.Labels[label] == "" {
			delete(stored.Labels, label)
		} else {
			stored.Labels[label] = image.Labels[label]
		}
	}

	return cloneImage(stored), nil
}

func (s *testImageStore) Delete(_ context.Context, name string, _ ...images.DeleteOpt) error {
	s.hook("delete", name)

	s.lock.Lock()
	defer s.lock.Unlock()

	if _, stored := s.images[name]; !stored {
		return errdefs.ErrNotFound
	}
	delete(s.images, name)

	return nil
}

// once runs do the first time the store starts the given operation.
func (s *testImageStore) once(operation string, do func()) {
	done := false
	s.before = func(op string, _ string) {
		if op == operation && !done {
			done = true
			do()
		}
	}
}

// testVolumes lays out volumes the way baggageclaim does: a directory per
// handle, and a "parent" symlink in a copy-on-write volume.
type testVolumes struct {
	t       *testing.T
	liveDir string
}

func newTestVolumes(t *testing.T) testVolumes {
	return testVolumes{t: t, liveDir: filepath.Join(t.TempDir(), "volumes", "live")}
}

// create makes a volume and returns it as the volume holding an image layout.
func (v testVolumes) create(handle string) imageVolume {
	dir := filepath.Join(v.liveDir, handle)
	require.NoError(v.t, os.MkdirAll(filepath.Join(dir, "volume", "rootfs"), 0755))
	return imageVolume{handle: handle, dir: dir}
}

// createCopyOnWrite makes a volume from another one and returns its data directory.
func (v testVolumes) createCopyOnWrite(handle string, parent string) string {
	v.create(handle)

	err := os.Symlink(filepath.Join(v.liveDir, parent), filepath.Join(v.liveDir, handle, "parent"))
	if err != nil && runtime.GOOS == "windows" {
		v.t.Skipf("creating a symlink takes a privilege on Windows that baggageclaim has and this test may not: %s", err)
	}
	require.NoError(v.t, err)

	return filepath.Join(v.liveDir, handle, "volume")
}

// destroy removes a volume, as baggageclaim does on the web node's request.
func (v testVolumes) destroy(handle string) {
	require.NoError(v.t, os.RemoveAll(filepath.Join(v.liveDir, handle)))
}

var (
	testImage     = ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("image"), Size: 1}
	testImageName = testImage.Digest.String()
	testNow       = time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	noVolume      = imageVolume{}
)

func volumeLabel(volume imageVolume) map[string]string {
	return map[string]string{volumeLabelPrefix + volume.handle: volume.dir}
}

func TestVolumeOfImageLayoutIsTheVolumeHoldingTheLayout(t *testing.T) {
	volumes := newTestVolumes(t)
	cache := volumes.create("resource-cache")

	// the image of a get step is in rootfs/, a resource type's is the volume itself
	for _, dir := range []string{
		filepath.Join(cache.dir, "volume", "rootfs"),
		filepath.Join(cache.dir, "volume"),
		filepath.Join(cache.dir, "volume", "rootfs") + string(filepath.Separator),
	} {
		volume, inVolume := volumeOfImageLayout(dir)
		require.True(t, inVolume, dir)
		require.Equal(t, cache, volume, dir)
	}
}

func TestVolumeOfImageLayoutIsTheVolumeThatAContainersCopyWasMadeFrom(t *testing.T) {
	volumes := newTestVolumes(t)
	cache := volumes.create("resource-cache")
	dataDir := volumes.createCopyOnWrite("container-volume", "resource-cache")

	volume, inVolume := volumeOfImageLayout(filepath.Join(dataDir, "rootfs"))
	require.True(t, inVolume)
	require.Equal(t, "resource-cache", volume.handle)

	// the symlink may resolve to another spelling of the same directory
	require.True(t, os.SameFile(mustStat(t, cache.dir), mustStat(t, volume.dir)))
}

func mustStat(t *testing.T, dir string) os.FileInfo {
	info, err := os.Stat(dir)
	require.NoError(t, err)
	return info
}

func TestVolumeOfImageLayoutStopsAtTheVolumeThatHoldsTheImage(t *testing.T) {
	// a task changed the image in its input in place: the image is in the
	// task's copy of the resource cache, not in the resource cache
	volumes := newTestVolumes(t)
	volumes.create("resource-cache")
	volumes.createCopyOnWrite("task-copy", "resource-cache")
	dataDir := volumes.createCopyOnWrite("container-volume", "task-copy")

	volume, inVolume := volumeOfImageLayout(filepath.Join(dataDir, "rootfs"))
	require.True(t, inVolume)
	require.Equal(t, "task-copy", volume.handle)
}

func TestVolumeOfImageLayoutStaysWithAVolumeWhoseParentIsGone(t *testing.T) {
	volumes := newTestVolumes(t)
	volumes.create("resource-cache")
	dataDir := volumes.createCopyOnWrite("container-volume", "resource-cache")
	volumes.destroy("resource-cache")

	volume, inVolume := volumeOfImageLayout(filepath.Join(dataDir, "rootfs"))
	require.True(t, inVolume)
	require.Equal(t, "container-volume", volume.handle)
}

func TestVolumeOfImageLayoutFindsNoVolumeElsewhere(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "rootfs"), 0755))

	for _, dir := range []string{dir, filepath.Join(dir, "rootfs")} {
		_, inVolume := volumeOfImageLayout(dir)
		require.False(t, inVolume, dir)
	}
}

func TestStoreImageLabelsItWithItsVolume(t *testing.T) {
	store := newTestImageStore()
	cache := newTestVolumes(t).create("resource-cache")

	require.NoError(t, storeImage(context.Background(), store, testImage, cache, true, 24*time.Hour, testNow))

	image, err := store.Get(context.Background(), testImageName)
	require.NoError(t, err)
	require.Equal(t, testImage, image.Target)
	require.Equal(t, volumeLabel(cache), image.Labels)
}

func TestStoreImageLabelsAStoredImageWithEachVolumeHoldingIt(t *testing.T) {
	store := newTestImageStore()
	volumes := newTestVolumes(t)
	first, second := volumes.create("streamed-1"), volumes.create("streamed-2")

	require.NoError(t, storeImage(context.Background(), store, testImage, first, true, 24*time.Hour, testNow))
	require.NoError(t, storeImage(context.Background(), store, testImage, second, true, 24*time.Hour, testNow))

	require.Equal(t, map[string]string{
		volumeLabelPrefix + "streamed-1": first.dir,
		volumeLabelPrefix + "streamed-2": second.dir,
	}, store.labels(testImageName))
}

func TestStoreImageClearsTheExpiryOfAnImageThatAVolumeHoldsAgain(t *testing.T) {
	store := newTestImageStore()
	_, err := store.Create(context.Background(), images.Image{
		Name:   testImageName,
		Target: testImage,
		Labels: map[string]string{expireLabel: stamp(testNow.Add(time.Hour))},
	})
	require.NoError(t, err)

	streamed := newTestVolumes(t).create("streamed-2")
	require.NoError(t, storeImage(context.Background(), store, testImage, streamed, true, 24*time.Hour, testNow))

	require.Equal(t, volumeLabel(streamed), store.labels(testImageName))
}

func TestStoreImageOutsideAVolumeExpiresAfterTheRetention(t *testing.T) {
	store := newTestImageStore()

	require.NoError(t, storeImage(context.Background(), store, testImage, noVolume, false, 24*time.Hour, testNow))
	require.Equal(t, map[string]string{expireLabel: stamp(testNow.Add(24 * time.Hour))}, store.labels(testImageName))

	// each use pushes the expiry out
	later := testNow.Add(time.Hour)
	require.NoError(t, storeImage(context.Background(), store, testImage, noVolume, false, 24*time.Hour, later))
	require.Equal(t, map[string]string{expireLabel: stamp(later.Add(24 * time.Hour))}, store.labels(testImageName))
}

func TestStoreImageOutsideAVolumeStoresNothingWithoutRetention(t *testing.T) {
	store := newTestImageStore()

	require.NoError(t, storeImage(context.Background(), store, testImage, noVolume, false, 0, testNow))

	require.Nil(t, store.labels(testImageName))
}

func TestStoreImageStoresItAgainWhenItWasRemovedMeanwhile(t *testing.T) {
	store := newTestImageStore()
	_, err := store.Create(context.Background(), images.Image{Name: testImageName, Target: testImage})
	require.NoError(t, err)

	// a sweep removes the expired image after storeImage found it stored
	store.once("update", func() { store.remove(testImageName) })

	streamed := newTestVolumes(t).create("streamed-1")
	require.NoError(t, storeImage(context.Background(), store, testImage, streamed, true, 24*time.Hour, testNow))

	require.Equal(t, volumeLabel(streamed), store.labels(testImageName))
}

func TestStoreImageFromContainersCreatedAtTheSameTime(t *testing.T) {
	store := newTestImageStore()
	volumes := newTestVolumes(t)

	const containers = 32
	errs := make(chan error, containers)
	for n := range containers {
		volume := volumes.create(fmt.Sprintf("volume-%d", n))
		go func() {
			errs <- storeImage(context.Background(), store, testImage, volume, true, 24*time.Hour, testNow)
		}()
	}

	for range containers {
		require.NoError(t, <-errs)
	}
	require.Len(t, store.labels(testImageName), containers)
}

func TestSweepImagesKeepsAnImageWhileAVolumeHoldsIt(t *testing.T) {
	store := newTestImageStore()
	volumes := newTestVolumes(t)
	streamed, cache := volumes.create("streamed-1"), volumes.create("resource-cache")
	require.NoError(t, storeImage(context.Background(), store, testImage, streamed, true, 24*time.Hour, testNow))
	require.NoError(t, storeImage(context.Background(), store, testImage, cache, true, 24*time.Hour, testNow))

	// the build's volume is destroyed, the resource cache stays for weeks
	volumes.destroy("streamed-1")
	require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, testNow.Add(21*24*time.Hour)))

	require.Equal(t, volumeLabel(cache), store.labels(testImageName))
}

func TestSweepImagesKeepsAnImageForTheRetentionAfterItsLastVolumeIsGone(t *testing.T) {
	store := newTestImageStore()
	volumes := newTestVolumes(t)
	require.NoError(t, storeImage(context.Background(), store, testImage, volumes.create("streamed-1"), true, 24*time.Hour, testNow))

	// the build ends and its volume is destroyed: the clock starts
	volumes.destroy("streamed-1")
	gone := testNow.Add(3 * time.Hour)
	require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, gone))
	require.Equal(t, map[string]string{expireLabel: stamp(gone.Add(24 * time.Hour))}, store.labels(testImageName))

	// later sweeps leave the stamp alone
	require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, gone.Add(23*time.Hour)))
	require.Equal(t, map[string]string{expireLabel: stamp(gone.Add(24 * time.Hour))}, store.labels(testImageName))

	require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, gone.Add(24*time.Hour)))
	require.Nil(t, store.labels(testImageName), "the image should be removed once its time has come")
}

func TestSweepImagesKeepsAnImageThatTheNextBuildUsesWithinTheRetention(t *testing.T) {
	store := newTestImageStore()
	volumes := newTestVolumes(t)
	require.NoError(t, storeImage(context.Background(), store, testImage, volumes.create("streamed-1"), true, 24*time.Hour, testNow))
	volumes.destroy("streamed-1")
	require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, testNow.Add(time.Hour)))

	// the next build streams the image into a new volume
	second := volumes.create("streamed-2")
	require.NoError(t, storeImage(context.Background(), store, testImage, second, true, 24*time.Hour, testNow.Add(20*time.Hour)))
	require.Equal(t, volumeLabel(second), store.labels(testImageName))

	// well past the first stamp, while the second build is still running
	require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, testNow.Add(30*time.Hour)))
	require.Equal(t, volumeLabel(second), store.labels(testImageName))
}

func TestSweepImagesRemovesAnImageWithItsLastVolumeWithoutRetention(t *testing.T) {
	store := newTestImageStore()
	volumes := newTestVolumes(t)
	require.NoError(t, storeImage(context.Background(), store, testImage, volumes.create("streamed-1"), true, 0, testNow))

	require.NoError(t, sweepImages(context.Background(), store, 0, testNow))
	require.NotNil(t, store.labels(testImageName))

	volumes.destroy("streamed-1")
	require.NoError(t, sweepImages(context.Background(), store, 0, testNow))
	require.Nil(t, store.labels(testImageName))
}

func TestSweepImagesExpiresImagesStoredWithoutLabels(t *testing.T) {
	// a worker built before images were labelled stored them like this
	store := newTestImageStore()
	_, err := store.Create(context.Background(), images.Image{Name: testImageName, Target: testImage})
	require.NoError(t, err)

	require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, testNow))
	require.Equal(t, map[string]string{expireLabel: stamp(testNow.Add(24 * time.Hour))}, store.labels(testImageName))

	require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, testNow.Add(25*time.Hour)))
	require.Nil(t, store.labels(testImageName))
}

func TestSweepImagesStampsAgainWhenTheStampIsBeyondTheRetention(t *testing.T) {
	for name, stamped := range map[string]string{
		"stamped under a longer retention": stamp(testNow.Add(24 * time.Hour)),
		"a stamp that cannot be read":      "tomorrow",
	} {
		t.Run(name, func(t *testing.T) {
			store := newTestImageStore()
			_, err := store.Create(context.Background(), images.Image{
				Name:   testImageName,
				Target: testImage,
				Labels: map[string]string{expireLabel: stamped},
			})
			require.NoError(t, err)

			require.NoError(t, sweepImages(context.Background(), store, time.Hour, testNow))
			require.Equal(t, map[string]string{expireLabel: stamp(testNow.Add(time.Hour))}, store.labels(testImageName))
		})
	}
}

func TestSweepImagesRemovesStampedImagesOnceThereIsNoRetention(t *testing.T) {
	store := newTestImageStore()
	_, err := store.Create(context.Background(), images.Image{
		Name:   testImageName,
		Target: testImage,
		Labels: map[string]string{expireLabel: stamp(testNow.Add(24 * time.Hour))},
	})
	require.NoError(t, err)

	require.NoError(t, sweepImages(context.Background(), store, 0, testNow))
	require.Nil(t, store.labels(testImageName))
}

func TestSweepImagesKeepsAnImageWhenItCannotTellWhetherItsVolumeExists(t *testing.T) {
	for _, retention := range []time.Duration{0, 24 * time.Hour} {
		t.Run(retention.String(), func(t *testing.T) {
			store := newTestImageStore()

			// a directory that can neither be found nor be found missing
			unreadable := imageVolume{handle: "resource-cache", dir: "\x00"}
			require.NoError(t, storeImage(context.Background(), store, testImage, unreadable, true, retention, testNow))

			err := sweepImages(context.Background(), store, retention, testNow.Add(48*time.Hour))
			require.ErrorContains(t, err, "look for volume")

			require.Equal(t, volumeLabel(unreadable), store.labels(testImageName))
		})
	}
}

func TestSweepImagesSweepsTheOtherImagesWhenOneFails(t *testing.T) {
	store := newTestImageStore()
	volumes := newTestVolumes(t)
	other := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("other image"), Size: 1}

	require.NoError(t, storeImage(context.Background(), store, testImage, imageVolume{handle: "unreadable", dir: "\x00"}, true, 0, testNow))
	require.NoError(t, storeImage(context.Background(), store, other, volumes.create("streamed-1"), true, 0, testNow))
	volumes.destroy("streamed-1")

	require.Error(t, sweepImages(context.Background(), store, 0, testNow))

	require.NotNil(t, store.labels(testImageName))
	require.Nil(t, store.labels(other.Digest.String()), "the image whose volume is gone should be removed all the same")
}

func TestSweepImagesKeepsTheLabelOfAContainerCreatedWhileItSweeps(t *testing.T) {
	store := newTestImageStore()
	volumes := newTestVolumes(t)
	require.NoError(t, storeImage(context.Background(), store, testImage, volumes.create("streamed-1"), true, 24*time.Hour, testNow))
	volumes.destroy("streamed-1")

	// a container is created from another volume after the sweep listed the
	// images, and before it removes the label of the volume that is gone
	second := volumes.create("streamed-2")
	store.once("update", func() {
		require.NoError(t, storeImage(context.Background(), store, testImage, second, true, 24*time.Hour, testNow))
	})

	require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, testNow))

	require.Equal(t, volumeLabel(second), store.labels(testImageName))
}

func TestSweepImagesDoesNotRemoveAnImageThatChangedSinceItFoundItExpired(t *testing.T) {
	for name, use := range map[string]func(store *testImageStore, volumes testVolumes) map[string]string{
		"a container labelled it with a volume": func(store *testImageStore, volumes testVolumes) map[string]string {
			second := volumes.create("streamed-2")
			require.NoError(t, storeImage(context.Background(), store, testImage, second, true, 24*time.Hour, testNow))
			return volumeLabel(second)
		},
		"a container outside a volume stamped it again": func(store *testImageStore, _ testVolumes) map[string]string {
			require.NoError(t, storeImage(context.Background(), store, testImage, noVolume, false, 24*time.Hour, testNow))
			return map[string]string{expireLabel: stamp(testNow.Add(24 * time.Hour))}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newTestImageStore()
			_, err := store.Create(context.Background(), images.Image{
				Name:   testImageName,
				Target: testImage,
				Labels: map[string]string{expireLabel: stamp(testNow.Add(-time.Minute))},
			})
			require.NoError(t, err)

			// after the sweep found the image expired, before it removes it
			var labels map[string]string
			store.once("get", func() { labels = use(store, newTestVolumes(t)) })

			require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, testNow))

			require.Equal(t, labels, store.labels(testImageName))
		})
	}
}

func TestSweepImagesClearsAStampLeftOnAnImageThatAVolumeHolds(t *testing.T) {
	// what a sweep that stamps an image leaves behind when a container
	// labels the image at the same moment
	store := newTestImageStore()
	second := newTestVolumes(t).create("streamed-2")
	labels := volumeLabel(second)
	labels[expireLabel] = stamp(testNow.Add(-time.Minute))
	_, err := store.Create(context.Background(), images.Image{Name: testImageName, Target: testImage, Labels: labels})
	require.NoError(t, err)

	require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, testNow))

	require.Equal(t, volumeLabel(second), store.labels(testImageName))
}

func TestSweepImagesToleratesAnImageRemovedWhileItSweeps(t *testing.T) {
	for _, operation := range []string{"update", "get", "delete"} {
		t.Run("before its "+operation, func(t *testing.T) {
			store := newTestImageStore()
			volumes := newTestVolumes(t)
			require.NoError(t, storeImage(context.Background(), store, testImage, volumes.create("streamed-1"), true, 0, testNow))
			volumes.destroy("streamed-1")

			store.once(operation, func() { store.remove(testImageName) })

			require.NoError(t, sweepImages(context.Background(), store, 0, testNow))
			require.Nil(t, store.labels(testImageName))
		})
	}

	t.Run("before it is stamped", func(t *testing.T) {
		store := newTestImageStore()
		_, err := store.Create(context.Background(), images.Image{Name: testImageName, Target: testImage})
		require.NoError(t, err)

		store.once("update", func() { store.remove(testImageName) })

		require.NoError(t, sweepImages(context.Background(), store, 24*time.Hour, testNow))
	})
}

func TestSweepImagesReportsWhenItCannotListTheImages(t *testing.T) {
	failed := errors.New("containerd is unreachable")

	err := sweepImages(context.Background(), failingImageStore{failed}, 24*time.Hour, testNow)
	require.ErrorIs(t, err, failed)
}

// failingImageStore fails to list its images.
type failingImageStore struct{ err error }

func (s failingImageStore) List(context.Context, ...string) ([]images.Image, error) {
	return nil, s.err
}
func (s failingImageStore) Get(context.Context, string) (images.Image, error) {
	return images.Image{}, s.err
}
func (s failingImageStore) Create(context.Context, images.Image) (images.Image, error) {
	return images.Image{}, s.err
}
func (s failingImageStore) Update(context.Context, images.Image, ...string) (images.Image, error) {
	return images.Image{}, s.err
}
func (s failingImageStore) Delete(context.Context, string, ...images.DeleteOpt) error {
	return s.err
}
