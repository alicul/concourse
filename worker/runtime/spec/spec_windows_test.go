//go:build windows

package spec_test

import (
	"testing"

	"code.cloudfoundry.org/garden"
	"github.com/concourse/concourse/v8/worker/runtime/spec"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func TestOciSpec(t *testing.T) {
	gdn := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: `raw://C:\rootfs`,
		Env:        []string{"foo=bar"},
		Limits: garden.Limits{
			Memory: garden.MemoryLimits{LimitInBytes: 1024},
		},
	}

	oci, err := spec.OciSpec(spec.DefaultInitBinPath, specs.LinuxSeccomp{}, specs.LinuxSeccomp{}, specs.Hooks{}, spec.FullPrivilegedMode, gdn, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	if oci.Root.Path != `C:\rootfs` {
		t.Errorf("unexpected root path: %s", oci.Root.Path)
	}

	if oci.Windows == nil {
		t.Fatal("expected a Windows section")
	}

	if *oci.Windows.Resources.Memory.Limit != 1024 {
		t.Errorf("unexpected memory limit: %d", *oci.Windows.Resources.Memory.Limit)
	}

	if oci.Process.Args[0] != `C:\concourse\bin\init.exe` {
		t.Errorf("unexpected process args: %v", oci.Process.Args)
	}

	foundInitMount := false
	for _, m := range oci.Mounts {
		if m.Destination == spec.InitBinContainerDir {
			foundInitMount = true
		}
	}
	if !foundInitMount {
		t.Errorf("expected a mount for the init binary dir, got: %v", oci.Mounts)
	}
}

func TestOciWindowsResourcesCPUShares(t *testing.T) {
	for _, tc := range []struct {
		desc   string
		limits garden.CPULimits
		want   uint16
	}{
		{desc: "shares within the Windows range are passed on", limits: garden.CPULimits{LimitInShares: 512}, want: 512},
		{desc: "the largest weight Windows takes is passed on", limits: garden.CPULimits{LimitInShares: 10000}, want: 10000},
		{desc: "one share beyond the Windows range becomes the largest weight", limits: garden.CPULimits{LimitInShares: 10001}, want: 10000},
		{desc: "shares far beyond the Windows range become the largest weight", limits: garden.CPULimits{LimitInShares: 20000}, want: 10000},
		{desc: "shares that do not fit the spec's 16 bits become the largest weight", limits: garden.CPULimits{LimitInShares: 70000}, want: 10000},
		{desc: "shares that wrap to nothing in 16 bits become the largest weight", limits: garden.CPULimits{LimitInShares: 65536}, want: 10000},
		{desc: "a weight beyond the Windows range becomes the largest weight", limits: garden.CPULimits{LimitInShares: 512, Weight: 20000}, want: 10000},
		{desc: "a weight goes before the shares", limits: garden.CPULimits{LimitInShares: 20000, Weight: 256}, want: 256},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			resources := spec.OciWindowsResources(garden.Limits{CPU: tc.limits})
			if resources == nil || resources.CPU == nil || resources.CPU.Shares == nil {
				t.Fatalf("expected CPU shares, got: %+v", resources)
			}

			if *resources.CPU.Shares != tc.want {
				t.Errorf("unexpected CPU shares: %d, want %d", *resources.CPU.Shares, tc.want)
			}
		})
	}

	if resources := spec.OciWindowsResources(garden.Limits{}); resources != nil {
		t.Errorf("expected no resources without limits, got: %+v", resources)
	}
}

func TestOciSpecEscapedRootfs(t *testing.T) {
	gdn := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: `raw://C:%5Cworkdir%5Cvolumes%5Clive%5Cguid%5Cvolume/rootfs`,
	}

	oci, err := spec.OciSpec(spec.DefaultInitBinPath, specs.LinuxSeccomp{}, specs.LinuxSeccomp{}, specs.Hooks{}, spec.FullPrivilegedMode, gdn, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	expected := `C:\workdir\volumes\live\guid\volume\rootfs`
	if oci.Root.Path != expected {
		t.Errorf("unexpected root path: %s", oci.Root.Path)
	}
}

func TestWindowsContainerPath(t *testing.T) {
	for input, expected := range map[string]string{
		"/tmp/build/abc":     `C:\tmp\build\abc`,
		"/scratch":           `C:\scratch`,
		`C:\already\windows`: `C:\already\windows`,
	} {
		if got := spec.WindowsContainerPath(input); got != expected {
			t.Errorf("WindowsContainerPath(%q) = %q, expected %q", input, got, expected)
		}
	}
}

func TestOciSpecBindMountsPosixDestinations(t *testing.T) {
	mounts, err := spec.OciSpecBindMounts([]garden.BindMount{
		{
			SrcPath: `C:\workdir\volumes\live\guid\volume`,
			DstPath: "/tmp/build/abc",
			Mode:    garden.BindMountModeRW,
			Origin:  garden.BindMountOriginHost,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if mounts[0].Destination != `C:\tmp\build\abc` {
		t.Errorf("unexpected destination: %s", mounts[0].Destination)
	}
}

func TestOciSpecRequiresHandle(t *testing.T) {
	_, err := spec.OciSpec(spec.DefaultInitBinPath, specs.LinuxSeccomp{}, specs.LinuxSeccomp{}, specs.Hooks{}, spec.FullPrivilegedMode, garden.ContainerSpec{}, 0, 0)
	if err == nil {
		t.Fatal("expected an error for a spec without a handle")
	}
}
