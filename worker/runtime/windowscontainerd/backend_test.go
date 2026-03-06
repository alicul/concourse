package windowscontainerd_test

import (
	"errors"
	"testing"
	"time"

	"code.cloudfoundry.org/garden"
	"github.com/concourse/concourse/worker/runtime/libcontainerd/libcontainerdfakes"
	"github.com/concourse/concourse/worker/runtime/windowscontainerd"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/errdefs"
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
