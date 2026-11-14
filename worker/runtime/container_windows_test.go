//go:build windows

package runtime

import (
	"io"
	"reflect"
	"testing"

	"code.cloudfoundry.org/garden"
	"github.com/opencontainers/runtime-spec/specs-go"
)

func TestProcessPath(t *testing.T) {
	for _, tc := range []struct {
		desc string
		path string
		want string
	}{
		{desc: "a name without a directory is left alone", path: "cmd.exe", want: "cmd.exe"},
		{desc: "a script in an input gets the separators of Windows", path: "repo/ci/build.cmd", want: `repo\ci\build.cmd`},
		{desc: "a path that has them already is left alone", path: `repo\ci\build.cmd`, want: `repo\ci\build.cmd`},
		{desc: "a path on a drive gets them too", path: "C:/tools/build.cmd", want: `C:\tools\build.cmd`},
		{desc: "a path that starts with a slash is left alone", path: "/opt/resource/in", want: "/opt/resource/in"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			if got := processPath(tc.path); got != tc.want {
				t.Errorf("unexpected path: %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProcessSpecGivesTheExecutableWindowsSeparators(t *testing.T) {
	container := &Container{}

	procSpec, err := container.setupContainerdProcSpec(
		garden.ProcessSpec{Path: "repo/ci/build.cmd", Args: []string{"/v", "some/argument"}},
		specs.Spec{Process: &specs.Process{}},
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{`repo\ci\build.cmd`, "/v", "some/argument"}
	if !reflect.DeepEqual(procSpec.Args, want) {
		t.Errorf("unexpected args: %q, want %q", procSpec.Args, want)
	}
}

func TestProcessIOCreatorKeepsContainersApart(t *testing.T) {
	stdio := containerdCIO(garden.ProcessIO{Stdout: io.Discard, Stderr: io.Discard}, false)

	first, err := processIOCreator("container-a", stdio)("task")
	if err != nil {
		t.Fatalf("first container: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	second, err := processIOCreator("container-b", stdio)("task")
	if err != nil {
		t.Fatalf("second container running a process with the same ID: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
}
