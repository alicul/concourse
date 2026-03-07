package windowscontainerd

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/garden"
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/worker/runtime/libcontainerd"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

type Backend struct {
	logger lager.Logger
	client libcontainerd.Client

	hypervIsolation bool
}

var _ garden.Backend = (*Backend)(nil)

func NewBackend(
	logger lager.Logger,
	client libcontainerd.Client,
	hypervIsolation bool,
) *Backend {
	return &Backend{
		logger:          logger,
		client:          client,
		hypervIsolation: hypervIsolation,
	}
}

func (b *Backend) Start() error {
	return b.client.Init()
}

func (b *Backend) Stop() error {
	return b.client.Stop()
}

func (b *Backend) Ping() error {
	return b.client.Version(context.Background())
}

func (b *Backend) Capacity() (garden.Capacity, error) {
	return garden.Capacity{
		MemoryInBytes: 0,
		DiskInBytes:   0,
		MaxContainers: 0,
	}, nil
}

func resolveRootFSPath(gdnSpec garden.ContainerSpec) (string, error) {
	rootfsURI := gdnSpec.RootFSPath
	if rootfsURI == "" {
		if gdnSpec.Image.URI == "" {
			return "", nil // neither rootfs nor image URI provided
		}
		rootfsURI = gdnSpec.Image.URI
	}

	if !strings.HasPrefix(rootfsURI, "raw://") {
		return "", fmt.Errorf("unsupported rootfs scheme: %s", rootfsURI)
	}

	path := strings.TrimPrefix(rootfsURI, "raw://")
	decodedPath, err := url.QueryUnescape(path)
	if err != nil {
		return "", fmt.Errorf("failed to decode rootfs path: %w", err)
	}

	if !filepath.IsAbs(decodedPath) {
		return "", fmt.Errorf("rootfs directory must be an absolute path: %s", decodedPath)
	}

	return filepath.FromSlash(decodedPath), nil
}

func toWindowsPath(p string) string {
	if strings.HasPrefix(p, "/") {
		return `C:` + filepath.FromSlash(p)
	}
	return filepath.FromSlash(p)
}

func (b *Backend) Create(gdnSpec garden.ContainerSpec) (garden.Container, error) {
	ctx := context.Background()

	id := gdnSpec.Handle

	labels := map[string]string{}
	for k, v := range gdnSpec.Properties {
		labels[k] = v
	}

	rootFSPath, err := resolveRootFSPath(gdnSpec)
	if err != nil {
		return nil, fmt.Errorf("resolve rootfs: %w", err)
	}

	var containerdImage client.Image

	if rootFSPath != "" {
		volumeDir := filepath.Dir(rootFSPath)
		imageTar := filepath.Join(volumeDir, "image.tar")

		if _, err := os.Stat(imageTar); err == nil {
			// Path 1: From OCI tarball
			file, err := os.Open(imageTar)
			if err != nil {
				return nil, fmt.Errorf("open image tar: %w", err)
			}
			defer file.Close()

			imgs, err := b.client.Import(ctx, file)
			if err != nil {
				return nil, fmt.Errorf("import image: %w", err)
			}

			if len(imgs) == 0 {
				return nil, fmt.Errorf("no images found in tar")
			}

			// We need to fetch the imported image through GetImage
			// to get a client.Image for Unpack and NewContainerFromImage
			containerdImage, err = b.client.GetImage(ctx, imgs[0].Name)
			if err != nil {
				return nil, fmt.Errorf("get image: %w", err)
			}

			err = b.client.Unpack(ctx, containerdImage, "windows")
			if err != nil {
				return nil, fmt.Errorf("unpack image: %w", err)
			}
		} else {
			// Path 2: from pre-extracted rootfs (Wait, does windows containerd support this out of the box?)
			// We might just pass the RootFSPath as WithRootFS.
			// Actually Windows requires layer folders.
			// Let's defer full implementation of Path 2, just set the rootfs mounts if possible.
		}
	}

	// Create OCI Spec
	spec, err := buildWindowsSpec(ctx, gdnSpec, b.hypervIsolation, rootFSPath)
	if err != nil {
		return nil, fmt.Errorf("build spec: %w", err)
	}

	var cont client.Container
	var contErr error
	if containerdImage != nil {
		opts := []oci.SpecOpts{
			func(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
				s.Process.Args = []string{"cmd.exe", "/S", "/C", "ping -t localhost > NUL"}
				s.Process.Cwd = spec.Process.Cwd
				s.Process.User.Username = spec.Process.User.Username
				s.Mounts = append(s.Mounts, spec.Mounts...)
				if s.Windows == nil {
					s.Windows = &specs.Windows{}
				}
				s.Windows.LayerFolders = append(s.Windows.LayerFolders, spec.Windows.LayerFolders...)
				s.Windows.Resources = spec.Windows.Resources
				s.Windows.HyperV = spec.Windows.HyperV
				s.Root = spec.Root
				return nil
			},
		}

		cont, contErr = b.client.NewContainerFromImage(ctx, id, labels, containerdImage, "windows", opts...)
	} else {
		cont, contErr = b.client.NewContainer(ctx, id, labels, spec)
	}

	if contErr != nil {
		return nil, fmt.Errorf("new container: %w", contErr)
	}

	err = b.startTask(ctx, cont)
	if err != nil {
		return nil, fmt.Errorf("starting task: %w", err)
	}

	return &Container{
		container: cont,
	}, nil
}

func buildWindowsSpec(ctx context.Context, gdnSpec garden.ContainerSpec, hypervIsolation bool, rootFsPath string) (*specs.Spec, error) {
	spec := &specs.Spec{
		Version: "1.0.2",
		Process: &specs.Process{
			Args: []string{"cmd.exe", "/S", "/C", "ping -t localhost > NUL"},
			User: specs.User{Username: "ContainerAdministrator"},
			Cwd:  "C:\\",
		},
		Root: &specs.Root{
			Path: rootFsPath,
		},
		Windows: &specs.Windows{
			LayerFolders: []string{rootFsPath},
		},
	}

	for _, bindMount := range gdnSpec.BindMounts {
		mode := "ro"
		if bindMount.Mode == garden.BindMountModeRW {
			mode = "rw"
		}

		src := toWindowsPath(bindMount.SrcPath)
		dst := toWindowsPath(bindMount.DstPath)

		spec.Mounts = append(spec.Mounts, specs.Mount{
			Source:      src,
			Destination: dst,
			Type:        "bind",
			Options:     []string{"bind", mode},
		})
	}

	if gdnSpec.Limits.Memory.LimitInBytes > 0 || gdnSpec.Limits.CPU.LimitInShares > 0 {
		var memoryLimit = gdnSpec.Limits.Memory.LimitInBytes
		var cpuLimit = uint16(gdnSpec.Limits.CPU.LimitInShares)

		spec.Windows.Resources = &specs.WindowsResources{
			Memory: &specs.WindowsMemoryResources{
				Limit: &memoryLimit,
			},
			CPU: &specs.WindowsCPUResources{
				Shares: &cpuLimit,
			},
		}
	}

	if hypervIsolation {
		spec.Windows.HyperV = &specs.WindowsHyperV{}
	}

	return spec, nil
}

func (b *Backend) startTask(ctx context.Context, cont client.Container) error {
	task, err := cont.NewTask(ctx, cio.NullIO)
	if err != nil {
		return fmt.Errorf("new task: %w", err)
	}

	return task.Start(ctx)
}

func (b *Backend) Destroy(handle string) error {
	ctx := context.Background()

	cont, err := b.client.GetContainer(ctx, handle)
	if err != nil {
		return fmt.Errorf("get container: %w", err)
	}

	task, err := cont.Task(ctx, cio.Load)
	if err == nil {
		// 0xf is CTRL_SHUTDOWN_EVENT on Windows
		_ = task.Kill(ctx, 0xf)

		statusC, err := task.Wait(ctx)
		if err == nil {
			select {
			case <-statusC:
			case <-time.After(10 * time.Second):
			}
		}

		_, _ = task.Delete(ctx, client.WithProcessKill)
	}

	return b.client.Destroy(ctx, handle)
}

func (b *Backend) Containers(properties garden.Properties) ([]garden.Container, error) {
	filters := []string{}
	encoded, err := encodeProperties(properties)
	if err != nil {
		return nil, err
	}
	for k, v := range encoded {
		filters = append(filters, fmt.Sprintf("labels.%q==%q", k, v))
	}

	conts, err := b.client.Containers(context.Background(), filters...)
	if err != nil {
		return nil, err
	}

	var result []garden.Container
	for _, c := range conts {
		result = append(result, &Container{container: c})
	}
	return result, nil
}

func (b *Backend) Lookup(handle string) (garden.Container, error) {
	cont, err := b.client.GetContainer(context.Background(), handle)
	if err != nil {
		return nil, err
	}
	return &Container{
		container: cont,
	}, nil
}

func (b *Backend) GraceTime(container garden.Container) time.Duration {
	val, err := container.Property("garden.grace-time")
	if err != nil {
		return 0
	}
	d, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0
	}
	return time.Duration(d)
}

func (b *Backend) BulkInfo(handles []string) (map[string]garden.ContainerInfoEntry, error) {
	return nil, nil // TODO
}

func (b *Backend) BulkMetrics(handles []string) (map[string]garden.ContainerMetricsEntry, error) {
	return nil, nil // TODO
}
