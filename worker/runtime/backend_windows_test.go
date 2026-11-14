//go:build windows

package runtime_test

import (
	"errors"
	"testing"

	"code.cloudfoundry.org/garden"
	"github.com/concourse/concourse/v8/worker/runtime"
	"github.com/concourse/concourse/v8/worker/runtime/libcontainerd/libcontainerdfakes"
	"github.com/concourse/concourse/v8/worker/runtime/runtimefakes"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

// attachingNetwork is a network that, like HCNNetwork, attaches a container
// when it is created.
type attachingNetwork struct {
	*runtimefakes.FakeNetwork

	detach   func(oci *specs.Spec) error
	detached []*specs.Spec
}

var _ runtime.ContainerAttacher = (*attachingNetwork)(nil)

func (n *attachingNetwork) AttachContainer(handle string, oci *specs.Spec) error {
	return nil
}

func (n *attachingNetwork) DetachContainer(oci *specs.Spec) error {
	n.detached = append(n.detached, oci)
	if n.detach != nil {
		return n.detach(oci)
	}
	return nil
}

func destroyableContainer(t *testing.T) (runtime.GardenBackend, *attachingNetwork, *libcontainerdfakes.FakeContainer, *specs.Spec) {
	network := &attachingNetwork{FakeNetwork: new(runtimefakes.FakeNetwork)}
	client := new(libcontainerdfakes.FakeClient)

	backend, err := runtime.NewGardenBackend(client,
		runtime.WithNetwork(network),
		runtime.WithKiller(new(runtimefakes.FakeKiller)),
		runtime.WithIOManager(new(runtimefakes.FakeIOManager)),
	)
	require.NoError(t, err)

	oci := &specs.Spec{Windows: &specs.Windows{Network: &specs.WindowsNetwork{NetworkNamespace: "some-namespace"}}}
	container := new(libcontainerdfakes.FakeContainer)
	container.SpecReturns(oci, nil)
	client.GetContainerReturns(container, nil)
	client.NewContainerFromImageLayoutReturns(container, nil)

	return backend, network, container, oci
}

func TestCreateRemovesAContainerWhoseTaskDidNotStart(t *testing.T) {
	backend, network, container, oci := destroyableContainer(t)

	task := new(libcontainerdfakes.FakeTask)
	container.NewTaskReturns(task, nil)
	container.TaskReturns(task, nil)

	// what a container gets from an image that the host cannot run under
	// process isolation
	failed := errors.New("hcs::System::Start: The virtual machine or container exited unexpectedly.")
	task.StartReturns(failed)

	_, err := backend.Create(garden.ContainerSpec{Handle: "some-handle", RootFSPath: `raw://C:\rootfs`})
	require.ErrorIs(t, err, failed)

	// nothing of it stays: not the container, which the web node would find
	// and run its step in, and not its network namespace
	require.Equal(t, 1, task.DeleteCallCount())
	require.Equal(t, []*specs.Spec{oci}, network.detached)
	require.Equal(t, 1, container.DeleteCallCount())
}

func TestDestroyDetachesAContainerOnceItsTaskIsDeleted(t *testing.T) {
	backend, network, container, oci := destroyableContainer(t)

	task := new(libcontainerdfakes.FakeTask)
	container.TaskReturns(task, nil)

	// Windows Server 2022 doesn't delete a network namespace that a
	// container still uses
	network.detach = func(*specs.Spec) error {
		require.Equal(t, 1, task.DeleteCallCount(), "the task should be deleted before the container is detached")
		require.Equal(t, 0, container.DeleteCallCount(), "the container should be deleted after it is detached")
		return nil
	}

	require.NoError(t, backend.Destroy("some-handle"))

	require.Equal(t, []*specs.Spec{oci}, network.detached)
	require.Equal(t, 1, container.DeleteCallCount())
}

func TestDestroyDetachesAContainerWhoseTaskIsGone(t *testing.T) {
	backend, network, container, oci := destroyableContainer(t)
	container.TaskReturns(nil, errdefs.ErrNotFound)

	// the container holds the spec that names its namespace
	network.detach = func(*specs.Spec) error {
		require.Equal(t, 0, container.DeleteCallCount(), "the container should be deleted after it is detached")
		return nil
	}

	require.NoError(t, backend.Destroy("some-handle"))

	require.Equal(t, []*specs.Spec{oci}, network.detached)
	require.Equal(t, 1, container.DeleteCallCount())
}

func TestDestroyKeepsAContainerThatItCouldNotDetach(t *testing.T) {
	backend, network, container, _ := destroyableContainer(t)
	container.TaskReturns(new(libcontainerdfakes.FakeTask), nil)

	failed := errors.New("detach-failed")
	network.detach = func(*specs.Spec) error { return failed }

	// the container holds the spec that names its namespace: the next
	// attempt to destroy it needs it
	require.ErrorIs(t, backend.Destroy("some-handle"), failed)
	require.Equal(t, 0, container.DeleteCallCount())
}
