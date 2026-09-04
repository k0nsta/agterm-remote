package main

import (
	"fmt"
	"io"
)

var version = "dev"

const usage = "usage: agr <command> [args...]"

func run(args []string, out, errw io.Writer) int {
	if len(args) == 0 {
		writeUsage(out)
		return 0
	}

	if args[0] == "--version" {
		_, _ = fmt.Fprintln(out, version)
		return 0
	}

	writeUsage(errw)
	return 2
}

func writeUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, usage)
}
