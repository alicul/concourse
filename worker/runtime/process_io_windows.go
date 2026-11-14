//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	winio "github.com/Microsoft/go-winio"
	"github.com/containerd/containerd/v2/pkg/cio"
)

// pipeIO is the worker's end of the named pipes that carry a process's stdio
// to and from the runhcs shim. The worker listens on the pipes and the shim
// connects to them.
//
// It takes the place of containerd's own IO for Windows, which cannot be
// attached to again:
//
//   - containerd's IO leaves the connections that the shim made open until the
//     process exits. A pipe's name stays taken for as long as any handle to it
//     is open, so nothing can listen on it again. pipeIO closes the
//     connections of stdout and stderr. The shim notices on its next write,
//     and from then on keeps trying to connect to the pipe again.
//   - The shim connects to stdin only once, and ends the process's stdin when
//     that connection closes, which ends a process that runs in a terminal.
//     So for such a process pipeIO keeps the stdin connection open when
//     whoever fed it goes away without ending it, and hands it over to the IO
//     that attaches next.
type pipeIO struct {
	config  cio.Config
	streams *cio.Streams

	lock      sync.Mutex
	cancelled bool
	closed    bool
	listeners []net.Listener
	stdin     net.Conn
	outputs   []net.Conn

	// the waits for the shim to connect that have not ended yet
	accepting sync.WaitGroup

	// the copies of stdout and stderr that are still running
	output sync.WaitGroup
}

var _ cio.IO = (*pipeIO)(nil)

// newPipeIO listens on the pipes named in config, and copies between the
// connections made to them and the streams. A pipe without a name is left out.
func newPipeIO(config cio.Config, streams *cio.Streams) (_ *pipeIO, err error) {
	pio := &pipeIO{config: config, streams: streams}
	defer func() {
		if err != nil {
			_ = pio.Close()
		}
	}()

	if config.Stdin != "" {
		listener, err := pio.listen(config.Stdin)
		if err != nil {
			return nil, fmt.Errorf("create stdin pipe: %w", err)
		}

		pio.accepting.Add(1)
		go func() {
			conn, ok := pio.accept(listener, true)
			pio.accepting.Done()
			if !ok {
				return
			}

			_, err := io.Copy(conn, streams.Stdin)
			if err != nil && config.Terminal {
				// Whoever fed stdin is gone without ending it, as when the
				// web node restarts. A process that runs in a terminal is
				// to carry on, so its stdin stays open until the IO is
				// closed. Any other process just finds its stdin ended.
				return
			}

			// closing the pipe is what tells the shim that stdin has ended
			_ = conn.Close()

			pio.lock.Lock()
			if pio.stdin == conn {
				pio.stdin = nil
			}
			pio.lock.Unlock()

			closeListener(listener)
		}()
	}

	err = pio.listenForOutput()
	if err != nil {
		return nil, err
	}

	return pio, nil
}

// attachPipeIO takes over the stdio of a running process from the IO that was
// there before: it listens on the output pipes named in config again, and
// keeps the connection to the process's stdin, if there is one. Nothing feeds
// that connection any more, because the shim reads stdin from the connection
// it made when the process started and from no other.
func attachPipeIO(config cio.Config, streams *cio.Streams, stdin net.Conn) (_ *pipeIO, err error) {
	pio := &pipeIO{config: config, streams: streams, stdin: stdin}
	defer func() {
		if err != nil {
			// the stdin of the process is not this IO's to close yet
			pio.stdin = nil
			_ = pio.Close()
		}
	}()

	err = pio.listenForOutput()
	if err != nil {
		return nil, err
	}

	if streams.Stdin != nil {
		// so that whoever writes to it isn't held up
		go func() { _, _ = io.Copy(io.Discard, streams.Stdin) }()
	}

	return pio, nil
}

// listenForOutput listens on the stdout and stderr pipes, and copies what the
// shim sends over them to the streams.
func (pio *pipeIO) listenForOutput() error {
	for _, out := range []struct {
		name   string
		pipe   string
		stream io.Writer
	}{
		{"stdout", pio.config.Stdout, pio.streams.Stdout},
		{"stderr", pio.config.Stderr, pio.streams.Stderr},
	} {
		if out.pipe == "" {
			continue
		}

		listener, err := pio.listen(out.pipe)
		if err != nil {
			return fmt.Errorf("create %s pipe: %w", out.name, err)
		}

		pio.accepting.Add(1)
		pio.output.Add(1)
		go func() {
			defer pio.output.Done()

			conn, ok := pio.accept(listener, false)
			pio.accepting.Done()
			if !ok {
				return
			}

			_, _ = io.Copy(out.stream, conn)

			_ = conn.Close()
			closeListener(listener)
		}()
	}

	return nil
}

// listen starts listening on a pipe, to be closed with the IO.
func (pio *pipeIO) listen(pipe string) (net.Listener, error) {
	listener, err := winio.ListenPipe(pipe, nil)
	if err != nil {
		return nil, err
	}

	pio.lock.Lock()
	pio.listeners = append(pio.listeners, listener)
	pio.lock.Unlock()

	return listener, nil
}

// accept waits for the shim to connect to a pipe. The connection is closed
// with the IO. It reports false when the IO was cancelled or closed first.
func (pio *pipeIO) accept(listener net.Listener, stdin bool) (net.Conn, bool) {
	for {
		conn, err := listener.Accept()

		pio.lock.Lock()
		switch {
		case err != nil && (pio.cancelled || errors.Is(err, winio.ErrPipeListenerClosed)):
			pio.lock.Unlock()
			return nil, false

		case err != nil:
			// Windows can abort the wait for a connection while the pipe is
			// still listened on, for one when something else tries to
			// create the same pipe. The shim has yet to connect.
			pio.lock.Unlock()
			time.Sleep(acceptRetryDelay)
			continue

		case pio.closed:
			pio.lock.Unlock()
			_ = conn.Close()
			return nil, false
		}

		if stdin {
			pio.stdin = conn
		} else {
			pio.outputs = append(pio.outputs, conn)
		}
		pio.lock.Unlock()

		return conn, true
	}
}

// acceptRetryDelay keeps accept from spinning should a pipe fail every wait.
const acceptRetryDelay = 10 * time.Millisecond

// Config returns the pipes of the IO.
func (pio *pipeIO) Config() cio.Config {
	return pio.config
}

// Cancel stops waiting for the shim to connect. Streams that are connected
// carry on until the shim closes them, which it does when the process exits.
func (pio *pipeIO) Cancel() {
	pio.lock.Lock()
	pio.cancelled = true
	listeners := pio.listeners
	pio.lock.Unlock()

	for _, listener := range listeners {
		closeListener(listener)
	}
}

// Wait blocks until all of the process's output has been copied. It doesn't
// wait for stdin, which only ends when whoever feeds it stops.
//
// The shim closes its end of the pipes before it reports that the process
// has exited, so by the time anyone waits after a Cancel there is only what
// is left in the pipes to copy. Should the shim keep a pipe open all the
// same, as for a process that it failed to start, Wait stops the copying
// rather than wait forever.
func (pio *pipeIO) Wait() {
	copied := make(chan struct{})
	go func() {
		pio.output.Wait()
		close(copied)
	}()

	pio.lock.Lock()
	cancelled := pio.cancelled
	pio.lock.Unlock()

	if !cancelled {
		<-copied
		return
	}

	select {
	case <-copied:
	case <-time.After(outputGracePeriod):
		_ = pio.Close()
		<-copied
	}
}

// outputGracePeriod is how long Wait gives the output left in the pipes to be
// copied once the IO is cancelled.
var outputGracePeriod = 10 * time.Second

// Close stops all copying and frees the pipes' names. It ends the stdin of a
// process that still runs.
func (pio *pipeIO) Close() error {
	stdin, err := pio.release()
	if stdin != nil {
		err = errors.Join(err, closeConn(stdin))
	}

	return err
}

// release stops the copying of stdout and stderr and frees the names of
// their pipes, and gives up the connection to the process's stdin without
// closing it, for the caller to close or to hand to another IO. It returns
// no connection when the shim never connected to stdin, or stdin has ended.
func (pio *pipeIO) release() (net.Conn, error) {
	pio.lock.Lock()
	pio.cancelled, pio.closed = true, true
	listeners := pio.listeners
	pio.lock.Unlock()

	for _, listener := range listeners {
		closeListener(listener)
	}

	// a connection made just before the listeners closed is on its way into
	// the IO, or being closed by accept: either way its pipe stays taken
	// until that is done
	pio.accepting.Wait()

	pio.lock.Lock()
	stdin, outputs := pio.stdin, pio.outputs
	pio.listeners, pio.stdin, pio.outputs = nil, nil, nil
	pio.lock.Unlock()

	var errs []error
	for _, conn := range outputs {
		errs = append(errs, closeConn(conn))
	}

	return stdin, errors.Join(errs...)
}

// closeConn closes a connection that a copy may have closed already.
func closeConn(conn net.Conn) error {
	err := conn.Close()
	if errors.Is(err, winio.ErrFileClosed) {
		return nil
	}

	return err
}

// closeListener closes a pipe listener, and returns once the listener has let
// go of its pipe.
//
// go-winio's Close sends the listener one request to stop. A wait for a
// connection takes that request, and when the wait ends with an error that
// go-winio doesn't expect (Windows aborting it, see accept), the listener
// carries on and Close waits for it forever. Closing again sends the
// listener another request.
func closeListener(listener net.Listener) {
	closed := make(chan struct{})
	go func() {
		_ = listener.Close()
		close(closed)
	}()

	for {
		select {
		case <-closed:
			return
		case <-time.After(closeRetryDelay):
			go func() { _ = listener.Close() }()
		}
	}
}

// closeRetryDelay is how long closeListener gives go-winio to close a
// listener, which normally takes no time, before it asks again.
const closeRetryDelay = 100 * time.Millisecond

// processStreams gathers what a process's stdio is to be connected to.
func processStreams(opts []cio.Opt) *cio.Streams {
	streams := &cio.Streams{}
	for _, opt := range opts {
		opt(streams)
	}

	return streams
}

// processPipes leaves out the pipes of the streams that nothing is connected to.
func processPipes(pipes cio.Config, streams *cio.Streams) cio.Config {
	if streams.Stdin == nil {
		pipes.Stdin = ""
	}
	if streams.Stdout == nil {
		pipes.Stdout = ""
	}
	if streams.Stderr == nil {
		pipes.Stderr = ""
	}

	return pipes
}

// processIOCreator names a process's stdio pipes after its container as well
// as its ID. containerd names them after the ID alone and pipe names are
// global on Windows, so two containers that each run a process with the same
// ID, as every task step does, would otherwise claim the same pipes.
func processIOCreator(handle string, opts []cio.Opt) cio.Creator {
	streams := processStreams(opts)

	return func(id string) (cio.IO, error) {
		pipes, err := cio.NewFIFOSetInDir("", handle+"-"+id, streams.Terminal)
		if err != nil {
			return nil, err
		}

		pio, err := newPipeIO(processPipes(pipes.Config, streams), streams)
		if err != nil {
			return nil, err
		}

		return pio, nil
	}
}

// attachedIO is what processIOAttach makes of the pipes of a running process:
// the streams to connect them to. replaceIO does the connecting, because only
// it has the IO that was there before.
type attachedIO struct {
	config  cio.Config
	streams *cio.Streams
}

var _ cio.IO = attachedIO{}

func (a attachedIO) Config() cio.Config { return a.config }
func (attachedIO) Cancel()              {}
func (attachedIO) Wait()                {}
func (attachedIO) Close() error         { return nil }

// processIOAttach prepares to listen again on the output pipes of a running
// process, which the shim reports back.
func processIOAttach(opts []cio.Opt) cio.Attach {
	streams := processStreams(opts)

	return func(pipes *cio.FIFOSet) (cio.IO, error) {
		if pipes == nil {
			return nil, errors.New("cannot attach, missing pipes")
		}

		return attachedIO{config: processPipes(pipes.Config, streams), streams: streams}, nil
	}
}

// replaceIO stops a process's previous IO before it attaches the new one,
// because the new one listens on the same output pipes, and keeps the
// process's stdin open across the two. It returns the IO that listens on the
// process's pipes from now on.
//
// When attaching fails nothing listens on the pipes any more, and the shim
// would hold the process at its next write until something does. So the
// streams of the previous IO get the pipes back.
func replaceIO(previous cio.IO, attach func() (cio.IO, error)) (cio.IO, error) {
	var (
		stdin        net.Conn
		previousPipe *pipeIO
	)

	switch p := previous.(type) {
	case *pipeIO:
		previousPipe = p
		stdin, _ = p.release()
	case nil:
	default:
		p.Cancel()
		_ = p.Close()
	}

	newIO, err := attach()
	if err == nil {
		if attached, ok := newIO.(attachedIO); ok {
			newIO, err = attachPipeIO(attached.config, attached.streams, stdin)
		}
	}
	if err == nil {
		return newIO, nil
	}

	if previousPipe != nil {
		// without its stdin, which the previous IO may still be feeding to
		// the process
		streams := *previousPipe.streams
		streams.Stdin = nil

		restored, restoreErr := attachPipeIO(previousPipe.config, &streams, stdin)
		if restoreErr == nil {
			return restored, err
		}
	}

	if stdin != nil {
		_ = closeConn(stdin)
	}

	return nil, err
}

// forgetIO closes the IO of a container that is being destroyed. An IO that
// still waits for the shim to connect, as one attached to a process that has
// not written since does, would otherwise wait for good.
func forgetIO(io cio.IO) {
	io.Cancel()
	_ = io.Close()
}
