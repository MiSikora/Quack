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

// args is the command line without the program name.
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
	device       string
	pkg          string
	json         bool
	listActions  bool
	interactive  bool
	listDevices  bool
	listRecipes  bool
	initRecipes  bool
	openApp      bool
	recipe       string
	startTimeout time.Duration
	help         bool
	version      bool
	usage        string
	action       string
	// nil unless listActions or listRecipes has a pattern.
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
	boolFlag(fs, &opts.initRecipes, "init-recipes", "", "write the files that let editors check recipes, and with --app a starter recipes file of the app")
	boolFlag(fs, &opts.openApp, "open-app", "", "show the app's main screen first, instead of starting it in the background")
	fs.StringVar(&opts.recipe, "recipe", "", "run the app's recipe `name`")
	fs.DurationVar(&opts.startTimeout, "start-timeout", 15*time.Second, "how long to wait for the app to answer")
	boolFlag(fs, &opts.version, "version", "", "show quack's version")
	boolFlag(fs, &opts.help, "help", "h", "show this help")
	opts.usage = "quack runs the debug actions of a quack-enabled Android app over adb.\n\n" +
		"With no action, quack opens a terminal UI to pick a device and an app, then find and run actions. It prints\n" +
		"this help instead when stdin or stdout is not a terminal, AI_AGENT is set, or CI is set. With an action,\n" +
		"quack runs it once, prints the outcome and exits. Scripts and agents: list the actions with --list-actions,\n" +
		"run them with quack <action>, and add --json for output to parse.\n\n" +
		"usage: quack [options]                                open the terminal UI\n" +
		"       quack [options] <action> [key=value]...        run an action\n" +
		"       quack [options] --recipe <name> [key=value]... run a recipe\n" +
		"       quack [options] --list-actions [regex]         list the app's actions and their params\n" +
		"       quack [options] --list-recipes [regex]         list the app's recipes and their params\n" +
		"       quack [--app <package>] --init-recipes         set up recipes and editors to check them\n" +
		"       quack [options] --list-devices                 list the devices and their quack-enabled apps\n\n" +
		"arguments:\n" +
		"  key=value   a param of the action or the recipe. Repeat the key for a list: tag=a tag=b. The value takes the\n" +
		"              environment variable NAME as ${NAME}, and $${NAME} writes ${NAME}:\n" +
		"              quack account.login 'token=${API_TOKEN}'\n\n" +
		"options:\n" + fs.FlagUsages() + "\n" +
		"recipes:\n" +
		"  A recipe is a TypeScript or JavaScript function that runs actions and commands, and decides what to do with\n" +
		"  what they return. The recipes of an app are the exports of $XDG_CONFIG_HOME/quack/recipes/<package>.ts, or of\n" +
		"  ~/.config/quack/recipes/<package>.ts when XDG_CONFIG_HOME is unset or empty, or of the .js file there. The\n" +
		"  file may import local files, but no packages. quack --init-recipes writes quack.d.ts and tsconfig.json there,\n" +
		"  so that editors check recipes, and with --app a starter file of the app. The terminal UI runs a recipe as\n" +
		"  $<name>:\n\n" +
		"    import quack from \"quack\"\n\n" +
		"    export const login = quack.recipe({\n" +
		"      description: \"Log in as the test user\",\n" +
		"      params: { user: { type: \"string\", default: \"foo\", hints: [\"foo\", \"bar\"] } },\n" +
		"      run({ user }) {\n" +
		"        quack.openApp()\n" +
		"        const status = quack.runAction<{ loggedIn: boolean }>(\"account.status\")\n" +
		"        if (!status.data.loggedIn) quack.runAction(\"account.login\", { user, remember: true })\n" +
		"        quack.shell(\"pm grant io.example android.permission.POST_NOTIFICATIONS\")\n" +
		"        quack.wait(\"2s\")\n" +
		"      },\n" +
		"    })\n\n" +
		"  A step that fails throws, and ends the recipe unless it catches the error.\n\n" +
		"exit codes:\n" +
		"  0  done\n" +
		"  1  the action rejected the call, failed, or ended without an answer, or a recipe failed\n" +
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
		if opts.listActions || opts.listDevices || opts.listRecipes || opts.initRecipes {
			return opts, usageErrorf("--recipe takes no --list-actions, --list-devices, --list-recipes or --init-recipes")
		}
		opts.args, err = parseArgs(rest)
		return opts, err
	case opts.initRecipes:
		if len(rest) > 0 || opts.listActions || opts.listDevices || opts.listRecipes || opts.json || fs.Changed("device") {
			return opts, usageErrorf("--init-recipes takes only --app, since it sets up files and no device")
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
	if opts.initRecipes {
		return initRecipes(terminal.Out, opts.pkg)
	}
	if opts.listDevices {
		return printDevices(terminal.Out, adb.Device(opts.device), opts.pkg, opts.json)
	}
	mode := client.Wake
	if opts.openApp {
		mode = client.OpenApp
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
	var script *recipe.Script
	if opts.recipe != "" {
		if script, err = findRecipe(target.Package, opts.recipe); err != nil {
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
	args, err := recipe.ExpandArgs(opts.args, app.Target())
	if err != nil {
		return usageErrorf("%v", err)
	}
	if opts.recipe != "" {
		return runRecipe(app, script, opts.recipe, args, terminal, opts.json)
	}
	result, err := app.Call(context.Background(), wire.Call{Action: opts.action, Args: args}, printNotifications(terminal, opts.json, 0))
	if err != nil {
		return err
	}
	printResult(terminal, opts.json, result, 0)
	return nil
}

// step is 0 outside a recipe.
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

// A repeated key keeps its values in command-line order.
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

// step is 0 outside a recipe.
func printNotifications(terminal Terminal, json bool, step int) client.NotificationFunc {
	return func(message string) {
		if json {
			printJSON(terminal.Out, notificationJSON{codeNotification, message, step})
		} else {
			fmt.Fprintln(terminal.Out, message)
		}
	}
}

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

// A code missing here exits with exitAction.
var errorExits = map[wire.ErrorCode]int{
	wire.ErrorCodeBadFrame:      exitUsage,
	wire.ErrorCodeUnknownAction: exitUsage,
	wire.ErrorCodeInvalidArgs:   exitUsage,
}

// Release builds set it with -ldflags "-X quack/internal/cli.version=<version>".
var version = "dev"
