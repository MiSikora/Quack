package cli

import (
	"io"
	"os"

	"golang.org/x/term"
)

type Terminal struct {
	Out io.Writer
	Err io.Writer
	TUI bool
}

func StdTerminal() Terminal {
	return Terminal{
		Out: os.Stdout,
		Err: os.Stderr,
		TUI: opensTUI(isTerminal(os.Stdin) && isTerminal(os.Stdout), os.Getenv),
	}
}

// An agent that runs commands in a terminal of its own could not use the TUI and would wait on it.
func opensTUI(terminal bool, getenv func(string) string) bool {
	ci := getenv("CI")
	return terminal && getenv("AI_AGENT") == "" && (ci == "" || ci == "false" || ci == "0")
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
