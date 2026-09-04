package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	hooksReadScript  = `p=$(readlink -f ~/.claude/settings.json 2>/dev/null || echo ~/.claude/settings.json); [ -e "$p" ] && cat "$p" || printf '{}'`
	hooksWriteScript = `p=$(readlink -f ~/.claude/settings.json 2>/dev/null || echo ~/.claude/settings.json); d=$(dirname "$p"); mkdir -p "$d"; if [ -e "$p" ]; then cp -p "$p" "$p.bak-agr"; fi; cat > "$p.tmp" && mv -f "$p.tmp" "$p"`
)

const agrHookPath = "$HOME/.local/bin/agr"

// MergeHooks adds or repairs the four Claude Code hooks while preserving
// unrelated settings and hooks. It performs no I/O; callers can refuse a
// malformed result before issuing the remote write command.
func MergeHooks(existing []byte) (merged []byte, changed bool, err error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		existing = []byte("{}")
	}

	var config map[string]any
	if err := json.Unmarshal(existing, &config); err != nil {
		return nil, false, fmt.Errorf("decode Claude Code settings: %w", err)
	}
	if config == nil {
		return nil, false, errors.New("claude code settings root must be an object")
	}

	hooks, ok := config["hooks"]
	if !ok || hooks == nil {
		hooks = map[string]any{}
		config["hooks"] = hooks
	}
	hookConfig, ok := hooks.(map[string]any)
	if !ok {
		return nil, false, errors.New("claude code settings hooks must be an object")
	}

	for _, wanted := range []struct {
		event string
		state string
		args  string
	}{
		{event: "UserPromptSubmit", state: "active", args: " --blink"},
		{event: "PostToolUse", state: "active", args: " --blink"},
		{event: "Notification", state: "blocked"},
		{event: "Stop", state: "completed", args: " --auto-reset"},
	} {
		wantCommand := agrHookPath + " status " + wanted.state + wanted.args
		var didChange bool
		didChange, err = mergeHookEvent(hookConfig, wanted.event, wanted.state, wantCommand)
		if err != nil {
			return nil, false, err
		}
		changed = changed || didChange
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, false, fmt.Errorf("encode Claude Code settings: %w", err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, encoded, "", "  "); err != nil {
		return nil, false, fmt.Errorf("indent Claude Code settings: %w", err)
	}
	formatted.WriteByte('\n')
	return formatted.Bytes(), changed, nil
}

func mergeHookEvent(config map[string]any, event, state, wantCommand string) (bool, error) {
	value, exists := config[event]
	if !exists || value == nil {
		config[event] = []any{canonicalHookEntry(event, wantCommand)}
		return true, nil
	}
	bucket, ok := value.([]any)
	if !ok {
		return false, fmt.Errorf("claude code hook %q must be an array", event)
	}

	found := false
	changed := false
	for _, entryValue := range bucket {
		entry, ok := entryValue.(map[string]any)
		if !ok {
			continue
		}
		hookValues, ok := entry["hooks"].([]any)
		if !ok {
			continue
		}
		for _, hookValue := range hookValues {
			hook, ok := hookValue.(map[string]any)
			if !ok {
				continue
			}
			command, ok := hook["command"].(string)
			if !ok || agrHookState(command) != state {
				continue
			}
			found = true
			if event == "Notification" {
				if matcher, ok := entry["matcher"].(string); !ok || matcher != "permission_prompt" {
					entry["matcher"] = "permission_prompt"
					changed = true
				}
			}
			if command != wantCommand {
				hook["command"] = wantCommand
				changed = true
			}
		}
	}
	if !found {
		bucket = append(bucket, canonicalHookEntry(event, wantCommand))
		config[event] = bucket
		changed = true
	}
	return changed, nil
}

func canonicalHookEntry(event, command string) map[string]any {
	entry := map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": command,
		}},
	}
	if event == "Notification" {
		entry["matcher"] = "permission_prompt"
	}
	return entry
}

func agrHookState(command string) string {
	parts := strings.Fields(command)
	for index, part := range parts {
		part = strings.Trim(part, "\"'")
		if part != "agr" && !strings.HasSuffix(part, "/agr") {
			continue
		}
		stateIndex := index + 1
		if stateIndex < len(parts) && parts[stateIndex] == "status" {
			stateIndex++
		}
		if stateIndex >= len(parts) {
			return ""
		}
		switch parts[stateIndex] {
		case "active", "blocked", "completed":
			return parts[stateIndex]
		}
	}
	return ""
}
