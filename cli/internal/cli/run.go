// Package cli is the quack command: it parses the command line, runs one request or the interactive TUI, and turns
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
	"quack/internal/recipe"
	"quack/internal/tui"
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
		outcome := outcomeJSON{Code: code, Message: message}
		var step *stepError
		if errors.As(err, &step) {
			outcome.Step = step.step
		}
		printJSON(terminal.Out, outcome)
	} else {
		fmt.Fprintln(terminal.Err, "quack:", err)
	}
	return exit
}

type options struct {
	device      string
	pkg         string
	json        bool
	listActions bool
	// interactive opens the TUI, which quack without an action does when a person sits at the terminal.
	interactive bool
	listDevices bool
	listRecipes bool
	openApp     bool
	// recipe is the name of the recipe to run, or "" for none.
	recipe       string
	startTimeout time.Duration
	// help asks for usage, which holds quack's own help.
	help    bool
	version bool
	usage   string
	action  string
	// filter keeps the actions whose id, or the recipes whose name, it matches when listActions or listRecipes has a
	// pattern, and is nil otherwise.
	filter *regexp.Regexp
	args   map[string]wire.RawArg
}

func parseOptions(argv []string, terminal Terminal) (options, error) {
	var opts options
	fs := pflag.NewFlagSet("quack", pflag.ContinueOnError)
	// Help lists the flags as declared here: alphabetically, with help last.
	fs.SortFlags = false
	// Parse errors print once, through Run.
	fs.SetOutput(io.Discard)
	fs.StringVarP(&opts.pkg, "app", "a", "", "the app, by its `package` (default: the only quack-enabled app on the device)")
	fs.StringVarP(&opts.device, "device", "d", "", "the device, by its `serial` as adb devices lists it (default: $ANDROID_SERIAL)")
	boolFlag(fs, &opts.json, "json", "j", "print JSON lines: each notification, then the outcome with its code")
	boolFlag(fs, &opts.listActions, "list-actions", "l", "list the app's actions, or only those whose id matches regex")
	boolFlag(fs, &opts.listDevices, "list-devices", "", "list the devices and the quack-enabled apps on each")
	boolFlag(fs, &opts.listRecipes, "list-recipes", "", "list the app's recipes, or only those whose name matches regex")
	boolFlag(fs, &opts.openApp, "open-app", "", "show the app's main screen first, instead of starting it in the background")
	fs.StringVar(&opts.recipe, "recipe", "", "run the steps of the app's recipe `name` in order")
	fs.DurationVar(&opts.startTimeout, "start-timeout", 15*time.Second, "how long to wait for the app to answer")
	boolFlag(fs, &opts.version, "version", "", "show quack's version")
	boolFlag(fs, &opts.help, "help", "h", "show this help")
	opts.usage = "quack runs the debug actions of a quack-enabled Android app over adb.\n\n" +
		"With no action, quack opens a terminal UI to pick a device and an app, then find and run actions. It prints\n" +
		"this help instead when stdin or stdout is not a terminal, AI_AGENT is set, or CI is set. With an action,\n" +
		"quack runs it once, prints the outcome and exits. Scripts and agents: list the actions with --list-actions,\n" +
		"run them with quack <action>, and add --json for output to parse.\n\n" +
		"usage: quack [options]                           open the terminal UI\n" +
		"       quack [options] <action> [key=value]...   run an action\n" +
		"       quack [options] --recipe <name>           run the steps of a recipe\n" +
		"       quack [options] --list-actions [regex]    list the app's actions and their params\n" +
		"       quack [options] --list-recipes [regex]    list the app's recipes and their steps\n" +
		"       quack [options] --list-devices            list the devices and their quack-enabled apps\n\n" +
		"arguments:\n" +
		"  key=value   a param of the action. Repeat the key for a list: tag=a tag=b\n\n" +
		"options:\n" + fs.FlagUsages() + "\n" +
		"recipes:\n" +
		"  A recipe runs steps in order with fixed args, and stops at the first that fails: a call of an action, open-app\n" +
		"  to show the app's main screen, wait for a duration, shell to run a command on the device, or run to run one on\n" +
		"  this computer. The terminal UI runs a recipe as $<name>. The recipes of an app are in\n" +
		"  $XDG_CONFIG_HOME/quack/recipes/<package>.toml, or in ~/.config/quack/recipes/<package>.toml when\n" +
		"  XDG_CONFIG_HOME is unset or empty:\n\n" +
		"    [login]\n" +
		"    description = \"Log in as the test user\"\n" +
		"    steps = [\n" +
		"      { open-app = true },\n" +
		"      { wait = \"2s\" },\n" +
		"      { action = \"account.login\", args = { user = \"foo\", remember = true, count = 3, tag = [\"a\", \"b\"] } },\n" +
		"      { shell = \"pm grant ${QUACK_PACKAGE} android.permission.POST_NOTIFICATIONS\" },\n" +
		"      { run = \"./seed-backend.sh ${QUACK_USER}\" },\n" +
		"    ]\n\n" +
		"  An arg or a command takes the environment variable NAME as ${NAME}, and $$ writes a $. QUACK_PACKAGE and\n" +
		"  ANDROID_SERIAL are the app and the device the recipe runs against, and run commands get them in their\n" +
		"  environment too.\n\n" +
		"exit codes:\n" +
		"  0  done\n" +
		"  1  the action rejected the call, failed, or ended without an answer, or a command of a recipe failed\n" +
		"  2  bad usage, an unknown action, or invalid args\n" +
		"  3  no device or app to reach\n"
	if err := fs.Parse(argv); err != nil {
		return opts, usageErrorf("%v. See quack --help.", err)
	}
	if opts.help || opts.version {
		return opts, nil
	}
	if opts.device == "" {
		opts.device = os.Getenv("ANDROID_SERIAL")
	}
	if opts.pkg != "" && !client.IsPackageName(opts.pkg) {
		return opts, usageErrorf("%q is not a package name", opts.pkg)
	}
	rest := fs.Args()
	var err error
	switch {
	case opts.recipe != "":
		if opts.listActions || opts.listDevices || opts.listRecipes {
			return opts, usageErrorf("--recipe takes no --list-actions, --list-devices or --list-recipes")
		}
		if len(rest) > 0 {
			return opts, usageErrorf("--recipe takes no action or key=value, the recipe fixes them")
		}
	case opts.listDevices:
		if len(rest) > 0 || opts.listActions || opts.listRecipes {
			return opts, usageErrorf("--list-devices takes no action, --list-actions or --list-recipes")
		}
	case opts.listActions && opts.listRecipes:
		return opts, usageErrorf("--list-actions and --list-recipes go one at a time")
	case opts.listActions:
		opts.filter, err = parseFilter(rest, "--list-actions")
		return opts, err
	case opts.listRecipes:
		opts.filter, err = parseFilter(rest, "--list-recipes")
		return opts, err
	case len(rest) == 0 && opts.json:
		return opts, usageErrorf("--json needs an action, --recipe, --list-actions, --list-recipes or --list-devices")
	case len(rest) == 0:
		opts.interactive, opts.help = terminal.TUI, !terminal.TUI
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
	if opts.version {
		fmt.Fprintln(terminal.Out, "quack", version)
		return nil
	}
	if opts.listDevices {
		return printDevices(terminal.Out, adb.Device(opts.device), opts.pkg, opts.json)
	}
	mode := client.Wake
	if opts.openApp {
		mode = client.Open
	}
	if opts.interactive {
		return tui.Run(client.Target{Device: adb.Device(opts.device), Package: opts.pkg}, mode, opts.startTimeout, version)
	}
	target, err := client.Find(adb.Device(opts.device), opts.pkg)
	if err != nil {
		return err
	}
	if opts.listRecipes {
		return printRecipes(terminal.Out, target.Package, opts.filter, opts.json)
	}
	var r recipe.Recipe
	if opts.recipe != "" {
		if r, err = findRecipe(target.Package, opts.recipe); err != nil {
			return err
		}
	}
	app, err := client.Resolve(client.Options{
		Target:       target,
		Mode:         mode,
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
	if opts.recipe != "" {
		return runRecipe(app, r, terminal, opts.json)
	}
	call := wire.Call{Action: opts.action, Args: opts.args}
	result, err := app.Call(context.Background(), call, printNotifications(terminal, opts.json, 0))
	if err != nil {
		return err
	}
	printResult(terminal, opts.json, result, 0)
	return nil
}

// printResult prints the message and the data of a call, or with json its outcomeJSON for step, which is 0 outside a
// recipe.
func printResult(terminal Terminal, json bool, result wire.MessageResult, step int) {
	if json {
		printJSON(terminal.Out, outcomeJSON{Code: codeOK, Message: result.Message, Data: result.Data, Step: step})
		return
	}
	fmt.Fprintln(terminal.Out, result.Message)
	if result.Data != nil {
		fmt.Fprintln(terminal.Out, result.IndentedData())
	}
}

// parseFilter reads the one regex that flag may take, or nil without one.
func parseFilter(rest []string, flag string) (*regexp.Regexp, error) {
	switch len(rest) {
	case 0:
		return nil, nil
	case 1:
		filter, err := regexp.Compile(rest[0])
		if err != nil {
			return nil, usageErrorf("invalid %s pattern: %v", flag, err)
		}
		return filter, nil
	}
	return nil, usageErrorf("%s takes at most one regex", flag)
}

// boolFlag declares an on/off flag that sets *p. pflag prints [=true|false] after every bool flag in help, and
// nothing after a BoolFunc flag, which parses the same.
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

// printNotifications prints the message of each notification, or with json each notification as a notificationJSON of
// step, which is 0 outside a recipe.
func printNotifications(terminal Terminal, json bool, step int) client.NotificationFunc {
	return func(message string) {
		if json {
			printJSON(terminal.Out, notificationJSON{codeNotification, message, step})
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

// version is quack's version. Release builds set it with -ldflags "-X quack/internal/cli.version=<version>".
var version = "dev"
