package windowscontainerd_test

import (
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/garden"
	"github.com/concourse/concourse/worker/runtime/libcontainerd/libcontainerdfakes"
	"github.com/concourse/concourse/worker/runtime/windowscontainerd"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/errdefs"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ProcessSuite struct {
	suite.Suite
	*require.Assertions

	io                  *libcontainerdfakes.FakeIO
	containerdProcess   *libcontainerdfakes.FakeProcess
	containerdContainer *libcontainerdfakes.FakeContainer
	ch                  chan containerd.ExitStatus
	container           *windowscontainerd.Container
	process             *windowscontainerd.Process
}

func (s *ProcessSuite) SetupTest() {
	s.io = new(libcontainerdfakes.FakeIO)
	s.containerdProcess = new(libcontainerdfakes.FakeProcess)
	s.containerdContainer = new(libcontainerdfakes.FakeContainer)
	s.ch = make(chan containerd.ExitStatus, 1)
	s.container = windowscontainerd.NewContainer(s.containerdContainer)
	s.process = windowscontainerd.NewProcess(s.containerdProcess, s.ch, s.container)
}

func (s *ProcessSuite) TestID() {
	s.containerdProcess.IDReturns("proc-id")
	id := s.process.ID()
	s.Equal("proc-id", id)
	s.Equal(1, s.containerdProcess.IDCallCount())
}

func (s *ProcessSuite) TestWaitSuccess() {
	s.ch <- *containerd.NewExitStatus(0, time.Now(), nil)
	s.containerdProcess.IOReturns(s.io)

	code, err := s.process.Wait()
	s.NoError(err)
	s.Equal(0, code)
	s.Equal(1, s.containerdProcess.CloseIOCallCount())
}

func (s *ProcessSuite) TestWaitNonZeroExitCode() {
	s.ch <- *containerd.NewExitStatus(42, time.Now(), nil)
	s.containerdProcess.IOReturns(s.io)

	code, err := s.process.Wait()
	s.NoError(err)
	s.Equal(42, code)
}

func (s *ProcessSuite) TestWaitStatusError() {
	expectedErr := errors.New("status-err")
	s.ch <- *containerd.NewExitStatus(0, time.Now(), expectedErr)

	_, err := s.process.Wait()
	s.True(errors.Is(err, expectedErr))
}

func (s *ProcessSuite) TestWaitDeleteError() {
	s.ch <- *containerd.NewExitStatus(0, time.Now(), nil)
	s.containerdProcess.IOReturns(s.io)
	s.containerdProcess.DeleteReturns(nil, errors.New("delete-err"))

	_, err := s.process.Wait()
	s.Error(err)
	s.Contains(err.Error(), "delete-err")
}

func (s *ProcessSuite) TestWaitDeleteNotFoundIgnored() {
	s.ch <- *containerd.NewExitStatus(0, time.Now(), nil)
	s.containerdProcess.IOReturns(s.io)
	s.containerdProcess.DeleteReturns(nil, fmt.Errorf("wrapped: %w", errdefs.ErrNotFound))

	code, err := s.process.Wait()
	s.NoError(err)
	s.Equal(0, code)
}

func (s *ProcessSuite) TestWaitBlocksUntilIOFinishes() {
	s.ch <- *containerd.NewExitStatus(0, time.Now(), nil)
	s.containerdProcess.IOReturns(s.io)

	s.io.WaitStub = func() {
		s.Equal(0, s.containerdProcess.DeleteCallCount())
	}

	_, err := s.process.Wait()
	s.NoError(err)

	s.Equal(3, s.containerdProcess.IOCallCount())
	s.Equal(1, s.io.CancelCallCount())
	s.Equal(1, s.io.WaitCallCount())
	s.Equal(1, s.io.CloseCallCount())
	s.Equal(1, s.containerdProcess.DeleteCallCount())
}

func (s *ProcessSuite) TestWaitStoresExitStatus() {
	s.ch <- *containerd.NewExitStatus(7, time.Now(), nil)
	s.containerdProcess.IOReturns(s.io)
	s.containerdContainer.SetLabelsReturns(nil, nil)

	code, err := s.process.Wait()
	s.NoError(err)
	s.Equal(7, code)

	s.Equal(1, s.containerdContainer.SetLabelsCallCount())
	_, labels := s.containerdContainer.SetLabelsArgsForCall(0)
	s.Equal("7", labels["garden.process-exit-status.0"])
}

func (s *ProcessSuite) TestSetTTYNilWindowSize() {
	err := s.process.SetTTY(garden.TTYSpec{})
	s.NoError(err)
	s.Equal(0, s.containerdProcess.ResizeCallCount())
}

func (s *ProcessSuite) TestSetTTYWithWindowSize() {
	err := s.process.SetTTY(garden.TTYSpec{
		WindowSize: &garden.WindowSize{
			Columns: 80,
			Rows:    24,
		},
	})
	s.NoError(err)
	s.Equal(1, s.containerdProcess.ResizeCallCount())

	_, width, height := s.containerdProcess.ResizeArgsForCall(0)
	s.Equal(uint32(80), width)
	s.Equal(uint32(24), height)
}

func (s *ProcessSuite) TestSetTTYResizeError() {
	s.containerdProcess.ResizeReturns(errors.New("resize-err"))

	err := s.process.SetTTY(garden.TTYSpec{
		WindowSize: &garden.WindowSize{Columns: 80, Rows: 24},
	})
	s.Error(err)
	s.Contains(err.Error(), "resize-err")
}

func (s *ProcessSuite) TestSignal() {
	err := s.process.Signal(garden.SignalTerminate)
	s.Error(err)
}

func (s *ProcessSuite) TestFinishedProcess() {
	fp := windowscontainerd.NewFinishedProcess("finished-id", 3)
	s.Equal("finished-id", fp.ID())

	code, err := fp.Wait()
	s.NoError(err)
	s.Equal(3, code)

	s.NoError(fp.SetTTY(garden.TTYSpec{}))
	s.Error(fp.Signal(garden.SignalTerminate))
}
