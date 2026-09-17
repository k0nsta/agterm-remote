package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/bridge"
)

func (d *Daemon) watchHostStates(ctx context.Context, host string, changes <-chan bridge.State) {
	for {
		select {
		case <-ctx.Done():
			return
		case state, ok := <-changes:
			if !ok {
				return
			}
			d.hostStateChanged(ctx, host, string(state))
		}
	}
}

func (d *Daemon) hostStateChanged(ctx context.Context, host, state string) {
	switch state {
	case string(bridge.StateUp), string(bridge.StateDown), string(bridge.StateConnecting):
	default:
		return
	}
	d.mu.Lock()
	runtime, ok := d.hosts[host]
	if !ok || runtime.state == state {
		d.mu.Unlock()
		return
	}
	previous := runtime.state
	runtime.state = state
	runtime.since = time.Now().UTC()
	d.mu.Unlock()
	// Connecting is reported (doctor shows it) but changes nothing else: the
	// resync waits for a proven bridge, and the reconnecting HUD marks the
	// loss of one that was up — not a start that has not been proven yet.
	if state == string(bridge.StateUp) && previous != string(bridge.StateUp) {
		d.resyncHost(ctx, host)
	}
	if previous == string(bridge.StateUp) && state == string(bridge.StateDown) {
		d.showReconnecting(ctx, host)
	}
}

func (d *Daemon) showReconnecting(ctx context.Context, host string) {
	if d.ui == nil {
		return
	}
	message := host + ": reconnecting…"
	for _, row := range boundRows(d.store.ForHost(host)) {
		if err := d.ui.HudOpen(ctx, row, message); err != nil {
			d.logf("warning: open reconnecting HUD for %q: %v", row, err)
		}
	}
}

// boundRows returns the distinct rows of bindings in order. The HUD is a row
// overlay, so a split row with both panes bound gets one, not two.
func boundRows(bound []bindings.Binding) []string {
	seen := make(map[string]struct{}, len(bound))
	rows := make([]string, 0, len(bound))
	for _, binding := range bound {
		if _, ok := seen[binding.Row]; ok {
			continue
		}
		seen[binding.Row] = struct{}{}
		rows = append(rows, binding.Row)
	}
	return rows
}

func (d *Daemon) resyncHost(ctx context.Context, host string) {
	bindingsForHost := d.store.ForHost(host)
	if d.ui != nil {
		for _, row := range boundRows(bindingsForHost) {
			if err := d.ui.HudClose(ctx, row); err != nil {
				d.logf("warning: close reconnecting HUD for %q: %v", row, err)
			}
		}
	}
	if d.remote == nil {
		return
	}
	sessions, err := d.remote.Sessions(ctx, host)
	if err != nil {
		d.logf("warning: resync sessions for %s: %v", host, err)
		return
	}
	live := make(map[string]string, len(sessions))
	for _, session := range sessions {
		live[session.Name] = session.State
	}
	for _, binding := range bindingsForHost {
		state, ok := live[binding.Name]
		if !ok || state == "" {
			continue
		}
		args := agterm.StatusArgs{Status: state, Pane: binding.Pane, PaneID: binding.PaneID}
		if state == "completed" {
			value := true
			args.AutoReset = &value
		}
		if err := d.Status(ctx, binding.Row, args); err != nil && !errors.Is(err, agterm.ErrUnknownTarget) {
			d.logf("warning: resync status for %q: %v", binding.Row, err)
		}
	}
}

func (d *Daemon) resyncAll(ctx context.Context) {
	d.mu.Lock()
	hosts := make([]string, 0, len(d.hosts))
	for host := range d.hosts {
		hosts = append(hosts, host)
	}
	d.mu.Unlock()
	for _, host := range hosts {
		d.mu.Lock()
		runtime, ok := d.hosts[host]
		state := ""
		if ok {
			state = runtime.state
		}
		d.mu.Unlock()
		if !ok {
			continue
		}
		if state == string(bridge.StateUp) {
			d.resyncHost(ctx, host)
		} else {
			d.showReconnecting(ctx, host)
		}
	}
}
