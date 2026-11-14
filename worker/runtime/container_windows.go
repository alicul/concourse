//go:build windows

package runtime

import (
	"context"
	"path/filepath"
	"strings"

	"code.cloudfoundry.org/garden"
	bespec "github.com/concourse/concourse/v8/worker/runtime/spec"
	containerd "github.com/containerd/containerd/v2/client"
)

// closeStdinAfterStart is a no-op on Windows: the runhcs shim closes the
// process's stdin as soon as CloseIO is called - before the client's stdin
// payload has necessarily been copied in - so processes reading stdin (like
// resource checks) would see an immediate EOF. The shim closes stdin by
// itself once the worker's stdin pipe reaches EOF.
func closeStdinAfterStart(ctx context.Context, proc containerd.Process, spec garden.ProcessSpec) error {
	return nil
}

func processCwd(dir string) string {
	if dir == "" {
		return `C:\`
	}
	return bespec.WindowsContainerPath(dir)
}

// processPath gives the path of a process's executable the separators of
// Windows. A pipeline names a script in one of its inputs as it does for
// Linux, with forward slashes (repo/ci/build.cmd). Windows runs a batch file
// through cmd.exe, which takes what follows the first slash for a switch:
// "'repo' is not recognized as an internal or external command".
//
// A path that starts with a slash is left as it is, like the web node's own
// /opt/resource/in, which Windows resolves.
func processPath(path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	return filepath.FromSlash(path)
}

// envWithDefaultPath never injects a PATH on Windows; the image's own
// environment applies.
func envWithDefaultPath(uid uint32, currentEnv []string) string {
	return ""
}
