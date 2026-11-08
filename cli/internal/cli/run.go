// Package cli is the quack command: it parses the command line, runs one request, and turns
// what happened into output and an exit code.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/wire"
)

// Run runs quack with args, the command line without the program name, and returns its exit code.
func Run(args []string, terminal Terminal) int {
	opts, err := parseOptions(args, terminal)
	if err == nil {
		err = run(opts, terminal)
	}
	if err == nil {
		return exitOK
	}
	code, message, exit := failure(err)
	if opts.json {
		printJSON(terminal.Out, outcomeJSON{Code: code, Message: message})
	} else {
		fmt.Fprintln(terminal.Err, "quack:", err)
	}
	return exit
}

type options struct {
	device       string
	pkg          string
	json         bool
	listActions  bool
	listDevices  bool
	startTimeout time.Duration
	// help asks for usage, which holds quack's own help.
	help   bool
	usage  string
	action string
	// filter keeps the actions whose id it matches when listActions has a pattern, and is nil otherwise.
	filter *regexp.Regexp
	args   map[string]wire.RawArg
}

func parseOptions(argv []string, terminal Terminal) (options, error) {
	var opts options
	fs := pflag.NewFlagSet("quack", pflag.ContinueOnError)
	// Parse errors print once, through Run.
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.pkg, "app", "", "the app, by its `package` (default: the only quack-enabled app on the device)")
	fs.StringVar(&opts.device, "device", "", "the device, by its `serial` as adb devices lists it (default: $ANDROID_SERIAL)")
	boolFlag(fs, &opts.json, "json", "print JSON lines: each event, then the outcome with its code")
	boolFlag(fs, &opts.listActions, "list-actions", "list the app's actions, or only those whose id matches regex")
	boolFlag(fs, &opts.listDevices, "list-devices", "list the devices and the quack-enabled apps on each")
	fs.DurationVar(&opts.startTimeout, "start-timeout", 15*time.Second, "how long to wait for the app to answer")
	boolFlag(fs, &opts.help, "help", "show this help")
	opts.usage = "usage: quack [options] <action> [key=value]...\n" +
		"       quack [options] --list-actions [regex]\n" +
		"       quack [options] --list-devices\n\n" +
		"options:\n" + fs.FlagUsages()
	if err := fs.Parse(argv); err != nil {
		return opts, usageErrorf("%v. See quack --help.", err)
	}
	if opts.help {
		return opts, nil
	}
	if opts.device == "" {
		opts.device = os.Getenv("ANDROID_SERIAL")
	}
	if opts.pkg != "" && !packagePattern.MatchString(opts.pkg) {
		return opts, usageErrorf("%q is not a package name", opts.pkg)
	}
	rest := fs.Args()
	switch {
	case opts.listDevices:
		if len(rest) > 0 || opts.listActions {
			return opts, usageErrorf("--list-devices takes no action or --list-actions")
		}
	case opts.listActions:
		if len(rest) > 1 {
			return opts, usageErrorf("--list-actions takes at most one regex")
		}
		if len(rest) == 1 {
			filter, err := regexp.Compile(rest[0])
			if err != nil {
				return opts, usageErrorf("invalid --list-actions pattern: %v", err)
			}
			opts.filter = filter
		}
	case len(rest) == 0:
		fmt.Fprint(terminal.Err, opts.usage)
		return opts, usageErrorf("missing action")
	default:
		args, err := parseArgs(rest[1:])
		if err != nil {
			return opts, err
		}
		opts.action, opts.args = rest[0], args
	}
	return opts, nil
}

func run(opts options, terminal Terminal) error {
	if opts.help {
		fmt.Fprint(terminal.Out, opts.usage)
		return nil
	}
	if opts.listDevices {
		return printDevices(terminal.Out, adb.Device(opts.device), opts.pkg, opts.json)
	}
	target, err := client.Find(adb.Device(opts.device), opts.pkg)
	if err != nil {
		return err
	}
	app, err := client.Resolve(client.Options{
		Target:       target,
		Mode:         client.Wake,
		StartTimeout: opts.startTimeout,
		Status:       terminal.Err,
	})
	if err != nil {
		return err
	}
	if opts.listActions {
		hello, actions, err := app.Describe(context.Background())
		if err != nil {
			return err
		}
		if actions, err = matching(actions, opts.filter); err != nil {
			return err
		}
		if opts.json {
			printActionsJSON(terminal.Out, actions)
		} else {
			printHelp(terminal.Out, hello, actions, opts.filter == nil)
		}
		return nil
	}
	result, err := app.Call(context.Background(), wire.Call{Action: opts.action, Args: opts.args}, printEvents(terminal, opts.json))
	if err != nil {
		return err
	}
	if opts.json {
		printJSON(terminal.Out, outcomeJSON{Code: codeOK, Message: result.Message, Data: result.Data})
		return nil
	}
	fmt.Fprintln(terminal.Out, result.Message)
	if result.Data != nil {
		fmt.Fprintln(terminal.Out, result.IndentedData())
	}
	return nil
}

// boolFlag declares an on/off flag that sets *p. pflag prints [=true|false] after every bool flag in help, and
// nothing after a BoolFunc flag, which parses the same.
func boolFlag(fs *pflag.FlagSet, p *bool, name, usage string) {
	fs.BoolFunc(name, usage, func(value string) error {
		on, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("must be true or false")
		}
		*p = on
		return nil
	})
}

// parseArgs groups key=value words by key. A repeated key keeps its values in command-line order.
func parseArgs(words []string) (map[string]wire.RawArg, error) {
	args := map[string]wire.RawArg{}
	for _, word := range words {
		key, value, ok := strings.Cut(word, "=")
		if !ok {
			return nil, usageErrorf("argument %q is not key=value", word)
		}
		args[key] = append(args[key], value)
	}
	return args, nil
}

// printEvents prints the message of each event, or with json each event as an eventJSON.
func printEvents(terminal Terminal, json bool) client.EventFunc {
	return func(message string) {
		if json {
			printJSON(terminal.Out, eventJSON{message})
		} else {
			fmt.Fprintln(terminal.Out, message)
		}
	}
}

// usageError is a command line that quack cannot run as written.
type usageError struct {
	message string
}

func (e *usageError) Error() string { return e.message }

func usageErrorf(format string, args ...any) error {
	return &usageError{fmt.Sprintf(format, args...)}
}

const (
	exitOK     = 0
	exitAction = 1
	exitUsage  = 2
	exitDevice = 3
)

// errorExits maps the app's error codes to exit codes. A code missing here exits with exitAction.
var errorExits = map[wire.ErrorCode]int{
	wire.ErrorCodeBadFrame:      exitUsage,  // the request is malformed
	wire.ErrorCodeUnknownAction: exitUsage,  // no action has that id
	wire.ErrorCodeInvalidArgs:   exitUsage,  // an argument is missing, unknown, or of the wrong type
	wire.ErrorCodeRejected:      exitAction, // the action refused the request
	wire.ErrorCodeFailed:        exitAction, // the action crashed
}

// packagePattern matches Android package names. Packages go into shell commands, so nothing else may pass.
var packagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
