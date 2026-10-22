package proxy

import (
	"fmt"
	"slices"
	"strings"
)

// optRole says how an option's value is treated by Authorize.
type optRole uint8

const (
	// rolePlain values pass through to agtermctl unchanged.
	rolePlain optRole = iota
	// roleRow is a row id (--target): a full id or a self marker.
	roleRow
	// rolePane is a pane id (--pane-id).
	rolePane
	// roleSide is a pane side (--pane): left, right, primary or split.
	roleSide
	// roleFile is a body file reference, written @<i>.
	roleFile
	// roleCommand is a command line that is rewritten before it runs.
	roleCommand
)

// optSpec is one option of a leaf's schema. An option that is not in the
// schema is refused.
type optSpec struct {
	name string
	// value is true when the option takes a value, false for a flag.
	value bool
	role  optRole
	// repeat allows the option more than once.
	repeat bool
	// allowed limits a plain value to these strings when set.
	allowed []string
}

type parsedOpt struct {
	spec  optSpec
	value string
}

// parsedArgs is the argv of one leaf after the subcommand path.
type parsedArgs struct {
	opts []parsedOpt
	pos  []string
	// help is set by --help; every other argument is then ignored.
	help bool
}

// universalOpts are accepted by every leaf. --window and --socket are
// stripped (the daemon adds the real socket); --json only formats output.
var universalOpts = []optSpec{
	{name: "window", value: true},
	{name: "socket", value: true},
	{name: "json"},
}

func isStripped(name string) bool { return name == "window" || name == "socket" }

// parseArgs parses args with agtermctl's grammar against the leaf's schema:
// `--opt value`, `--opt=value`, options after positionals, `--` ending the
// options. Anything ambiguous is refused: an unknown option, a repeat of a
// non-repeatable option, a value that looks like an option (write it as
// --opt=value), a short option.
func parseArgs(leaf *leafSpec, args []string) (parsedArgs, error) {
	var out parsedArgs
	seen := make(map[string]bool)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.ContainsRune(arg, 0) {
			return parsedArgs{}, fmt.Errorf("argument %d contains a NUL byte", i+1)
		}
		if arg == "--" {
			for _, rest := range args[i+1:] {
				if strings.ContainsRune(rest, 0) {
					return parsedArgs{}, fmt.Errorf("an argument contains a NUL byte")
				}
			}
			out.pos = append(out.pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			out.pos = append(out.pos, arg)
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			return parsedArgs{}, fmt.Errorf("short option %q is not allowed (use the long form)", arg)
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		if name == "help" && !hasValue {
			out.help = true
			continue
		}
		spec, ok := leaf.option(name)
		if !ok {
			return parsedArgs{}, unknownOption(leaf, name)
		}
		if spec.value {
			if !hasValue {
				if i+1 >= len(args) {
					return parsedArgs{}, fmt.Errorf("option --%s needs a value", name)
				}
				i++
				value = args[i]
				if strings.HasPrefix(value, "-") && value != "-" {
					return parsedArgs{}, fmt.Errorf("value %q of --%s looks like an option; write --%s=%s", value, name, name, value)
				}
			}
		} else if hasValue {
			return parsedArgs{}, fmt.Errorf("option --%s takes no value", name)
		}
		if strings.ContainsRune(value, 0) {
			return parsedArgs{}, fmt.Errorf("the value of --%s contains a NUL byte", name)
		}
		if seen[name] && !spec.repeat {
			return parsedArgs{}, fmt.Errorf("option --%s given more than once", name)
		}
		seen[name] = true
		if len(spec.allowed) > 0 && !slices.Contains(spec.allowed, value) {
			return parsedArgs{}, fmt.Errorf("--%s: %q is not allowed (one of %s)", name, value, strings.Join(spec.allowed, ", "))
		}
		if isStripped(name) {
			continue
		}
		out.opts = append(out.opts, parsedOpt{spec: spec, value: value})
	}
	if out.help {
		return parsedArgs{help: true}, nil
	}
	return out, nil
}

// refusedOptions name why an option that is in no schema is refused.
var refusedOptions = map[string]string{
	"persistent": "keeps a browser login on the Mac",
	"browse":     "opens a browser on the Mac",
	"file":       "reads a file on the Mac",
	"select":     "selects text on the Mac",
	"follow":     "moves the user's selection",
	"cwd":        "names a directory on the Mac",
	"navigation": "adds browser chrome to a page",
	"chromeless": "changes a page's chrome",
}

func unknownOption(leaf *leafSpec, name string) error {
	if why, ok := refusedOptions[name]; ok {
		return fmt.Errorf("option --%s is not allowed for `%s` from a host (%s)", name, leaf.name(), why)
	}
	return fmt.Errorf("option --%s is not allowed for `%s` from a host", name, leaf.name())
}
