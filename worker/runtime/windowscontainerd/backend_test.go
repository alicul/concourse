package windowscontainerd_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/garden"
	"github.com/concourse/concourse/worker/runtime/libcontainerd/libcontainerdfakes"
	"github.com/concourse/concourse/worker/runtime/windowscontainerd"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type BackendSuite struct {
	suite.Suite
	*require.Assertions

	backend windowscontainerd.GardenBackend
	client  *libcontainerdfakes.FakeClient
}

func (s *BackendSuite) SetupTest() {
	s.client = new(libcontainerdfakes.FakeClient)

	var err error
	s.backend, err = windowscontainerd.NewGardenBackend(s.client)
	s.NoError(err)
}

func (s *BackendSuite) TestNewWithNilClient() {
	_, err := windowscontainerd.NewGardenBackend(nil)
	s.EqualError(err, "nil client")
}

func (s *BackendSuite) TestNewWithOptions() {
	_, err := windowscontainerd.NewGardenBackend(s.client,
		windowscontainerd.WithRequestTimeout(5*time.Second),
		windowscontainerd.WithMaxContainers(100),
		windowscontainerd.WithDNSServers([]string{"8.8.8.8"}),
	)
	s.NoError(err)
}

func (s *BackendSuite) TestStart() {
	s.client.InitReturns(nil)

	err := s.backend.Start()
	s.NoError(err)
	s.Equal(1, s.client.InitCallCount())
}

func (s *BackendSuite) TestStartError() {
	s.client.InitReturns(errors.New("init-err"))

	err := s.backend.Start()
	s.EqualError(err, "init-err")
}

func (s *BackendSuite) TestStop() {
	s.client.StopReturns(nil)

	err := s.backend.Stop()
	s.NoError(err)
	s.Equal(1, s.client.StopCallCount())
}

func (s *BackendSuite) TestPing() {
	for _, tc := range []struct {
		desc          string
		versionReturn error
		succeeds      bool
	}{
		{
			desc:          "success",
			succeeds:      true,
			versionReturn: nil,
		},
		{
			desc:          "failure",
			succeeds:      false,
			versionReturn: errors.New("version-err"),
		},
	} {
		s.T().Run(tc.desc, func(t *testing.T) {
			s.client.VersionReturns(tc.versionReturn)

			err := s.backend.Ping()
			if tc.succeeds {
				s.NoError(err)
			} else {
				s.Error(err)
			}
		})
	}
}

var (
	invalidGdnSpec      = garden.ContainerSpec{}
	minimumValidGdnSpec = garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
	}
)

func (s *BackendSuite) TestCreateWithInvalidSpec() {
	_, err := s.backend.Create(invalidGdnSpec)
	s.Error(err)
	s.Equal(0, s.client.NewContainerCallCount())
}

func (s *BackendSuite) TestCreateWithNewContainerFailure() {
	s.client.NewContainerReturns(nil, errors.New("container-err"))

	_, err := s.backend.Create(minimumValidGdnSpec)
	s.Error(err)
	s.Equal(1, s.client.NewContainerCallCount())
}

func (s *BackendSuite) TestCreateNewTaskFailure() {
	fakeContainer := new(libcontainerdfakes.FakeContainer)
	fakeContainer.NewTaskReturns(nil, errors.New("task-err"))
	s.client.NewContainerReturns(fakeContainer, nil)

	_, err := s.backend.Create(minimumValidGdnSpec)
	s.Error(err)
	s.Contains(err.Error(), "task-err")
	s.Equal(1, fakeContainer.NewTaskCallCount())
}

func (s *BackendSuite) TestCreateTaskStartFailure() {
	fakeTask := new(libcontainerdfakes.FakeTask)
	fakeContainer := new(libcontainerdfakes.FakeContainer)

	s.client.NewContainerReturns(fakeContainer, nil)
	fakeContainer.NewTaskReturns(fakeTask, nil)
	fakeTask.StartReturns(errors.New("start-err"))

	_, err := s.backend.Create(minimumValidGdnSpec)
	s.Error(err)
	s.Contains(err.Error(), "start-err")
}

func (s *BackendSuite) TestCreateSuccess() {
	fakeTask := new(libcontainerdfakes.FakeTask)
	fakeContainer := new(libcontainerdfakes.FakeContainer)

	fakeContainer.IDReturns("handle")
	fakeContainer.NewTaskReturns(fakeTask, nil)
	fakeTask.StartReturns(nil)
	s.client.NewContainerReturns(fakeContainer, nil)

	cont, err := s.backend.Create(minimumValidGdnSpec)
	s.NoError(err)
	s.Equal("handle", cont.Handle())
}

func (s *BackendSuite) TestCreatePassesLabelsFromProperties() {
	fakeTask := new(libcontainerdfakes.FakeTask)
	fakeContainer := new(libcontainerdfakes.FakeContainer)

	fakeContainer.NewTaskReturns(fakeTask, nil)
	s.client.NewContainerReturns(fakeContainer, nil)

	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
		Properties: garden.Properties{
			"key": "value",
		},
	}

	_, err := s.backend.Create(spec)
	s.NoError(err)

	s.Equal(1, s.client.NewContainerCallCount())
	_, _, labels, _ := s.client.NewContainerArgsForCall(0)
	s.Equal("value", labels["key.0"])
}

func (s *BackendSuite) TestCreateMaxContainersReached() {
	backend, err := windowscontainerd.NewGardenBackend(s.client,
		windowscontainerd.WithMaxContainers(1),
	)
	s.NoError(err)

	s.client.ContainersReturns([]containerd.Container{
		new(libcontainerdfakes.FakeContainer),
	}, nil)

	_, err = backend.Create(minimumValidGdnSpec)
	s.Error(err)
	s.Contains(err.Error(), "max containers reached")
}

func (s *BackendSuite) TestCreateMaxContainersZeroMeansUnlimited() {
	fakeTask := new(libcontainerdfakes.FakeTask)
	fakeContainer := new(libcontainerdfakes.FakeContainer)
	fakeContainer.NewTaskReturns(fakeTask, nil)
	s.client.NewContainerReturns(fakeContainer, nil)

	s.client.ContainersReturns([]containerd.Container{
		new(libcontainerdfakes.FakeContainer),
		new(libcontainerdfakes.FakeContainer),
	}, nil)

	_, err := s.backend.Create(minimumValidGdnSpec)
	s.NoError(err)
}

func (s *BackendSuite) TestDestroyEmptyHandle() {
	err := s.backend.Destroy("")
	s.Error(err)
	s.Contains(err.Error(), "empty handle")
}

func (s *BackendSuite) TestDestroyGetContainerFailure() {
	s.client.GetContainerReturns(nil, errors.New("get-err"))

	err := s.backend.Destroy("handle")
	s.Error(err)
	s.Contains(err.Error(), "get-err")
}

func (s *BackendSuite) TestDestroyTaskNotFound() {
	fakeContainer := new(libcontainerdfakes.FakeContainer)
	fakeContainer.TaskReturns(nil, errdefs.ErrNotFound)
	fakeContainer.DeleteReturns(nil)
	s.client.GetContainerReturns(fakeContainer, nil)

	err := s.backend.Destroy("handle")
	s.NoError(err)
	s.Equal(1, fakeContainer.DeleteCallCount())
}

func (s *BackendSuite) TestContainersList() {
	fakeContainer := new(libcontainerdfakes.FakeContainer)
	fakeContainer.IDReturns("container-1")
	s.client.ContainersReturns([]containerd.Container{fakeContainer}, nil)

	containers, err := s.backend.Containers(garden.Properties{})
	s.NoError(err)
	s.Len(containers, 1)
	s.Equal("container-1", containers[0].Handle())
}

func (s *BackendSuite) TestContainersListError() {
	s.client.ContainersReturns(nil, errors.New("list-err"))

	_, err := s.backend.Containers(garden.Properties{})
	s.Error(err)
	s.Contains(err.Error(), "list-err")
}

func (s *BackendSuite) TestLookup() {
	fakeContainer := new(libcontainerdfakes.FakeContainer)
	fakeContainer.IDReturns("handle")
	s.client.GetContainerReturns(fakeContainer, nil)

	container, err := s.backend.Lookup("handle")
	s.NoError(err)
	s.Equal("handle", container.Handle())
}

func (s *BackendSuite) TestLookupEmptyHandle() {
	_, err := s.backend.Lookup("")
	s.Error(err)
	s.Contains(err.Error(), "empty handle")
}

func (s *BackendSuite) TestLookupError() {
	s.client.GetContainerReturns(nil, errors.New("lookup-err"))

	_, err := s.backend.Lookup("handle")
	s.Error(err)
	s.Contains(err.Error(), "lookup-err")
}

func (s *BackendSuite) TestGraceTime() {
	fakeContainer := new(libcontainerdfakes.FakeContainer)
	fakeContainer.IDReturns("handle")
	fakeContainer.LabelsReturns(map[string]string{
		"garden.grace-time.0": "5000000000",
	}, nil)
	s.client.GetContainerReturns(fakeContainer, nil)

	container, err := s.backend.Lookup("handle")
	s.NoError(err)

	duration := s.backend.GraceTime(container)
	s.Equal(time.Duration(5000000000), duration)
}

func (s *BackendSuite) TestGraceTimeNotSet() {
	fakeContainer := new(libcontainerdfakes.FakeContainer)
	fakeContainer.LabelsReturns(map[string]string{}, nil)
	s.client.GetContainerReturns(fakeContainer, nil)

	container, err := s.backend.Lookup("handle")
	s.NoError(err)

	duration := s.backend.GraceTime(container)
	s.Equal(time.Duration(0), duration)
}

func (s *BackendSuite) TestCapacity() {
	_, err := s.backend.Capacity()
	s.Error(err)
}

func (s *BackendSuite) TestBulkInfo() {
	_, err := s.backend.BulkInfo(nil)
	s.Error(err)
}

func (s *BackendSuite) TestBulkMetrics() {
	_, err := s.backend.BulkMetrics(nil)
	s.Error(err)
}

type fakeImage struct {
	name string
}

func (f *fakeImage) Name() string                                                    { return f.name }
func (f *fakeImage) Target() ocispec.Descriptor                                      { return ocispec.Descriptor{} }
func (f *fakeImage) Labels() map[string]string                                       { return nil }
func (f *fakeImage) Unpack(_ context.Context, _ string, _ ...containerd.UnpackOpt) error { return nil }
func (f *fakeImage) RootFS(_ context.Context) ([]digest.Digest, error)               { return nil, nil }
func (f *fakeImage) Size(_ context.Context) (int64, error)                           { return 0, nil }
func (f *fakeImage) Usage(_ context.Context, _ ...containerd.UsageOpt) (int64, error) { return 0, nil }
func (f *fakeImage) Config(_ context.Context) (ocispec.Descriptor, error)            { return ocispec.Descriptor{}, nil }
func (f *fakeImage) IsUnpacked(_ context.Context, _ string) (bool, error)            { return false, nil }
func (f *fakeImage) ContentStore() content.Store                                     { return nil }
func (f *fakeImage) Metadata() images.Image                                          { return images.Image{} }
func (f *fakeImage) Platform() platforms.MatchComparer                               { return nil }
func (f *fakeImage) Spec(_ context.Context) (ocispec.Image, error)                   { return ocispec.Image{}, nil }

var _ containerd.Image = (*fakeImage)(nil)

type fakeImageClient struct {
	importImageCalled bool
	importImageErr    error
	importImageResult images.Image

	getImageCalled bool
	getImageErr    error
	getImageResult containerd.Image

	unpackCalled bool
	unpackErr    error

	newContainerCalled bool
	newContainerErr    error
	newContainerResult containerd.Container
}

func (f *fakeImageClient) ImportImage(_ context.Context, _ io.Reader, _ string) (images.Image, error) {
	f.importImageCalled = true
	return f.importImageResult, f.importImageErr
}

func (f *fakeImageClient) GetImage(_ context.Context, _ string) (containerd.Image, error) {
	f.getImageCalled = true
	return f.getImageResult, f.getImageErr
}

func (f *fakeImageClient) UnpackImage(_ context.Context, _ containerd.Image, _ string) error {
	f.unpackCalled = true
	return f.unpackErr
}

func (f *fakeImageClient) NewContainerFromImage(_ context.Context, _ string, _ containerd.Image, _ map[string]string, _ string, _ ...oci.SpecOpts) (containerd.Container, error) {
	f.newContainerCalled = true
	return f.newContainerResult, f.newContainerErr
}

func (s *BackendSuite) setupOCITarballVolume(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "image.tar")
	_ = os.WriteFile(tarPath, []byte("fake-oci-tarball"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "repository"), []byte("mcr.microsoft.com/windows/nanoserver"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "tag"), []byte("ltsc2022"), 0644)
	rootfsDir := filepath.Join(dir, "rootfs")
	return rootfsDir, tarPath
}

func (s *BackendSuite) TestCreateDetectsOCITarball() {
	rootfsDir, _ := s.setupOCITarballVolume(s.T())

	fakeContainer := new(libcontainerdfakes.FakeContainer)
	fakeTask := new(libcontainerdfakes.FakeTask)
	fakeContainer.NewTaskReturns(fakeTask, nil)
	fakeContainer.IDReturns("handle")

	imgClient := &fakeImageClient{
		importImageResult:  images.Image{Name: "mcr.microsoft.com/windows/nanoserver:ltsc2022"},
		getImageResult:     &fakeImage{name: "mcr.microsoft.com/windows/nanoserver:ltsc2022"},
		newContainerResult: fakeContainer,
	}

	backend, err := windowscontainerd.NewGardenBackend(s.client,
		windowscontainerd.WithImageClient(imgClient),
	)
	s.NoError(err)

	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw://" + rootfsDir,
	}

	cont, err := backend.Create(spec)
	s.NoError(err)
	s.Equal("handle", cont.Handle())

	s.True(imgClient.importImageCalled)
	s.True(imgClient.getImageCalled)
	s.True(imgClient.unpackCalled)
	s.True(imgClient.newContainerCalled)
	s.Equal(0, s.client.NewContainerCallCount(), "should not use spec-based container creation")
}

func (s *BackendSuite) TestCreateFallsBackToRootFSWhenNoImageTar() {
	dir := s.T().TempDir()
	rootfsDir := filepath.Join(dir, "rootfs")
	_ = os.MkdirAll(rootfsDir, 0755)

	imgClient := &fakeImageClient{}

	fakeContainer := new(libcontainerdfakes.FakeContainer)
	fakeTask := new(libcontainerdfakes.FakeTask)
	fakeContainer.NewTaskReturns(fakeTask, nil)
	s.client.NewContainerReturns(fakeContainer, nil)

	backend, err := windowscontainerd.NewGardenBackend(s.client,
		windowscontainerd.WithImageClient(imgClient),
		windowscontainerd.WithWorkDir(s.T().TempDir()),
	)
	s.NoError(err)

	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw://" + rootfsDir,
	}

	_, err = backend.Create(spec)
	s.NoError(err)

	s.False(imgClient.importImageCalled, "should not attempt OCI import")
	s.Equal(1, s.client.NewContainerCallCount(), "should use spec-based container creation")
}

func (s *BackendSuite) TestCreateOCITarballImportFailure() {
	rootfsDir, _ := s.setupOCITarballVolume(s.T())

	imgClient := &fakeImageClient{
		importImageErr: errors.New("import-failed"),
	}

	backend, err := windowscontainerd.NewGardenBackend(s.client,
		windowscontainerd.WithImageClient(imgClient),
	)
	s.NoError(err)

	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw://" + rootfsDir,
	}

	_, err = backend.Create(spec)
	s.Error(err)
	s.Contains(err.Error(), "import-failed")
}

func (s *BackendSuite) TestCreateOCITarballUnpackFailure() {
	rootfsDir, _ := s.setupOCITarballVolume(s.T())

	imgClient := &fakeImageClient{
		importImageResult: images.Image{Name: "test-image"},
		getImageResult:    &fakeImage{name: "test-image"},
		unpackErr:         errors.New("unpack-failed"),
	}

	backend, err := windowscontainerd.NewGardenBackend(s.client,
		windowscontainerd.WithImageClient(imgClient),
	)
	s.NoError(err)

	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw://" + rootfsDir,
	}

	_, err = backend.Create(spec)
	s.Error(err)
	s.Contains(err.Error(), "unpack-failed")
}

func (s *BackendSuite) TestCreateOCITarballNewContainerFailure() {
	rootfsDir, _ := s.setupOCITarballVolume(s.T())

	imgClient := &fakeImageClient{
		importImageResult: images.Image{Name: "test-image"},
		getImageResult:    &fakeImage{name: "test-image"},
		newContainerErr:   errors.New("container-create-failed"),
	}

	backend, err := windowscontainerd.NewGardenBackend(s.client,
		windowscontainerd.WithImageClient(imgClient),
	)
	s.NoError(err)

	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw://" + rootfsDir,
	}

	_, err = backend.Create(spec)
	s.Error(err)
	s.Contains(err.Error(), "container-create-failed")
}
