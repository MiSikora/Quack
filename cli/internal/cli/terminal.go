package cli

import (
	"io"
	"os"

	"golang.org/x/term"
)

// Terminal is where quack talks to the user.
type Terminal struct {
	Out io.Writer
	Err io.Writer
	// TUI is whether quack without an action opens the TUI rather than printing its help.
	TUI bool
}

// StdTerminal is the process's own terminal.
func StdTerminal() Terminal {
	return Terminal{
		Out: os.Stdout,
		Err: os.Stderr,
		TUI: opensTUI(isTerminal(os.Stdin) && isTerminal(os.Stdout), os.Getenv),
	}
}

// opensTUI is whether a person sits at the terminal: stdin and stdout are one, no AI agent says it runs quack with
// AI_AGENT, and no CI system runs it. An agent that runs commands in a terminal of its own could not use the TUI and
// would wait on it.
func opensTUI(terminal bool, getenv func(string) string) bool {
	ci := getenv("CI")
	return terminal && getenv("AI_AGENT") == "" && (ci == "" || ci == "false" || ci == "0")
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
