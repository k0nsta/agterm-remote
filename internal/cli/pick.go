package cli

import (
	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/bindings"
	"github.com/k0nsta/agterm-remote/internal/remote"
)

// ItemsFor builds the native picker entries for one host's remote sessions.
// The state component is the agterm row state (bound, stale, or -), matching
// the table's ROW column; the remote agent level remains in the session data
// and is shown by ls's STATE column.
func ItemsFor(host string, sessions []remote.Session, bound []bindings.Binding, tree []string) []agterm.PickItem {
	liveSet := make(map[string]struct{}, len(tree))
	for _, row := range tree {
		liveSet[row] = struct{}{}
	}
	treeAvailable := tree != nil
	items := make([]agterm.PickItem, 0, len(sessions))
	for _, session := range sessions {
		state := "-"
		for _, binding := range bound {
			// Name alone is ambiguous: the same session name exists on many
			// hosts, so matching without the host shows another host's row
			// state against this host's session.
			if binding.Name != session.Name || binding.Host != host {
				continue
			}
			if !treeAvailable {
				state = "-"
			} else if _, ok := liveSet[binding.Row]; ok {
				state = "bound"
			} else {
				state = "stale"
			}
			break
		}
		command := session.Cmds
		if command == "" {
			command = "-"
		}
		items = append(items, agterm.PickItem{
			ID:       session.Name,
			Label:    session.Name,
			Subtitle: command + " · " + state + " · " + HumanizeSecs(session.IdleSecs),
		})
	}
	return items
}
