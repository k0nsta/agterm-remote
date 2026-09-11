package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// List loads remote sessions, persistent row bindings, and the live agterm
// tree, then renders the user-facing ls table.
func List(ctx context.Context, host string, sessions Sessions, store BindingStore, rows Rows) (string, error) {
	if !token.ValidHost(host) {
		return "", fmt.Errorf("invalid remote host %q", host)
	}
	if sessions == nil {
		return "", errors.New("session client is unavailable")
	}
	if store == nil {
		return "", errors.New("binding store is unavailable")
	}
	if ctx == nil {
		return "", errors.New("nil list context")
	}
	items, err := sessions.Sessions(ctx, host)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return fmt.Sprintf("no agr sessions on '%s'\n", host), nil
	}
	bound, err := store.Load()
	if err != nil {
		return "", err
	}
	var live []string
	treeAvailable := false
	if rows != nil {
		live, err = rows.Tree(ctx)
		if err == nil {
			treeAvailable = true
		}
	}
	return RenderSessions(host, items, bound, live, treeAvailable), nil
}

// RenderSessions renders the stable agr ls table. When treeAvailable is
// false, every row column is '-' because the live agterm state is unknown.
func RenderSessions(host string, sessions []remote.Session, bound []bindings.Binding, live []string, treeAvailable bool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%-16s %-4s %-6s %-10s %-16s %s\n", "NAME", "ATT", "IDLE", "STATE", "CMD", "ROW")
	liveSet := make(map[string]struct{}, len(live))
	for _, row := range live {
		liveSet[row] = struct{}{}
	}
	for _, session := range sessions {
		attached := "-"
		if session.Attached > 0 {
			attached = fmt.Sprintf("%d", session.Attached)
		}
		state := session.State
		if state == "" {
			state = "-"
		}
		command := session.Cmds
		if command == "" {
			command = "-"
		}
		row := bindingState(host, session.Name, bound, liveSet, treeAvailable)
		fmt.Fprintf(&out, "%-16s %-4s %-6s %-10s %-16s %s\n", session.Name, attached, HumanizeSecs(session.IdleSecs), state, command, row)
	}

	if treeAvailable {
		dangling := make([]string, 0)
		for _, binding := range bound {
			if binding.Host != host {
				continue
			}
			if _, ok := liveSet[binding.Row]; !ok {
				dangling = append(dangling, binding.Row)
			}
		}
		if len(dangling) > 0 {
			fmt.Fprintf(&out, "rows without a session: %s\n", strings.Join(dangling, ", "))
		}
	}
	return out.String()
}

// HumanizeSecs formats the remote event age in the compact form used by ls
// and the picker. A negative value is the unknown sentinel.
func HumanizeSecs(seconds int) string {
	if seconds < 0 {
		return "-"
	}
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dd", seconds/86400)
	}
}

func bindingState(host, name string, bound []bindings.Binding, live map[string]struct{}, treeAvailable bool) string {
	for _, binding := range bound {
		if binding.Host != host || binding.Name != name {
			continue
		}
		if !treeAvailable {
			return "-"
		}
		if _, ok := live[binding.Row]; ok {
			return "bound"
		}
		return "stale"
	}
	return "-"
}
