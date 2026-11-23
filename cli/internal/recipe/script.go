package recipe

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
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
	"quack/internal/wire"
)

type Script struct {
	Path string
	// Sorted by name.
	Recipes []Recipe
	code    string
}

// Started and then Ended or Failed frame each Step, with the notifications of a Call between them. Any of them
// may be nil.
type Observer struct {
	Started  func(step Step)
	Notified func(message string)
	Ended    func(step Step, result wire.MessageResult)
	Failed   func(step Step, err error)
	Logged   func(text string)
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

var (
	//go:embed types/quack.d.ts
	quackTypes []byte
	//go:embed types/tsconfig.json
	tsconfig []byte
	//go:embed types/starter.ts
	starter []byte
)

// ~/.config on every OS when XDG_CONFIG_HOME is unset or empty.
func Dir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "quack", "recipes")
}

func Path(pkg string) string {
	js := filepath.Join(Dir(), pkg+".js")
	if _, err := os.Stat(js); err == nil && !exists(filepath.Join(Dir(), pkg+".ts")) {
		return js
	}
	return filepath.Join(Dir(), pkg+".ts")
}

// Load returns nil without a recipes file. It also brings quack.d.ts and tsconfig.json next to recipes files up to
// date, so that editors check recipes against the "quack" this quack runs.
func Load(pkg string) (*Script, error) {
	ts, js := filepath.Join(Dir(), pkg+".ts"), filepath.Join(Dir(), pkg+".js")
	switch {
	case exists(ts) && exists(js):
		return nil, fmt.Errorf("%s and %s both hold recipes of %s: keep one", ts, js, pkg)
	case !exists(ts) && !exists(js):
		return nil, nil
	}
	// Recipes still run where the editor files cannot be written, such as in a read-only config dir.
	_, _ = writeOwned()
	path := Path(pkg)
	code, err := bundle(path)
	if err != nil {
		return nil, err
	}
	s := &Script{Path: path, code: code}
	r := newRuntime(s, nil, nil, Observer{})
	exports, err := r.load()
	if err != nil {
		return nil, err
	}
	named, err := r.named(exports)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, declared := range named {
		s.Recipes = append(s.Recipes, declared.Recipe)
	}
	return s, nil
}

func Init(pkg string) ([]string, error) {
	written, err := writeOwned()
	if err != nil || pkg == "" {
		return written, err
	}
	ts, js := filepath.Join(Dir(), pkg+".ts"), filepath.Join(Dir(), pkg+".js")
	if exists(ts) || exists(js) {
		return written, nil
	}
	if err := os.WriteFile(ts, starter, 0o644); err != nil {
		return nil, err
	}
	return append(written, ts), nil
}

func writeOwned() ([]string, error) {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return nil, err
	}
	var written []string
	for name, content := range map[string][]byte{"quack.d.ts": quackTypes, "tsconfig.json": tsconfig} {
		path := filepath.Join(Dir(), name)
		if current, err := os.ReadFile(path); err == nil && string(current) == string(content) {
			continue
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return nil, err
		}
		written = append(written, path)
	}
	slices.Sort(written)
	return written, nil
}

// Ending ctx interrupts the recipe wherever it is.
func (s *Script) Run(ctx context.Context, app App, name string, args map[string]wire.RawArg, observer Observer) error {
	at := slices.IndexFunc(s.Recipes, func(r Recipe) bool { return r.Name == name })
	if at < 0 {
		return fmt.Errorf("no recipe named %s in %s", name, s.Path)
	}
	values, err := recipeArgs(s.Recipes[at], args)
	if err != nil {
		return err
	}
	r := newRuntime(s, ctx, app, observer)
	// Steps show the variables of params by name too, as if the recipe read them.
	for _, variable := range s.Recipes[at].env {
		r.env.Get(variable)
	}
	exports, err := r.load()
	if err != nil {
		return err
	}
	// The bundle's exports are getters, which an interrupt would panic out of, so the recipe is found first.
	named, err := r.named(exports)
	if err != nil {
		return err
	}
	run := named[slices.IndexFunc(named, func(d declaredRecipe) bool { return d.Name == name })].run
	stop := context.AfterFunc(ctx, func() { r.vm.Interrupt(canceled) })
	defer stop()
	if _, err := run(goja.Undefined(), r.vm.ToValue(values)); err != nil {
		return r.thrown(err)
	}
	return nil
}

var canceled = &client.CallError{Message: "canceled"}

// app is nil for a load, which only reads what the script declares.
type runtime struct {
	script   *Script
	vm       *goja.Runtime
	ctx      context.Context
	app      App
	observer Observer
	recipes  map[goja.Value]declaredRecipe
	env      *environment
}

type declaredRecipe struct {
	Recipe
	run goja.Callable
}

func newRuntime(s *Script, ctx context.Context, app App, observer Observer) *runtime {
	r := &runtime{script: s, vm: goja.New(), ctx: ctx, app: app, observer: observer, recipes: map[goja.Value]declaredRecipe{}}
	r.env = newEnvironment(r.vm)
	r.vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	return r
}

func (r *runtime) load() (*goja.Object, error) {
	vm := r.vm
	quack := vm.NewObject()
	target := client.Target{}
	if r.app != nil {
		target = r.app.Target()
	}
	for name, value := range map[string]any{
		"recipe":    r.recipe,
		"runAction": r.runAction,
		"openApp":   func() { r.do(OpenApp{}, nil) },
		"shell":     func(command string) string { return r.do(Shell{command}, nil).Message },
		"run":       func(command string) string { return r.do(Run{command}, nil).Message },
		"wait":      func(d goja.Value) { r.do(Wait{r.duration(d, "wait")}, nil) },
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
		if r.observer.Logged != nil {
			r.observer.Logged(strings.Join(words, " "))
		}
		return goja.Undefined()
	})
	_ = vm.Set("__quack", quack)
	_ = vm.Set("console", console)
	if _, err := vm.RunScript(filepath.Base(r.script.Path), r.script.code); err != nil {
		err = r.thrown(err)
		// The outermost frame is the function the bundle wraps the file in.
		var thrown *ScriptError
		if errors.As(err, &thrown) && len(thrown.Stack) > 0 {
			thrown.Stack = thrown.Stack[:len(thrown.Stack)-1]
		}
		return nil, err
	}
	exports, ok := vm.Get("__recipes").(*goja.Object)
	if !ok {
		exports = vm.NewObject()
	}
	return exports, nil
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
	declared := declaredRecipe{run: run}
	if description := definition.Get("description"); description != nil && !goja.IsUndefined(description) {
		declared.Description = description.String()
	}
	if name := definition.Get("name"); name != nil && !goja.IsUndefined(name) {
		declared.Name = name.String()
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
	// The object stands for the recipe, so that Load finds which exports are recipes.
	handle := vm.NewObject()
	r.recipes[handle] = declared
	return handle
}

// The variable comes back only while it is set, and the param then shows it as its default.
// A recipe without a name of its own takes the name of each export that holds it, and is left out without one.
func (r *runtime) named(exports *goja.Object) ([]declaredRecipe, error) {
	var named []declaredRecipe
	for _, declared := range r.recipes {
		if declared.Name != "" {
			named = append(named, declared)
		}
	}
	for _, key := range exports.Keys() {
		if declared, ok := r.recipes[exports.Get(key)]; ok && declared.Name == "" {
			declared.Name = key
			named = append(named, declared)
		}
	}
	slices.SortFunc(named, func(a, b declaredRecipe) int { return strings.Compare(a.Name, b.Name) })
	for i, declared := range named {
		if !isName(declared.Name) {
			return nil, fmt.Errorf("recipe '%s': its name must not start with '$'", declared.Name)
		}
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
		if !ok || slices.ContainsFunc(values, func(v string) bool { return !parses(p, v) }) {
			return wire.Param{}, "", fmt.Errorf("param '%s': its default is not a %s", name, expected(p))
		}
		p.Presence = wire.Default{Values: values}
	}
	if fields["env"] == nil {
		return p, "", nil
	}
	variable, ok := fields["env"].(string)
	if !ok || !isVariable(variable) {
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
	Message       string     `json:"message"`
	Data          goja.Value `json:"data"`
	Notifications []string   `json:"notifications"`
}

func (r *runtime) runAction(id string, args map[string]any) actionResult {
	call := Call{Action: id}
	for key, value := range args {
		values, ok := rawValues(value)
		if !ok {
			panic(r.vm.NewTypeError(fmt.Sprintf("arg '%s' must be a string, a number, a boolean, or an array of strings", key)))
		}
		if call.Args == nil {
			call.Args = map[string]wire.RawArg{}
		}
		call.Args[key] = values
	}
	notifications := []string{}
	result := r.do(call, func(message string) { notifications = append(notifications, message) })
	out := actionResult{Message: result.Message, Data: goja.Null(), Notifications: notifications}
	if result.Data != nil {
		// JSON.parse keeps the keys in the app's order, which Go maps do not. The app sends valid JSON.
		parse, _ := goja.AssertFunction(r.vm.Get("JSON").ToObject(r.vm).Get("parse"))
		out.Data, _ = parse(goja.Undefined(), r.vm.ToValue(string(result.Data)))
	}
	return out
}

func (r *runtime) do(step Step, collect func(string)) wire.MessageResult {
	if r.app == nil {
		panic(r.vm.NewTypeError("the \"quack\" module runs steps only inside a recipe's run"))
	}
	shown := r.env.hide(step)
	if r.observer.Started != nil {
		r.observer.Started(shown)
	}
	result, err := Do(r.ctx, r.app, step, func(message string) {
		if collect != nil {
			collect(message)
		}
		if r.observer.Notified != nil {
			r.observer.Notified(message)
		}
	})
	if err != nil {
		if r.observer.Failed != nil {
			r.observer.Failed(shown, err)
		}
		panic(r.vm.NewGoError(err))
	}
	if r.observer.Ended != nil {
		r.observer.Ended(shown, result)
	}
	return result
}

// Its waits between checks are no steps of their own.
func (r *runtime) waitUntil(check goja.Callable, options *goja.Object) {
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
			return
		}
		if time.Now().Add(every).After(deadline) {
			panic(r.vm.NewGoError(fmt.Errorf("waitUntil: the check did not pass within %s", timeout)))
		}
		if r.app == nil {
			panic(r.vm.NewTypeError("the \"quack\" module runs steps only inside a recipe's run"))
		}
		if _, err := Do(r.ctx, r.app, Wait{every}, nil); err != nil {
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

// A goja interrupt is the run's ctx ending.
func (r *runtime) thrown(err error) error {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		return canceled
	}
	var exception *goja.Exception
	if !errors.As(err, &exception) {
		return err
	}
	cause := exception.Unwrap()
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

// A param without a value and without a default stays out.
func recipeArgs(r Recipe, args map[string]wire.RawArg) (map[string]any, error) {
	fail := func(format string, a ...any) error {
		return wire.Error{Code: wire.ErrorCodeInvalidArgs, Message: fmt.Sprintf("recipe '%s': ", r.Name) + fmt.Sprintf(format, a...)}
	}
	for _, key := range slices.Sorted(maps.Keys(args)) {
		if !slices.ContainsFunc(r.Params, func(p wire.Param) bool { return p.Name == key }) {
			return nil, fail("unknown argument '%s'", key)
		}
	}
	values := map[string]any{}
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
			return nil, fail("'%s' expects %s, got none", p.Name, expected(p))
		}
		for _, value := range raw {
			if !parses(p, value) {
				return nil, fail("'%s' expects %s, got '%s'", p.Name, expected(p), value)
			}
		}
		values[p.Name] = argValue(p, raw)
	}
	return values, nil
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
		GlobalName:    "__recipes",
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

// Remembers the variables the recipe reads, so that its steps show them by name, as the recipe would write them.
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

func (e *environment) hide(step Step) Step {
	var pairs []string
	// Longest first, so that a value inside another does not split it.
	for _, name := range slices.SortedFunc(maps.Keys(e.read), func(a, b string) int { return len(e.read[b]) - len(e.read[a]) }) {
		if e.read[name] != "" {
			pairs = append(pairs, e.read[name], "${"+name+"}")
		}
	}
	if pairs == nil {
		return step
	}
	replacer := strings.NewReplacer(pairs...)
	switch step := step.(type) {
	case Call:
		args := make(map[string]wire.RawArg, len(step.Args))
		for key, values := range step.Args {
			args[key] = slices.Clone(values)
			for i, value := range values {
				args[key][i] = replacer.Replace(value)
			}
		}
		step.Args = args
		return step
	case Shell:
		return Shell{replacer.Replace(step.Command)}
	case Run:
		return Run{replacer.Replace(step.Command)}
	}
	return step
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
