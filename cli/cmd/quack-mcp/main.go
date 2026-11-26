package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"quack/internal/command"
	"quack/internal/mcpserver"
	"quack/internal/recipe"
)

func main() {
	fs := command.NewFlagSet("quack-mcp")
	var projectDir string
	command.ProjectDirFlag(fs, &projectDir)
	startTimeout := fs.Duration("start-timeout", 15*time.Second, "how long to wait for an app to answer")
	var showVersion, help bool
	command.BoolFlag(fs, &showVersion, "version", "", "show quack-mcp's version")
	command.BoolFlag(fs, &help, "help", "h", "show this help")
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
		fmt.Println("quack-mcp", command.Version)
	default:
		// A deleted working directory prevents recipe lookup, but must not prevent unrelated action tools.
		dir, _ := os.Getwd()
		location := recipe.Location{Dir: dir, ProjectDir: projectDir}
		// stdout carries the protocol. MCP clients log stderr.
		if err := mcpserver.Serve(context.Background(), command.Version, location, *startTimeout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "quack-mcp:", err)
			os.Exit(1)
		}
	}
}
