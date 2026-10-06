package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/k0nsta/agterm-remote/internal/remote"
)

// SetupDependencies are the collaborators of Setup.
type SetupDependencies struct {
	// Config locates and reloads agterm's files; nil or failing means agterm
	// is not reachable, and the default config directory under Home is used.
	Config AgtermConfig
	// Agr is the absolute path of this binary, written into the config lines.
	Agr  string
	Home string
	Out  io.Writer
	// EndKey is the agterm chord for the End remote session command (--end-key);
	// empty keeps the key an existing command has.
	EndKey string
	// AskKey asks for that chord when no command runs agr end yet; nil (not a
	// terminal) adds the command without a key.
	AskKey func() (string, error)
}

const (
	endName    = `"End remote session"`
	hooksNote  = "# agr setup: on a row bound with `agr open`, ⌘D opens <name>-2 and ⌘J a shell on its host"
	keymapNote = "# agr setup: end a row's remote work (confirm, kill its sessions, close the row)"
)

// Setup wires agterm to agr on this Mac: the pane.split and pane.scratch
// hooks that make ⌘D and ⌘J follow a remote row, and the keymap command that
// runs agr end. Lines that already run an agr have only the binary's path
// replaced, so a rebound key or a second run changes nothing else; missing
// lines are appended. agterm then reloads both files.
func Setup(ctx context.Context, deps SetupDependencies) error {
	if !filepath.IsAbs(deps.Agr) {
		return fmt.Errorf("cannot locate the agr binary (%q)", deps.Agr)
	}
	// A quote would not survive the config words' re-reading on the next run.
	if !remote.Printable(deps.Agr) || strings.ContainsAny(deps.Agr, `'"`) {
		return fmt.Errorf("unsupported agr path %q: move the binary to a path without quotes", deps.Agr)
	}
	out := deps.Out
	if out == nil {
		out = io.Discard
	}
	reachable := deps.Config != nil
	var hooksPath, keymapPath string
	if reachable {
		var err error
		hooksPath, keymapPath, err = deps.Config.ConfigPaths(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(out, "agterm is not reachable (%v); using the default config directory\n", err)
			reachable = false
		}
	}
	if !reachable {
		if deps.Home == "" {
			return errors.New("cannot locate agterm's config directory")
		}
		dir := filepath.Join(deps.Home, ".config", "agterm")
		hooksPath, keymapPath = filepath.Join(dir, "hooks.conf"), filepath.Join(dir, "keymap.conf")
	}
	key, err := endKey(keymapPath, deps)
	if err != nil {
		return err
	}
	agr := remote.QuoteRemoteCommand(deps.Agr)
	for _, file := range []struct {
		path    string
		rewrite func(string) string
	}{
		{hooksPath, func(content string) string { return wireHooks(content, agr) }},
		{keymapPath, func(content string) string { return wireKeymap(content, agr, key) }},
	} {
		changed, err := rewriteFile(file.path, file.rewrite)
		if err != nil {
			return err
		}
		state := "unchanged"
		if changed {
			state = "updated"
		}
		_, _ = fmt.Fprintf(out, "%-9s %s\n", state, file.path)
	}
	if key == "" && !keymapHasKey(keymapPath) {
		_, _ = fmt.Fprintln(out, "End remote session has no key: run it from agterm's command palette, or bind one with `agr setup --end-key 'ctrl+a>x'`")
	}
	if !reachable {
		_, _ = fmt.Fprintln(out, "start agterm, or run `agtermctl hooks reload` and `agtermctl keymap reload`, to apply")
		return nil
	}
	var problems []string
	for _, file := range []string{"hooks", "keymap"} {
		count, err := deps.Config.Reload(ctx, file)
		if err != nil {
			return err
		}
		if count > 0 {
			problems = append(problems, fmt.Sprintf("%d in %s.conf (agtermctl %s list)", count, file, file))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("agterm reports problems: %s", strings.Join(problems, ", "))
	}
	_, _ = fmt.Fprintln(out, "agterm reloaded: ⌘D and ⌘J follow a remote row; End remote session runs agr end")
	return nil
}

// RunSetup adapts Setup to agr's integer-exit CLI convention.
func RunSetup(ctx context.Context, deps SetupDependencies, errw io.Writer) int {
	if err := Setup(ctx, deps); err != nil {
		if errw != nil {
			_, _ = fmt.Fprintf(errw, "agr: setup: %v\n", err)
		}
		return 1
	}
	return 0
}

// confToken is one word of an agterm config line: a quoted span or a run of
// non-space characters, the same split agterm's comment stripper respects.
var confToken = regexp.MustCompile(`'[^']*'|"[^"]*"|\S+`)

// isAgr reports whether a config word names an agr binary, however it is
// spelled: bare, $HOME-relative, absolute or single-quoted.
func isAgr(word string) bool {
	return path.Base(strings.Trim(word, "'")) == "agr"
}

// wireHooks points every `on pane.split|pane.scratch <agr> on-split|on-scratch`
// line at agr, drops a repeat of one (agterm reports an identical line as a
// problem), and appends whichever of the two is missing.
func wireHooks(content, agr string) string {
	lines := splitLines(content)
	seen := map[string]bool{}
	kept := lines[:0]
	for _, line := range lines {
		spans := confToken.FindAllStringIndex(line, -1)
		if len(spans) >= 4 && word(line, spans[0]) == "on" && isAgr(word(line, spans[2])) &&
			isHookPair(word(line, spans[1]), word(line, spans[3])) &&
			(len(spans) == 4 || strings.HasPrefix(word(line, spans[4]), "#")) {
			kind := word(line, spans[1])
			if seen[kind] {
				continue
			}
			seen[kind] = true
			line = line[:spans[2][0]] + agr + line[spans[2][1]:]
		}
		kept = append(kept, line)
	}
	var missing []string
	for _, kind := range []string{"split", "scratch"} {
		if !seen["pane."+kind] {
			missing = append(missing, fmt.Sprintf("on %-14s %s on-%s", "pane."+kind, agr, kind))
		}
	}
	return joinLines(kept, hooksNote, missing)
}

func isHookPair(kind, verb string) bool {
	return (kind == "pane.split" && verb == "on-split") || (kind == "pane.scratch" && verb == "on-scratch")
}

// endKey is the chord to bind: --end-key when given, else an answer to AskKey
// when no command runs agr end yet. Empty keeps an existing key, or adds the
// command without one.
func endKey(keymapPath string, deps SetupDependencies) (string, error) {
	key := strings.TrimSpace(deps.EndKey)
	if key == "" && deps.AskKey != nil && endCommandLine(splitLines(readConf(keymapPath))) < 0 {
		answer, err := deps.AskKey()
		if err != nil {
			return "", err
		}
		key = strings.TrimSpace(answer)
	}
	if key != "" && (strings.HasPrefix(key, "-") || strings.ContainsAny(key, " \t'\"#") || !remote.Printable(key)) {
		return "", fmt.Errorf("invalid key %q: use an agterm chord such as ctrl+a>x", key)
	}
	return key, nil
}

// wireKeymap points the custom command that runs `<agr> end` at agr, keeping
// its name and options, and binds it to key when one is given; with no such
// command it appends End remote session, bound to key or to nothing.
func wireKeymap(content, agr, key string) string {
	lines := splitLines(content)
	i := endCommandLine(lines)
	if i < 0 {
		line := "command " + endName
		if key != "" {
			line += " " + key
		}
		return joinLines(lines, keymapNote, []string{fmt.Sprintf(`%s --error-hud %s end "{AGT_SESSION_ID}"`, line, agr)})
	}
	line := lines[i]
	spans := confToken.FindAllStringIndex(line, -1)
	at := agrWord(line, spans)
	line = line[:spans[at][0]] + agr + line[spans[at][1]:]
	if key != "" {
		// The chord, when there is one, is the word after the name.
		if at > 2 && !strings.HasPrefix(word(line, spans[2]), "-") {
			line = line[:spans[2][0]] + key + line[spans[2][1]:]
		} else {
			line = line[:spans[1][1]] + " " + key + line[spans[1][1]:]
		}
	}
	lines[i] = line
	return joinLines(lines, "", nil)
}

// endCommandLine is the index of the first custom command running agr end,
// or -1.
func endCommandLine(lines []string) int {
	for i, line := range lines {
		if agrWord(line, confToken.FindAllStringIndex(line, -1)) > 0 {
			return i
		}
	}
	return -1
}

// agrWord is the index of the `<agr>` word followed by `end` on a command
// line, or -1.
func agrWord(line string, spans [][]int) int {
	if len(spans) < 4 || word(line, spans[0]) != "command" {
		return -1
	}
	for j := len(spans) - 2; j > 1; j-- {
		if isAgr(word(line, spans[j])) && word(line, spans[j+1]) == "end" {
			return j
		}
	}
	return -1
}

// keymapHasKey reports whether the agr end command in the file has a chord.
func keymapHasKey(name string) bool {
	lines := splitLines(readConf(name))
	i := endCommandLine(lines)
	if i < 0 {
		return false
	}
	spans := confToken.FindAllStringIndex(lines[i], -1)
	return agrWord(lines[i], spans) > 2 && !strings.HasPrefix(word(lines[i], spans[2]), "-")
}

// readConf reads a config file, empty when it is missing or unreadable.
func readConf(name string) string {
	content, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(content)
}

func word(line string, span []int) string { return line[span[0]:span[1]] }

// splitLines splits content into lines without the final newline's empty tail.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

// joinLines rejoins lines and appends added under a note, separated from any
// existing content by a blank line.
func joinLines(lines []string, note string, added []string) string {
	if len(added) > 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(append(lines, note), added...)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// rewriteFile applies rewrite to the file's content and replaces it atomically
// when the result differs. A symlinked file (a dotfiles checkout) is written
// through to its target, so the link survives.
func rewriteFile(name string, rewrite func(string) string) (bool, error) {
	if target, err := filepath.EvalSymlinks(name); err == nil {
		name = target
	}
	mode := fs.FileMode(0o644)
	content, err := os.ReadFile(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return false, err
		}
	case err != nil:
		return false, err
	default:
		if info, err := os.Stat(name); err == nil {
			mode = info.Mode().Perm()
		}
	}
	updated := rewrite(string(content))
	if updated == string(content) {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(updated); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), name); err != nil {
		return false, err
	}
	return true, nil
}
