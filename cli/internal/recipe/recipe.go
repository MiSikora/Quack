// Package recipe reads the recipes of an app and runs their steps. A recipe is a named list of steps with fixed args,
// kept for each app in a TOML file of the user's config dir. An arg or a command may hold ${NAME}, which takes the value
// of the environment variable NAME when the step runs, and $${NAME}, which stands for ${NAME}. ${QUACK_PACKAGE} and
// ${ANDROID_SERIAL} are the app and the device the recipe runs against, whatever the environment holds.
package recipe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/wire"
)

// Recipe is a recipe as its file defines it.
type Recipe struct {
	Name        string
	Description string
	Steps       []Step
}

// Step is one step of a recipe: a Call, OpenApp, Wait, Shell or Run.
type Step interface{ step() }

// Call calls an action with the args the recipe fixes.
type Call wire.Call

// OpenApp shows the app's main screen, as --open-app does.
type OpenApp struct{}

// Wait waits for Duration, which is more than zero.
type Wait struct {
	Duration time.Duration
}

// Shell runs Command on the app's device, as adb shell does.
type Shell struct {
	Command string
}

// Run runs Command on this computer with sh -c.
type Run struct {
	Command string
}

// CommandError is a command of a Shell or Run step that exited with a status other than 0.
type CommandError struct {
	Exit int
	// Output is what the command printed to stderr, or to stdout when stderr is empty.
	Output string
}

func (e *CommandError) Error() string {
	if e.Output == "" {
		return fmt.Sprintf("the command exited with %d", e.Exit)
	}
	return fmt.Sprintf("the command exited with %d: %s", e.Exit, e.Output)
}

func (Call) step()    {}
func (OpenApp) step() {}
func (Wait) step()    {}
func (Shell) step()   {}
func (Run) step()     {}

// App is what a step needs from a quack app. client.App satisfies it.
type App interface {
	Call(ctx context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error)
	Start(mode client.StartMode) error
	Command(ctx context.Context, cmd string) (adb.Output, error)
	Target() client.Target
}

// Path is the file of the recipes of pkg: quack/recipes/<pkg>.toml in $XDG_CONFIG_HOME, or in ~/.config when that is
// unset or empty, on every OS.
func Path(pkg string) string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "quack", "recipes", pkg+".toml")
}

// Load reads the recipes of pkg, sorted by name. A missing file has none. A file that does not parse, or breaks a rule
// of recipes, fails with an error that names the file and, where it can, the recipe and the step.
func Load(pkg string) ([]Recipe, error) {
	path := Path(pkg)
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var raw map[string]rawRecipe
	if err := toml.NewDecoder(file).DisallowUnknownFields().Decode(&raw); err != nil {
		return nil, decodeError(path, err)
	}
	var recipes []Recipe
	for _, name := range slices.Sorted(maps.Keys(raw)) {
		r, err := raw[name].parse(name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		recipes = append(recipes, r)
	}
	return recipes, nil
}

// Check says whether each call of r, run against target, would pass the app's checks of its args, so that a recipe
// that cannot finish fails before its first step. Its errors carry the codes the app would answer with, except for an environment
// variable that is not set.
func Check(r Recipe, actions []wire.Action, target client.Target) error {
	for i, written := range r.Steps {
		step, err := expandStep(written, lookup(target))
		if err != nil {
			return fmt.Errorf("recipe '%s' step %d: %w", r.Name, i+1, err)
		}
		call, ok := step.(Call)
		if !ok {
			continue
		}
		fail := func(code wire.ErrorCode, format string, args ...any) error {
			return wire.Error{Code: code, Message: fmt.Sprintf("recipe '%s' step %d: ", r.Name, i+1) + fmt.Sprintf(format, args...)}
		}
		at := slices.IndexFunc(actions, func(a wire.Action) bool { return a.ID == call.Action })
		if at < 0 {
			return fail(wire.ErrorCodeUnknownAction, "no action '%s'", call.Action)
		}
		action := actions[at]
		for _, key := range slices.Sorted(maps.Keys(call.Args)) {
			if !slices.ContainsFunc(action.Params, func(p wire.Param) bool { return p.Name == key }) {
				return fail(wire.ErrorCodeInvalidArgs, "unknown argument '%s'", key)
			}
		}
		for _, p := range action.Params {
			values, given := call.Args[p.Name]
			switch {
			case !given:
				if _, required := p.Presence.(wire.Required); required {
					return fail(wire.ErrorCodeInvalidArgs, "missing argument '%s'", p.Name)
				}
			case !p.Type.IsList() && len(values) > 1:
				return fail(wire.ErrorCodeInvalidArgs, "'%s' given more than once", p.Name)
			case !p.Type.IsList() && len(values) == 0:
				return fail(wire.ErrorCodeInvalidArgs, "'%s' expects %s, got none", p.Name, expected(p))
			}
			for j, value := range values {
				if !parses(p, value) {
					// The value as written, so that the error shows ${NAME} rather than the variable's value.
					typed := written.(Call).Args[p.Name][j]
					return fail(wire.ErrorCodeInvalidArgs, "'%s' expects %s, got '%s'", p.Name, expected(p), typed)
				}
			}
		}
	}
	return nil
}

// Do runs step with its variables replaced. A Call returns the action's message and data, a Shell or Run the stdout
// of its command, and the other steps an empty message. Canceling ctx cancels the step, and a step that starts after
// ctx ended does not get far: the app cancels a call, the app does not open, and a command ends at once.
func Do(ctx context.Context, app App, step Step, notify client.NotificationFunc) (wire.MessageResult, error) {
	step, err := expandStep(step, lookup(app.Target()))
	if err != nil {
		return wire.MessageResult{}, err
	}
	switch step := step.(type) {
	case Call:
		return app.Call(ctx, wire.Call(step), notify)
	case OpenApp:
		if ctx.Err() != nil {
			return wire.MessageResult{}, &client.CallError{Message: "canceled"}
		}
		return wire.MessageResult{}, app.Start(client.Open)
	case Wait:
		timer := time.NewTimer(step.Duration)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return wire.MessageResult{}, &client.CallError{Message: "canceled"}
		}
	case Shell:
		out, err := app.Command(ctx, step.Command)
		if ctx.Err() != nil {
			return wire.MessageResult{}, &client.CallError{Message: "canceled"}
		}
		if err != nil {
			return wire.MessageResult{}, err
		}
		return commandResult(out)
	case Run:
		return run(ctx, step.Command, app.Target())
	}
	return wire.MessageResult{}, nil
}

// run runs command on this computer, with QUACK_PACKAGE and ANDROID_SERIAL naming target in its environment, so that adb
// in command reaches the recipe's device. Ending ctx kills sh, and the commands it started get a second to let go of
// its output.
func run(ctx context.Context, command string, target client.Target) (wire.MessageResult, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(), "QUACK_PACKAGE="+target.Package, "ANDROID_SERIAL="+string(target.Device))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	var exited *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return wire.MessageResult{}, &client.CallError{Message: "canceled"}
	case errors.As(err, &exited):
		return commandResult(adb.Output{Stdout: stdout.String(), Stderr: stderr.String(), Exit: exited.ExitCode()})
	case err != nil:
		return wire.MessageResult{}, err
	}
	return commandResult(adb.Output{Stdout: stdout.String(), Stderr: stderr.String()})
}

// commandResult is the message of a command that exited with 0, its stdout, and a CommandError for any other status.
func commandResult(out adb.Output) (wire.MessageResult, error) {
	if out.Exit != 0 {
		output := strings.TrimSpace(out.Stderr)
		if output == "" {
			output = strings.TrimSpace(out.Stdout)
		}
		return wire.MessageResult{}, &CommandError{Exit: out.Exit, Output: output}
	}
	return wire.MessageResult{Message: strings.TrimRight(out.Stdout, "\n")}, nil
}

// Line writes step the way a user types it: a call as on the command line, /open-app as the TUI's command, wait with
// its duration, and shell and run with their commands. The args of a call go in the param order of its action in actions, or in key order when actions
// lack it.
func Line(step Step, actions []wire.Action) string {
	switch step := step.(type) {
	case Call:
		at := slices.IndexFunc(actions, func(a wire.Action) bool { return a.ID == step.Action })
		if at >= 0 {
			return CommandLine(actions[at], step.Args)
		}
		action := wire.Action{ID: step.Action}
		for _, key := range slices.Sorted(maps.Keys(step.Args)) {
			action.Params = append(action.Params, wire.Param{Name: key})
		}
		return CommandLine(action, step.Args)
	case OpenApp:
		return "/open-app"
	case Wait:
		return "wait " + step.Duration.String()
	case Shell:
		return "shell " + step.Command
	case Run:
		return "run " + step.Command
	}
	return ""
}

// CommandLine writes a call the way it would be typed on the command line, with args in the action's param order.
func CommandLine(action wire.Action, args map[string]wire.RawArg) string {
	words := []string{action.ID}
	for _, p := range action.Params {
		for _, value := range args[p.Name] {
			words = append(words, p.Name+"="+quote(value))
		}
	}
	return strings.Join(words, " ")
}

// rawRecipe and rawStep are a recipe and a step as the file writes them. Load turns them into a Recipe.
type (
	rawRecipe struct {
		Description string    `toml:"description"`
		Steps       []rawStep `toml:"steps"`
	}
	rawStep struct {
		Action  *string        `toml:"action"`
		Args    map[string]any `toml:"args"`
		OpenApp *bool          `toml:"open-app"`
		Wait    *string        `toml:"wait"`
		Shell   *string        `toml:"shell"`
		Run     *string        `toml:"run"`
	}
)

func (r rawRecipe) parse(name string) (Recipe, error) {
	if !isName(name) {
		return Recipe{}, fmt.Errorf("recipe '%s': its name must not be empty, contain whitespace, or start with '-', '/' or '$'", name)
	}
	if len(r.Steps) == 0 {
		return Recipe{}, fmt.Errorf("recipe '%s' has no steps", name)
	}
	recipe := Recipe{Name: name, Description: r.Description}
	for i, raw := range r.Steps {
		step, err := raw.parse()
		if err != nil {
			return Recipe{}, fmt.Errorf("recipe '%s' step %d: %w", name, i+1, err)
		}
		recipe.Steps = append(recipe.Steps, step)
	}
	return recipe, nil
}

func (s rawStep) parse() (Step, error) {
	kinds := 0
	for _, set := range []bool{s.Action != nil, s.OpenApp != nil, s.Wait != nil, s.Shell != nil, s.Run != nil} {
		if set {
			kinds++
		}
	}
	switch {
	case s.Args != nil && s.Action == nil:
		return nil, errors.New("args needs an action")
	case kinds == 0:
		return nil, errors.New("it needs one of action, open-app, wait, shell or run")
	case kinds > 1:
		return nil, errors.New("it takes only one of action, open-app, wait, shell or run")
	case s.OpenApp != nil:
		if !*s.OpenApp {
			return nil, errors.New("open-app must be true")
		}
		return OpenApp{}, nil
	case s.Wait != nil:
		d, err := time.ParseDuration(*s.Wait)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("wait '%s' is not a duration above zero, such as 2s or 500ms", *s.Wait)
		}
		return Wait{d}, nil
	case s.Shell != nil:
		command, err := parseCommand("shell", *s.Shell)
		return Shell{command}, err
	case s.Run != nil:
		command, err := parseCommand("run", *s.Run)
		return Run{command}, err
	}
	var args map[string]wire.RawArg
	for key, value := range s.Args {
		values, ok := rawValues(value)
		if !ok {
			return nil, fmt.Errorf("arg '%s' must be a string, a number, a boolean, or an array of those", key)
		}
		for _, v := range values {
			if _, err := expand(v, anyValue); err != nil {
				return nil, fmt.Errorf("arg '%s': %w", key, err)
			}
		}
		if args == nil {
			args = map[string]wire.RawArg{}
		}
		args[key] = values
	}
	return Call{Action: *s.Action, Args: args}, nil
}

// parseCommand checks the command of a shell or run step, which kind names.
func parseCommand(kind, command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("%s needs a command", kind)
	}
	if _, err := expand(command, anyValue); err != nil {
		return "", fmt.Errorf("%s: %w", kind, err)
	}
	return command, nil
}

// rawValues are the values of an arg as the command line would give them: one for a scalar, and one for each item of
// an array.
func rawValues(value any) (wire.RawArg, bool) {
	items, isArray := value.([]any)
	if !isArray {
		items = []any{value}
	}
	values := wire.RawArg{}
	for _, item := range items {
		var text string
		switch item := item.(type) {
		case string:
			text = item
		case int64:
			text = strconv.FormatInt(item, 10)
		case float64:
			text = strconv.FormatFloat(item, 'g', -1, 64)
		case bool:
			text = strconv.FormatBool(item)
		default:
			return nil, false
		}
		values = append(values, text)
	}
	return values, true
}

// expandStep returns step with the variables in the args of a call, or in a command, replaced by the values lookup
// gives.
func expandStep(step Step, lookup func(string) (string, bool)) (Step, error) {
	switch s := step.(type) {
	case Shell:
		command, err := expand(s.Command, lookup)
		return Shell{command}, err
	case Run:
		command, err := expand(s.Command, lookup)
		return Run{command}, err
	}
	call, ok := step.(Call)
	if !ok {
		return step, nil
	}
	args, err := expandArgs(call.Args, lookup)
	if err != nil {
		return nil, err
	}
	return Call{Action: call.Action, Args: args}, nil
}

// ExpandArgs returns args with their variables replaced, as a recipe that runs against target replaces them.
func ExpandArgs(args map[string]wire.RawArg, target client.Target) (map[string]wire.RawArg, error) {
	return expandArgs(args, lookup(target))
}

func expandArgs(args map[string]wire.RawArg, lookup func(string) (string, bool)) (map[string]wire.RawArg, error) {
	if args == nil {
		return nil, nil
	}
	expanded := make(map[string]wire.RawArg, len(args))
	for _, key := range slices.Sorted(maps.Keys(args)) {
		values := make(wire.RawArg, len(args[key]))
		for i, value := range args[key] {
			v, err := expand(value, lookup)
			if err != nil {
				return nil, fmt.Errorf("arg '%s': %w", key, err)
			}
			values[i] = v
		}
		expanded[key] = values
	}
	return expanded, nil
}

// expand replaces each ${NAME} in text with the value lookup gives for NAME, and each $${ with ${. Any other $ stays as
// it is, so only text that mentions ${ needs escaping.
func expand(text string, lookup func(string) (string, bool)) (string, error) {
	var out strings.Builder
	rest := text
	for {
		at := strings.IndexByte(rest, '$')
		if at < 0 || at == len(rest)-1 {
			out.WriteString(rest)
			return out.String(), nil
		}
		out.WriteString(rest[:at])
		switch rest[at+1] {
		case '$':
			if strings.HasPrefix(rest[at+2:], "{") {
				out.WriteString("${")
				rest = rest[at+3:]
			} else {
				out.WriteByte('$')
				rest = rest[at+1:]
			}
		case '{':
			end := strings.IndexByte(rest[at:], '}')
			if end < 0 {
				return "", fmt.Errorf("'%s' has a ${ without a closing }", text)
			}
			name := rest[at+2 : at+end]
			if !isVariable(name) {
				return "", fmt.Errorf("'${%s}' does not name an environment variable", name)
			}
			value, ok := lookup(name)
			if !ok {
				return "", fmt.Errorf("environment variable '%s' is not set", name)
			}
			out.WriteString(value)
			rest = rest[at+end+1:]
		default:
			out.WriteByte('$')
			rest = rest[at+1:]
		}
	}
}

// lookup finds the variables of a recipe that runs against target: QUACK_PACKAGE and ANDROID_SERIAL name target, and
// any other name is an environment variable.
func lookup(target client.Target) func(string) (string, bool) {
	return func(name string) (string, bool) {
		switch name {
		case "QUACK_PACKAGE":
			return target.Package, true
		case "ANDROID_SERIAL":
			return string(target.Device), true
		}
		return os.LookupEnv(name)
	}
}

// anyValue is a lookup for checking the form of variables alone, which has a value for every name.
func anyValue(string) (string, bool) { return "", true }

// isVariable is whether name is an environment variable name: letters, digits and _, not starting with a digit.
func isVariable(name string) bool {
	for i, r := range name {
		if r != '_' && !(r < unicode.MaxASCII && (unicode.IsLetter(r) || i > 0 && unicode.IsDigit(r))) {
			return false
		}
	}
	return name != ""
}

// decodeError names the file and the place in it where the decoder failed. The decoder knows the recipe there but not
// the step, so the place stands for both.
func decodeError(path string, err error) error {
	var decode *toml.DecodeError
	var strict *toml.StrictMissingError
	switch {
	case errors.As(err, &strict):
		decode = &strict.Errors[0]
		key := decode.Key()
		err = fmt.Errorf("unknown key '%s'", key[len(key)-1])
	case !errors.As(err, &decode):
		return fmt.Errorf("%s: %w", path, err)
	}
	row, column := decode.Position()
	return fmt.Errorf("%s:%d:%d: %s", path, row, column, strings.TrimPrefix(err.Error(), "toml: "))
}

// parses is whether the app would parse value for p, as the Kotlin library's params do. A type this CLI does not know
// takes any value.
func parses(p wire.Param, value string) bool {
	switch p.Type {
	case wire.ParamTypeInt:
		_, err := strconv.ParseInt(value, 10, 32)
		return err == nil
	case wire.ParamTypeLong:
		_, err := strconv.ParseInt(value, 10, 64)
		return err == nil
	case wire.ParamTypeDouble:
		f, err := strconv.ParseFloat(value, 64)
		return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
	case wire.ParamTypeBoolean:
		return value == "true" || value == "false"
	case wire.ParamTypeEnum:
		return slices.ContainsFunc(p.Choices, func(c string) bool { return strings.EqualFold(c, value) })
	}
	return true
}

// expected is what p takes, as the app says it.
func expected(p wire.Param) string {
	if p.Type == wire.ParamTypeEnum {
		return "one of " + strings.Join(p.Choices, ", ")
	}
	return string(p.Type)
}

// isName is whether name can follow '$' in the TUI and --recipe on the command line, under the rules of action ids.
func isName(name string) bool {
	return name != "" && strings.IndexFunc(name, unicode.IsSpace) < 0 && !strings.ContainsRune("-/$", rune(name[0]))
}

// quote single-quotes a value that a shell would split or change. Double quotes would leave $ and ` to the shell.
func quote(value string) string {
	if value != "" && strings.IndexFunc(value, unsafeInShell) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func unsafeInShell(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_./:=,@%+", r)
}
