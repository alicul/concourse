package windowscontainerd_test

import (
	"testing"

	"code.cloudfoundry.org/garden"
	"github.com/concourse/concourse/worker/runtime/windowscontainerd"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type SpecSuite struct {
	suite.Suite
	*require.Assertions
}

func (s *SpecSuite) TestOciSpecValidations() {
	for _, tc := range []struct {
		desc string
		spec garden.ContainerSpec
	}{
		{
			desc: "no handle specified",
			spec: garden.ContainerSpec{},
		},
		{
			desc: "rootfsPath not specified",
			spec: garden.ContainerSpec{
				Handle: "handle",
			},
		},
		{
			desc: "rootfsPath without scheme",
			spec: garden.ContainerSpec{
				Handle:     "handle",
				RootFSPath: "foo",
			},
		},
		{
			desc: "rootfsPath with unknown scheme",
			spec: garden.ContainerSpec{
				Handle:     "handle",
				RootFSPath: "weird://foo",
			},
		},
		{
			desc: "rootfsPath not absolute",
			spec: garden.ContainerSpec{
				Handle:     "handle",
				RootFSPath: "raw://../not/absolute",
			},
		},
		{
			desc: "no rootfsPath, image with unknown scheme",
			spec: garden.ContainerSpec{
				Handle: "handle",
				Image:  garden.ImageRef{URI: "weird://bar"},
			},
		},
	} {
		s.T().Run(tc.desc, func(t *testing.T) {
			_, err := windowscontainerd.OciSpec(tc.spec, `C:\scratch`)
			s.Error(err)
		})
	}
}

func (s *SpecSuite) TestOciSpecValid() {
	spec := garden.ContainerSpec{
		Handle:     "my-container",
		RootFSPath: "raw:///rootfs",
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)
	s.NotNil(oci)

	s.Equal("my-container", oci.Hostname)
	s.NotNil(oci.Root)
	s.Equal("/rootfs", oci.Root.Path)
	s.NotNil(oci.Windows)
	s.NotNil(oci.Windows.Network)
	s.True(oci.Windows.Network.AllowUnqualifiedDNSQuery)
	s.Contains(oci.Windows.LayerFolders, "/rootfs")
}

func (s *SpecSuite) TestOciSpecUsesImageURIWhenNoRootFSPath() {
	spec := garden.ContainerSpec{
		Handle: "handle",
		Image:  garden.ImageRef{URI: "raw:///image-rootfs"},
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)
	s.Equal("/image-rootfs", oci.Root.Path)
}

func (s *SpecSuite) TestOciSpecDefaultProcess() {
	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)

	s.NotNil(oci.Process)
	s.Equal(`C:\`, oci.Process.Cwd)
	s.Equal("ContainerAdministrator", oci.Process.User.Username)
	s.NotEmpty(oci.Process.Args)
}

func (s *SpecSuite) TestOciSpecEnvVars() {
	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
		Env:        []string{"FOO=bar", "BAZ=qux"},
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)
	s.Contains(oci.Process.Env, "FOO=bar")
	s.Contains(oci.Process.Env, "BAZ=qux")
}

func (s *SpecSuite) TestOciSpecProperties() {
	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
		Properties: garden.Properties{
			"concourse:team": "main",
		},
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)
	s.Equal("main", oci.Annotations["concourse:team"])
}

func (s *SpecSuite) TestOciSpecBindMounts() {
	for _, tc := range []struct {
		desc     string
		mounts   []garden.BindMount
		succeeds bool
	}{
		{
			desc:     "empty src",
			succeeds: false,
			mounts: []garden.BindMount{
				{DstPath: "/b", Origin: garden.BindMountOriginHost},
			},
		},
		{
			desc:     "empty dst",
			succeeds: false,
			mounts: []garden.BindMount{
				{SrcPath: "/a", Origin: garden.BindMountOriginHost},
			},
		},
		{
			desc:     "non-absolute src",
			succeeds: false,
			mounts: []garden.BindMount{
				{SrcPath: "a", DstPath: "/b", Origin: garden.BindMountOriginHost},
			},
		},
		{
			desc:     "non-absolute dst",
			succeeds: false,
			mounts: []garden.BindMount{
				{SrcPath: "/a", DstPath: "b", Origin: garden.BindMountOriginHost},
			},
		},
		{
			desc:     "unknown origin",
			succeeds: false,
			mounts: []garden.BindMount{
				{SrcPath: "/a", DstPath: "/b", Origin: 42},
			},
		},
		{
			desc:     "unknown mode",
			succeeds: false,
			mounts: []garden.BindMount{
				{SrcPath: "/a", DstPath: "/b", Origin: garden.BindMountOriginHost, Mode: 123},
			},
		},
		{
			desc:     "valid read-only mount",
			succeeds: true,
			mounts: []garden.BindMount{
				{SrcPath: "/a", DstPath: "/b", Origin: garden.BindMountOriginHost, Mode: garden.BindMountModeRO},
			},
		},
		{
			desc:     "valid read-write mount",
			succeeds: true,
			mounts: []garden.BindMount{
				{SrcPath: "/a", DstPath: "/b", Origin: garden.BindMountOriginHost, Mode: garden.BindMountModeRW},
			},
		},
	} {
		s.T().Run(tc.desc, func(t *testing.T) {
			spec := garden.ContainerSpec{
				Handle:     "handle",
				RootFSPath: "raw:///rootfs",
				BindMounts: tc.mounts,
			}

			_, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
			if tc.succeeds {
				s.NoError(err)
			} else {
				s.Error(err)
			}
		})
	}
}

func (s *SpecSuite) TestOciSpecBindMountOptions() {
	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
		BindMounts: []garden.BindMount{
			{SrcPath: "/src-ro", DstPath: "/dst-ro", Origin: garden.BindMountOriginHost, Mode: garden.BindMountModeRO},
			{SrcPath: "/src-rw", DstPath: "/dst-rw", Origin: garden.BindMountOriginHost, Mode: garden.BindMountModeRW},
		},
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)

	s.Len(oci.Mounts, 2)
	s.Contains(oci.Mounts[0].Options, "ro")
	s.Contains(oci.Mounts[1].Options, "rw")
}

func (s *SpecSuite) TestOciSpecNoResourceLimits() {
	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)
	s.Nil(oci.Windows.Resources)
}

func (s *SpecSuite) TestOciSpecCPULimits() {
	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
		Limits: garden.Limits{
			CPU: garden.CPULimits{
				LimitInShares: 512,
			},
		},
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)

	s.NotNil(oci.Windows.Resources)
	s.NotNil(oci.Windows.Resources.CPU)
	s.Equal(uint16(512), *oci.Windows.Resources.CPU.Shares)
}

func (s *SpecSuite) TestOciSpecCPUWeight() {
	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
		Limits: garden.Limits{
			CPU: garden.CPULimits{
				Weight: 256,
			},
		},
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)

	s.NotNil(oci.Windows.Resources.CPU)
	s.Equal(uint16(256), *oci.Windows.Resources.CPU.Shares)
}

func (s *SpecSuite) TestOciSpecMemoryLimits() {
	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
		Limits: garden.Limits{
			Memory: garden.MemoryLimits{
				LimitInBytes: 1024 * 1024 * 512,
			},
		},
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)

	s.NotNil(oci.Windows.Resources)
	s.NotNil(oci.Windows.Resources.Memory)
	s.Equal(uint64(1024*1024*512), *oci.Windows.Resources.Memory.Limit)
}

func (s *SpecSuite) TestOciSpecNoLinuxSection() {
	spec := garden.ContainerSpec{
		Handle:     "handle",
		RootFSPath: "raw:///rootfs",
	}

	oci, err := windowscontainerd.OciSpec(spec, `C:\scratch`)
	s.NoError(err)
	s.Nil(oci.Linux)
}
