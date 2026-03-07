package windowscontainerd

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/garden"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/opencontainers/runtime-spec/specs-go"
)

type Container struct {
	container client.Container
}

var _ garden.Container = (*Container)(nil)

func (c *Container) Handle() string {
	return c.container.ID()
}

func (c *Container) Stop(kill bool) error {
	ctx := context.Background()

	task, err := c.container.Task(ctx, cio.Load)
	if err != nil {
		return fmt.Errorf("task lookup: %w", err)
	}

	// Just forcefully kill in windows for minimal implementation complexity right now
	_, _ = task.Delete(ctx, client.WithProcessKill)
	return nil
}

func (c *Container) Info() (garden.ContainerInfo, error) {
	return garden.ContainerInfo{}, nil
}

func (c *Container) StreamIn(spec garden.StreamInSpec) error {
	return nil
}

func (c *Container) StreamOut(spec garden.StreamOutSpec) (io.ReadCloser, error) {
	return nil, nil
}

func (c *Container) CurrentBandwidthLimits() (garden.BandwidthLimits, error) {
	return garden.BandwidthLimits{}, nil
}

func (c *Container) CurrentCPULimits() (garden.CPULimits, error) {
	return garden.CPULimits{}, nil
}

func (c *Container) CurrentDiskLimits() (garden.DiskLimits, error) {
	return garden.DiskLimits{}, nil
}

func (c *Container) CurrentMemoryLimits() (garden.MemoryLimits, error) {
	return garden.MemoryLimits{}, nil
}

func (c *Container) NetIn(hostPort, containerPort uint32) (uint32, uint32, error) {
	return 0, 0, nil
}

func (c *Container) NetOut(netOutRule garden.NetOutRule) error {
	return nil
}

func (c *Container) BulkNetOut(netOutRules []garden.NetOutRule) error {
	return nil
}

func (c *Container) Run(spec garden.ProcessSpec, gdnProcIO garden.ProcessIO) (garden.Process, error) {
	ctx := context.Background()

	task, err := c.container.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			task, err = c.container.NewTask(ctx, cio.NullIO)
			if err != nil {
				return nil, fmt.Errorf("task create: %w", err)
			}
			if err := task.Start(ctx); err != nil {
				return nil, fmt.Errorf("task start: %w", err)
			}
		} else {
			return nil, fmt.Errorf("task retrieval: %w", err)
		}
	}

	id := spec.ID
	if id == "" {
		newUuid, err := uuid.NewRandom()
		if err != nil {
			return nil, fmt.Errorf("uuid gen: %w", err)
		}
		id = newUuid.String()
	}

	opts := []cio.Opt{
		cio.WithStreams(gdnProcIO.Stdin, gdnProcIO.Stdout, gdnProcIO.Stderr),
	}
	if spec.TTY != nil {
		opts = append(opts, cio.WithTerminal)
	}
	ioCreator := cio.NewCreator(opts...)

	if spec.Dir == "" {
		spec.Dir = `C:\`
	}

	containerSpec, err := c.container.Spec(ctx)
	if err == nil && containerSpec != nil && containerSpec.Process != nil {
		envMap := make(map[string]string)
		for _, e := range containerSpec.Process.Env {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}
		for _, e := range spec.Env {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
		}
		var mergedEnv []string
		for k, v := range envMap {
			mergedEnv = append(mergedEnv, k+"="+v)
		}
		spec.Env = mergedEnv
	}

	procSpec := &specs.Process{
		Args: append([]string{spec.Path}, spec.Args...),
		Env:  spec.Env,
		Cwd:  spec.Dir,
	}

	if spec.User != "" {
		procSpec.User = specs.User{Username: spec.User}
	}

	proc, err := task.Exec(ctx, id, procSpec, ioCreator)
	if err != nil {
		return nil, fmt.Errorf("task exec: %w", err)
	}

	exitStatusC, err := proc.Wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("proc wait: %w", err)
	}

	err = proc.Start(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "no such file or directory") {
			return nil, garden.ExecutableNotFoundError{Message: err.Error()}
		}
		return nil, fmt.Errorf("proc start: %w", err)
	}

	if spec.TTY == nil {
		err = proc.CloseIO(ctx, client.WithStdinCloser)
		if err != nil {
			return nil, fmt.Errorf("proc closeio: %w", err)
		}
	}

	return &Process{
		proc:      proc,
		exitMsg:   exitStatusC,
		container: c,
	}, nil
}

func (c *Container) Attach(processID string, gdnProcIO garden.ProcessIO) (garden.Process, error) {
	ctx := context.Background()

	task, err := c.container.Task(ctx, cio.Load)
	if err != nil {
		return nil, fmt.Errorf("task lookup: %w", err)
	}

	opts := []cio.Opt{
		cio.WithStreams(gdnProcIO.Stdin, gdnProcIO.Stdout, gdnProcIO.Stderr),
	}
	ioAttach := cio.NewAttach(opts...)

	var proc client.Process
	for i := 0; i < 5; i++ {
		proc, err = task.LoadProcess(ctx, processID, ioAttach)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err != nil {
		if errdefs.IsNotFound(err) {
			if exitCodeStr, propErr := c.Property(fmt.Sprintf("garden.process-exit-status.%s", processID)); propErr == nil {
				if exitCode, parseErr := strconv.Atoi(exitCodeStr); parseErr == nil {
					return &FinishedProcess{id: processID, exitCode: exitCode}, nil
				}
			}
		}
		return nil, fmt.Errorf("load proc: %w", err)
	}

	exitStatusC, err := proc.Wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("proc wait: %w", err)
	}

	return &Process{
		proc:      proc,
		exitMsg:   exitStatusC,
		container: c,
	}, nil
}

type Process struct {
	proc      client.Process
	exitMsg   <-chan client.ExitStatus
	container *Container
}

var _ garden.Process = (*Process)(nil)

func (p *Process) ID() string {
	return p.proc.ID()
}

func (p *Process) Wait() (int, error) {
	ctx := context.Background()
	exitStatus := <-p.exitMsg
	if err := exitStatus.Error(); err != nil {
		return -1, err
	}

	io := p.proc.IO()
	if io != nil {
		io.Cancel()
		io.Wait()
		io.Close()
	}

	_, _ = p.proc.Delete(ctx)

	exitCode := int(exitStatus.ExitCode())
	_ = p.container.SetProperty(fmt.Sprintf("garden.process-exit-status.%s", p.ID()), strconv.Itoa(exitCode))

	return exitCode, nil
}

type FinishedProcess struct {
	id       string
	exitCode int
}

var _ garden.Process = (*FinishedProcess)(nil)

func (p *FinishedProcess) ID() string {
	return p.id
}

func (p *FinishedProcess) Wait() (int, error) {
	return p.exitCode, nil
}

func (p *FinishedProcess) SetTTY(spec garden.TTYSpec) error {
	return nil
}

func (p *FinishedProcess) Signal(signal garden.Signal) error {
	return nil
}

func (p *Process) SetTTY(spec garden.TTYSpec) error {
	ctx := context.Background()
	if spec.WindowSize != nil {
		return p.proc.Resize(ctx, uint32(spec.WindowSize.Columns), uint32(spec.WindowSize.Rows))
	}
	return nil
}

func (p *Process) Signal(signal garden.Signal) error {
	ctx := context.Background()
	// Just kill for simplicity
	return p.proc.Kill(ctx, 9)
}

func (c *Container) Metrics() (garden.Metrics, error) {
	return garden.Metrics{}, nil
}

func (c *Container) SetGraceTime(graceTime time.Duration) error {
	return c.SetProperty("garden.grace-time", fmt.Sprintf("%d", graceTime))
}

func (c *Container) Properties() (garden.Properties, error) {
	ctx := context.Background()
	labels, err := c.container.Labels(ctx)
	if err != nil {
		return nil, err
	}
	return decodeProperties(labels), nil
}

func (c *Container) Property(name string) (string, error) {
	ctx := context.Background()
	labels, err := c.container.Labels(ctx)
	if err != nil {
		return "", err
	}
	props := decodeProperties(labels)
	if v, ok := props[name]; ok {
		return v, nil
	}
	return "", fmt.Errorf("property not found")
}

func (c *Container) SetProperty(name string, value string) error {
	ctx := context.Background()
	labels, err := c.container.Labels(ctx)
	if err != nil {
		return err
	}

	// Decode, overwrite, re-encode
	props := decodeProperties(labels)
	props[name] = value

	newLabels, err := encodeProperties(props)
	if err != nil {
		return err
	}

	_, err = c.container.SetLabels(ctx, newLabels)
	return err
}

func (c *Container) RemoveProperty(name string) error {
	ctx := context.Background()
	labels, err := c.container.Labels(ctx)
	if err != nil {
		return err
	}

	props := decodeProperties(labels)
	if _, ok := props[name]; !ok {
		return nil
	}
	delete(props, name)

	newLabels, err := encodeProperties(props)
	if err != nil {
		return err
	}

	_, err = c.container.SetLabels(ctx, newLabels)
	return err
}
