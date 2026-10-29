package cli

import (
	"fmt"
	"io"

	"github.com/k0nsta/agterm-remote/internal/skill"
)

// RunSkill prints the agr skill, for agents that read instructions from
// somewhere other than ~/.claude/skills.
func RunSkill(version string, out, errw io.Writer) int {
	if out == nil {
		out = io.Discard
	}
	if _, err := out.Write(skill.Bytes(version)); err != nil {
		if errw != nil {
			_, _ = fmt.Fprintf(errw, "agr: skill: %v\n", err)
		}
		return 1
	}
	return 0
}
