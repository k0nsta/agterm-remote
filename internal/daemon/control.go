package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"time"

	"github.com/k0nsta/agterm-remote/internal/token"
)

const maxControlLine = 1 << 20

type controlRequest struct {
	Op   string `json:"op"`
	Host string `json:"host,omitempty"`
}

type controlResponse struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// HostStatus is the daemon's current view of one remote host.
type HostStatus struct {
	Host      string `json:"host"`
	State     string `json:"state"`
	Since     string `json:"since,omitempty"`
	Attempts  int    `json:"attempts"`
	LastEvent string `json:"last_event,omitempty"`
}

func (d *Daemon) startControl(ctx context.Context) error {
	listener, err := ListenClean(d.dirs.Sock())
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.control = listener
	d.mu.Unlock()
	d.children.Add(1)
	go func() {
		defer d.children.Done()
		d.serveControl(ctx, listener)
	}()
	return nil
}

func (d *Daemon) serveControl(ctx context.Context, listener net.Listener) {
	closeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-closeDone:
		}
	}()
	defer close(closeDone)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				d.logf("warning: daemon control listener stopped: %v", err)
			}
			return
		}
		d.children.Add(1)
		go func() {
			defer d.children.Done()
			d.serveControlConn(ctx, conn)
		}()
	}
}

func (d *Daemon) serveControlConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > maxControlLine {
			_ = writeControlResponse(conn, controlResponse{Error: "control request exceeds 1 MiB"})
			return
		}
		if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				d.logf("warning: read daemon control request: %v", err)
			}
			return
		}
		if len(line) > 0 {
			var request controlRequest
			if decodeErr := json.Unmarshal(bytesTrimSpace(line), &request); decodeErr != nil {
				if writeErr := writeControlResponse(conn, controlResponse{Error: fmt.Sprintf("malformed control request: %v", decodeErr)}); writeErr != nil {
					return
				}
			} else {
				response := d.handleControl(ctx, request)
				if writeErr := writeControlResponse(conn, response); writeErr != nil {
					return
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return
		}
	}
}

func writeControlResponse(writer io.Writer, response controlResponse) error {
	if !response.OK && response.Error == "" {
		response.Error = "control operation failed"
	}
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = writer.Write(data)
	return err
}

func (d *Daemon) handleControl(ctx context.Context, request controlRequest) controlResponse {
	switch request.Op {
	case "up":
		if err := validControlHost(request.Host); err != nil {
			return controlError(err)
		}
		if err := d.startHost(ctx, request.Host); err != nil {
			return controlError(err)
		}
		return controlOK(d.hostStatus(request.Host))
	case "down":
		if err := validControlHost(request.Host); err != nil {
			return controlError(err)
		}
		d.stopHost(request.Host)
		return controlOK(d.hostStatus(request.Host))
	case "status":
		if request.Host == "" {
			return controlOK(d.hostStatuses())
		}
		if err := validControlHost(request.Host); err != nil {
			return controlError(err)
		}
		return controlOK(d.hostStatus(request.Host))
	case "reload-bindings":
		if err := d.reloadBindings(ctx); err != nil {
			return controlError(err)
		}
		return controlOK(d.hostStatuses())
	default:
		return controlError(fmt.Errorf("unknown control operation %q", request.Op))
	}
}

func validControlHost(host string) error {
	if !token.ValidHost(host) {
		return fmt.Errorf("invalid host %q", host)
	}
	return nil
}

func controlOK(result any) controlResponse {
	return controlResponse{OK: true, Result: result}
}

func controlError(err error) controlResponse {
	return controlResponse{OK: false, Error: err.Error()}
}

func (d *Daemon) reloadBindings(ctx context.Context) error {
	bound, err := d.store.Load()
	if err != nil {
		return err
	}
	desired := make(map[string]struct{}, len(bound))
	for _, binding := range bound {
		if !token.ValidHost(binding.Host) {
			return fmt.Errorf("invalid host %q in bindings", binding.Host)
		}
		desired[binding.Host] = struct{}{}
	}
	d.mu.Lock()
	running := make([]string, 0, len(d.hosts))
	for host := range d.hosts {
		running = append(running, host)
	}
	d.mu.Unlock()
	for _, host := range running {
		if _, ok := desired[host]; !ok {
			d.stopHost(host)
		}
	}
	for host := range desired {
		if err := d.startHost(ctx, host); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) hostStatus(host string) HostStatus {
	d.mu.Lock()
	runtime, ok := d.hosts[host]
	if !ok {
		d.mu.Unlock()
		return HostStatus{Host: host, State: "down"}
	}
	status := HostStatus{
		Host:     runtime.host,
		State:    runtime.state,
		Attempts: runtime.attempts,
	}
	if !runtime.since.IsZero() {
		status.Since = runtime.since.UTC().Format(time.RFC3339Nano)
	}
	listener := runtime.listener
	d.mu.Unlock()
	if listener != nil {
		if last := listener.LastEvent(); !last.IsZero() {
			status.LastEvent = last.UTC().Format(time.RFC3339Nano)
		}
	}
	return status
}

func (d *Daemon) hostStatuses() []HostStatus {
	d.mu.Lock()
	hosts := make([]string, 0, len(d.hosts))
	for host := range d.hosts {
		hosts = append(hosts, host)
	}
	d.mu.Unlock()
	sort.Strings(hosts)
	result := make([]HostStatus, 0, len(hosts))
	for _, host := range hosts {
		result = append(result, d.hostStatus(host))
	}
	return result
}

func bytesTrimSpace(value []byte) []byte {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\t' || value[0] == '\r' || value[0] == '\n') {
		value = value[1:]
	}
	for len(value) > 0 {
		last := value[len(value)-1]
		if last != ' ' && last != '\t' && last != '\r' && last != '\n' {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}
