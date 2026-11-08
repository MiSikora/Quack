// Command quack talks to a quack-enabled Android app over adb.
package main

import (
	"os"

	"quack/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.StdTerminal()))
}
