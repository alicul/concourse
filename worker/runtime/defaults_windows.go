//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/opencontainers/runtime-spec/specs-go"
)

func defaultNetwork() (Network, error) {
	return NewNoopNetwork(), nil
}

// defaultSeccompProfiles returns empty profiles; seccomp is a Linux-only
// concept and is never applied to Windows containers.
func defaultSeccompProfiles() (profile, profileFuse specs.LinuxSeccomp) {
	return specs.LinuxSeccomp{}, specs.LinuxSeccomp{}
}

func defaultTaskOpts() []containerd.NewTaskOpts {
	return nil
}

// newContainer creates the container from a snapshot of the image in its
// rootfs directory, an OCI image layout. The windows snapshotter applies each
// layer with the NTFS security descriptors and file attributes from its tar
// headers and with its own registry hive deltas.
func (b *GardenBackend) newContainer(ctx context.Context, handle string, labels map[string]string, oci *specs.Spec) (containerd.Container, error) {
	imageLayout := oci.Root.Path
	oci.Root = nil

	if b.hyperV != nil {
		b.hyperV.Isolate(oci)
	}

	attacher, attach := b.network.(ContainerAttacher)
	if attach {
		err := attacher.AttachContainer(handle, oci)
		if err != nil {
			return nil, fmt.Errorf("attach container network: %w", err)
		}
	}

	cont, err := b.client.NewContainerFromImageLayout(ctx, handle, labels, oci, imageLayout)
	if err != nil && attach {
		return nil, errors.Join(err, attacher.DetachContainer(oci))
	}

	return cont, err
}

// detachContainer deletes the network namespace that newContainer attached to
// the container, and the endpoint in it. Windows Server 2022 refuses to
// delete a namespace that a running container still uses, so this has to
// come after the container's task is deleted.
func (b *GardenBackend) detachContainer(ctx context.Context, cont containerd.Container) error {
	attacher, attach := b.network.(ContainerAttacher)
	if !attach {
		return nil
	}

	oci, err := cont.Spec(ctx)
	if err != nil {
		return fmt.Errorf("container spec: %w", err)
	}

	return attacher.DetachContainer(oci)
}
