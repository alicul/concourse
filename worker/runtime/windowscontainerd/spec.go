package windowscontainerd

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"code.cloudfoundry.org/garden"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// OciSpec creates a Windows container OCI spec from a Garden container spec.
//
// Windows OCI specs use the specs.Windows section instead of specs.Linux.
// There is no seccomp, cgroups, user namespaces, or Linux capabilities.
// Resource limits use the Windows-native Job Object model via the Windows
// section of the OCI spec.
func OciSpec(gdn garden.ContainerSpec, scratchDir string) (*specs.Spec, error) {
	if gdn.Handle == "" {
		return nil, fmt.Errorf("handle must be specified")
	}

	if gdn.RootFSPath == "" {
		gdn.RootFSPath = gdn.Image.URI
	}

	rootfs, err := rootfsDir(gdn.RootFSPath)
	if err != nil {
		return nil, err
	}

	mounts, err := ociSpecBindMounts(gdn.BindMounts)
	if err != nil {
		return nil, err
	}

	windowsResources := ociWindowsResources(gdn.Limits)

	oci := &specs.Spec{
		Version:  specs.Version,
		Hostname: gdn.Handle,
		Process: &specs.Process{
			Args: []string{"cmd.exe", "/S", "/C", "ping -t localhost > NUL"},
			Cwd:  `C:\`,
			Env:  gdn.Env,
			User: specs.User{
				Username: "ContainerAdministrator",
			},
		},
		Root: &specs.Root{
			Path: rootfs,
		},
		Mounts:      mounts,
		Annotations: map[string]string(gdn.Properties),
		Windows: &specs.Windows{
			LayerFolders: []string{rootfs, scratchDir},
			Resources:    windowsResources,
			Network: &specs.WindowsNetwork{
				AllowUnqualifiedDNSQuery: true,
			},
		},
	}

	return oci, nil
}

func ociSpecBindMounts(bindMounts []garden.BindMount) ([]specs.Mount, error) {
	var mounts []specs.Mount

	for _, bm := range bindMounts {
		if bm.SrcPath == "" || bm.DstPath == "" {
			return nil, fmt.Errorf("src and dst must not be empty")
		}

		srcPath := toWindowsPath(bm.SrcPath)
		dstPath := toWindowsPath(bm.DstPath)

		if !filepath.IsAbs(srcPath) || !filepath.IsAbs(dstPath) {
			return nil, fmt.Errorf("src and dst must be absolute")
		}

		if bm.Origin != garden.BindMountOriginHost {
			return nil, fmt.Errorf("unknown bind mount origin %d", bm.Origin)
		}

		options := []string{"bind"}
		switch bm.Mode {
		case garden.BindMountModeRO:
			options = append(options, "ro")
		case garden.BindMountModeRW:
			options = append(options, "rw")
		default:
			return nil, fmt.Errorf("unknown bind mount mode %d", bm.Mode)
		}

		mounts = append(mounts, specs.Mount{
			Source:      srcPath,
			Destination: dstPath,
			Options:     options,
		})
	}

	return mounts, nil
}

// toWindowsPath converts Unix-style absolute paths to Windows paths.
// The ATC may send paths like /scratch or /tmp/build/abc without knowing
// the worker is Windows. These are converted to C:\scratch, C:\tmp\build\abc, etc.
func toWindowsPath(p string) string {
	if strings.HasPrefix(p, "/") {
		return `C:` + filepath.FromSlash(p)
	}
	return filepath.FromSlash(p)
}

func ociWindowsResources(limits garden.Limits) *specs.WindowsResources {
	var cpuResources *specs.WindowsCPUResources
	var memoryResources *specs.WindowsMemoryResources

	shares := limits.CPU.LimitInShares
	if limits.CPU.Weight > 0 {
		shares = limits.CPU.Weight
	}

	if shares > 0 {
		cpuShares := uint16(shares)
		cpuResources = &specs.WindowsCPUResources{
			Shares: &cpuShares,
		}
	}

	memoryLimit := limits.Memory.LimitInBytes
	if memoryLimit > 0 {
		memoryResources = &specs.WindowsMemoryResources{
			Limit: &memoryLimit,
		}
	}

	if cpuResources == nil && memoryResources == nil {
		return nil
	}

	return &specs.WindowsResources{
		CPU:    cpuResources,
		Memory: memoryResources,
	}
}

func rootfsDir(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("rootfs must not be empty")
	}

	parts := strings.SplitN(raw, "://", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformatted rootfs: must be of form 'scheme://<abs_dir>'")
	}

	scheme, directory := parts[0], parts[1]
	if scheme != "raw" {
		return "", fmt.Errorf("unsupported scheme '%s'", scheme)
	}

	// The RootFSPath may contain URL-encoded backslashes (%5C) when Windows
	// paths are passed through the Garden API as URIs.
	decoded, err := url.PathUnescape(directory)
	if err == nil {
		directory = decoded
	}

	directory = filepath.FromSlash(directory)

	if !filepath.IsAbs(directory) {
		return "", fmt.Errorf("directory must be an absolute path")
	}

	return directory, nil
}
