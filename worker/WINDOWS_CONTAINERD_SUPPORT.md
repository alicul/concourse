# Adding containerd Runtime Support on Windows

## Overview

This document describes the changes made to add containerd as a runtime option for Concourse workers running on Windows. Previously, Windows (and Darwin) workers were restricted to the Houdini runtime, while Linux workers could choose between containerd, Guardian, and Houdini. Since containerd natively supports Windows containers (via the Host Compute Service), this change extends that support to Concourse's worker architecture.

## Background: How Concourse Selects Runtimes

Concourse uses Go build tags and platform-specific files to separate runtime logic per platform:

| Platform | File | Runtimes Available |
|----------|------|--------------------|
| Linux (amd64) | `worker_linux.go` + `worker_linux_amd64.go` | containerd (default), guardian, houdini |
| Linux (arm64) | `worker_linux.go` + `worker_linux_arm64.go` | containerd (default), houdini |
| Non-Linux | `worker_nonlinux.go` (`//go:build !linux`) | houdini only |

The `gardenServerRunner()` method is the key entry point: it examines the `--runtime` flag and creates the appropriate runner. On non-Linux platforms, there was no `--runtime` flag at all — Houdini was hardcoded.

### The Garden Interface

All runtimes expose a `garden.Backend` interface to the Garden server. The Garden server provides a REST API that the ATC (Air Traffic Controller) uses to manage containers on workers. This interface is the contract between the runtime and the rest of Concourse:

```
ATC → Garden Server → garden.Backend → Runtime (containerd / guardian / houdini)
```

## Why a Separate Windows Backend Package?

The Linux containerd backend (`worker/runtime/`) is deeply coupled to Linux-specific concepts:

| Linux Concept | Implementation | Windows Equivalent |
|---------------|----------------|--------------------|
| **OCI Spec** | Uses `specs.Linux` (cgroups, namespaces, seccomp) | Uses `specs.Windows` (Job Objects, HCS) |
| **Networking** | CNI plugins + iptables | HNS (Host Networking Service) |
| **User namespaces** | `/proc/self/uid_map` mapping | Not applicable (SID-based users) |
| **Seccomp** | Syscall filtering profiles | Not available on Windows |
| **Linux capabilities** | `CAP_NET_RAW`, `CAP_SYS_ADMIN`, etc. | Not applicable |
| **Cgroups** | CPU shares, memory limits, PID limits | Job Objects |
| **Container mounts** | proc, sysfs, devpts, cgroup, tmpfs | Not applicable |
| **Process signals** | `SIGTERM` / `SIGKILL` via runc | `CTRL_SHUTDOWN_EVENT` / `TerminateProcess` via HCS |
| **Init binary** | `/tmp/gdn-init` injected into container | Windows containers use their own entrypoint |
| **DNS** | resolv.conf parsing + DNS proxy | Windows DNS via HNS network configuration |
| **Rootfs user lookup** | `/etc/passwd` and `/etc/group` | Windows user accounts (SIDs) |

Given these fundamental differences, we chose to create a **separate package** (`worker/runtime/windowscontainerd/`) rather than trying to make the Linux backend cross-platform. This approach:

1. **Minimizes risk**: No changes to existing Linux runtime code that is already tested and in production
2. **Follows existing patterns**: Houdini is also a separate package (`github.com/concourse/houdini`)
3. **Clearer separation**: Windows and Linux container models are fundamentally different; a shared backend would require excessive abstraction that obscures the actual behavior
4. **Reuses what can be reused**: The `libcontainerd` client package (which wraps the containerd gRPC API) is already cross-platform and shared between both backends

## Changes Made

### 1. Modified: `worker/workercmd/worker_nonlinux.go`

**Change**: Build tag changed from `//go:build !linux` to `//go:build darwin`

**Reasoning**: The old `!linux` tag applied to both Windows and Darwin. Since Windows now has its own file with containerd support, we restrict this file to Darwin only. Darwin continues to use Houdini exclusively (there is no containerd support for Darwin/macOS containers).

### 2. New: `worker/workercmd/worker_windows.go`

**Purpose**: Windows-specific worker command with runtime selection

**Key decisions**:
- **Default runtime is `houdini`**: Unlike Linux where containerd is the default, we default to houdini on Windows for backward compatibility. Users must explicitly opt into containerd with `--runtime containerd` or `CONCOURSE_RUNTIME=containerd`.
- **`RuntimeConfiguration`** includes `choice:"containerd" choice:"houdini"` — no Guardian option since Guardian (garden-runc) is Linux-only.
- **`ContainerdRuntime` struct** is a simplified version of the Linux one, omitting Linux-specific fields:
  - No `InitBin` — Windows containers don't need an injected init binary
  - No `SeccompProfilePath` — seccomp is a Linux kernel feature
  - No `CNIPluginsDir` — Windows uses HNS for networking, not CNI
  - No `PrivilegedMode` — the Linux concept of privileged containers (with full capabilities) doesn't map directly to Windows
  - No `AllowedDevices` — Linux device cgroup rules don't apply
  - Simplified `Network` struct without IPv6 config, restricted networks, etc.
- **`gardenServerRunner()`** dispatches to either `containerdRunner()` or `houdiniRunner()` based on the `--runtime` flag.

### 3. New: `worker/workercmd/containerd_windows.go`

**Purpose**: Starts the containerd daemon and Garden server on Windows

**Key differences from the Linux version (`containerd.go`)**:
- **Named pipe** instead of Unix socket: containerd on Windows uses `\\.\pipe\containerd-containerd` for its gRPC API (rather than `/run/containerd/containerd.sock`)
- **No `SysProcAttr.Pdeathsig`**: `Pdeathsig` (parent death signal) is a Linux-specific mechanism. On Windows, child processes are managed differently.
- **No DNS proxy**: The Linux version optionally runs a DNS proxy server. On Windows, DNS is configured through HNS network settings.
- **No CNI network setup**: The Linux `containerdGardenServerRunner()` builds CNI network options with multiple configuration parameters. Windows networking is handled by the HNS-based backend.
- **Simplified config**: The default containerd config disables the CRI plugin (not needed for Concourse) without specifying Linux-specific snapshotters.
- **Backend creation** uses `windowscontainerd.NewGardenBackend()` instead of `runtime.NewGardenBackend()`.

### 4. New Package: `worker/runtime/windowscontainerd/`

This package provides a Windows-specific `garden.Backend` implementation backed by containerd. It consists of:

#### `backend.go` — GardenBackend

The Windows `GardenBackend` struct is simpler than the Linux version:

```go
// Linux GardenBackend has 14 fields including:
//   seccompProfile, seccompProfileFuse, allowedDevices,
//   userNamespace, initBinPath, ociHooksDir, etc.

// Windows GardenBackend has 4 fields:
type GardenBackend struct {
    client         libcontainerd.Client
    maxContainers  int
    requestTimeout time.Duration
    dnsServers     []string
}
```

**Reasoning**: The removed fields correspond to Linux-only features. Adding them would create unused configuration that misleads operators.

The `Create()` method follows a simpler flow than the Linux version:
1. Check container capacity
2. Generate Windows OCI spec (no seccomp, no user namespace mapping)
3. Create container via containerd
4. Create and start a task (no network namespace setup, no hermetic container traffic rules)

The `Destroy()` method sends a kill signal and waits up to 10 seconds before force-deleting. The Linux version has a more elaborate graceful/ungraceful kill cycle with individual process targeting — this simpler approach is appropriate for Windows where process groups work differently.

#### `container.go` — Container

The `Container` type implements `garden.Container`. Key differences from the Linux version:

- **No Killer/RootfsManager/IOManager dependencies**: The Linux `Container` uses injected strategy objects for killing processes, managing rootfs, and tracking I/O. The Windows version handles these directly since the approaches are simpler.
- **Default working directory**: `C:\` instead of `/`
- **Default PATH**: Windows system PATH instead of Linux `/usr/local/bin:/usr/bin:/bin`
- **User specification**: Uses `specs.User.Username` (e.g., "ContainerAdministrator") instead of `specs.User.UID/GID`
- **Resource limits**: `CurrentMemoryLimits()` reads from `spec.Windows.Resources.Memory` instead of `spec.Linux.Resources.Memory`
- **Error patterns**: Different regex patterns for detecting "executable not found" errors (Windows error messages differ from Linux)

#### `process.go` — Process

Nearly identical to the Linux version. The `garden.Process` interface is fundamentally the same on both platforms — wait for exit, get exit code, resize TTY. The containerd Process API is cross-platform.

#### `spec.go` — OCI Spec Generation

This is where the most significant platform differences manifest:

**Linux OCI Spec** (`worker/runtime/spec/spec.go`):
```go
&specs.Spec{
    Process: &specs.Process{
        Args:         []string{"/tmp/gdn-init"},
        Capabilities: &capabilities,  // Linux capabilities
    },
    Linux: &specs.Linux{
        Namespaces:  namespaces,       // PID, IPC, UTS, Mount, Network, User
        Resources:   resources,         // cgroups-based
        Seccomp:     &seccompProfile,
        Devices:     devices,
        UIDMappings: uidMappings,
        GIDMappings: gidMappings,
    },
    Mounts: containerMounts,  // proc, dev, devpts, sysfs, cgroup, etc.
}
```

**Windows OCI Spec** (`worker/runtime/windowscontainerd/spec.go`):
```go
&specs.Spec{
    Process: &specs.Process{
        Args: []string{"cmd.exe", "/S", "/C", "ping -t localhost > NUL"},
        User: specs.User{Username: "ContainerAdministrator"},
    },
    Windows: &specs.Windows{
        LayerFolders: []string{rootfs},
        Resources:    windowsResources,  // Job Object-based
        Network: &specs.WindowsNetwork{
            AllowUnqualifiedDNSQuery: true,
        },
    },
}
```

**Key design decisions for the Windows spec**:
- **Init process**: The Linux backend injects a custom `gdn-init` binary that sleeps until the actual command is exec'd. On Windows, we use `cmd.exe /S /C ping -t localhost > NUL` as a long-running init process that can be similarly exec'd into. This avoids needing to compile and distribute a Windows init binary.
- **User**: Windows containers use `ContainerAdministrator` by default (the Windows equivalent of root).
- **LayerFolders**: Windows containers require layer folders to be specified. We set the rootfs path as the single layer.
- **Resources**: CPU limits use `WindowsCPUResources.Shares` (capped at uint16) and memory limits use `WindowsMemoryResources.Limit`. This maps to Windows Job Object resource controls.
- **Network**: `AllowUnqualifiedDNSQuery` is set to `true` to allow simple hostname resolution within containers.

#### `signals.go` — Signal Definitions

Defines signal constants for Windows process management. Go's `syscall.Signal` type exists on Windows, and the containerd Windows shim (runhcs) translates these to appropriate Windows mechanisms:
- Signal 0xf (SIGTERM equivalent) → graceful shutdown via `CTRL_SHUTDOWN_EVENT`
- Signal 0x9 (SIGKILL equivalent) → forced termination via `TerminateProcess`

## What Reuses Existing Code

The `libcontainerd` package (`worker/runtime/libcontainerd/`) is **shared** between Linux and Windows. This package wraps the containerd gRPC API and has no build tags — it's already cross-platform. Both the Linux `GardenBackend` and the Windows `GardenBackend` use it to:
- Connect to the containerd daemon
- Create/delete/list containers
- Manage container specs and labels

The `garden_server_runner.go` is also **shared** — it wraps any `garden.Backend` implementation into an HTTP server that exposes the Garden API. The `houdini.go` file (Houdini backend) is also shared across all platforms.

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `CONCOURSE_RUNTIME` | Runtime to use (`containerd` or `houdini`) | `houdini` |
| `CONCOURSE_CONTAINERD_BIN` | Path to containerd binary | `containerd` (from PATH) |
| `CONCOURSE_CONTAINERD_CONFIG` | Path to containerd config file | Auto-generated |
| `CONCOURSE_CONTAINERD_LOG_LEVEL` | Containerd log level | `info` |
| `CONCOURSE_CONTAINERD_REQUEST_TIMEOUT` | Timeout for containerd requests | `5m` |
| `CONCOURSE_CONTAINERD_MAX_CONTAINERS` | Maximum container count | `250` |
| `CONCOURSE_CONTAINERD_DNS_SERVER` | DNS server for containers | System default |

### Example Usage

```powershell
# Start a Concourse worker with containerd runtime on Windows
concourse.exe worker `
    --work-dir C:\concourse `
    --runtime containerd `
    --tsa-host ci.example.com:2222 `
    --tsa-public-key tsa-host-key.pub `
    --tsa-worker-private-key worker-key
```

## Future Work

1. **HNS Network Integration**: The current implementation uses containerd's default Windows networking. A full HNS integration would allow network isolation, port mapping, and traffic filtering similar to the Linux CNI backend.

2. **Windows Container Image Support**: The current `raw://` rootfs scheme works for pre-extracted container images. Adding support for pulling Windows container images from registries would improve usability.

3. **Resource Monitoring**: The `Capacity()`, `Metrics()`, and `BulkMetrics()` methods are not implemented. These could be implemented using Windows Performance Counters and Job Object queries.

4. **Init Binary**: Creating a dedicated Windows init binary (similar to Linux's `gdn-init`) would provide cleaner process lifecycle management. The current `ping -t` approach is functional but inelegant.

5. **Process I/O Management**: The Linux backend has a sophisticated `IOManager` that prevents multiple readers from attaching to the same FIFO files. The Windows backend currently uses simpler I/O handling. A similar manager could be added if I/O issues are observed.

6. **Integration Tests**: The Linux backend has integration tests in `worker/runtime/integration/`. Equivalent tests should be created for the Windows backend.

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                        Concourse Worker                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  worker.go (shared)                                             │
│    ├── gardenServerRunner()                                     │
│    │     │                                                      │
│    │     ├── [Linux] worker_linux.go                             │
│    │     │     ├── containerdRunner()  → runtime.GardenBackend  │
│    │     │     ├── guardianRunner()    → gdn binary             │
│    │     │     └── houdiniRunner()     → houdini.Backend        │
│    │     │                                                      │
│    │     ├── [Windows] worker_windows.go         ← NEW          │
│    │     │     ├── containerdRunner()             ← NEW          │
│    │     │     │    → windowscontainerd.GardenBackend ← NEW     │
│    │     │     └── houdiniRunner()     → houdini.Backend        │
│    │     │                                                      │
│    │     └── [Darwin] worker_nonlinux.go (renamed)              │
│    │           └── houdiniRunner()     → houdini.Backend        │
│    │                                                            │
│    └── Garden Server (shared)                                   │
│          └── garden.Backend interface                            │
│                                                                 │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  libcontainerd/ (shared, cross-platform)                        │
│    └── Client interface → containerd gRPC API                   │
│                                                                 │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  containerd daemon                                              │
│    ├── [Linux]   Unix socket, runc runtime                      │
│    └── [Windows] Named pipe, runhcs/HCS runtime                 │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## Change Summary

### Problem

Concourse used containerd as the default runtime for Linux workers but only supported Houdini (an insecure non-container runtime) on Windows and Darwin. Since containerd natively supports Windows containers via HCS (Host Compute Service), there was no reason not to allow it on Windows.

### Approach

Rather than refactoring the heavily Linux-coupled `worker/runtime/` package (which uses cgroups, seccomp, user namespaces, CNI, iptables, etc.), a **separate Windows containerd backend package** was created -- consistent with how Houdini is also a separate package. This minimizes risk to the existing Linux code while enabling containerd on Windows.

### Files Modified (1)

- **`worker/workercmd/worker_nonlinux.go`** -- Build tag changed from `//go:build !linux` to `//go:build darwin`, since Windows now has its own file

### Files Created (7)

**Worker command layer:**

- **`worker/workercmd/worker_windows.go`** -- Windows worker command with `--runtime` flag supporting `containerd` and `houdini` (defaulting to `houdini` for backward compatibility)
- **`worker/workercmd/containerd_windows.go`** -- Windows containerd runner that starts the containerd daemon via named pipe (`\\.\pipe\containerd-containerd`) and creates the Garden server

**Windows containerd backend package (`worker/runtime/windowscontainerd/`):**

- **`backend.go`** -- `GardenBackend` implementing `garden.Backend` for Windows containers, reusing the cross-platform `libcontainerd` client
- **`container.go`** -- `Container` implementing `garden.Container` with Windows-appropriate defaults (Windows paths, `ContainerAdministrator` user, Windows memory limits via `spec.Windows.Resources`)
- **`process.go`** -- `Process` implementing `garden.Process` (containerd's process API is cross-platform)
- **`spec.go`** -- Windows OCI spec generation using `specs.Windows` (Job Objects for resource limits, HNS for networking) instead of `specs.Linux` (cgroups, seccomp, namespaces)
- **`signals.go`** -- Signal constants for Windows process management

**Documentation:**

- **`worker/WINDOWS_CONTAINERD_SUPPORT.md`** -- This file: detailed rationale for every design decision, architecture diagram, configuration reference, and future work items

### Build Verification

All three target platforms compile successfully:

- `GOOS=linux go build ./worker/workercmd/...` -- OK
- `GOOS=windows go build ./worker/workercmd/...` -- OK
- `GOOS=darwin go build ./worker/workercmd/...` -- OK
