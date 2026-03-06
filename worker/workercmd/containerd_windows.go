//go:build windows

package workercmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/worker/runtime/libcontainerd"
	"github.com/concourse/concourse/worker/runtime/windowscontainerd"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
)

const containerdNamespace = "concourse"
const defaultContainerdPipe = `\\.\pipe\containerd-containerd`

func WriteDefaultContainerdConfig(dest string) error {
	const config = `
version = 3

disabled_plugins = ["io.containerd.grpc.v1.cri"]
`
	err := os.WriteFile(dest, []byte(config), 0755)
	if err != nil {
		return fmt.Errorf("write file %s: %w", dest, err)
	}

	return nil
}

func (cmd *WorkerCommand) containerdGardenServerRunner(
	logger lager.Logger,
	containerdAddr string,
) (ifrit.Runner, error) {
	const graceTime = 0

	gardenBackend, err := windowscontainerd.NewGardenBackend(
		libcontainerd.New(containerdAddr, containerdNamespace, cmd.Containerd.RequestTimeout),
		windowscontainerd.WithRequestTimeout(cmd.Containerd.RequestTimeout),
		windowscontainerd.WithMaxContainers(cmd.Containerd.MaxContainers),
		windowscontainerd.WithDNSServers(cmd.Containerd.Network.DNSServers),
	)
	if err != nil {
		return nil, fmt.Errorf("windows containerd backend init: %w", err)
	}

	return newGardenServerRunner(
		"tcp",
		cmd.bindAddr(),
		graceTime,
		&gardenBackend,
		logger,
	), nil
}

func (cmd *WorkerCommand) containerdRunner(logger lager.Logger) (ifrit.Runner, error) {
	var (
		pipe   = defaultContainerdPipe
		config = filepath.Join(cmd.WorkDir.Path(), "containerd.toml")
		root   = filepath.Join(cmd.WorkDir.Path(), "containerd")
		bin    = "containerd"
	)

	err := os.MkdirAll(root, 0755)
	if err != nil {
		return nil, err
	}

	if cmd.Containerd.Config.Path() != "" {
		config = cmd.Containerd.Config.Path()
	} else {
		err := WriteDefaultContainerdConfig(config)
		if err != nil {
			return nil, fmt.Errorf("write default containerd config: %w", err)
		}
	}

	if cmd.Containerd.Bin != "" {
		bin = cmd.Containerd.Bin
	}

	command := exec.Command(bin,
		"--address="+pipe,
		"--root="+root,
		"--config="+config,
		"--log-level="+cmd.Containerd.LogLevel,
	)

	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	gardenServerRunner, err := cmd.containerdGardenServerRunner(logger, pipe)
	if err != nil {
		return nil, fmt.Errorf("containerd garden server runner: %w", err)
	}

	members := grouper.Members{
		{
			Name: "containerd",
			Runner: CmdRunner{
				Cmd: command,
				Ready: func() bool {
					client := libcontainerd.New(pipe, containerdNamespace, cmd.Containerd.RequestTimeout)
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
	}

	return grouper.NewOrdered(os.Interrupt, members), nil
}
