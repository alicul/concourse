//go:build windows

package runtime

import (
	"context"
	"fmt"

	"github.com/Microsoft/hcsshim/cmd/containerd-shim-runhcs-v1/options"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/typeurl/v2"
)

// taskExecedProcesses retrieves a task's processes. On Windows the runhcs
// shim reports process details for every process, with ExecID left empty for
// the init process.
//
// The shim lists them by asking the container's compute system, which fails
// once that has shut down, as it has for a container that failed to start. A
// task that doesn't run has no process to signal, so that is no error.
func taskExecedProcesses(ctx context.Context, task containerd.Task) ([]containerd.Process, error) {
	pids, err := task.Pids(context.Background())
	if err != nil {
		if !taskRuns(ctx, task) {
			return nil, nil
		}

		return nil, fmt.Errorf("pid listing: %w", err)
	}

	procs := []containerd.Process{}
	for _, pid := range pids {
		if pid.Info == nil { // init
			continue
		}

		// the protobuf message has a "catch-all" field for `pid.Info`,
		// thus, we need to unmarshal the message ourselves.
		//
		info, err := typeurl.UnmarshalAny(pid.Info)
		if err != nil {
			return nil, fmt.Errorf("proc details unmarshal: %w", err)
		}

		pinfo, ok := info.(*options.ProcessDetails)
		if !ok {
			return nil, fmt.Errorf("unknown proc detail type")
		}

		if pinfo.ExecID == "" || pinfo.ExecID == task.ID() { // init
			continue
		}

		proc, err := task.LoadProcess(ctx, pinfo.ExecID, cio.Load)
		if err != nil {
			return nil, fmt.Errorf("load process: %w", err)
		}

		procs = append(procs, proc)
	}

	return procs, nil
}

// taskRuns tells whether a task can have processes. It cannot before its
// init process is started, which the shim requires before it executes any
// other, nor after the init process has exited, which ends all others. A task
// whose status cannot be read counts as running.
func taskRuns(ctx context.Context, task containerd.Task) bool {
	status, err := task.Status(ctx)
	if err != nil {
		return true
	}

	return status.Status != containerd.Created && status.Status != containerd.Stopped
}
