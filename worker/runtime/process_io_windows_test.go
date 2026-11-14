//go:build windows

package runtime

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/garden"
	winio "github.com/Microsoft/go-winio"
	"github.com/containerd/containerd/v2/pkg/cio"
)

// lockedBuffer is where a test's process output ends up.
type lockedBuffer struct {
	lock   sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.buffer.String()
}

// shimConnect connects to a pipe as the runhcs shim does for a process.
func shimConnect(t *testing.T, pipe string) net.Conn {
	t.Helper()

	timeout := 5 * time.Second
	conn, err := winio.DialPipe(pipe, &timeout)
	if err != nil {
		t.Fatalf("connect to %s: %v", pipe, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

func within(t *testing.T, what string, do func()) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		do()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// holdsStdin reports whether an IO has a connection to its process's stdin.
func holdsStdin(t *testing.T, pio cio.IO) bool {
	t.Helper()

	pipe, isPipe := pio.(*pipeIO)
	if !isPipe {
		t.Fatalf("expected a pipe IO, got %T", pio)
	}

	pipe.lock.Lock()
	defer pipe.lock.Unlock()

	return pipe.stdin != nil
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProcessIOCreatorFailsWithoutAnIO(t *testing.T) {
	stdio := containerdCIO(garden.ProcessIO{Stdout: io.Discard}, false)

	first, err := processIOCreator(t.Name(), stdio)("task")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })

	// the same pipes again, while the first IO holds them
	second, err := processIOCreator(t.Name(), stdio)("task")
	if err == nil {
		t.Fatal("expected the pipes to be taken")
	}
	if second != nil {
		t.Fatalf("expected no IO with the error, got %#v", second)
	}
}

func TestProcessIOCarriesStdio(t *testing.T) {
	stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
	stdio := containerdCIO(garden.ProcessIO{
		Stdin:  strings.NewReader("request"),
		Stdout: stdout,
		Stderr: stderr,
	}, false)

	pio, err := processIOCreator(t.Name(), stdio)("task")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pio.Close() })

	pipes := pio.Config()
	if !strings.Contains(pipes.Stdout, t.Name()+"-task") {
		t.Errorf("expected the pipes to be named after the container and the process, got %s", pipes.Stdout)
	}

	// the shim reads stdin up to the end that the worker signals by closing the pipe
	stdin := shimConnect(t, pipes.Stdin)
	within(t, "the end of stdin", func() {
		request, err := io.ReadAll(stdin)
		if err != nil || string(request) != "request" {
			t.Errorf("unexpected stdin %q: %v", request, err)
		}
	})

	// a stdin that has ended is not there for an attach to take over
	eventually(t, "the IO to let go of the stdin that ended", func() bool { return !holdsStdin(t, pio) })

	out, errOut := shimConnect(t, pipes.Stdout), shimConnect(t, pipes.Stderr)
	_, _ = out.Write([]byte("to stdout"))
	_, _ = errOut.Write([]byte("to stderr"))

	// the process exits: the shim closes its ends, and the worker waits for
	// the output still on its way, as Process.Wait does
	_ = out.Close()
	_ = errOut.Close()
	within(t, "the output to be copied", func() {
		pio.Cancel()
		pio.Wait()
	})

	if stdout.String() != "to stdout" || stderr.String() != "to stderr" {
		t.Errorf("unexpected output: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestProcessIOWithATerminalHasNoStderrPipe(t *testing.T) {
	stdio := containerdCIO(garden.ProcessIO{Stdout: io.Discard, Stderr: io.Discard}, true)

	pio, err := processIOCreator(t.Name(), stdio)("task")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pio.Close() })

	if pipes := pio.Config(); !pipes.Terminal || pipes.Stdout == "" || pipes.Stderr != "" {
		t.Errorf("unexpected pipes: %+v", pipes)
	}
}

func TestProcessIOStopsWaitingForAStreamThatNeverConnects(t *testing.T) {
	stdio := containerdCIO(garden.ProcessIO{Stdout: io.Discard, Stderr: io.Discard}, false)

	pio, err := processIOCreator(t.Name(), stdio)("task")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pio.Close() })

	// the shim only connects to stdout; without Cancel, Wait would wait for
	// stderr forever
	out := shimConnect(t, pio.Config().Stdout)
	_ = out.Close()

	within(t, "the IO to stop waiting", func() {
		pio.Cancel()
		pio.Wait()
	})
}

// What makes attaching to a running process possible: once the first IO is
// closed its pipes can be listened on again, and the shim connects to the
// new listener when writing to its closed connection fails.
func TestProcessIOAttachTakesOverTheOutputOfARunningProcess(t *testing.T) {
	firstOut := &lockedBuffer{}
	ioManager := NewIOManager()

	first, err := ioManager.Creator(t.Name(), "task", processIOCreator(t.Name(), containerdCIO(garden.ProcessIO{
		Stdout: firstOut,
		Stderr: io.Discard,
	}, false)))("task")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	pipes := first.Config()

	out, errOut := shimConnect(t, pipes.Stdout), shimConnect(t, pipes.Stderr)
	_, _ = out.Write([]byte("before"))
	eventually(t, "the first output", func() bool { return firstOut.String() == "before" })

	// the web node comes back and attaches to the process; the shim hands
	// the process's pipes back
	secondOut := &lockedBuffer{}
	second, err := ioManager.Attach(t.Name(), "task", processIOAttach(containerdCIO(garden.ProcessIO{
		Stdout: secondOut,
		Stderr: io.Discard,
	}, false)))(cio.NewFIFOSet(pipes, nil))
	if err != nil {
		t.Fatalf("attach to a process whose IO is still open: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	tracked, _ := ioManager.Get(t.Name(), "task")
	if tracked != second {
		t.Error("expected the attached IO to be the one tracked")
	}

	// the shim's write to the connection that was closed under it fails...
	eventually(t, "the shim to notice that its connection is closed", func() bool {
		_, err := out.Write([]byte("lost"))
		return err != nil
	})
	_ = errOut.Close()

	// ...and it connects to the pipe again, which reaches the new listener
	out = shimConnect(t, pipes.Stdout)
	_, _ = out.Write([]byte("after"))
	eventually(t, "the output after the attach", func() bool { return secondOut.String() == "after" })

	if strings.Contains(firstOut.String(), "after") {
		t.Errorf("the first IO should no longer receive output, got %q", firstOut.String())
	}
}

// stdinEnded reports whether the shim's end of a stdin pipe has seen the end
// of stdin, giving it a moment to arrive.
func stdinEnded(t *testing.T, stdin net.Conn) bool {
	t.Helper()

	if err := stdin.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	_, err := stdin.Read(make([]byte, 1))
	return err != nil && !errors.Is(err, winio.ErrTimeout)
}

// The shim ends the process's stdin when the pipe closes, and that ends a
// process that runs in a terminal, as every task does. A web node that goes
// away must not have that effect, nor the attach when it comes back.
func TestProcessIOKeepsStdinOpenWhenTheWebNodeGoesAwayAndComesBack(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	ioManager := NewIOManager()

	first, err := ioManager.Creator(t.Name(), "task", processIOCreator(t.Name(), containerdCIO(garden.ProcessIO{
		Stdin:  stdinReader,
		Stdout: io.Discard,
	}, true)))("task")
	if err != nil {
		t.Fatal(err)
	}
	pipes := first.Config()
	stdin, out := shimConnect(t, pipes.Stdin), shimConnect(t, pipes.Stdout)

	// what the garden server does to the stdin of a process when its
	// connection to the web node is lost
	_ = stdinWriter.CloseWithError(errors.New("Connection closed"))
	if stdinEnded(t, stdin) {
		t.Fatal("stdin ended when the web node went away")
	}

	secondOut := &lockedBuffer{}
	second, err := ioManager.Attach(t.Name(), "task", processIOAttach(containerdCIO(garden.ProcessIO{
		Stdin:  strings.NewReader("ignored"),
		Stdout: secondOut,
	}, false)))(cio.NewFIFOSet(pipes, nil))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if stdinEnded(t, stdin) {
		t.Fatal("stdin ended when the web node attached again")
	}

	// output reaches the new streams, as in the test above
	eventually(t, "the shim to notice that its connection is closed", func() bool {
		_, err := out.Write([]byte("lost"))
		return err != nil
	})
	out = shimConnect(t, pipes.Stdout)
	_, _ = out.Write([]byte("after"))
	eventually(t, "the output after the attach", func() bool { return secondOut.String() == "after" })

	// the process exits and its IO is closed: only now does stdin end
	_ = second.Close()
	if !stdinEnded(t, stdin) {
		t.Fatal("stdin still open after the IO was closed")
	}
}

// A process without a terminal isn't ended by the end of its stdin, and may
// wait for it: a resource script reads its request up to there. So its stdin
// ends with the web node that fed it, as it does on Linux.
func TestProcessIOEndsTheStdinOfAProcessWithoutATerminalWhenTheWebNodeGoesAway(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()

	pio, err := processIOCreator(t.Name(), containerdCIO(garden.ProcessIO{
		Stdin:  stdinReader,
		Stdout: io.Discard,
	}, false))("resource")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pio.Close() })

	stdin := shimConnect(t, pio.Config().Stdin)

	go func() {
		_, _ = stdinWriter.Write([]byte("request"))
		_ = stdinWriter.CloseWithError(errors.New("Connection closed"))
	}()

	within(t, "the end of stdin", func() {
		request, err := io.ReadAll(stdin)
		if err != nil || string(request) != "request" {
			t.Errorf("unexpected stdin %q: %v", request, err)
		}
	})
}

// A failed attach has already closed the IO that was there. Something has to
// listen on the pipes again, or the shim holds the process at its next write.
func TestProcessIOAttachThatFailsLeavesThePreviousStreamsListening(t *testing.T) {
	firstOut := &lockedBuffer{}
	ioManager := NewIOManager()

	first, err := ioManager.Creator(t.Name(), "task", processIOCreator(t.Name(), containerdCIO(garden.ProcessIO{
		Stdout: firstOut,
	}, false)))("task")
	if err != nil {
		t.Fatal(err)
	}
	pipes := first.Config()
	out := shimConnect(t, pipes.Stdout)

	failed := errors.New("attach-failed")
	attached, err := ioManager.Attach(t.Name(), "task", func(*cio.FIFOSet) (cio.IO, error) {
		return nil, failed
	})(cio.NewFIFOSet(pipes, nil))
	if !errors.Is(err, failed) || attached != nil {
		t.Fatalf("expected the attach to fail without an IO, got %#v, %v", attached, err)
	}

	restored, tracked := ioManager.Get(t.Name(), "task")
	if !tracked || restored == first {
		t.Fatal("expected a new IO for the previous streams to be tracked")
	}
	t.Cleanup(func() { _ = restored.Close() })

	eventually(t, "the shim to notice that its connection is closed", func() bool {
		_, err := out.Write([]byte("lost"))
		return err != nil
	})

	out = shimConnect(t, pipes.Stdout)
	_, _ = out.Write([]byte("after"))
	eventually(t, "the output after the failed attach", func() bool { return strings.HasSuffix(firstOut.String(), "after") })
}

// The IO from before a failed attach may still be feeding the process's
// stdin. What listens in its place must not take that input away.
func TestProcessIOAttachThatFailsLeavesStdinToWhoeverFeedsIt(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	t.Cleanup(func() { _ = stdinWriter.Close() })
	ioManager := NewIOManager()

	first, err := ioManager.Creator(t.Name(), "task", processIOCreator(t.Name(), containerdCIO(garden.ProcessIO{
		Stdin:  stdinReader,
		Stdout: io.Discard,
	}, true)))("task")
	if err != nil {
		t.Fatal(err)
	}
	pipes := first.Config()
	stdin := shimConnect(t, pipes.Stdin)
	eventually(t, "the IO to take the connection to stdin", func() bool { return holdsStdin(t, first) })

	failed := errors.New("attach-failed")
	_, err = ioManager.Attach(t.Name(), "task", func(*cio.FIFOSet) (cio.IO, error) {
		return nil, failed
	})(cio.NewFIFOSet(pipes, nil))
	if !errors.Is(err, failed) {
		t.Fatalf("expected the attach to fail, got %v", err)
	}

	restored, tracked := ioManager.Get(t.Name(), "task")
	if !tracked {
		t.Fatal("expected an IO for the previous streams to be tracked")
	}
	t.Cleanup(func() { _ = restored.Close() })

	const keys = 20
	go func() {
		for range keys {
			_, _ = stdinWriter.Write([]byte("k"))
		}
	}()

	within(t, "the input after the failed attach", func() {
		_, err := io.ReadFull(stdin, make([]byte, keys))
		if err != nil {
			t.Errorf("expected all of the input to reach the process: %v", err)
		}
	})
}

func TestProcessIOWaitGivesUpOnAShimThatKeepsItsPipesOpen(t *testing.T) {
	gracePeriod := outputGracePeriod
	outputGracePeriod = 100 * time.Millisecond
	t.Cleanup(func() { outputGracePeriod = gracePeriod })

	pio, err := processIOCreator(t.Name(), containerdCIO(garden.ProcessIO{Stdout: io.Discard}, false))("task")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pio.Close() })

	// connected, and never closed: what containerd finds when the shim
	// fails to start a process after it connected to the pipes
	shimConnect(t, pio.Config().Stdout)

	within(t, "the IO to give up", func() {
		pio.Cancel()
		pio.Wait()
	})
}

func TestIOManagerDeleteFreesThePipesOfADestroyedContainer(t *testing.T) {
	stdio := containerdCIO(garden.ProcessIO{Stdout: io.Discard}, false)
	ioManager := NewIOManager()

	_, err := ioManager.Creator(t.Name(), "task", processIOCreator(t.Name(), stdio))("task")
	if err != nil {
		t.Fatal(err)
	}

	ioManager.Delete(t.Name())

	again, err := processIOCreator(t.Name(), stdio)("task")
	if err != nil {
		t.Fatalf("expected the pipes of the destroyed container to be free: %v", err)
	}
	_ = again.Close()
}

func TestProcessIOAttachFailsWithoutAnIO(t *testing.T) {
	attached, err := processIOAttach(containerdCIO(garden.ProcessIO{Stdout: io.Discard}, false))(nil)
	if err == nil {
		t.Fatal("expected an error without pipes to attach to")
	}
	if attached != nil {
		t.Fatalf("expected no IO with the error, got %#v", attached)
	}
}

func TestProcessIOCloseIsSafeToRepeat(t *testing.T) {
	pio, err := processIOCreator(t.Name(), containerdCIO(garden.ProcessIO{Stdout: io.Discard}, false))("task")
	if err != nil {
		t.Fatal(err)
	}

	out := shimConnect(t, pio.Config().Stdout)
	defer out.Close()

	// Process.Wait and an attach can both close the IO of one process
	if err := errors.Join(pio.Close(), pio.Close()); err != nil {
		t.Errorf("unexpected error closing twice: %v", err)
	}
	within(t, "the copies to end", pio.Wait)
}
