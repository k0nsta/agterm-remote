package daemon

import (
	"context"
	"net"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
)

var (
	agtermRecoveryInterval = 5 * time.Second
	eventRetryInterval     = 100 * time.Millisecond
)

func (d *Daemon) startEvents(ctx context.Context) {
	if d.events == nil {
		return
	}
	d.children.Add(1)
	go func() {
		defer d.children.Done()
		d.eventLoop(ctx)
	}()
}

func (d *Daemon) eventLoop(ctx context.Context) {
	for {
		rows, err := d.events.ClosedRows(ctx)
		if err != nil {
			if isConnectionRefused(err) {
				if done := d.beginAgtermRecovery(); done != nil {
					select {
					case <-done:
					case <-ctx.Done():
						return
					}
				}
			} else {
				if !waitForEventRetry(ctx) {
					return
				}
			}
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if rows == nil {
			if !waitForEventRetry(ctx) {
				return
			}
			continue
		}
		d.mu.Lock()
		reset := d.eventReset
		d.mu.Unlock()
		resetRequested := false
		streamClosed := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-reset:
				resetRequested = true
			case row, ok := <-rows:
				if !ok {
					streamClosed = true
				} else {
					d.handleClosedRow(row)
				}
			}
			if resetRequested || streamClosed {
				break
			}
		}
		if !resetRequested && streamClosed {
			if !waitForEventRetry(ctx) {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func waitForEventRetry(ctx context.Context) bool {
	timer := time.NewTimer(eventRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (d *Daemon) handleClosedRow(row string) {
	if row == "" {
		return
	}
	// Both panes of a split row go with it, and they may be bound to
	// different hosts: each host's bridge stops only when nothing else is
	// bound to it.
	bound := d.store.ForRow(row)
	if len(bound) == 0 {
		return
	}
	if err := d.store.UnbindRow(row); err != nil {
		d.logf("warning: unbind closed agterm row %q: %v", row, err)
		return
	}
	checked := make(map[string]struct{}, len(bound))
	for _, binding := range bound {
		if _, ok := checked[binding.Host]; ok {
			continue
		}
		checked[binding.Host] = struct{}{}
		if len(d.store.ForHost(binding.Host)) == 0 {
			d.stopHost(binding.Host)
		}
	}
}

func (d *Daemon) beginAgtermRecovery() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started || d.closing || d.ctx == nil {
		return nil
	}
	if d.recoveryRunning {
		return d.recoveryDone
	}
	d.recoveryRunning = true
	d.recoveryDone = make(chan struct{})
	if !d.recoveryLogged {
		d.logf("warning: agterm unavailable (connection refused); waiting for it to return")
		d.recoveryLogged = true
	}
	d.children.Add(1)
	done := d.recoveryDone
	ctx := d.ctx
	go func() {
		defer d.children.Done()
		d.recoverAgterm(ctx, done)
	}()
	return done
}

func (d *Daemon) recoverAgterm(ctx context.Context, done chan struct{}) {
	defer func() {
		d.mu.Lock()
		d.recoveryRunning = false
		d.recoveryLogged = false
		close(done)
		d.mu.Unlock()
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		if agtermSocketAvailable() && d.handshake(ctx) {
			d.resetEvents()
			d.resyncAll(ctx)
			return
		}
		timer := time.NewTimer(agtermRecoveryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (d *Daemon) resetEvents() {
	d.mu.Lock()
	old := d.eventReset
	d.eventReset = make(chan struct{})
	d.mu.Unlock()
	close(old)
}

func agtermSocketAvailable() bool {
	path := agterm.SocketPath()
	if path == "" {
		return false
	}
	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
