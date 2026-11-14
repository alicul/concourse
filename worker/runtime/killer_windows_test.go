//go:build windows

package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/v8/worker/runtime"
	"github.com/concourse/concourse/v8/worker/runtime/libcontainerd/libcontainerdfakes"
	"github.com/concourse/concourse/v8/worker/runtime/runtimefakes"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/stretchr/testify/require"
)

// The shim cannot list the processes of a task that has stopped, which is
// what is left of a container that failed to start. Destroying that container
// has to get past the killer all the same.
func TestKillTaskThatDoesNotRun(t *testing.T) {
	for name, status := range map[string]containerd.ProcessStatus{
		"because it has stopped":     containerd.Stopped,
		"because it was not started": containerd.Created,
	} {
		t.Run(name, func(t *testing.T) {
			task := new(libcontainerdfakes.FakeTask)
			task.PidsReturns(nil, errors.New("hcsshim: the handle has already been closed"))
			task.StatusReturns(containerd.Status{Status: status}, nil)

			processKiller := new(runtimefakes.FakeProcessKiller)
			killer := runtime.NewKiller(runtime.WithProcessKiller(processKiller))

			require.NoError(t, killer.Kill(context.Background(), task, runtime.KillGracefully))
			require.NoError(t, killer.Kill(context.Background(), task, runtime.KillUngracefully))
			require.Zero(t, processKiller.KillCallCount())
		})
	}
}

func TestKillTaskWhoseProcessesCannotBeListed(t *testing.T) {
	pidsErr := errors.New("pids-err")

	for name, status := range map[string]func(*libcontainerdfakes.FakeTask){
		"while it runs": func(task *libcontainerdfakes.FakeTask) {
			task.StatusReturns(containerd.Status{Status: containerd.Running}, nil)
		},
		"and its status is unknown": func(task *libcontainerdfakes.FakeTask) {
			task.StatusReturns(containerd.Status{}, errors.New("status-err"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			task := new(libcontainerdfakes.FakeTask)
			task.PidsReturns(nil, pidsErr)
			status(task)

			killer := runtime.NewKiller(runtime.WithProcessKiller(new(runtimefakes.FakeProcessKiller)))

			require.ErrorIs(t, killer.Kill(context.Background(), task, runtime.KillGracefully), pidsErr)
			require.ErrorIs(t, killer.Kill(context.Background(), task, runtime.KillUngracefully), pidsErr)
		})
	}
}
