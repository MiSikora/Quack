package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/spf13/pflag"

	"quack/internal/mcpserver"
)

// Release builds set this with -ldflags "-X main.version=<version>".
var version = "dev"

func main() {
	fs := pflag.NewFlagSet("quack-mcp", pflag.ContinueOnError)
	// Keeps declaration order, as quack does: alphabetical, with help last.
	fs.SortFlags = false
	fs.SetOutput(io.Discard)
	startTimeout := fs.Duration("start-timeout", 15*time.Second, "how long to wait for an app to answer")
	var showVersion, help bool
	boolFlag(fs, &showVersion, "version", "", "show quack-mcp's version")
	boolFlag(fs, &help, "help", "h", "show this help")
	usage := "usage: quack-mcp [options]\n\n" +
		"Serves quack-enabled Android apps to an MCP client over stdin and stdout. Run it from the client, for example:\n" +
		"  claude mcp add quack -- quack-mcp\n\n" +
		"options:\n" + fs.FlagUsages()
	if err := fs.Parse(os.Args[1:]); err != nil || fs.NArg() > 0 {
		if err == nil {
			err = fmt.Errorf("unexpected argument %q", fs.Arg(0))
		}
		fmt.Fprintf(os.Stderr, "quack-mcp: %v. See quack-mcp --help.\n", err)
		os.Exit(2)
	}
	switch {
	case help:
		fmt.Print(usage)
	case showVersion:
		fmt.Println("quack-mcp", version)
	default:
		// stdout carries the protocol. MCP clients log stderr.
		if err := mcpserver.Serve(context.Background(), version, *startTimeout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "quack-mcp:", err)
			os.Exit(1)
		}
	}
}

// pflag prints [=true|false] after every bool flag in help, and nothing after a BoolFunc flag, which parses the same.
func boolFlag(fs *pflag.FlagSet, p *bool, name, shorthand, usage string) {
	fs.BoolFuncP(name, shorthand, usage, func(value string) error {
		on, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("must be true or false")
		}
		*p = on
		return nil
	})
}
