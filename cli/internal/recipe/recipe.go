package recipe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/wire"
)

type Recipe struct {
	Name        string
	Description string
	Params      []wire.Param
	// The environment variable of each param that takes one, while it is set.
	env map[string]string
}

type Step interface{ step() }

type Call wire.Call

type OpenApp struct{}

type Wait struct {
	Duration time.Duration
}

type Shell struct {
	Command string
}

type Run struct {
	Command string
}

// Args are the ones the recipe runs with, defaults included. The recipe's own steps follow it, as if the caller
// took them.
type RunRecipe struct {
	Recipe Recipe
	Args   map[string]wire.RawArg
}

type CommandError struct {
	Exit int
	// stderr, or stdout when stderr is empty.
	Output string
}

func (e *CommandError) Error() string {
	if e.Output == "" {
		return fmt.Sprintf("the command exited with %d", e.Exit)
	}
	return fmt.Sprintf("the command exited with %d: %s", e.Exit, e.Output)
}

func (Call) step()      {}
func (OpenApp) step()   {}
func (Wait) step()      {}
func (Shell) step()     {}
func (Run) step()       {}
func (RunRecipe) step() {}

type App interface {
	Call(ctx context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error)
	Start(mode client.StartMode) error
	Command(ctx context.Context, cmd string) (adb.Output, error)
	Target() client.Target
}

// A step that starts after ctx ended does not get far: the app cancels a call, the app does not open, and a
// command ends at once.
func Do(ctx context.Context, app App, step Step, notify client.NotificationFunc) (wire.MessageResult, error) {
	switch step := step.(type) {
	case Call:
		return app.Call(ctx, wire.Call(step), notify)
	case OpenApp:
		if ctx.Err() != nil {
			return wire.MessageResult{}, &client.CallError{Message: "canceled"}
		}
		return wire.MessageResult{}, app.Start(client.OpenApp)
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

// QUACK_PACKAGE and ANDROID_SERIAL make adb in command reach the recipe's device. Ending ctx kills sh, and the
// commands it started get a second to let go of its output.
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
	case RunRecipe:
		// A recipe that has no name is neither named nor exported, and shows by its args alone.
		return CommandLine(wire.Action{ID: strings.TrimSpace("recipe " + step.Recipe.Name), Params: step.Recipe.Params}, step.Args)
	}
	return ""
}

func CommandLine(action wire.Action, args map[string]wire.RawArg) string {
	words := []string{action.ID}
	for _, p := range action.Params {
		for _, value := range args[p.Name] {
			words = append(words, p.Name+"="+quote(value))
		}
	}
	return strings.Join(words, " ")
}

// Only args typed on the command line or in the TUI take variables. A recipe reads the environment through env.
func ExpandArgs(args map[string]wire.RawArg, target client.Target) (map[string]wire.RawArg, error) {
	if args == nil {
		return nil, nil
	}
	expanded := make(map[string]wire.RawArg, len(args))
	for _, key := range slices.Sorted(maps.Keys(args)) {
		values := make(wire.RawArg, len(args[key]))
		for i, value := range args[key] {
			v, err := expand(value, target)
			if err != nil {
				return nil, fmt.Errorf("arg '%s': %w", key, err)
			}
			values[i] = v
		}
		expanded[key] = values
	}
	return expanded, nil
}

// Any other $ stays as it is, so only text that mentions ${ needs escaping.
func expand(text string, target client.Target) (string, error) {
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
			value, ok := lookup(target, name)
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

func lookup(target client.Target, name string) (string, bool) {
	switch name {
	case "QUACK_PACKAGE":
		return target.Package, true
	case "ANDROID_SERIAL":
		return string(target.Device), true
	}
	return os.LookupEnv(name)
}

func isVariable(name string) bool {
	for i, r := range name {
		if r != '_' && !(r < unicode.MaxASCII && (unicode.IsLetter(r) || i > 0 && unicode.IsDigit(r))) {
			return false
		}
	}
	return name != ""
}

// Matches the Kotlin library's parsing. A type this CLI does not know takes any value.
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

func expected(p wire.Param) string {
	if p.Type == wire.ParamTypeEnum {
		return "one of " + strings.Join(p.Choices, ", ")
	}
	return string(p.Type)
}

// Follows the rules of action ids.
func isName(name string) bool {
	return name != "" && strings.IndexFunc(name, unicode.IsSpace) < 0 && !strings.ContainsRune("-/$", rune(name[0]))
}

// Double quotes would leave $ and ` to the shell.
func quote(value string) string {
	if value != "" && strings.IndexFunc(value, unsafeInShell) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func unsafeInShell(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_./:=,@%+", r)
}
