package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
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
	if state != string(bridge.StateUp) && state != string(bridge.StateDown) {
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
	if previous == string(bridge.StateDown) && state == string(bridge.StateUp) {
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
	for _, binding := range d.store.ForHost(host) {
		if err := d.ui.HudOpen(ctx, binding.Row, message); err != nil {
			d.logf("warning: open reconnecting HUD for %q: %v", binding.Row, err)
		}
	}
}

func (d *Daemon) resyncHost(ctx context.Context, host string) {
	bindingsForHost := d.store.ForHost(host)
	if d.ui != nil {
		for _, binding := range bindingsForHost {
			if err := d.ui.HudClose(ctx, binding.Row); err != nil {
				d.logf("warning: close reconnecting HUD for %q: %v", binding.Row, err)
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
