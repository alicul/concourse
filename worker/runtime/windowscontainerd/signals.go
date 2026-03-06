package windowscontainerd

import "syscall"

// Windows uses CTRL_SHUTDOWN_EVENT for graceful termination and
// TerminateProcess for forced termination. The containerd shim
// translates standard signals to Windows-appropriate mechanisms.
var (
	WindowsGracefulSignal  = syscall.Signal(0xf) // SIGTERM equivalent
	WindowsTerminateSignal = syscall.Signal(0x9) // SIGKILL equivalent
)
