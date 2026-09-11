package receiver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/paths"
	"github.com/k0nsta/agterm-remote/internal/unixsock"
)

const maxEventLine = 64 << 10

// Listener receives events for one remote host. Each host has its own socket
// because the forwarded socket itself supplies the host identity.
//
// The Listener never unlinks its socket path — not on Close, not when Serve
// returns (see unixsock.ListenClean). Only its owner (the daemon's host
// lifecycle) knows whether the path still belongs to this listener or to a
// successor that bound it after this one was closed, so only the owner removes
// it. A late Serve exit therefore cannot unlink a successor's socket.
type Listener struct {
	host     string
	path     string
	sink     StatusSink
	resolver Resolver
	liveness Liveness

	lastEvent atomic.Int64
	mu        sync.Mutex
	ln        net.Listener
	conns     map[net.Conn]struct{}
}

// NewListener constructs a listener for host at dirs.Recv(hostKey).
func NewListener(host, hostKey string, dirs paths.Dirs, sink StatusSink, resolver Resolver, liveness Liveness) *Listener {
	return &Listener{
		host:     host,
		path:     dirs.Recv(hostKey),
		sink:     sink,
		resolver: resolver,
		liveness: liveness,
		conns:    make(map[net.Conn]struct{}),
	}
}

// Bind creates the listening socket and returns any bind failure to the
// caller. Serve calls it too, so it stays optional — but a caller that reports
// a host as started needs the failure synchronously: binding inside the Serve
// goroutine meant an unusable socket path (one over the 104-byte sun_path
// limit, say) only reached a log line while `agr up` reported success and the
// SSH tunnel ran on with nothing to receive events.
func (l *Listener) Bind() error {
	if l == nil {
		return errors.New("nil receiver listener")
	}
	_, err := l.bind()
	return err
}

// bind is idempotent: a listener already bound returns its existing socket, so
// Serve after Bind does not re-listen.
func (l *Listener) bind() (net.Listener, error) {
	l.mu.Lock()
	if l.ln != nil {
		existing := l.ln
		l.mu.Unlock()
		return existing, nil
	}
	l.mu.Unlock()

	listener, err := unixsock.ListenClean(l.path)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.ln = listener
	l.mu.Unlock()
	return listener, nil
}

// Serve listens until ctx is canceled or the socket fails. A socket already
// created by Bind is reused rather than re-listened.
func (l *Listener) Serve(ctx context.Context) error {
	if l == nil {
		return errors.New("nil receiver listener")
	}
	if ctx == nil {
		return errors.New("nil receiver context")
	}
	listener, err := l.bind()
	if err != nil {
		return err
	}
	defer l.closeResources()

	ctxDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			l.closeResources()
		case <-ctxDone:
		}
	}()
	defer close(ctxDone)

	var wg sync.WaitGroup
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			wg.Wait()
			return fmt.Errorf("accept receiver connection: %w", acceptErr)
		}
		l.addConn(conn)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer l.removeConn(conn)
			l.serveConn(ctx, conn)
		}()
	}
}

// LastEvent returns the time at which the most recent valid event arrived.
// The zero time means that no valid event has been received.
func (l *Listener) LastEvent() time.Time {
	if l == nil {
		return time.Time{}
	}
	nanos := l.lastEvent.Load()
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

func (l *Listener) serveConn(ctx context.Context, conn net.Conn) {
	reader := bufio.NewReader(conn)
	for {
		line, oversized, err := readEventLine(reader)
		if oversized {
			log.Printf("warning: skipping oversized receiver event from %s", l.host)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				log.Printf("warning: receiver read from %s: %v", l.host, err)
			}
			return
		}
		if oversized {
			continue
		}

		event, decodeErr := Decode(l.host, line)
		if decodeErr != nil {
			log.Printf("warning: receiver event from %s: %v", l.host, decodeErr)
			continue
		}
		l.lastEvent.Store(time.Now().UnixNano())
		if l.liveness != nil {
			l.liveness.MarkAlive()
		}
		l.route(ctx, event)
	}
}

func (l *Listener) route(ctx context.Context, event Event) {
	target := event.SessionID
	args := statusArgs(event)
	if event.Session != "" {
		if l.resolver == nil {
			log.Printf("warning: no binding resolver for remote session %q on %s", event.Session, l.host)
			return
		}
		binding, ok := l.resolver.ByHostName(l.host, event.Session)
		if !ok {
			log.Printf("warning: no binding for remote session %q on %s", event.Session, l.host)
			return
		}
		target = binding.Row
		args.Pane = binding.Pane
		args.PaneID = binding.PaneID
	}
	if l.sink == nil {
		log.Printf("warning: no status sink for receiver event target %q", target)
		return
	}
	if err := l.sink.Status(ctx, target, args); err != nil {
		log.Printf("warning: status push for receiver event target %q: %v", target, err)
	}
}

func statusArgs(event Event) agterm.StatusArgs {
	args := agterm.StatusArgs{Status: event.State, Pane: event.Pane, PaneID: event.PaneID}
	if event.Blink {
		value := true
		args.Blink = &value
	}
	if event.AutoReset {
		value := true
		args.AutoReset = &value
	}
	return args
}

func (l *Listener) addConn(conn net.Conn) {
	l.mu.Lock()
	l.conns[conn] = struct{}{}
	l.mu.Unlock()
}

func (l *Listener) removeConn(conn net.Conn) {
	l.mu.Lock()
	delete(l.conns, conn)
	l.mu.Unlock()
	_ = conn.Close()
}

func (l *Listener) closeResources() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ln != nil {
		_ = l.ln.Close()
	}
	for conn := range l.conns {
		_ = conn.Close()
	}
}

// readEventLine reads one complete line while retaining the connection after
// an oversized line. ReadSlice is used instead of Scanner so overflow can be
// discarded through the next newline and subsequent events remain usable.
func readEventLine(reader *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		part, err := reader.ReadSlice('\n')
		if !oversized {
			if len(line)+len(part) > maxEventLine {
				oversized = true
				line = nil
			} else {
				line = append(line, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 && !oversized {
				return line, false, nil
			}
			return nil, oversized, err
		}
		return line, oversized, nil
	}
}
