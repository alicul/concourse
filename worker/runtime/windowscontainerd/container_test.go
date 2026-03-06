package windowscontainerd_test

import (
	"errors"

	"code.cloudfoundry.org/garden"
	"github.com/concourse/concourse/worker/runtime/libcontainerd/libcontainerdfakes"
	"github.com/concourse/concourse/worker/runtime/windowscontainerd"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ContainerSuite struct {
	suite.Suite
	*require.Assertions

	container           *windowscontainerd.Container
	containerdContainer *libcontainerdfakes.FakeContainer
	containerdTask      *libcontainerdfakes.FakeTask
}

func (s *ContainerSuite) SetupTest() {
	s.containerdContainer = new(libcontainerdfakes.FakeContainer)
	s.containerdTask = new(libcontainerdfakes.FakeTask)
	s.container = windowscontainerd.NewContainer(s.containerdContainer)
}

func (s *ContainerSuite) TestHandle() {
	s.containerdContainer.IDReturns("my-handle")
	s.Equal("my-handle", s.container.Handle())
}

func (s *ContainerSuite) TestStopGraceful() {
	s.containerdContainer.TaskReturns(s.containerdTask, nil)

	err := s.container.Stop(false)
	s.NoError(err)

	s.Equal(1, s.containerdTask.KillCallCount())
}

func (s *ContainerSuite) TestStopKill() {
	s.containerdContainer.TaskReturns(s.containerdTask, nil)

	err := s.container.Stop(true)
	s.NoError(err)

	s.Equal(1, s.containerdTask.KillCallCount())
}

func (s *ContainerSuite) TestStopTaskLookupError() {
	s.containerdContainer.TaskReturns(nil, errors.New("task-err"))

	err := s.container.Stop(false)
	s.Error(err)
	s.Contains(err.Error(), "task-err")
}

func (s *ContainerSuite) TestStopTaskNotFound() {
	s.containerdContainer.TaskReturns(nil, errdefs.ErrNotFound)

	err := s.container.Stop(false)
	s.NoError(err)
}

func (s *ContainerSuite) TestProperties() {
	s.containerdContainer.LabelsReturns(map[string]string{
		"key.0": "value",
	}, nil)

	props, err := s.container.Properties()
	s.NoError(err)
	s.Equal("value", props["key"])
}

func (s *ContainerSuite) TestPropertiesError() {
	s.containerdContainer.LabelsReturns(nil, errors.New("labels-err"))

	_, err := s.container.Properties()
	s.Error(err)
}

func (s *ContainerSuite) TestProperty() {
	s.containerdContainer.LabelsReturns(map[string]string{
		"mykey.0": "myval",
	}, nil)

	val, err := s.container.Property("mykey")
	s.NoError(err)
	s.Equal("myval", val)
}

func (s *ContainerSuite) TestPropertyNotFound() {
	s.containerdContainer.LabelsReturns(map[string]string{}, nil)

	_, err := s.container.Property("missing")
	s.Error(err)
	s.Contains(err.Error(), "not found")
}

func (s *ContainerSuite) TestSetProperty() {
	s.containerdContainer.SetLabelsReturns(nil, nil)

	err := s.container.SetProperty("key", "value")
	s.NoError(err)

	s.Equal(1, s.containerdContainer.SetLabelsCallCount())
	_, labels := s.containerdContainer.SetLabelsArgsForCall(0)
	s.Equal("value", labels["key.0"])
}

func (s *ContainerSuite) TestSetGraceTime() {
	s.containerdContainer.SetLabelsReturns(nil, nil)

	err := s.container.SetGraceTime(5000000000)
	s.NoError(err)

	s.Equal(1, s.containerdContainer.SetLabelsCallCount())
	_, labels := s.containerdContainer.SetLabelsArgsForCall(0)
	s.Equal("5000000000", labels["garden.grace-time.0"])
}

func (s *ContainerSuite) TestCurrentBandwidthLimits() {
	limits, err := s.container.CurrentBandwidthLimits()
	s.NoError(err)
	s.Equal(garden.BandwidthLimits{}, limits)
}

func (s *ContainerSuite) TestCurrentCPULimits() {
	limits, err := s.container.CurrentCPULimits()
	s.NoError(err)
	s.Equal(garden.CPULimits{}, limits)
}

func (s *ContainerSuite) TestCurrentDiskLimits() {
	limits, err := s.container.CurrentDiskLimits()
	s.NoError(err)
	s.Equal(garden.DiskLimits{}, limits)
}

func (s *ContainerSuite) TestCurrentMemoryLimitsNoSpec() {
	s.containerdContainer.SpecReturns(nil, errors.New("spec-err"))

	_, err := s.container.CurrentMemoryLimits()
	s.Error(err)
}

func (s *ContainerSuite) TestCurrentMemoryLimitsNoWindowsResources() {
	s.containerdContainer.SpecReturns(&specs.Spec{}, nil)

	limits, err := s.container.CurrentMemoryLimits()
	s.NoError(err)
	s.Equal(garden.MemoryLimits{}, limits)
}

func (s *ContainerSuite) TestCurrentMemoryLimitsWithLimit() {
	memLimit := uint64(1024 * 1024 * 256)
	s.containerdContainer.SpecReturns(&specs.Spec{
		Windows: &specs.Windows{
			Resources: &specs.WindowsResources{
				Memory: &specs.WindowsMemoryResources{
					Limit: &memLimit,
				},
			},
		},
	}, nil)

	limits, err := s.container.CurrentMemoryLimits()
	s.NoError(err)
	s.Equal(uint64(1024*1024*256), limits.LimitInBytes)
}

func (s *ContainerSuite) TestNotImplementedMethods() {
	s.Error(s.container.RemoveProperty("key"))
	_, err := s.container.Info()
	s.Error(err)
	_, err = s.container.Metrics()
	s.Error(err)
	s.Error(s.container.StreamIn(garden.StreamInSpec{}))
	_, err = s.container.StreamOut(garden.StreamOutSpec{})
	s.Error(err)
	_, _, err = s.container.NetIn(0, 0)
	s.Error(err)
	s.Error(s.container.NetOut(garden.NetOutRule{}))
	s.Error(s.container.BulkNetOut(nil))
}
