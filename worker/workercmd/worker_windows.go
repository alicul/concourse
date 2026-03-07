//go:build windows

package workercmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/worker/runtime/libcontainerd"
	"github.com/concourse/concourse/worker/runtime/windowscontainerd"
	"github.com/concourse/flag/v2"
	"github.com/jessevdk/go-flags"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
)

const containerdRuntime = "containerd"
const guardianRuntime = "guardian"
const houdiniRuntime = "houdini"

type RuntimeConfiguration struct {
	Runtime string `long:"runtime" default:"houdini" description:"Runtime to use for managing containers." choice:"houdini" choice:"containerd"`
}

type GuardianRuntime struct {
	RequestTimeout time.Duration `long:"request-timeout" default:"5m" description:"How long to wait for requests to the Garden server to complete. 0 means no timeout."`
}

type ContainerdRuntime struct {
	Bin             string        `long:"containerd-bin" default:"containerd" description:"Path to a containerd executable (non-absolute names get resolved from $PATH)."`
	Config          flag.File     `long:"containerd-config" description:"Path to a config file to use for the Containerd daemon."`
	Address         string        `long:"address" default:"\\\\.\\pipe\\concourse-containerd" description:"containerd daemon address."`
	Namespace       string        `long:"namespace" default:"concourse" description:"containerd daemon namespace."`
	RequestTimeout  time.Duration `long:"request-timeout" default:"5m" description:"How long to wait for requests to Containerd to complete. 0 means no timeout."`
	HypervIsolation bool          `long:"hyperv-isolation" description:"Enable Hyper-V isolation for Windows containers."`
}

type Certs struct{}

func (cmd WorkerCommand) LessenRequirements(prefix string, command *flags.Command) {
	// created in the work-dir
	command.FindOptionByLongName(prefix + "baggageclaim-volumes").Required = false
}

func (cmd *WorkerCommand) buildContainerdRunner(logger lager.Logger) (ifrit.Runner, error) {
	client := libcontainerd.New(
		cmd.Containerd.Address,
		cmd.Containerd.Namespace,
		cmd.Containerd.RequestTimeout, // Use Containerd's timeout
	)

	backend := windowscontainerd.NewBackend(
		logger.Session("windowscontainerd"),
		client,
		cmd.Containerd.HypervIsolation,
	)

	gardenServerRunner := newGardenServerRunner(
		"tcp",
		cmd.bindAddr(),
		0,
		backend,
		logger,
	)

	return grouper.NewOrdered(os.Interrupt, grouper.Members{
		{
			Name: "containerd",
			Runner: CmdRunner{
				Cmd: cmd.containerdCmd(),
				Ready: func() bool {
					err := client.Init()
					if err != nil {
						logger.Info("failed-to-connect-to-containerd", lager.Data{"error": err.Error()})
					}
					return err == nil
				},
				Timeout: 60 * time.Second,
			},
		},
		{
			Name:   "containerd-garden-backend",
			Runner: gardenServerRunner,
		},
	}), nil
}

func (cmd *WorkerCommand) containerdCmd() *exec.Cmd {
	var (
		config = filepath.Join(cmd.WorkDir.Path(), "containerd.toml")
		root   = filepath.Join(cmd.WorkDir.Path(), "containerd")
		state  = filepath.Join(cmd.WorkDir.Path(), "containerd-state")
		bin    = "containerd"
	)

	if cmd.Containerd.Config.Path() != "" {
		config = cmd.Containerd.Config.Path()
	} else {
		if _, err := os.Stat(config); os.IsNotExist(err) {
			defaultConfig := `version = 3
disabled_plugins = ["io.containerd.grpc.v1.cri"]
`
			_ = os.WriteFile(config, []byte(defaultConfig), 0644)
		}
	}

	if cmd.Containerd.Bin != "" {
		bin = cmd.Containerd.Bin
	}

	command := exec.Command(bin,
		"--address="+cmd.Containerd.Address,
		"--root="+root,
		"--state="+state,
		"--config="+config,
		"--log-level=debug",
	)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command
}

func (cmd *WorkerCommand) gardenServerRunner(logger lager.Logger) (atc.Worker, ifrit.Runner, error) {
	worker := cmd.Worker.Worker()
	worker.Platform = runtime.GOOS
	var err error
	worker.Name, err = cmd.workerName()
	if err != nil {
		return atc.Worker{}, nil, err
	}

	var runner ifrit.Runner

	switch cmd.Runtime {
	case containerdRuntime:
		runner, err = cmd.buildContainerdRunner(logger)
		if err != nil {
			return atc.Worker{}, nil, fmt.Errorf("build containerd runner: %w", err)
		}

	case houdiniRuntime:
		runner, err = cmd.houdiniRunner(logger)
		if err != nil {
			return atc.Worker{}, nil, err
		}

	case guardianRuntime:
		return atc.Worker{}, nil, fmt.Errorf("guardian is not supported on Windows")

	default:
		return atc.Worker{}, nil, fmt.Errorf("unsupported runtime: %s", cmd.Runtime)
	}

	return worker, runner, nil
}

func (cmd *WorkerCommand) baggageclaimRunner(logger lager.Logger) (ifrit.Runner, error) {
	volumesDir := filepath.Join(cmd.WorkDir.Path(), "volumes")

	err := os.MkdirAll(volumesDir, 0755)
	if err != nil {
		return nil, err
	}

	cmd.Baggageclaim.VolumesDir = flag.Dir(volumesDir)

	cmd.Baggageclaim.OverlaysDir = filepath.Join(cmd.WorkDir.Path(), "overlays")

	return cmd.Baggageclaim.Runner(logger, nil)
}
