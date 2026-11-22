// Package recipe reads the recipes of an app and runs their steps. A recipe is a named list of steps with fixed args,
// kept for each app in a TOML file of the user's config dir.
package recipe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"quack/internal/client"
	"quack/internal/wire"
)

// Recipe is a recipe as its file defines it.
type Recipe struct {
	Name        string
	Description string
	Steps       []Step
}

// Step is one step of a recipe: a Call, OpenApp or Wait.
type Step interface{ step() }

// Call calls an action with the args the recipe fixes.
type Call wire.Call

// OpenApp shows the app's main screen, as --open-app does.
type OpenApp struct{}

// Wait waits for Duration, which is more than zero.
type Wait struct {
	Duration time.Duration
}

func (Call) step()    {}
func (OpenApp) step() {}
func (Wait) step()    {}

// App is what a step needs from a quack app. client.App satisfies it.
type App interface {
	Call(ctx context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error)
	Start(mode client.StartMode) error
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

// Check says whether each call of r would pass the app's checks of its args, so that a recipe that cannot finish
// fails before its first step. Its errors carry the codes the app would answer with.
func Check(r Recipe, actions []wire.Action) error {
	for i, step := range r.Steps {
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
			for _, value := range values {
				if !parses(p, value) {
					return fail(wire.ErrorCodeInvalidArgs, "'%s' expects %s, got '%s'", p.Name, expected(p), value)
				}
			}
		}
	}
	return nil
}

// Do runs step. A Call returns the action's message and data, and the other steps an empty message. Canceling ctx
// cancels the step, and a step that starts after ctx ended does not run.
func Do(ctx context.Context, app App, step Step, notify client.NotificationFunc) (wire.MessageResult, error) {
	if ctx.Err() != nil {
		return wire.MessageResult{}, &client.CallError{Message: "canceled"}
	}
	switch step := step.(type) {
	case Call:
		return app.Call(ctx, wire.Call(step), notify)
	case OpenApp:
		return wire.MessageResult{}, app.Start(client.Open)
	case Wait:
		timer := time.NewTimer(step.Duration)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return wire.MessageResult{}, &client.CallError{Message: "canceled"}
		}
	}
	return wire.MessageResult{}, nil
}

// Line writes step the way a user types it: a call as on the command line, /open-app as the TUI's command, and wait
// with its duration. The args of a call go in the param order of its action in actions, or in key order when actions
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
	for _, set := range []bool{s.Action != nil, s.OpenApp != nil, s.Wait != nil} {
		if set {
			kinds++
		}
	}
	switch {
	case s.Args != nil && s.Action == nil:
		return nil, errors.New("args needs an action")
	case kinds == 0:
		return nil, errors.New("it needs one of action, open-app or wait")
	case kinds > 1:
		return nil, errors.New("it takes only one of action, open-app or wait")
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
	}
	args := map[string]wire.RawArg{}
	for key, value := range s.Args {
		values, ok := rawValues(value)
		if !ok {
			return nil, fmt.Errorf("arg '%s' must be a string, a number, a boolean, or an array of those", key)
		}
		args[key] = values
	}
	return Call{Action: *s.Action, Args: args}, nil
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
