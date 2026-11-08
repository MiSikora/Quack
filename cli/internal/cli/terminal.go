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
	// Prompts is whether a person sits at the terminal on stdin and stdout, which the interactive TUI needs.
	Prompts bool
}

// StdTerminal is the process's own terminal.
func StdTerminal() Terminal {
	return Terminal{
		Out:     os.Stdout,
		Err:     os.Stderr,
		Prompts: isTerminal(os.Stdin) && isTerminal(os.Stdout),
	}
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
