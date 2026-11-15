# Test report for #9786: the containerd runtime on Windows workers

I ran the head of this PR (`cebda20cc`, with the commits it carries over from #9766) on a real Windows worker, next to a web node and a Linux worker, under both isolation modes, and then ran the same tests again with fixes for what turned up. This is what I found, with a reproduction for each numbered problem and a commit for most of them.

## Summary

The core works. On Windows Server 2022, under process isolation and under Hyper-V isolation, tasks run from images that a Linux worker fetched and streamed over. Inputs, outputs, caches, parameters, `run.dir` and `run.user` behave, and limits within the range of Windows arrive in the container's spec. Files travel between Linux and Windows tasks in both directions. Six tasks run at once. `fly intercept`, aborting a build and destroying a pipeline under a running task work, and a task outlives its worker and containerd being killed and started again (a stop by Ctrl+C ends it: problem 9).

Running it harder, and reading its code, brought out twelve problems, three of them (2, 5 and 12) gaps that your descriptions already list. **Seven are fixed** on the branch [`alicul/concourse:windows-containerd-hyperv-isolation`](https://github.com/alicul/concourse/commits/windows-containerd-hyperv-isolation): ten commits on top of your head, each for one thing and each with unit tests for what it changes (an eleventh commit, the newest, only adds this report). The ten were run together on the worker, under both isolation modes: the PR head before, the branch after (`6b8c62f6e` has its effect on Linux and was shown there). Take them, squash them or rewrite them as you see fit. **Five are still open:** I reproduced them on the worker and have no fix for them.

| | What goes wrong on the PR head | Where | With the branch |
|---|---|---|---|
| 1 | A web node that restarts errors every step that is still running on a Windows worker when it comes back | both modes | fixed: `9a7072cdc`, with `1fdc0d7f7` and `d7ba07773` in shared code (limits: D) |
| 2 | A container's first destroy fails (all 39 that were destroyed in one pass) and its network namespace stays until the machine restarts: the gap listed as known on #9766 | process isolation | fixed: `b30b3733b` (limits: D) |
| 3 | A container whose task has died can never be destroyed | both modes | fixed: `6cec933d4` (limits: D) |
| 4 | A task that cannot start shows the wrong error in its build, and its container stays | both modes | fixed: `e1d72cdbf`, `6b8c62f6e` (limits: D) |
| 5 | Stored images are never removed, as the PR says | both modes | fixed: `717b824bb`, which adds the removal (limits: D) |
| 6 | A task with more than 10000 CPU shares does not start | both modes | fixed: `917b67ec8` |
| 7 | A script named with forward slashes (`run.path: repo/ci/hello.cmd`) does not run | both modes | fixed: `9f4b04d56` (limits: D) |
| 8 | Every task's log carries terminal control sequences | both modes | **open** |
| 9 | Ctrl+C on the worker's console ends the steps that the worker started | both modes | **open** |
| 10 | A containerd left behind by a worker that died keeps serving, and the next worker's own containerd hangs | both modes | **open** |
| 11 | A step can reach services on its worker's host | both modes | **open** |
| 12 | A step with `hermetic: true` keeps its network access: another gap listed as known on #9766 | both modes | **open** |

The five open problems are not all that is left. Still there with the branch are: problems 8 to 12; five smaller notes; one more gap that #9766 lists (unpacking inside the create lock) and one that #9786 lists and that is the web node's default (the image streamed for every build); what the fixes of six of the seven problems do not cover ("limits: D" in the table); four Concourse problems that are not this PR's; and three things that the runtime needs from outside. The next section lists them all in one place, together with what I did not test.

## What is still open

This section holds everything that this report leaves open with the ten commits applied, in seven groups. A and B are about this runtime and have no fix on the branch. C says where the gaps that you list yourself stand. D is what my fixes leave. E is Concourse's and not this PR's. F is what I did not test, which is not the same as broken. G lies outside this PR. "Reproduced" means that I saw it on the Server 2022 worker; where a point comes from reading code or from a review, it says so.

**A. Five problems of this runtime, reproduced, with no fix on the branch.** Each has a section under "Problems 8 to 12 in detail" with what I saw, and a step under "Reproducing".

| | Still open | What it means in practice | What a fix needs |
|---|---|---|---|
| 8 | Every task's log starts with terminal control sequences | `fly watch` clears the screen and renames the window at every Windows task | ask for a console only for `fly intercept`, not for a task's process |
| 9 | Ctrl+C on the worker's console ends the steps that the worker started | the usual way to stop a console program errors the builds that run on that worker | start containerd off the worker's console |
| 10 | When the worker process dies its containerd lives on, and the next worker's own containerd hangs | the new worker's builds run on the old containerd, with its binary and configuration, and a restart that was meant to upgrade containerd changes nothing | end containerd with its worker (a job object); refusing to start while the pipe still answers would at least say so |
| 11 | A step can reach services on its worker's host | whatever listens on the host beyond the loopback address, and that the host's firewall lets in, is open to every task | rules on the containers' network or on its endpoints, as a Linux worker has them |
| 12 | A step with `hermetic: true` keeps its network access | a hermetic step reaches its host and the internet | attach no endpoint to a hermetic container (not tried in the runtime) |

**B. Five smaller notes on this runtime, reproduced, with no fix on the branch.** They are described under "Smaller notes". In the first two a Linux worker does the same; they are here because Houdini did not, so a pipeline that moves from Houdini meets them.

- A task that names no image fails, as every task written for Houdini would, and the hint in the build does not say what a Windows worker needs.
- `run.path` without the script's extension (`repo/ci/hello` for `hello.cmd`) is not found. Houdini found such a script.
- `privileged: true` is ignored, and nothing in the build says so.
- Aborting a build whose task is a `cmd.exe` takes the full 10 seconds of grace.
- A Windows worker that has resource types and no tag can be given the checks and gets of any untagged pipeline. Your notes on #9766 say to tag such a worker; nothing enforces it.

**C. The gaps that your descriptions list, and where each stands with the branch.**

| Gap | Listed on | With the branch |
|---|---|---|
| Unpacked layers are kept only while a container uses them | #9766 | closed by #9786 itself, which stores the image |
| Nothing removes a stored image | #9786 | closed: `717b824bb` (problem 5) |
| A destroyed container's network namespace stays behind | #9766 | closed: `b30b3733b` (problem 2) |
| Hermetic containers keep their network access | #9766 | **open**: problem 12 |
| Unpacking runs inside the worker's create lock | #9766 | **open**: not touched by the branch, and not measured (my passes with the lock on used `nanoserver`, a small image) |
| The image's volume is streamed to the worker for every build | #9786 | unchanged: it is the web node's default, and happens for every container, not only for every build. With `CONCOURSE_ENABLE_CACHE_STREAMED_VOLUMES=true` on the web node the image is streamed once (see the results) |

**D. What the fixes on the branch do not cover.** Apart from the namespaces that the PR head left behind, none of this was seen on the worker. It comes from reading the code, from the reviews of the commits and from their unit tests.

- Problem 1 (`9a7072cdc`, `d7ba07773`): on the worker only one web node was restarted. A second web node that attaches to a process while the first is still attached to it was tried in unit tests only. Two things are left. An attach that fails after it has taken the pipes over keeps them open until the container is destroyed. And in a narrow window a new wait can still fail: when an earlier wait deletes the process between the attach's check of the process and the new wait's call. The second is in shared code, on Linux as before.
- Problem 2 (`b30b3733b`): a namespace is deleted when its container is destroyed. Namespaces and endpoints whose container is already gone are not looked for. Those that the PR head left behind stay until the machine restarts. A worker that is killed while it unpacks an image leaves a namespace and an endpoint that nothing removes, because the network is attached before the image is imported; whether a restart of the machine removes such an endpoint I did not check.
- Problem 3 (`6cec933d4`): on the worker I tried a task that had died and a task whose start had failed. A task that was created and never started, because the worker process was killed in between, was tried in a unit test only.
- Problem 4 (`e1d72cdbf`): if the container cannot be removed, it stays, and the web node finds it and takes it as before. On Windows the step then fails with the wrong error, as long as the container's task is still there; on Linux a step can run in it. The full remedy is on the web node: not to take a container whose task is not running.
- Problem 5 (`717b824bb`): images are removed by time alone. There is no limit on their size and no reaction to a disk that fills. The tests ran with a retention of 2 minutes and a handful of images; the default of 24 hours and a tag whose image changes with every build were not run.
- Problem 7 (`9f4b04d56`): a path that starts with a slash is left as it is, so that the web node's `/opt/resource/in` keeps working. I did not try a batch file named that way, and expect it to fail as before.

**E. Concourse problems that are not this PR's.** A Linux worker has them too. They are described under "What I met on the way that is not this PR".

- A get that ends while no web node is attached errors with `output a null version`. This is the one scenario of the results table that fails on the branch.
- What a task prints while no web node is attached is lost.
- A `fly intercept` whose client is cut off stops the whole container, and with it the task of a running build. On Windows I saw it only on my Windows 11 machine.
- `--containerd-request-timeout=0` fails every create while the limit of containers is on, as it is by default. That a Linux worker has it too comes from reading the code: I did not run it there.

**F. Not tested.**

- A Server 2022 with current updates (the worker's build is 20348.587), and any other Windows version in the scenarios of the tables. On my Windows 11 machine process isolation does not start any container, with plain `ctr` either, so the fault lies with that machine and not with this runtime. What problems 1 and 3 report of the fixes on that machine was tried with the branch as it stood at six commits, not with the branch tip.
- `servercore` images beyond fetching them, and a put on a Windows worker.
- More than one Windows worker, more than one web node, and more than a few containers at once.
- A worker process that runs for longer than about 20 minutes: every pass restarts it several times.
- The commits one by one on a worker. They were run together, so which commit makes which scenario pass rests on what each one changes and on its unit tests.
- What the web UI makes of the control sequences of problem 8: I have not looked at it in a browser.
- The cases that D names as tried in unit tests only or not run: a second web node, a task that was created and never started, the default retention of 24 hours, and a batch file named with a leading slash.
- Performance, and the costs of Hyper-V isolation that your description of #9786 measures.

**G. What the runtime needs from outside this PR.** These are the three questions further down, under "Three questions".

- concourse/registry-image-resource#414, which writes a Windows image in the form this runtime runs, is not merged yet. It has no published Windows build, and its `platform` takes no OS version.
- A Windows archive that carries `containerd.exe`, the shim, `init.exe` and a Windows build of `registry-image`. As far as I can see, concourse/ci builds and packs these for Linux only.
- Tests that start a container on Windows. The repository has none.

## How it was tested

- **Windows worker:** Windows Server 2022 from the evaluation ISO (build 20348.587, no later updates) in a Hyper-V machine with 4 CPUs and 6 GB, nested virtualization on for Hyper-V isolated containers, containerd 2.4.2 with its runhcs shim. Before any Concourse test, plain `ctr run` was checked to start `nanoserver` images under both isolation modes on that machine. The worker ran with `--containerd-max-containers=0` (no limit of containers, which also turns the create lock off), except in the probe of `--containerd-request-timeout=0` and in the shorter passes with the default limit; under Hyper-V isolation with `--containerd-hyperv-memory-mb=640`; and, where the build has the flag, with `--containerd-image-retention=2m`.
- **Web node and Linux worker:** in docker compose, built from the branch: at its seventh commit for the PR head's full passes and for the Linux column of the results, at its ninth from the probes on (the tenth changes only what a Windows worker does). The branch changes only `worker/`, and makes go-winio a direct requirement in `go.mod`, so the web node is the PR head's. The Linux worker has every change of the branch that alters what Linux does: the two fixes named under problem 1, `9a7072cdc`'s changes to Destroy and the IO manager, `e1d72cdbf` and, from the probes on, `6b8c62f6e`. Concourse's default settings, except the containerd DNS proxy on the Linux worker and, in the one scenario that restarts the web node during a get, a web node that picks its builds up every 2 seconds, not every 10.
- **Images:** `mcr.microsoft.com/windows/nanoserver` (`ltsc2022` and older 20348 builds; `ltsc2025` for the image for a newer Windows), fetched on the Linux worker by a `registry-image` built from concourse/registry-image-resource#414 and streamed to the Windows worker. A Windows build of the same resource type was given to the Windows worker only for the scenarios that need a get on it; the two that take the web node away during a get fetch `servercore` there, a get that lasts long enough.
- **One pass** runs the 28 scenarios of the results table against one build of the worker under one isolation mode, started from a restarted machine with an empty work directory, with the hash of the binary on the machine checked against the build. The 28 took 51 to 67 minutes. On the branch tip nine of the ten probes in the table under "Problems 8 to 12 in detail" ran at the end of the same pass, about ten minutes more; on the PR head they ran in a pass of their own. (The tenth, the hermetic step, ran in the shorter passes against a web node that caches streamed volumes, below.) Each scenario is a script that drives `fly` and reads the worker's log, and ends in a verdict.
- **Builds:** the PR head and the branch tip, each in a full pass under each isolation mode. The scenarios that disturb the web node were also run against a step on the Linux worker, to tell what the Windows runtime does from what Concourse does anyway.
- **Two shorter passes** per build and mode followed the full passes: fourteen of the scenarios with the worker's default limit of containers, which turns the create lock on, and two scenarios about a task's image against a web node that caches streamed volumes (`CONCOURSE_ENABLE_CACHE_STREAMED_VOLUMES=true`), with two probes of what a task reaches over the network.
- **Windows 11:** on my Windows 11 machine process isolation does not start any container, with plain `ctr` either, so every result in the tables is from Server 2022. What I saw only on Windows 11 is marked as such: in problem 1 under Hyper-V isolation, in problem 3 a container that process isolation could not start, and one of the points that are not this PR.

What was not tested is listed under F of "What is still open" above.

## Results

In the table below, every scenario passes on the branch in both modes, with one exception: the get that ends while the web node is away, which fails on the Linux worker in the same way and is not this PR's problem. (An image for a newer Windows runs under Hyper-V isolation, on the PR head too; that row is a note, not a verdict.) The probes of problems 8 to 12 are in the table of probes under "Problems 8 to 12 in detail", and they fail on the branch as they do on the PR head.

| scenario | PR head, process | PR head, Hyper-V | branch tip, process | branch tip, Hyper-V | Linux worker (branch) |
|---|---|---|---|---|---|
| a task runs (image fetched on the Linux worker, streamed over) | pass | pass | pass | pass |  |
| a task's output is the next task's input | pass | pass | pass | pass |  |
| six tasks at once | pass | pass | pass | pass |  |
| 3000 lines of output as the task ends | pass | pass | pass | pass |  |
| a task that exits 3 fails its build | pass | pass | pass | pass |  |
| `params`, `run.dir`, a renamed input | pass | pass | pass | pass |  |
| a task cache is kept between builds | pass | pass | pass | pass |  |
| `run.user`: ContainerUser, ContainerAdministrator | pass | pass | pass | pass |  |
| `container_limits` (512 CPU shares, 512 MB) are in the container's spec, and the task runs | pass | pass | pass | pass |  |
| `container_limits: {cpu: 20000}` | **FAIL** | **FAIL** | pass | pass |  |
| `privileged: true` (the task runs) | pass | pass | pass | pass |  |
| files from a Linux task to a Windows task and back | pass | pass | pass | pass |  |
| the web node restarts while a task runs | **FAIL** | **FAIL** | pass | pass | pass |
| the web node restarts twice during one task | **FAIL** | **FAIL** | pass | pass | pass |
| the web node is away for 75 s; the task ends meanwhile | pass | pass | pass | pass | pass |
| a running build is aborted | pass | pass | pass | pass |  |
| a pipeline is destroyed while its task runs | pass | pass | pass | pass |  |
| the worker and its containerd are killed and started while a task runs | pass | pass | pass | pass |  |
| a task from an image for a newer Windows than the host | pass | note | pass | note |  |
| `fly intercept` into a running task | pass | pass | pass | pass |  |
| no failed destroys and no stray namespaces after the scenarios above | **FAIL** | pass | pass | pass |  |
| five builds from five image versions: the images are removed afterwards | **FAIL** | **FAIL** | pass | pass |  |
| a get on the Windows worker, and a task from its image | pass | pass | pass | pass |  |
| that image stays while its resource cache holds it, then goes | **FAIL** | **FAIL** | pass | pass |  |
| the web node restarts while a resource script runs | **FAIL** | **FAIL** | pass | pass | pass |
| the web node is away until the resource script has ended | **FAIL** | **FAIL** | **FAIL** | **FAIL** | **FAIL** |
| a task from an image that cannot start | **FAIL** | **FAIL** | pass | pass |  |
| a task's container dies under it (init or utility VM killed) | **FAIL** | **FAIL** | pass | pass |  |

"note" is a result that is recorded without a verdict, and an empty cell in the Linux column means that the scenario was not run there. What the failures look like is in the sections below. Some rows need a word here:

- **Three of the PR head's failures are gaps that your descriptions already list:** the two image rows (nothing removes a stored image) and the row of failed destroys and stray namespaces under process isolation (the namespace of #9766).
- **A task from an image for a newer Windows than the host:** as your descriptions say, under process isolation the worker refuses it when it creates the container, before unpacking anything, and says why (`snapshotter windows does not support platform {amd64 windows 10.0.26100.33828 [] }`), and under Hyper-V isolation `nanoserver:ltsc2025` runs on the 20348 host.
- **The web node is away until the resource script has ended:** the get errors with `resource script (/opt/resource/in /tmp/build/get) output a null version`. The Linux worker does exactly the same: what a resource script prints while no web node is attached is lost, and for a get that is its result. Not this PR's problem.
- **`privileged: true`:** the task runs, and the flag is ignored: the Windows `OciSpec` never reads it, and its comment counts privileged mode among the Linux-only concepts it ignores (see the smaller notes).

For a task, output printed while no web node is attached is lost too, on Linux as on Windows. With the fixes, one restart let 48 of 60 lines reach the log on the Windows worker under process isolation, 48 under Hyper-V isolation and 49 on the Linux worker; two restarts let 38, 38 and 38 through.

**With the default limit of containers.** The scenarios above ran with no limit of containers, and so without the create lock. Fourteen of them were run again with the worker's default limit, which turns the lock on: every one of them came out as it had without the lock, the failures with the same errors.

| scenario | PR head, process | PR head, Hyper-V | branch tip, process | branch tip, Hyper-V |
|---|---|---|---|---|
| a task runs (image fetched on the Linux worker, streamed over) | pass | pass | pass | pass |
| a task's output is the next task's input | pass | pass | pass | pass |
| a task cache is kept between builds | pass | pass | pass | pass |
| files from a Linux task to a Windows task and back | pass | pass | pass | pass |
| a task from an image for a newer Windows than the host | pass | note | pass | note |
| `container_limits: {cpu: 20000}` | **FAIL** | **FAIL** | pass | pass |
| six tasks at once | pass | pass | pass | pass |
| 3000 lines of output as the task ends | pass | pass | pass | pass |
| the web node restarts while a task runs | **FAIL** | **FAIL** | pass | pass |
| a pipeline is destroyed while its task runs | pass | pass | pass | pass |
| no failed destroys and no stray namespaces after the scenarios above | **FAIL** | pass | pass | pass |
| a get on the Windows worker, and a task from its image | pass | pass | pass | pass |
| a task from an image that cannot start | **FAIL** | **FAIL** | pass | pass |
| a task's container dies under it (init or utility VM killed) | **FAIL** | **FAIL** | pass | pass |

**With a web node that caches streamed volumes.** As your description says, Concourse streams the image's volume to the worker for every build; for every container, in fact: six tasks at once print `streaming volume for image from worker linux-1` six times. That is the web node's default, whatever the worker. With `CONCOURSE_ENABLE_CACHE_STREAMED_VOLUMES=true` it keeps the first copy as a resource cache on the worker. Both builds were run against such a web node as well. Under both modes, on the PR head as on the branch, six tasks at once then streamed the image once (the other five waited for that copy) and the six of the next build streamed nothing. On the branch the image in containerd's store stayed for as long as that cache held it, past the retention time, and was removed 197 seconds after its pipeline was destroyed, under either mode. On the PR head it stayed, as every image does there.

| scenario | PR head, process | PR head, Hyper-V | branch tip, process | branch tip, Hyper-V |
|---|---|---|---|---|
| six tasks at once, twice: their image is streamed to the worker once | pass | pass | pass | pass |
| the stored image stays while that streamed copy is a cache, then goes | **FAIL** | **FAIL** | pass | pass |

## Problems that have a fix on the branch

### 1. A web node that restarts errors every step that is still running when it comes back

Restart the web node while a task runs on a Windows worker, as every deploy of Concourse does. On the Linux worker the build carries on. On the PR head, under both isolation modes, it ends like this:

```
start process: backend error: Exit status: 500, message: {"Type":"","Message":"task exec: failed to create stdout pipe \\\\.\\pipe\\ctr-<handle>-task-stdout: open \\\\.\\pipe\\ctr-<handle>-task-stdout: Access is denied.","Handle":"","ProcessID":"","Binary":""}
errored
```

This is not the collision that your stdio pipe commit fixed: the names carry the container's handle. Here it is the process's own pipes that the attach cannot listen on again. The worker's log for such a container (the first of two restarts in one build):

```
19:43:44.678 run.spawned
19:44:08.768 attach.failed  load proc: failed to create stdout pipe \\.\pipe\ctr-<handle>-task-stdout: ... Access is denied.
19:44:08.865 run.failed     task exec: failed to create stdout pipe \\.\pipe\ctr-<handle>-task-stdout: ... Access is denied.
19:44:45.847 run.exited     status=0
```

The web node that comes back attaches to the step's process, the attach fails, it falls back to running the process again, and that fails on the same pipe. The task itself runs on: it ran to its end 37 seconds later, with exit status 0, for a build that had already errored. A get is hit in the same way while its resource script runs (the pipe is then `ctr-<handle>-resource-stdout`). A step that has ended before the web node comes back is not affected.

The cause is in how a process's stdio reaches the worker on Windows. The worker listens on a named pipe per stream and the shim connects to it. containerd's IO for Windows keeps listening on the stdout and stderr pipes, and keeps the shim's connections to them open, until the process exits. A pipe's name stays taken for as long as a handle to it is open, so the attach, which listens on the same names again, is refused.

There is a second half that only showed on a Windows 11 host (build 26300, Hyper-V isolation, `nanoserver:ltsc2025`). The shim connects to stdin once, and closes the process's stdin when that connection closes. On Server 2022 it did that too when the web node went away, and the task ran on. On the Windows 11 host the same thing ended the task, which runs in a terminal like every task, with exit status 3221225786. With the fix below, in the branch as it stood at six commits, the build succeeded on that host as well; the branch tip was not tried there.

`9a7072cdc` gives the Windows runtime its own IO for these pipes (`worker/runtime/process_io_windows.go`): an attach takes the pipes over from the IO before it, the shim connects again with its next write, the stdin of a process in a terminal stays open when whoever fed it goes away, and the pipes are closed when the container's task is deleted. Two smaller fixes came out of testing this path, in code that the PR only opens to Windows by a build tag: `1fdc0d7f7` (the IO manager stored the nil IO of a failed attach, so the next attach to that process panicked) and `d7ba07773` (after an attach two waits delete the same process, and the second one failed, which could error a step that had finished). Both are upstream bugs that a Linux worker has as well, they do not depend on the Windows runtime, and they could go in a pull request of their own.

With the fixes the build succeeds under both modes, also with two restarts during one task, and a get whose script is still running when the web node comes back succeeds too.

### 2. On Server 2022 with process isolation, a container's first destroy fails and its network namespace stays

One pass of the scenarios on the PR head under process isolation created 41 containers. None was destroyed at the first attempt. For 39 of them the first destroy failed with:

```
garden-server.destroy.failed  network remove: delete namespace: hcnDeleteNamespace failed in Win32: The specified request is unsupported. (0x803b0015) {"Success":false,"Error":"The specified request is unsupported. ","ErrorCode":2151350293}
```

Each of these was destroyed at the next attempt, usually half a minute later, but its HCN namespace was not. (The other two were still there when the pass ended: the container of problem 3, whose destroys all failed, and that of a task that had failed, which Concourse keeps. So were two containers whose create had failed, those of problems 4 and 6.) The pass began with 1 namespace on the machine and ended with 44: the host's, those of the 4 containers still there, and the 39 left behind. A restart of the machine clears them; in an earlier run on another Server 2022 machine, `Remove-HnsNamespace` got the same "unsupported" answer. Under Hyper-V isolation none of this happens.

This is the gap that #9766 lists as known: destroy deletes the endpoint, then fails to delete the namespace while the container's task, and with it its compute system, still exists. The retry finds the endpoint gone, and the namespace is only found through the endpoint, so the task and the container are deleted and the namespace stays.

`b30b3733b` deletes the namespace and its endpoint once the task is gone, or was never there, and before the container is deleted. On the branch, a pass of the same scenarios had 42 containers and no failed destroy, and ended with 2 namespaces: the host's and that of the one container still there.

### 3. A container whose task has died can never be destroyed

Kill what a running task's container stands on: its init process under process isolation, the `vmwp.exe` of its utility VM under Hyper-V isolation. The build fails, as it should. But from then on every attempt to destroy the container fails, every 30 seconds, for good:

```
garden-server.destroy.failed  gracefully killing task: graceful kill: kill task execed processes: task execed processes: pid listing: hcs::System::Properties <handle>: hcsshim: the handle has already been closed
```

(under Hyper-V isolation: `pid listing: bridge closed: use of closed network connection`). The container keeps its volumes, its endpoint, its namespace and its image. The same happens to a container whose task never started, which is the next problem; on a Windows 11 host such a container stayed across a restart of the worker too. With the branch as it stood at six commits, such a container was destroyed there at the first attempt.

To stop a task gracefully the worker asks the shim for the task's processes, and the shim cannot list the processes of a compute system that has shut down. `6cec933d4`: a task that has stopped, or was never started, has no process left to stop. With it the container is destroyed half a minute after its pipeline, under both modes.

### 4. A task that cannot start shows the wrong error, and its container stays

Run a task from an image that cannot start (I replaced `smss.exe` in a layer on top of `nanoserver`). On the PR head the build says:

```
start process: backend error: Exit status: 500, message: {"Type":"","Message":"task exec: exec: '' in task: '<handle>' must be running to create additional execs: failed precondition", ...}
errored
```

The real reason is only in the worker's log: `hcs::System::Start <handle>: The virtual machine or container exited unexpectedly` under process isolation, `bridge closed: use of closed network connection` under Hyper-V isolation. And the container was still there three minutes after its pipeline had been destroyed, because of problem 3.

When the task of a new container fails to start, Create returns the error and leaves the container. After a failed create the web node looks the handle up and takes a container it finds for the one it asked for (that lookup is there for two web nodes that create the same container at once, as in a rolling restart, #9712), so it tries to run the step in that container, and on Windows that fails with the error above. On a Linux worker the same sequence can run a step in a container whose network was never set up, or a hermetic step whose network was never shut off; that part is from reading the code.

`e1d72cdbf` removes a container whose task did not start before the error is returned (on Windows the removal relies on `6cec933d4`, which comes before it). The build then shows

```
find or create container on worker windows-1: starting task: hcs::System::Start <handle>: The virtual machine or container exited unexpectedly.
```

(under Hyper-V isolation: `starting task: bridge closed: use of closed network connection`) and nothing of the container is left. `6b8c62f6e` completes it for Linux: a container without a task was deleted without the `hosts`, `hostname` and `resolv.conf` files that its creation had written under the worker's `networkmounts` directory. On a Linux worker whose containers could not get a task (an OCI hook that fails), three builds left three such directories with the branch up to `e1d72cdbf` and none with `6b8c62f6e`.

### 5. Images are never removed from containerd's store

The last commit of the PR keeps an imported image between containers, and its description says what that leaves open: nothing removes a stored image. In numbers: five builds from five versions of `nanoserver` left five images in the store, about 650 MiB of content and unpacked layers each, all still there eight minutes after their pipelines had been destroyed. At the end of a pass the store held 9 images under process isolation and 10 under Hyper-V isolation. A pipeline that builds an image and runs a task from it adds one for every build, and a `servercore` image is several GB.

`717b824bb` adds the removal. An image is kept for as long as a volume on the worker holds its layout, so an image that is in use, or that a resource cache still holds, is kept. After the last such volume is gone it is kept for the time of a new flag, `--containerd-image-retention` (24 hours by default, 2 minutes in these tests), so that an image streamed in again for every build stays unpacked between builds. Then the volume sweeper removes it, and containerd collects its content and layers.

With it, the five images were gone 212 seconds after their pipelines were destroyed under process isolation and 280 seconds under Hyper-V isolation, and the store was back from 3.9 GiB to 0.7 GiB (4.0 to 0.7 under Hyper-V isolation). An image fetched by a get on the Windows worker stayed while its resource cache existed, past the retention time, and was removed 200 and 177 seconds after its pipeline was destroyed.

### 6. A task with more than 10000 CPU shares does not start

```yaml
config:
  platform: windows
  container_limits: {cpu: 20000}
```

```
start process: backend error: Exit status: 500, message: {"Type":"","Message":"recreating init task: failed to create shim task: hcs::CreateComputeSystem <handle>: The parameter is incorrect.\n[Event Detail: The specified relative processor weight is out of valid range (20000). Provider: 17103e3f-3c6e-4677-bb17-3b267eb5be57 EventID: 11501]","Handle":"","ProcessID":"","Binary":""}
errored
```

The shares go into the Windows spec as they come. Windows takes a weight of at most 10000 (the runtime spec and hcsshim both say 0 to 10000; Docker rejects more on Windows), while a Linux worker takes far larger ones, so the same task can come with 20000. Above 65535 the value also wraps in the spec's 16-bit field. `container_limits` of `{cpu: 512, memory: 536870912}` arrive in the container's spec, and the task runs, under both modes.

`917b67ec8` caps the shares at 10000. On the PR head 10000 shares ran and 10001 did not. With the fix 10000, 10001 and 20000 shares all run, under both modes.

### 7. A script named with forward slashes does not run

A script in an input is named in a pipeline as it is for Linux, for example:

```yaml
run: {path: repo/ci/hello.cmd}
```

```
'repo' is not recognized as an internal or external command,
```

With `repo\ci\hello.cmd` the same script runs. Windows runs a batch file through `cmd.exe`, which takes what follows the first slash for a switch. Houdini converted the slashes, so a task written for it can be written this way.

`9f4b04d56` gives the path of a step's executable the separators of Windows, unless it starts with a slash, like the web node's own `/opt/resource/in`, which works as it is. A batch file that a pipeline names with a leading slash is left as it is too; I did not try one, and expect it to fail as before. With the commit the script runs under both modes, named either way, and a program named with forward slashes (`tool/bin/tool.exe`) still runs, as your `bin/hello.exe` sample on #9766 does without the commit.

### What stands behind each commit

The ten commits, oldest first:

| | Commit | Title | For |
|---|---|---|---|
| 1 | `717b824bb` | worker: behaviour: garbage collect cached Windows containerd images | problem 5 |
| 2 | `1fdc0d7f7` | worker: fix: keep a process's IO when attaching to it fails | problem 1 (shared code) |
| 3 | `d7ba07773` | worker: fix: wait for a process that another wait has deleted | problem 1 (shared code) |
| 4 | `9a7072cdc` | worker: fix: keep a Windows step running when its web node goes away | problem 1 |
| 5 | `b30b3733b` | worker: fix: delete a Windows container's network namespace | problem 2 |
| 6 | `6cec933d4` | worker: fix: destroy a Windows container whose task has stopped | problem 3 |
| 7 | `e1d72cdbf` | worker: fix: remove a container whose task did not start | problem 4 |
| 8 | `6b8c62f6e` | worker: fix: delete the network files of a container that has no task | problem 4 (on Linux) |
| 9 | `917b67ec8` | worker: fix: cap a Windows container's CPU shares at what Windows takes | problem 6 |
| 10 | `9f4b04d56` | worker: fix: run a Windows script named with forward slashes | problem 7 |

The ten commits were run together, so the table of results compares the PR head with the whole branch. What ties a scenario to a commit is what the commit changes, and each commit comes with unit tests for what it changes. For `1fdc0d7f7` and `d7ba07773` those tests are the main evidence: the first needs an attach that fails in the right moment, the second is a race.

## Problems 8 to 12 in detail

Problems 8 to 11 came out of reading the PR's Windows code rather than out of the scenarios above, and each was then tried on the worker. Problem 12 is the gap that #9766 lists, measured. These are the probes, each with what it belongs to:

| scenario | PR head, process | PR head, Hyper-V | branch tip, process | branch tip, Hyper-V | belongs to |
|---|---|---|---|---|---|
| the log holds what the task printed, and nothing else | **FAIL** | **FAIL** | **FAIL** | **FAIL** | problem 8: **open** |
| Ctrl+C on the worker's console while a task runs | **FAIL** | **FAIL** | **FAIL** | **FAIL** | problem 9: **open** |
| the worker process dies, its containerd lives on, the worker starts again | **FAIL** | **FAIL** | **FAIL** | **FAIL** | problem 10: **open** |
| a task cannot reach a service on its worker's host | **FAIL** | **FAIL** | **FAIL** | **FAIL** | problem 11: **open** |
| a task with `hermetic: true` reaches nothing outside | **FAIL** | **FAIL** | **FAIL** | **FAIL** | problem 12: **open** |
| `run.path` names a script in an input with forward slashes (`repo/ci/hello.cmd`) | **FAIL** | **FAIL** | pass | pass | problem 7: fixed |
| 10000 and 10001 CPU shares | **FAIL** | **FAIL** | pass | pass | problem 6: fixed |
| `run.path` names the script without its extension (`repo/ci/hello` for `hello.cmd`) | note | note | note | note | smaller note: open |
| a task that names no image | note | note | note | note | smaller note: open |
| `--containerd-request-timeout=0` ("no timeout") with the default limit of containers | **FAIL** | **FAIL** | **FAIL** | **FAIL** | not this PR: open |

The two rows that pass on the branch are the probes of problems 6 and 7, which have a fix. In the two rows marked note, the task fails in the same way on the PR head and on the branch. They are recorded as notes, not as failures, because a Linux worker does the same while Houdini did not; they are described under "Smaller notes".

### 8. Every task's log carries terminal control sequences

A task that prints `first`, an empty line and `third`, as `fly watch` receives it, with the control characters made visible:

```
<ESC>[2J<ESC>[m<ESC>[Hfirst<CR>
<ESC>]0;C:\Windows\system32\cmd.exe<BEL><ESC>[?25h<CR>
third<CR>
```

The same from a Linux task is `first<CR>`, `<CR>`, `third<CR>`. So every Windows task's output begins with "clear the screen" and "cursor to the top left", and carries a window title a little later. In a terminal, `fly watch` and `fly trigger-job -w` clear the screen at every Windows task and rename the window. What the web UI makes of a cursor move to the top of a step's log I have not looked at in a browser; its parser does act on cursor positions.

Every task step asks for a terminal (`atc/exec/task_step.go`, 500 columns by 500 rows). On Linux that is a pty, which adds only a carriage return to each line. On Windows the shim has HCS emulate a console (`EmulateConsole`), and what comes out is a rendering of that console. Houdini ignored the request on Windows. I would stop asking for a console for a task's process on Windows and ask for one only for `fly intercept`. The stdin half of problem 1 only exists for a process in a console, too.

### 9. Ctrl+C on the worker's console ends the steps that the worker started

Ctrl+C is how a console program is asked to stop, also by the wrappers that run one as a Windows service. Sent to the worker's console while a task ran (the numbers count processes; `taskshim` is the shim of that task):

```
before: concourse=1 containerd=1 shims=5 taskshim=1
after:  concourse=0 containerd=0 shims=4 taskshim=0
wait for process completion: backend error: ... task attach: no running task found: task <handle> not found
errored
```

The worker and containerd stop, as asked, and the shim of the running task goes with them, which ends the task. Killing the worker and containerd outright does not do that: the shim lives on and the build succeeds once the worker runs again (the "worker and its containerd are killed" row of the results). So the polite stop is the destructive one. containerd is started on the worker's console (`worker/workercmd/containerd_windows.go`), the shims it starts inherit it, and a console control event goes to every process on the console. The four other shims in the output belonged to containers made under earlier starts of the worker, each in a console of its own; in a worker that has run since its start, every shim is on its console. Starting containerd off the worker's console (with `CREATE_NO_WINDOW` it gets one of its own, which its shims inherit) would keep the event from the shims. On Linux a Ctrl+C on the worker's terminal reaches neither: the worker starts its containerd in a process group of its own (`worker/workercmd/containerd.go`), and containerd starts each shim in one of its own, where on Windows it starts them with no flags, so they share its console (`pkg/shim/util_unix.go` and `util_windows.go` in containerd). The Linux worker also starts containerd with `Pdeathsig`, which settles the next problem there. The Windows runner has no counterpart of either.

### 10. A containerd left behind by a worker that died keeps serving

Kill only `concourse.exe` (a crash, a task manager): its `containerd.exe` lives on, since nothing ties one to the other. Start the worker again. The probe then counts the `containerd.exe` processes and, in the new worker's error log, where its containerd writes, the lines "containerd successfully booted" (it is up) and "waiting for response from boltdb open" (it waits for the database):

```
containerd after the worker was started again: 2 (pid 4972,5764; started 22:11:05,22:10:55)
the new worker's containerd says it is up: 0
the new worker's containerd waits for the database: 1
== a build on the worker after that
succeeded
```

The new containerd blocks on the database that the old one holds, for good. The worker does not notice: the old containerd answers on the pipe, whose name is fixed. (Its readiness check makes a client and sends no request, but a request would not tell either, since the old containerd would answer it.) So its builds run on the old containerd: the binary, the configuration and the log of a worker that no longer exists. A restart that was meant to upgrade containerd changes nothing, and every such restart leaves one more containerd behind. A job object that ends containerd with its worker, and that the shims do not join (`JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK`), would settle it; a refusal to start when the pipe already answers, or a readiness check that asks the containerd behind the pipe for its process id (its introspection service reports it) and compares that with the process the worker started, would at least say so.

### 11. A step can reach services on its worker's host

```
   IPv4 Address. . . . . . . . . . . : 10.80.0.119
   Default Gateway . . . . . . . . . : 10.80.0.1
* Established connection to 10.80.0.1 (10.80.0.1 port 22) from 10.80.0.119 port 49157
SSH-2.0-OpenSSH_for_Windows_10.4 Win32-OpenSSH-GitHub
```

A task connected to the sshd of its worker's host on the gateway address of the containers' network. A Linux worker rejects traffic from a container to its host unless told otherwise, and can keep containers out of other networks; the Windows runtime sets no rule on its network or its endpoints. This goes beyond the hermetic gap listed on #9766: a step that is not hermetic reaches the host too. On the host, it can reach whatever listens beyond the loopback address and the host's firewall lets in.

### 12. A step with `hermetic: true` keeps its network access

```
== host
* Established connection to 10.80.0.1 (10.80.0.1 port 22) from 10.80.0.231 port 49156
SSH-2.0-OpenSSH_for_Windows_10.4 Win32-OpenSSH-GitHub
== address
* Established connection to 1.1.1.1 (1.1.1.1 port 80) from 10.80.0.231 port 49159
== name
* Established connection to mcr.microsoft.com (150.171.69.10 port 443) from 10.80.0.231 port 49160
```

That is the task of a step that says `hermetic: true`, on the branch under process isolation: it connected to its worker's host, to `1.1.1.1:80` and to `mcr.microsoft.com:443`. It did the same under Hyper-V isolation, and on the PR head under both modes. This is the gap that #9766 lists: `HCNNetwork.DropContainerTraffic` does nothing, so a hermetic container keeps its network access. With the flags that are there, what can be cut off is a whole worker. I made an HNS network of type `Private` beforehand and named it with `--containerd-network-name`. On the PR head and on the branch, under both modes, a task on that worker ran and reached neither the outside nor its host at 10.80.0.1, where the other tasks reach the host. In a further run on the branch, the task did not reach its host at the private network's own gateway address (10.90.0.1) either, nor at the address the host has on its own network, which a task on the worker's usual network does reach. That takes the network from every step on that worker, hermetic or not, so it is no substitute. For a hermetic step, attaching no endpoint may be enough: the worker knows that a container is hermetic before it creates it, and a container without any network starts under both modes (plain `ctr run` on this machine). I have not tried that in the runtime.

## Smaller notes

These five are still open on the branch, and none has a fix there.

- **A task that names no image**, as every task for Houdini is written, fails with `new container: garden spec to oci spec: rootfs must not be empty`, followed by Concourse's hint that ends "The image must be in rootfs format." A Linux worker with containerd says the same, but for a worker that moves from Houdini to containerd every existing task is such a task, and on Windows the hint says too little: the image's `rootfs/` has to hold an OCI image layout, as a `registry-image` with concourse/registry-image-resource#414 writes it.
- **`run.path: repo/ci/hello`**, where the file is `hello.cmd`, fails with `hcs::System::CreateProcess: repo/ci/hello <handle>: The system cannot find the file specified` under process isolation (`guest RPC failure: The system cannot find the file specified` under Hyper-V isolation). Houdini found such a script through `PATHEXT`; a Linux worker never adds an extension.
- **`privileged: true`** is ignored on Windows: `OciSpec` in `spec_windows.go` never reads it (its comment counts privileged mode among the Linux-only concepts it ignores), and nothing in the build says so. A task that needs an administrator has to ask for `run.user: ContainerAdministrator`, which works.
- **Aborting a build** whose task is a `cmd.exe` takes the full 10 seconds of grace: the signal sent first reaches the process as a shutdown event, which a `cmd.exe` in a console ignores. The task then ends with status 1067.
- **Resource types on a Windows worker.** Your notes on #9766 say that a worker advertises the resource types next to its install directory, and that a Windows worker that has them should be tagged. I ran into what happens otherwise, by accident: I had put the Windows build of `registry-image` there. In an early run of these tests, which I set aside because of it, the gets of untagged pipelines then went to either worker, and the check of an image in a registry that only the Linux worker could reach went to the Windows worker, so its build waited for a version that never came. A check, get or put asks for no platform, only for a resource type by name (besides tags and team), so nothing but the tag keeps them apart. That advice belongs in the documentation; a platform for resource types would settle it.

## What I met on the way that is not this PR

None of these has a fix here, and none is special to this runtime.

- A get that ends while no web node is attached errors with `output a null version` on Linux too (see the results above).
- What a task prints while no web node is attached is lost, on Linux as on Windows (see the results above).
- A `fly intercept` whose client is cut off (fly killed, connection lost) makes the web node stop the whole container, and with it the running task of a build. That is in upstream master (`atc/worker/gardenruntime/process.go`), on Linux as on Windows (there I saw it on my Windows 11 machine).
- `--containerd-request-timeout=0`, which the flag's help calls "no timeout", makes every container fail with `new container: acquiring create container lock: context deadline exceeded` while the limit of containers is on, as it is by default. The lock (`worker/runtime/timeout_lock.go`) is code that a Linux worker runs too.

## Three questions

- **Tests on a real worker.** `worker/runtime/integration` builds for Linux only, and none of the tests that this PR and the branch add starts a container (they use fakes, a content store in a temporary directory, or named pipes standing in for the shim), so nothing in the repository runs a container on Windows. Most of what is in this report only showed on a real worker. Do you plan a suite for Windows? (The tests behind this report are scripts that drive `fly` against a compose deployment and a real Windows worker.)
- **Packaging.** Your notes on #9766 list what a Windows worker with this runtime needs: `containerd.exe` and `containerd-shim-runhcs-v1.exe` on the path, the `init.exe` of `cmd/init/windows`, and a Windows build of `registry-image` if it is to run checks, gets and puts itself. As far as I can see, concourse/ci builds containerd for Linux only (`tasks/containerd/build.sh`) and adds binaries and resource types to the Linux archive only (`tasks/concourse-build/run.sh`). Is the Windows archive meant to carry them?
- **concourse/registry-image-resource#414** is what makes any of this usable, and it is still open. As your notes on #9766 say, its Windows build has to be made by hand (I made one; its get works on the Windows worker, see the results). Its `platform` takes an `os` and an `architecture` but no OS version, so for a tag that holds images for several Windows versions a get cannot say which one it wants, and under process isolation only the host's version runs. Do you plan a published Windows build of the resource, and an OS version in `platform`?

## Reproducing

Numbered as the problems above. The tasks used are of this kind; the get needs a `registry-image` on the Linux worker that writes Windows images as OCI layouts (concourse/registry-image-resource#414):

```yaml
resources:
- name: nanoserver
  type: registry-image
  source:
    repository: mcr.microsoft.com/windows/nanoserver
    tag: ltsc2022
    platform: {os: windows, architecture: amd64}

jobs:
- name: ticker
  plan:
  - get: nanoserver
  - task: tick
    image: nanoserver
    config:
      platform: windows
      run:
        path: cmd.exe
        args: ["/c", "(for /L %i in (1,1,60) do @(echo tick %i & ping -n 2 127.0.0.1 > nul)) & echo ticker done"]
```

1. Trigger `ticker`, and restart the web node once about ten ticks are in the build's log, so that the task is still running when the web node comes back.
2. Run any few builds under process isolation on Server 2022, then `Get-HnsNamespace` on the worker, and look for `destroy.failed` in the worker's log.
3. Trigger `ticker`, then `Get-Process init | Stop-Process -Force` on the worker (under Hyper-V isolation: `Get-Process vmwp`), destroy the pipeline, and watch the worker's log. Do this on a worker that runs nothing else: it ends the init of every container, or every virtual machine on the host.
4. A task from an image whose `smss.exe` is not an executable: `crane append --platform windows/amd64` onto `nanoserver` a tar that holds a text file at `Windows/System32/smss.exe` (crane puts it under the layer's `Files`).
5. Run tasks from several image tags, destroy their pipelines, then `ctr --address \\.\pipe\containerd-concourse --namespace concourse images ls` on the worker, now and an hour later (with the fix: start the worker with `--containerd-image-retention=2m` and look again after a few minutes).
6. The task above with `container_limits: {cpu: 20000}`.
7. A task with an input that holds `ci\hello.cmd`, and `run: {path: <input>/ci/hello.cmd}`.
8. `fly watch -j <pipeline>/<job> | cat -v` for any Windows task.
9. Start a long task, then send a Ctrl+C event to the worker's console from another process (`FreeConsole`, `AttachConsole(<pid of the worker>)`, `SetConsoleCtrlHandler(NULL, TRUE)`, `GenerateConsoleCtrlEvent(CTRL_C_EVENT, 0)`), and look for the task's `containerd-shim-runhcs-v1` before and after.
10. `Stop-Process -Name concourse -Force`, start the worker again, then `Get-Process containerd` and the worker's error log.
11. A task that runs `curl.exe -v -m 6 telnet://<gateway of the containers' network>:<a port the host listens on> < nul`. The connect line shows the connection. Keep both: without `< nul` the telnet session kept the build running despite `-m 6`, and with it curl ended when its 6 seconds ran out.
12. The task of 11 in a step that says `hermetic: true`, with an address outside in place of the gateway (`telnet://1.1.1.1:80`).
