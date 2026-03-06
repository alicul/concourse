package windowscontainerd

import (
	"context"
	"errors"
	"fmt"

	"code.cloudfoundry.org/garden"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/errdefs"
)

type Process struct {
	process     containerd.Process
	exitStatusC <-chan containerd.ExitStatus
	container   *Container
}

func NewProcess(
	p containerd.Process,
	ch <-chan containerd.ExitStatus,
	container *Container,
) *Process {
	return &Process{
		process:     p,
		exitStatusC: ch,
		container:   container,
	}
}

var _ garden.Process = (*Process)(nil)

func (p *Process) ID() string {
	return p.process.ID()
}

func (p *Process) Wait() (int, error) {
	status := <-p.exitStatusC
	err := status.Error()
	if err != nil {
		return 0, fmt.Errorf("waiting for exit status: %w", err)
	}

	err = p.process.CloseIO(context.Background(), containerd.WithStdinCloser)
	if err != nil {
		return 0, fmt.Errorf("proc closeio: %w", err)
	}

	p.process.IO().Cancel()
	p.process.IO().Wait()
	p.process.IO().Close()

	_, err = p.process.Delete(context.Background())
	if err != nil && !errors.Is(err, errdefs.ErrNotFound) {
		return 0, fmt.Errorf("delete process: %w", err)
	}

	p.container.SetProperty(ProcessExitStatusKey, fmt.Sprintf("%d", status.ExitCode()))
	return int(status.ExitCode()), nil
}

func (p *Process) SetTTY(spec garden.TTYSpec) error {
	if spec.WindowSize == nil {
		return nil
	}

	err := p.process.Resize(context.Background(),
		uint32(spec.WindowSize.Columns),
		uint32(spec.WindowSize.Rows),
	)
	if err != nil {
		return fmt.Errorf("resize: %w", err)
	}

	return nil
}

func (p *Process) Signal(signal garden.Signal) error {
	return fmt.Errorf("not implemented")
}

// FinishedProcess represents a process that has already exited.
type FinishedProcess struct {
	id         string
	exitStatus int
}

func NewFinishedProcess(id string, exitStatus int) garden.Process {
	return &FinishedProcess{id: id, exitStatus: exitStatus}
}

func (p *FinishedProcess) ID() string                        { return p.id }
func (p *FinishedProcess) Wait() (int, error)                { return p.exitStatus, nil }
func (p *FinishedProcess) SetTTY(garden.TTYSpec) error       { return nil }
func (p *FinishedProcess) Signal(signal garden.Signal) error { return fmt.Errorf("not implemented") }
