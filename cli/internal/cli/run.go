package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"quack/internal/adb"
	"quack/internal/command"
	"quack/internal/device"
	"quack/internal/recipe"
	"quack/internal/session"
	"quack/internal/step"
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
		var failed *session.StepError
		if errors.As(err, &failed) {
			outcome.Step = failed.Step
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
	projectDir   string
	startTimeout time.Duration
	help         bool
	version      bool
	usage        string
	action       string
	filter       *regexp.Regexp
	args         map[string]wire.RawArg
}

func parseOptions(argv []string, terminal Terminal) (options, error) {
	var opts options
	fs := command.NewFlagSet("quack")
	fs.StringVarP(&opts.pkg, "app", "a", "", "the app, by its `package` (default: the only quack-enabled app on the device)")
	fs.StringVarP(&opts.device, "device", "d", "", "the device, by its `serial` as adb devices lists it (default: $ANDROID_SERIAL)")
	command.BoolFlag(fs, &opts.json, "json", "j", "print JSON lines: each notification, then the outcome with its code")
	command.BoolFlag(fs, &opts.listActions, "list-actions", "l", "list the app's actions, or only those whose id matches regex")
	command.BoolFlag(fs, &opts.listDevices, "list-devices", "", "list the devices and the quack-enabled apps on each")
	command.BoolFlag(fs, &opts.listRecipes, "list-recipes", "", "list the app's recipes, or only those whose name matches regex")
	command.ProjectDirFlag(fs, &opts.projectDir)
	command.BoolFlag(fs, &opts.initRecipes, "init-recipes", "", "write the files that let editors check recipes, and with --app start a project with a recipe of the app")
	command.BoolFlag(fs, &opts.openApp, "open-app", "", "show the app's main screen first, instead of starting it in the background")
	fs.StringVar(&opts.recipe, "recipe", "", "run the app's recipe `name`")
	fs.DurationVar(&opts.startTimeout, "start-timeout", 15*time.Second, "how long to wait for the app to answer")
	command.BoolFlag(fs, &opts.version, "version", "", "show quack's version")
	command.BoolFlag(fs, &opts.help, "help", "h", "show this help")
	opts.usage = usage(fs.FlagUsages())
	if err := fs.Parse(argv); err != nil {
		return opts, usageErrorf("%v. See quack --help.", err)
	}
	if opts.help || opts.version {
		return opts, nil
	}
	if opts.device == "" {
		opts.device = os.Getenv("ANDROID_SERIAL")
	}
	if opts.pkg != "" && !device.IsPackageName(opts.pkg) {
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
			return opts, usageErrorf("--init-recipes takes only --app and --project-dir, since it sets up files and no device")
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
		fmt.Fprintln(terminal.Out, "quack", command.Version)
		return nil
	}
	var location recipe.Location
	if opts.initRecipes || opts.interactive || opts.listRecipes || opts.recipe != "" {
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		location = recipe.Location{Dir: dir, ProjectDir: opts.projectDir}
	}
	if opts.initRecipes {
		return initRecipes(terminal.Out, location, opts.pkg)
	}
	backend := session.ADB{StartTimeout: opts.startTimeout, Status: terminal.Err}
	if opts.listDevices {
		return printDevices(terminal.Out, backend, adb.Device(opts.device), opts.pkg, opts.json)
	}
	if opts.interactive {
		return tui.Run(device.Target{Device: adb.Device(opts.device), Package: opts.pkg}, opts.openApp, opts.startTimeout, command.Version, location)
	}
	target, err := backend.Find(adb.Device(opts.device), opts.pkg)
	if err != nil {
		return err
	}
	if opts.listRecipes {
		return printRecipes(terminal, location, target.Package, opts.filter, opts.json)
	}
	var cookbook *recipe.Cookbook
	if opts.recipe != "" {
		if cookbook, err = findRecipe(location, target.Package, opts.recipe, terminal); err != nil {
			return err
		}
	}
	app, err := backend.Connect(target, opts.openApp)
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
	args, err := step.ExpandArgs(opts.args, app.Target())
	if err != nil {
		return usageErrorf("%v", err)
	}
	if opts.recipe != "" {
		return runPrinted(app, cookbook, session.Runnable{Name: opts.recipe, Recipe: true}, args, opts.args, terminal, opts.json)
	}
	return runPrinted(app, nil, session.Runnable{Name: opts.action}, args, opts.args, terminal, opts.json)
}

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

func printNotification(terminal Terminal, json bool, notification wire.Notification, step int) {
	if json {
		printJSON(terminal.Out, notificationJSON{Code: codeNotification, Message: notification.Message, Data: notification.Data, Step: step})
		return
	}
	fmt.Fprintln(terminal.Out, notification.Message)
	if notification.Data != nil {
		fmt.Fprintln(terminal.Out, notification.IndentedData())
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

var errorExits = map[wire.ErrorCode]int{
	wire.ErrorCodeBadFrame:      exitUsage,
	wire.ErrorCodeUnknownAction: exitUsage,
	wire.ErrorCodeInvalidArgs:   exitUsage,
}
