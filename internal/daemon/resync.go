package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/bridge"
	"github.com/k0nsta/agterm-remote/internal/remote"
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
	hostKey := runtime.hostKey
	d.mu.Unlock()
	if previous == string(bridge.StateUp) {
		d.markLeftUp(hostKey)
	}
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
	live := make(map[string]remote.Session, len(sessions))
	for _, session := range sessions {
		live[session.Name] = session
	}
	leftUp, known := d.leftUp(d.hostKey(host))
	now := time.Now()
	for _, binding := range bindingsForHost {
		session, ok := live[binding.Name]
		state := session.State
		if !ok || state == "" {
			continue
		}
		// A finish from before the bridge went down was shown then, and
		// visiting the row cleared it; replaying it would turn it green again.
		if state == "completed" && known && !finishedSince(now, session.IdleSecs, leftUp) {
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

// leftUpSlack widens the replay window: a finish relayed in the last moments
// before the bridge dropped may never have arrived.
const leftUpSlack = 10 * time.Second

// finishedSince reports whether a state set idleSecs ago (by the host's own
// clock, so skew cancels out) is newer than leftUp. An unknown age replays.
func finishedSince(now time.Time, idleSecs int, leftUp time.Time) bool {
	if idleSecs < 0 {
		return true
	}
	return !now.Add(-time.Duration(idleSecs) * time.Second).Before(leftUp.Add(-leftUpSlack))
}

func (d *Daemon) hostKey(host string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if runtime, ok := d.hosts[host]; ok {
		return runtime.hostKey
	}
	return ""
}

// markLeftUp records that hostKey's bridge stopped being up now. It is kept
// on disk so a restarted daemon still knows which finishes it already showed.
func (d *Daemon) markLeftUp(hostKey string) {
	if hostKey == "" {
		return
	}
	path := d.dirs.HostLeftUp(hostKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		d.logf("warning: record bridge down for %s: %v", hostKey, err)
		return
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(path, []byte(stamp+"\n"), 0o600); err != nil {
		d.logf("warning: record bridge down for %s: %v", hostKey, err)
	}
}

// leftUp reads markLeftUp's record. Without one (first start, or a daemon
// that was killed) every completed state replays, as before.
func (d *Daemon) leftUp(hostKey string) (time.Time, bool) {
	if hostKey == "" {
		return time.Time{}, false
	}
	data, err := os.ReadFile(d.dirs.HostLeftUp(hostKey))
	if err != nil {
		return time.Time{}, false
	}
	stamp, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}, false
	}
	return stamp, true
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
