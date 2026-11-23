package recipe

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"

	"quack/internal/client"
	"quack/internal/config"
	"quack/internal/device"
	"quack/internal/step"
	"quack/internal/wire"
)

type script struct {
	path    string
	recipes []Recipe
	code    string
}

// Stack is innermost first.
type ScriptError struct {
	Err   error
	Stack []string
}

func (e *ScriptError) Error() string {
	text := e.Err.Error()
	for _, frame := range e.Stack {
		text += "\n    at " + frame
	}
	return text
}

func (e *ScriptError) Unwrap() error { return e.Err }

// ~/.config on every OS when XDG_CONFIG_HOME is unset or empty.
func Dir() string { return filepath.Join(config.Dir(), "recipes") }

func loadScript(path string) (*script, error) {
	code, err := bundle(path)
	if err != nil {
		return nil, err
	}
	s := &script{path: path, code: code}
	r := newRuntime(s, nil, nil, nil, func(step.Event) {})
	if err := r.load(); err != nil {
		return nil, err
	}
	named, err := r.named()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, declared := range named {
		s.recipes = append(s.recipes, declared.Recipe)
	}
	return s, nil
}

func (s *script) run(ctx context.Context, app step.App, actions []wire.Action, name string, args map[string]wire.RawArg, emit func(step.Event)) (json.RawMessage, error) {
	r := newRuntime(s, ctx, app, actions, emit)
	if err := r.load(); err != nil {
		return nil, err
	}
	named, err := r.named()
	if err != nil {
		return nil, err
	}
	declared := named[slices.IndexFunc(named, func(d declaredRecipe) bool { return d.Name == name })]
	stop := context.AfterFunc(ctx, func() { r.vm.Interrupt(client.ErrCanceled) })
	defer stop()
	data, threw, err := r.call(declared, args, false)
	if threw != nil {
		return nil, r.thrown(threw)
	}
	return data, err
}

func (r *runtime) call(declared declaredRecipe, args map[string]wire.RawArg, nested bool) (data json.RawMessage, threw, err error) {
	raw, err := recipeArgs(declared.Recipe, args)
	if err != nil {
		return nil, nil, err
	}
	for _, variable := range declared.env {
		r.env.Get(variable)
	}
	if nested {
		r.do(step.RunRecipe{Name: declared.Name, Params: declared.Params, Args: raw}, nil)
	}
	returned, threw := declared.run(goja.Undefined(), r.vm.ToValue(argValues(declared.Recipe, raw)))
	if threw != nil {
		return nil, threw, nil
	}
	data, err = r.stringify(returned, declared.Name)
	return data, nil, err
}

type runtime struct {
	script  *script
	vm      *goja.Runtime
	ctx     context.Context
	app     step.App
	actions []wire.Action
	emit    func(step.Event)
	steps   int
	recipes map[*goja.Object]declaredRecipe
	env     *environment
	depth   int
}

// Each runRecipe and each JS call takes Go stack, whose overflow kills quack rather than failing the recipe.
const (
	maxRecipeDepth = 100
	maxCallDepth   = 10000
)

type declaredRecipe struct {
	Recipe
	run goja.Callable
}

func (r *runtime) caller() string {
	for _, frame := range r.vm.CaptureCallStack(0, nil) {
		if file := frame.Position().Filename; file != "" {
			return filepath.Join(filepath.Dir(r.script.path), file)
		}
	}
	return ""
}

func newRuntime(s *script, ctx context.Context, app step.App, actions []wire.Action, emit func(step.Event)) *runtime {
	r := &runtime{script: s, vm: goja.New(), ctx: ctx, app: app, actions: actions, emit: emit, recipes: map[*goja.Object]declaredRecipe{}}
	r.env = newEnvironment(r.vm)
	r.vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	r.vm.SetMaxCallStackSize(maxCallDepth)
	return r
}

func (r *runtime) load() error {
	vm := r.vm
	quack := vm.NewObject()
	target := device.Target{}
	if r.app != nil {
		target = r.app.Target()
	}
	for name, value := range map[string]any{
		"recipe":    r.recipe,
		"runAction": r.runAction,
		"runRecipe": r.runRecipe,
		"fail":      func(message string) { panic(r.vm.NewGoError(&failure{message})) },
		"openApp":   func() { r.do(step.OpenApp{}, nil) },
		"shell":     func(command string) string { return r.do(step.Shell{Command: command}, nil).Message },
		"run":       func(command string) string { return r.do(step.Run{Command: command}, nil).Message },
		"wait":      func(d goja.Value) { r.do(step.Wait{Duration: r.duration(d, "wait")}, nil) },
		"waitUntil": r.waitUntil,
		"app":       map[string]string{"package": target.Package},
		"device":    map[string]string{"serial": string(target.Device)},
		"env":       vm.NewDynamicObject(r.env),
	} {
		_ = quack.Set(name, value)
	}
	console := vm.NewObject()
	_ = console.Set("log", func(call goja.FunctionCall) goja.Value {
		words := make([]string, len(call.Arguments))
		for i, value := range call.Arguments {
			words[i] = value.String()
		}
		r.emit(step.Logged{Number: r.steps, Text: strings.Join(words, " ")})
		return goja.Undefined()
	})
	_ = vm.Set("__quack", quack)
	_ = vm.Set("console", console)
	if _, err := vm.RunScript(filepath.Base(r.script.path), r.script.code); err != nil {
		err = r.thrown(err)
		// The outermost frame is the function the bundle wraps the file in.
		var thrown *ScriptError
		if errors.As(err, &thrown) && len(thrown.Stack) > 0 {
			thrown.Stack = thrown.Stack[:len(thrown.Stack)-1]
		}
		return err
	}
	return nil
}

func (r *runtime) recipe(definition *goja.Object) goja.Value {
	vm := r.vm
	if definition == nil {
		panic(vm.NewTypeError("recipe() takes a definition with a run function"))
	}
	run, ok := goja.AssertFunction(definition.Get("run"))
	if !ok {
		panic(vm.NewTypeError("recipe() takes a definition with a run function"))
	}
	var name string
	if value := definition.Get("name"); value != nil {
		name, _ = value.Export().(string)
	}
	if name == "" {
		panic(vm.NewTypeError("recipe() takes a definition with a name"))
	}
	if !isName(name) {
		panic(vm.NewTypeError(fmt.Sprintf("recipe '%s': its name may hold only ASCII letters, digits, '_', '.' and '-', and must not start with '-'", name)))
	}
	declared := declaredRecipe{Recipe: Recipe{Name: name, File: cmp.Or(r.caller(), r.script.path)}, run: run}
	if description := definition.Get("description"); description != nil && !goja.IsUndefined(description) {
		declared.Description = description.String()
	}
	if params := definition.Get("params"); params != nil && !goja.IsUndefined(params) {
		object := params.ToObject(vm)
		for _, name := range object.Keys() {
			p, variable, err := param(name, object.Get(name).Export())
			if err != nil {
				panic(vm.NewTypeError(err.Error()))
			}
			if variable != "" {
				if declared.env == nil {
					declared.env = map[string]string{}
				}
				declared.env[name] = variable
			}
			declared.Params = append(declared.Params, p)
		}
	}
	handle := vm.NewObject()
	r.recipes[handle] = declared
	return handle
}

// named is the recipes that the script's own file declares, sorted by name. Those of the files it imports run only
// through quack.runRecipe(). A file can be a link, which esbuild follows, so files compare by their real paths.
func (r *runtime) named() ([]declaredRecipe, error) {
	var named []declaredRecipe
	for _, declared := range r.recipes {
		if realPath(declared.File) == realPath(r.script.path) {
			declared.File = r.script.path
			named = append(named, declared)
		}
	}
	slices.SortFunc(named, func(a, b declaredRecipe) int { return strings.Compare(a.Name, b.Name) })
	for i, declared := range named {
		if i > 0 && named[i-1].Name == declared.Name {
			return nil, fmt.Errorf("two recipes are named '%s'", declared.Name)
		}
	}
	return named, nil
}

func param(name string, declaration any) (wire.Param, string, error) {
	fields, ok := declaration.(map[string]any)
	if !ok {
		return wire.Param{}, "", fmt.Errorf("param '%s' must be an object with a type", name)
	}
	p := wire.Param{Name: name, Presence: wire.Optional{}}
	text := func(key string) string { s, _ := fields[key].(string); return s }
	p.Type, p.Description = wire.ParamType(text("type")), text("description")
	if !slices.Contains(paramTypes, p.Type) {
		return wire.Param{}, "", fmt.Errorf("param '%s' has type '%s', not one of %s", name, p.Type, joinTypes())
	}
	for key, list := range map[string]*[]string{"hints": &p.Hints, "choices": &p.Choices} {
		if fields[key] == nil {
			continue
		}
		values, ok := rawValues(fields[key])
		if !ok {
			return wire.Param{}, "", fmt.Errorf("param '%s': %s must be an array of strings", name, key)
		}
		*list = values
	}
	if p.Type == wire.ParamTypeEnum && len(p.Choices) == 0 {
		return wire.Param{}, "", fmt.Errorf("param '%s' is an enum without choices", name)
	}
	if required, _ := fields["required"].(bool); required {
		p.Presence = wire.Required{}
	}
	if value, given := fields["default"]; given && value != nil {
		values, ok := rawValues(value)
		if !ok || slices.ContainsFunc(values, func(v string) bool { return !p.Parses(v) }) {
			return wire.Param{}, "", fmt.Errorf("param '%s': its default is not a %s", name, p.Expected())
		}
		p.Presence = wire.Default{Values: values}
	}
	if fields["env"] == nil {
		return p, "", nil
	}
	variable, ok := fields["env"].(string)
	if !ok || !step.IsVariable(variable) {
		return wire.Param{}, "", fmt.Errorf("param '%s': env must name an environment variable", name)
	}
	if os.Getenv(variable) == "" {
		return p, "", nil
	}
	p.Presence = wire.Default{Values: []string{"${" + variable + "}"}}
	return p, variable, nil
}

var paramTypes = []wire.ParamType{wire.ParamTypeString, wire.ParamTypeInt, wire.ParamTypeLong, wire.ParamTypeDouble,
	wire.ParamTypeBoolean, wire.ParamTypeStringList, wire.ParamTypeEnum}

func joinTypes() string {
	names := make([]string, len(paramTypes))
	for i, t := range paramTypes {
		names[i] = string(t)
	}
	return strings.Join(names, ", ")
}

type actionResult struct {
	Message       string         `json:"message"`
	Data          goja.Value     `json:"data"`
	Notifications []notification `json:"notifications"`
}

type notification struct {
	Message string     `json:"message"`
	Data    goja.Value `json:"data"`
}

func (r *runtime) runAction(id string, args map[string]any) actionResult {
	call := step.Call{Action: id, Args: r.rawArgs(args)}
	notifications := []notification{}
	result := r.do(call, func(n wire.Notification) {
		notifications = append(notifications, notification{Message: n.Message, Data: r.parse(n.Data)})
	})
	return actionResult{Message: result.Message, Data: r.parse(result.Data), Notifications: notifications}
}

func (r *runtime) rawArgs(args map[string]any) map[string]wire.RawArg {
	var raw map[string]wire.RawArg
	for key, value := range args {
		values, ok := rawValues(value)
		if !ok {
			panic(r.vm.NewTypeError(fmt.Sprintf("arg '%s' must be a string, a number, a boolean, or an array of strings", key)))
		}
		if raw == nil {
			raw = map[string]wire.RawArg{}
		}
		raw[key] = values
	}
	return raw
}

func (r *runtime) runRecipe(handle goja.Value, args map[string]any) goja.Value {
	object, _ := handle.(*goja.Object)
	declared, ok := r.recipes[object]
	if !ok {
		panic(r.vm.NewTypeError("runRecipe() takes a recipe that quack.recipe() returned"))
	}
	r.requireRun()
	if r.depth == maxRecipeDepth {
		panic(r.vm.NewGoError(fmt.Errorf("recipes run each other more than %d deep", maxRecipeDepth)))
	}
	r.depth++
	defer func() { r.depth-- }()
	maps.DeleteFunc(args, func(_ string, value any) bool { return value == nil })
	data, threw, err := r.call(declared, r.rawArgs(args), true)
	if threw != nil {
		panic(threw)
	}
	if err != nil {
		panic(r.vm.NewGoError(err))
	}
	if data == nil {
		return goja.Undefined()
	}
	return r.parse(data)
}

func (r *runtime) stringify(value goja.Value, name string) (json.RawMessage, error) {
	stringify, _ := goja.AssertFunction(r.vm.Get("JSON").ToObject(r.vm).Get("stringify"))
	text, err := stringify(goja.Undefined(), value)
	if err != nil {
		if err = r.thrown(err); err == client.ErrCanceled {
			return nil, err
		}
		return nil, fmt.Errorf("recipe '%s' returned a value that JSON cannot hold: %w", name, err)
	}
	if goja.IsUndefined(text) {
		return nil, nil
	}
	return json.RawMessage(text.String()), nil
}

// JSON.parse keeps the keys in the app's order, which Go maps do not.
func (r *runtime) parse(data json.RawMessage) goja.Value {
	if data == nil {
		return goja.Null()
	}
	parse, _ := goja.AssertFunction(r.vm.Get("JSON").ToObject(r.vm).Get("parse"))
	value, _ := parse(goja.Undefined(), r.vm.ToValue(string(data)))
	return value
}

func (r *runtime) requireRun() {
	if r.app == nil {
		panic(r.vm.NewTypeError("the \"quack\" module runs steps only inside a recipe's run"))
	}
}

func (r *runtime) do(s step.Step, collect func(wire.Notification)) wire.MessageResult {
	r.requireRun()
	shown := r.env.hide(s)
	r.steps++
	number := r.steps
	r.emit(step.Started{Number: number, Line: step.Line(shown, r.actions)})
	result, err := step.Do(r.ctx, r.app, s, func(notification wire.Notification) {
		if collect != nil {
			collect(notification)
		}
		r.emit(step.Notified{Number: number, Notification: notification})
	})
	if err != nil {
		r.emit(step.Failed{Number: number, Err: err})
		panic(r.vm.NewGoError(err))
	}
	r.emit(step.Ended{Number: number, Step: shown, Result: result})
	return result
}

func (r *runtime) waitUntil(check goja.Callable, options *goja.Object) goja.Value {
	timeout, every := 10*time.Second, 500*time.Millisecond
	if options != nil {
		if value := options.Get("timeout"); value != nil && !goja.IsUndefined(value) {
			timeout = r.duration(value, "waitUntil timeout")
		}
		if value := options.Get("every"); value != nil && !goja.IsUndefined(value) {
			every = r.duration(value, "waitUntil every")
		}
	}
	deadline := time.Now().Add(timeout)
	for {
		done, err := check(goja.Undefined())
		if err != nil {
			panic(err)
		}
		if done.ToBoolean() {
			return done
		}
		if time.Now().Add(every).After(deadline) {
			panic(r.vm.NewGoError(fmt.Errorf("waitUntil: the check did not pass within %s", timeout)))
		}
		r.requireRun()
		if _, err := step.Do(r.ctx, r.app, step.Wait{Duration: every}, nil); err != nil {
			panic(r.vm.NewGoError(err))
		}
	}
}

func (r *runtime) duration(value goja.Value, what string) time.Duration {
	var d time.Duration
	var err error
	switch v := value.Export().(type) {
	case string:
		d, err = time.ParseDuration(v)
	case int64:
		d = time.Duration(v) * time.Millisecond
	case float64:
		d = time.Duration(v * float64(time.Millisecond))
	default:
		err = errors.New("not a duration")
	}
	if err != nil || d <= 0 {
		panic(r.vm.NewTypeError(fmt.Sprintf("%s takes a duration above zero, such as \"2s\", \"500ms\" or 500, not %s", what, value)))
	}
	return d
}

type failure struct {
	message string
}

func (f *failure) Error() string { return f.message }

func (r *runtime) thrown(err error) error {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		return client.ErrCanceled
	}
	var overflow *goja.StackOverflowError
	if errors.As(err, &overflow) {
		return fmt.Errorf("the recipe calls functions more than %d deep", maxCallDepth)
	}
	var exception *goja.Exception
	if !errors.As(err, &exception) {
		return err
	}
	cause := exception.Unwrap()
	var failed *failure
	if errors.As(cause, &failed) {
		return failed
	}
	if cause == nil {
		cause = errors.New(exception.Value().String())
	}
	var stack []string
	for _, frame := range exception.Stack() {
		position := frame.Position()
		if frame.SrcName() == "" || position.Filename == "" {
			continue
		}
		place := fmt.Sprintf("%s:%d:%d", position.Filename, position.Line, position.Column)
		if name := frame.FuncName(); name != "" && name != "<anonymous>" {
			place = name + " (" + place + ")"
		}
		stack = append(stack, place)
	}
	return &ScriptError{Err: cause, Stack: stack}
}

func recipeArgs(r Recipe, args map[string]wire.RawArg) (map[string]wire.RawArg, error) {
	fail := func(format string, a ...any) error {
		return wire.Error{Code: wire.ErrorCodeInvalidArgs, Message: fmt.Sprintf("recipe '%s': ", r.Name) + fmt.Sprintf(format, a...)}
	}
	for _, key := range slices.Sorted(maps.Keys(args)) {
		if !slices.ContainsFunc(r.Params, func(p wire.Param) bool { return p.Name == key }) {
			return nil, fail("unknown argument '%s'", key)
		}
	}
	values := map[string]wire.RawArg{}
	for _, p := range r.Params {
		raw, given := args[p.Name]
		if !given {
			switch presence := p.Presence.(type) {
			case wire.Required:
				return nil, fail("missing argument '%s'", p.Name)
			case wire.Default:
				raw = presence.Values
				if variable, ok := r.env[p.Name]; ok {
					raw = wire.RawArg{os.Getenv(variable)}
				}
			default:
				continue
			}
		}
		switch {
		case !p.Type.IsList() && len(raw) > 1:
			return nil, fail("'%s' given more than once", p.Name)
		case !p.Type.IsList() && len(raw) == 0:
			return nil, fail("'%s' expects %s, got none", p.Name, p.Expected())
		}
		for _, value := range raw {
			if !p.Parses(value) {
				return nil, fail("'%s' expects %s, got '%s'", p.Name, p.Expected(), value)
			}
		}
		values[p.Name] = raw
	}
	return values, nil
}

func argValues(r Recipe, args map[string]wire.RawArg) map[string]any {
	values := map[string]any{}
	for _, p := range r.Params {
		if raw, given := args[p.Name]; given {
			values[p.Name] = argValue(p, raw)
		}
	}
	return values
}

func argValue(p wire.Param, raw wire.RawArg) any {
	switch p.Type {
	case wire.ParamTypeStringList:
		return []string(raw)
	case wire.ParamTypeInt, wire.ParamTypeLong:
		n, _ := strconv.ParseInt(raw[0], 10, 64)
		return n
	case wire.ParamTypeDouble:
		f, _ := strconv.ParseFloat(raw[0], 64)
		return f
	case wire.ParamTypeBoolean:
		return raw[0] == "true"
	case wire.ParamTypeEnum:
		at := slices.IndexFunc(p.Choices, func(c string) bool { return strings.EqualFold(c, raw[0]) })
		return p.Choices[at]
	}
	return raw[0]
}

func rawValues(value any) (wire.RawArg, bool) {
	var items []any
	switch v := value.(type) {
	case []any:
		items = v
	case []string:
		for _, s := range v {
			items = append(items, s)
		}
	default:
		items = []any{value}
	}
	values := wire.RawArg{}
	for _, item := range items {
		switch item := item.(type) {
		case string:
			values = append(values, item)
		case int64:
			values = append(values, strconv.FormatInt(item, 10))
		case float64:
			values = append(values, strconv.FormatFloat(item, 'g', -1, 64))
		case bool:
			values = append(values, strconv.FormatBool(item))
		default:
			return nil, false
		}
	}
	return values, true
}

// The inline source map lets errors point into the files as written.
func bundle(path string) (string, error) {
	quack := api.Plugin{Name: "quack", Setup: func(build api.PluginBuild) {
		build.OnResolve(api.OnResolveOptions{Filter: `^quack$`}, func(api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: "quack", Namespace: "quack"}, nil
		})
		build.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "quack"}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
			contents := "module.exports = __quack"
			return api.OnLoadResult{Contents: &contents, Loader: api.LoaderJS}, nil
		})
	}}
	result := api.Build(api.BuildOptions{
		EntryPoints:   []string{path},
		AbsWorkingDir: filepath.Dir(path),
		Bundle:        true,
		Format:        api.FormatIIFE,
		Platform:      api.PlatformNeutral,
		Target:        api.ES2017,
		Sourcemap:     api.SourceMapInline,
		Plugins:       []api.Plugin{quack},
		LogLevel:      api.LogLevelSilent,
	})
	if len(result.Errors) > 0 {
		var lines []string
		for _, message := range result.Errors {
			if at := message.Location; at != nil {
				lines = append(lines, fmt.Sprintf("%s:%d:%d: %s", filepath.Join(filepath.Dir(path), at.File), at.Line, at.Column+1, message.Text))
			} else {
				lines = append(lines, path+": "+message.Text)
			}
		}
		return "", errors.New(strings.Join(lines, "\n"))
	}
	return string(result.OutputFiles[0].Contents), nil
}

type environment struct {
	vm     *goja.Runtime
	values map[string]string
	read   map[string]string
}

func newEnvironment(vm *goja.Runtime) *environment {
	e := &environment{vm: vm, values: map[string]string{}, read: map[string]string{}}
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			e.values[name] = value
		}
	}
	return e
}

func (e *environment) Get(name string) goja.Value {
	value, ok := e.values[name]
	if !ok {
		return goja.Undefined()
	}
	e.read[name] = value
	return e.vm.ToValue(value)
}

func (e *environment) Set(string, goja.Value) bool { return false }

func (e *environment) Has(name string) bool {
	_, ok := e.values[name]
	return ok
}

func (e *environment) Delete(string) bool { return false }

func (e *environment) Keys() []string { return slices.Sorted(maps.Keys(e.values)) }

func (e *environment) hide(s step.Step) step.Step {
	var pairs []string
	for _, name := range slices.SortedFunc(maps.Keys(e.read), func(a, b string) int { return len(e.read[b]) - len(e.read[a]) }) {
		if e.read[name] != "" {
			pairs = append(pairs, e.read[name], "${"+name+"}")
		}
	}
	if pairs == nil {
		return s
	}
	replacer := strings.NewReplacer(pairs...)
	replace := func(args map[string]wire.RawArg) map[string]wire.RawArg {
		replaced := make(map[string]wire.RawArg, len(args))
		for key, values := range args {
			replaced[key] = slices.Clone(values)
			for i, value := range values {
				replaced[key][i] = replacer.Replace(value)
			}
		}
		return replaced
	}
	switch s := s.(type) {
	case step.Call:
		s.Args = replace(s.Args)
		return s
	case step.RunRecipe:
		s.Args = replace(s.Args)
		return s
	case step.Shell:
		return step.Shell{Command: replacer.Replace(s.Command)}
	case step.Run:
		return step.Run{Command: replacer.Replace(s.Command)}
	}
	return s
}
