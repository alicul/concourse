package libcontainerd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// volumeLabelPrefix starts the label that ties a stored image to a
	// baggageclaim volume holding its image layout. The volume's handle
	// follows it, and the label's value is the volume's directory.
	volumeLabelPrefix = "concourse.ci/volume."

	// expireLabel holds the time after which sweepImages removes a stored
	// image that no volume holds.
	expireLabel = "concourse.ci/expires"

	// sweepTimeout bounds one sweep of the stored images, so that a
	// containerd that doesn't answer cannot hold up whoever sweeps.
	sweepTimeout = time.Minute
)

// imageVolume is a baggageclaim volume that holds an image layout.
type imageVolume struct {
	handle string
	dir    string
}

// volumeOfImageLayout finds the baggageclaim volume that holds the image
// layout in dir. It returns no volume when dir isn't inside one.
//
// The web node doesn't hand a container the volume that holds its image, but
// a copy-on-write volume of it, which it destroys with the container.
// Baggageclaim links such a volume to the one it was made from with a "parent"
// symlink. The volume returned is that parent, which is what outlives the
// container: a resource cache, a resource type's volume, a task's output or
// a volume streamed in from another worker. The parent's own parents hold
// other things, so the search stops there.
func volumeOfImageLayout(dir string) (imageVolume, bool) {
	volume := filepath.Clean(dir)
	if filepath.Base(volume) == "rootfs" {
		volume = filepath.Dir(volume)
	}
	if filepath.Base(volume) == "volume" {
		volume = filepath.Dir(volume)
	}

	if !isLiveVolume(volume) {
		return imageVolume{}, false
	}

	parent, err := filepath.EvalSymlinks(filepath.Join(volume, "parent"))
	if err == nil && isLiveVolume(parent) {
		volume = parent
	}

	return imageVolume{handle: filepath.Base(volume), dir: volume}, true
}

// isLiveVolume reports whether dir is where baggageclaim keeps a volume:
// <volumes directory>/live/<handle>.
func isLiveVolume(dir string) bool {
	return filepath.Base(filepath.Dir(dir)) == "live"
}

// volumeExists reports whether baggageclaim still has the volume in dir. To
// destroy a volume, baggageclaim first moves it out of the live directory.
func volumeExists(dir string) (bool, error) {
	_, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil
}

// storeImage records target in the image store, so that containerd keeps its
// content and unpacked layers after the container that imported it is gone,
// and a later container on the same image skips the copy and the unpack.
//
// The image is labelled with the volume holding its image layout, and stays
// stored until sweepImages finds the volume gone. An image that is in no
// volume only stays for the retention, counted from its last use, and isn't
// stored at all without one.
func storeImage(
	ctx context.Context,
	store images.Store,
	target ocispec.Descriptor,
	volume imageVolume,
	inVolume bool,
	retention time.Duration,
	now time.Time,
) error {
	image := images.Image{
		Name:   target.Digest.String(),
		Target: target,
		Labels: map[string]string{},
	}

	var labels []string
	switch {
	case inVolume:
		// a volume holds the image again, so an expiry stamped while none
		// did no longer applies: updating a label to no value removes it
		image.Labels[volumeLabelPrefix+volume.handle] = volume.dir
		labels = []string{"labels." + volumeLabelPrefix + volume.handle, "labels." + expireLabel}
	case retention > 0:
		image.Labels[expireLabel] = stamp(now.Add(retention))
		labels = []string{"labels." + expireLabel}
	default:
		return nil
	}

	// sweepImages may remove the image between the Create that finds it
	// stored and the Update
	for range 3 {
		_, err := store.Create(ctx, image)
		if err == nil {
			return nil
		}
		if !errdefs.IsAlreadyExists(err) {
			return fmt.Errorf("store image %s: %w", image.Name, err)
		}

		_, err = store.Update(ctx, image, labels...)
		if err == nil {
			return nil
		}
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("label stored image %s: %w", image.Name, err)
		}
	}

	return fmt.Errorf("store image %s: removed each time it was stored", image.Name)
}

// sweepImages lets go of the stored images that no volume on the worker holds
// any more.
//
// An image is kept for as long as one of its volumes exists. When the last
// one is gone, the image is stamped to expire after the retention. It is
// removed once that time has passed, unless a new container labelled it with
// another volume in the meantime. With the image removed, containerd collects
// its content and unpacked layers.
func sweepImages(ctx context.Context, store images.Store, retention time.Duration, now time.Time) error {
	stored, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}

	var errs []error
	for _, image := range stored {
		err := sweepImage(ctx, store, image, retention, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("image %s: %w", image.Name, err))
		}
	}

	return errors.Join(errs...)
}

// sweepImage applies sweepImages to one stored image.
func sweepImage(ctx context.Context, store images.Store, image images.Image, retention time.Duration, now time.Time) error {
	var (
		gone   = images.Image{Name: image.Name, Labels: map[string]string{}}
		labels []string
		errs   []error
	)

	for label, dir := range image.Labels {
		if !strings.HasPrefix(label, volumeLabelPrefix) {
			continue
		}

		exists, err := volumeExists(dir)
		if err != nil {
			// not knowing is no reason to let go of the image
			errs = append(errs, fmt.Errorf("look for volume %s: %w", dir, err))
			continue
		}

		if !exists {
			gone.Labels[label] = ""
			labels = append(labels, "labels."+label)
		}
	}

	if len(labels) > 0 {
		// only removes the labels of the volumes that are gone, and returns
		// the image with any label a new container added since it was listed
		var err error
		image, err = store.Update(ctx, gone, labels...)
		if errdefs.IsNotFound(err) {
			return errors.Join(errs...)
		}
		if err != nil {
			return errors.Join(append(errs, fmt.Errorf("remove volume labels: %w", err))...)
		}
	}

	stamped := image.Labels[expireLabel]
	expire, err := time.Parse(time.RFC3339, stamped)
	readable := err == nil

	switch {
	case heldByVolume(image):
		if stamped != "" {
			// a container labelled the image while a sweep was stamping it
			errs = append(errs, stampImage(ctx, store, image.Name, ""))
		}

	case retention <= 0:
		errs = append(errs, removeImage(ctx, store, image.Name, stamped))

	case !readable || expire.After(now.Add(retention)):
		// not stamped yet, or stamped under a longer retention
		errs = append(errs, stampImage(ctx, store, image.Name, stamp(now.Add(retention))))

	case !now.Before(expire):
		errs = append(errs, removeImage(ctx, store, image.Name, stamped))
	}

	return errors.Join(errs...)
}

// heldByVolume reports whether a stored image is labelled with a volume.
func heldByVolume(image images.Image) bool {
	for label := range image.Labels {
		if strings.HasPrefix(label, volumeLabelPrefix) {
			return true
		}
	}

	return false
}

// stamp formats the time at which a stored image expires.
func stamp(expire time.Time) string {
	return expire.UTC().Format(time.RFC3339)
}

// stampImage sets the time at which a stored image expires, or clears it
// when expire is empty.
func stampImage(ctx context.Context, store images.Store, name string, expire string) error {
	_, err := store.Update(ctx, images.Image{
		Name:   name,
		Labels: map[string]string{expireLabel: expire},
	}, "labels."+expireLabel)
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("stamp expiry: %w", err)
	}

	return nil
}

// removeImage removes a stored image that no volume holds, unless a container
// labelled it with a volume, or stamped it again, since it was found to be
// expired with the given stamp.
//
// A container that labels the image between this check and the removal loses
// the label. That container still runs on its own snapshot of the layers,
// and the next one on the image imports it again.
func removeImage(ctx context.Context, store images.Store, name string, stamped string) error {
	image, err := store.Get(ctx, name)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}

	if heldByVolume(image) || image.Labels[expireLabel] != stamped {
		return nil
	}

	err = store.Delete(ctx, name)
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove: %w", err)
	}

	return nil
}
