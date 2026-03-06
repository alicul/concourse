package windowscontainerd

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/garden"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/opencontainers/runtime-spec/specs-go"
)

const (
	DefaultWindowsPath = `PATH=C:\Windows\system32;C:\Windows;C:\Windows\System32\Wbem;C:\Windows\System32\WindowsPowerShell\v1.0\`

	GraceTimeKey         = "garden.grace-time"
	ProcessExitStatusKey = "garden.process-exit-status"
)

var (
	pathRegexp         = regexp.MustCompile("^PATH=.*$")
	noSuchFile         = regexp.MustCompile(`(not found|cannot find the file|does not exist)`)
	executableNotFound = regexp.MustCompile(`(executable file not found|is not recognized)`)
)

type Container struct {
	container containerd.Container
}

func NewContainer(container containerd.Container) *Container {
	return &Container{container: container}
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

	signal := WindowsGracefulSignal
	if kill {
		signal = WindowsTerminateSignal
	}

	if err := task.Kill(ctx, signal); err != nil {
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("kill: %w", err)
		}
	}

	return nil
}

func (c *Container) Run(
	spec garden.ProcessSpec,
	processIO garden.ProcessIO,
) (garden.Process, error) {
	ctx := context.Background()

	containerSpec, err := c.container.Spec(ctx)
	if err != nil {
		return nil, fmt.Errorf("container spec: %w", err)
	}

	procSpec := c.setupProcSpec(spec, *containerSpec)

	task, err := c.container.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			initTask, err := c.container.NewTask(ctx, cio.NullIO)
			if err != nil {
				return nil, fmt.Errorf("recreating init task: %w", err)
			}
			if err := initTask.Start(ctx); err != nil {
				return nil, fmt.Errorf("restarting init task: %w", err)
			}
			task = initTask
		} else {
			return nil, fmt.Errorf("task retrieval: %w", err)
		}
	}

	id := procID(spec)
	cioOpts := containerdCIO(processIO, spec.TTY != nil)

	proc, err := task.Exec(ctx, id, &procSpec, cio.NewCreator(cioOpts...))
	if err != nil {
		return nil, fmt.Errorf("task exec: %w", err)
	}

	exitStatusC, err := proc.Wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("proc wait: %w", err)
	}

	if err := proc.Start(ctx); err != nil {
		if isNoSuchExecutable(err) {
			return nil, garden.ExecutableNotFoundError{Message: err.Error()}
		}
		return nil, fmt.Errorf("proc start: %w", err)
	}

	if spec.TTY == nil {
		if err := proc.CloseIO(ctx, containerd.WithStdinCloser); err != nil {
			return nil, fmt.Errorf("proc closeio: %w", err)
		}
	}

	return NewProcess(proc, exitStatusC, c), nil
}

func (c *Container) Attach(pid string, processIO garden.ProcessIO) (garden.Process, error) {
	ctx := context.Background()

	if pid == "" {
		return nil, fmt.Errorf("empty process id")
	}

	task, err := c.container.Task(ctx, cio.Load)
	if err != nil {
		return nil, fmt.Errorf("task attach: %w", err)
	}

	cioOpts := containerdCIO(processIO, false)

	const maxRetries = 5
	var proc containerd.Process
	var lastErr error

	for attempt := range maxRetries {
		proc, lastErr = task.LoadProcess(ctx, pid, cio.NewAttach(cioOpts...))
		if lastErr == nil {
			break
		}

		if attempt < maxRetries-1 && strings.Contains(lastErr.Error(), "no running process found") {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		break
	}

	if lastErr != nil {
		if errdefs.IsNotFound(lastErr) {
			if code, ok := c.lookupStoredExit(); ok {
				return NewFinishedProcess(pid, code), nil
			}
		}
		return nil, fmt.Errorf("load proc: %w", lastErr)
	}

	status, err := proc.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("proc status: %w", err)
	}

	if status.Status != containerd.Running {
		return nil, fmt.Errorf("proc not running: status = %s", status.Status)
	}

	exitStatusC, err := proc.Wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("proc wait: %w", err)
	}

	return NewProcess(proc, exitStatusC, c), nil
}

func (c *Container) Properties() (garden.Properties, error) {
	labels, err := c.container.Labels(context.Background())
	if err != nil {
		return garden.Properties{}, fmt.Errorf("labels retrieval: %w", err)
	}

	return labelsToProperties(labels), nil
}

func (c *Container) Property(name string) (string, error) {
	properties, err := c.Properties()
	if err != nil {
		return "", err
	}

	v, found := properties[name]
	if !found {
		return "", fmt.Errorf("not found: %s", name)
	}

	return v, nil
}

func (c *Container) SetProperty(name string, value string) error {
	labelSet, err := propertiesToLabels(garden.Properties{name: value})
	if err != nil {
		return err
	}
	_, err = c.container.SetLabels(context.Background(), labelSet)
	if err != nil {
		return fmt.Errorf("set label: %w", err)
	}

	return nil
}

func (c *Container) RemoveProperty(name string) error {
	return fmt.Errorf("not implemented")
}

func (c *Container) Info() (garden.ContainerInfo, error) {
	return garden.ContainerInfo{}, fmt.Errorf("not implemented")
}

func (c *Container) Metrics() (garden.Metrics, error) {
	return garden.Metrics{}, fmt.Errorf("not implemented")
}

func (c *Container) StreamIn(spec garden.StreamInSpec) error {
	return fmt.Errorf("not implemented")
}

func (c *Container) StreamOut(spec garden.StreamOutSpec) (io.ReadCloser, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *Container) SetGraceTime(graceTime time.Duration) error {
	return c.SetProperty(GraceTimeKey, fmt.Sprintf("%d", graceTime))
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
	spec, err := c.container.Spec(context.Background())
	if err != nil {
		return garden.MemoryLimits{}, err
	}

	if spec == nil || spec.Windows == nil || spec.Windows.Resources == nil ||
		spec.Windows.Resources.Memory == nil || spec.Windows.Resources.Memory.Limit == nil {
		return garden.MemoryLimits{}, nil
	}

	return garden.MemoryLimits{
		LimitInBytes: *spec.Windows.Resources.Memory.Limit,
	}, nil
}

func (c *Container) NetIn(hostPort, containerPort uint32) (uint32, uint32, error) {
	return 0, 0, fmt.Errorf("not implemented")
}

func (c *Container) NetOut(netOutRule garden.NetOutRule) error {
	return fmt.Errorf("not implemented")
}

func (c *Container) BulkNetOut(netOutRules []garden.NetOutRule) error {
	return fmt.Errorf("not implemented")
}

func (c *Container) setupProcSpec(gdnProcSpec garden.ProcessSpec, containerSpec specs.Spec) specs.Process {
	procSpec := containerSpec.Process

	procSpec.Args = append([]string{gdnProcSpec.Path}, gdnProcSpec.Args...)
	procSpec.Env = append(procSpec.Env, gdnProcSpec.Env...)

	cwd := gdnProcSpec.Dir
	if cwd == "" {
		cwd = `C:\`
	}
	procSpec.Cwd = cwd

	if gdnProcSpec.TTY != nil {
		procSpec.Terminal = true

		if gdnProcSpec.TTY.WindowSize != nil {
			procSpec.ConsoleSize = &specs.Box{
				Width:  uint(gdnProcSpec.TTY.WindowSize.Columns),
				Height: uint(gdnProcSpec.TTY.WindowSize.Rows),
			}
		}
	}

	if gdnProcSpec.User != "" {
		procSpec.User = specs.User{
			Username: gdnProcSpec.User,
		}
	}

	if pathEnv := envWithDefaultPath(procSpec.Env); pathEnv != "" {
		procSpec.Env = append(procSpec.Env, pathEnv)
	}

	return *procSpec
}

func envWithDefaultPath(currentEnv []string) string {
	pathFound := slices.ContainsFunc(currentEnv, pathRegexp.MatchString)
	if pathFound {
		return ""
	}
	return DefaultWindowsPath
}

func containerdCIO(gdnProcIO garden.ProcessIO, tty bool) []cio.Opt {
	if !tty {
		return []cio.Opt{
			cio.WithStreams(
				gdnProcIO.Stdin,
				gdnProcIO.Stdout,
				gdnProcIO.Stderr,
			),
		}
	}

	return []cio.Opt{
		cio.WithStreams(
			gdnProcIO.Stdin,
			gdnProcIO.Stdout,
			gdnProcIO.Stderr,
		),
		cio.WithTerminal,
	}
}

func procID(gdnProcSpec garden.ProcessSpec) string {
	id := gdnProcSpec.ID
	if id == "" {
		u, err := uuid.NewRandom()
		if err != nil {
			panic(fmt.Errorf("uuid gen: %w", err))
		}
		id = u.String()
	}

	return id
}

func isNoSuchExecutable(err error) bool {
	return noSuchFile.MatchString(err.Error()) || executableNotFound.MatchString(err.Error())
}

func (c *Container) lookupStoredExit() (int, bool) {
	val, err := c.Property(ProcessExitStatusKey)
	if err != nil || val == "" {
		return 0, false
	}
	code, convErr := strconv.Atoi(val)
	if convErr != nil {
		return 0, false
	}
	return code, true
}
