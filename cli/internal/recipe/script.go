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

// Script is the recipes file of an app, bundled with the files it imports.
type Script struct {
	Path string
	// Recipes are the recipes the file exports, sorted by name.
	Recipes []Recipe
	code    string
}

// Observer hears what a run does. Started and then Ended or Failed frame each Step, with the notifications of a Call
// between them. Logged is a line that console.log wrote. The error of a Step that failed then ends the run, unless the
// recipe catches it. Any of them may be nil.
type Observer struct {
	Started  func(step Step)
	Notified func(message string)
	Ended    func(step Step, result wire.MessageResult)
	Failed   func(step Step, err error)
	Logged   func(text string)
}

// ScriptError is a recipe that threw: what it threw, which is the Go error of a failed Step when it threw that, and
// where in the recipes file it was, innermost first.
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

// Dir is where recipes files are: quack/recipes in $XDG_CONFIG_HOME, or in ~/.config when that is unset or empty, on
// every OS.
func Dir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "quack", "recipes")
}

// Path is the recipes file of pkg: <pkg>.js when only that exists, and <pkg>.ts otherwise.
func Path(pkg string) string {
	js := filepath.Join(Dir(), pkg+".js")
	if _, err := os.Stat(js); err == nil && !exists(filepath.Join(Dir(), pkg+".ts")) {
		return js
	}
	return filepath.Join(Dir(), pkg+".ts")
}

// Load reads the recipes of pkg, or returns nil when pkg has no recipes file. A file that does not bundle, throws as it
// loads, or declares a recipe wrong fails with an error that says where. Load also brings the files that quack owns next
// to recipes files up to date with this quack, so that editors check recipes against the "quack" quack runs.
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
	for _, name := range slices.Sorted(slices.Values(exports.Keys())) {
		if declared, ok := r.recipes[exports.Get(name)]; ok {
			if !isName(name) {
				return nil, fmt.Errorf("%s: recipe '%s': its name must not start with '$'", path, name)
			}
			declared.Name = name
			s.Recipes = append(s.Recipes, declared.Recipe)
		}
	}
	return s, nil
}

// Init writes the files that quack owns into Dir, so that editors check recipes files there before quack loads any,
// and with pkg also a starter recipes file of pkg when it has none. It returns the files it wrote: none when they are
// up to date and pkg has its file.
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

// writeOwned writes quack.d.ts and tsconfig.json into Dir, where they differ from this quack's, and returns the ones it
// wrote.
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

// Run runs the recipe name against app with args, which the recipe's params check and convert as an action's params
// would. It ends when the recipe returns or throws, or when ctx ends, which interrupts the recipe wherever it is.
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
	exports, err := r.load()
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { r.vm.Interrupt(canceled) })
	defer stop()
	run := r.recipes[exports.Get(name)].run
	if _, err := run(goja.Undefined(), r.vm.ToValue(values)); err != nil {
		return r.thrown(err)
	}
	return nil
}

// canceled is the error of a run that ctx ended.
var canceled = &client.CallError{Message: "canceled"}

// runtime is a goja runtime with the "quack" module, for one load or run of a script. Its app is nil for a load, which
// only reads what the script declares.
type runtime struct {
	script   *Script
	vm       *goja.Runtime
	ctx      context.Context
	app      App
	observer Observer
	// recipes are the recipes the script declared, by the object recipe() returned for each.
	recipes map[goja.Value]declaredRecipe
}

type declaredRecipe struct {
	Recipe
	run goja.Callable
}

func newRuntime(s *Script, ctx context.Context, app App, observer Observer) *runtime {
	r := &runtime{script: s, vm: goja.New(), ctx: ctx, app: app, observer: observer, recipes: map[goja.Value]declaredRecipe{}}
	r.vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	return r
}

// load runs the script's top level with the "quack" module and console, and returns its exports.
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
		"openApp":   func() { r.step(OpenApp{}) },
		"shell":     func(command string) string { return r.step(Shell{command}).Message },
		"run":       func(command string) string { return r.step(Run{command}).Message },
		"wait":      func(d goja.Value) { r.step(Wait{r.duration(d, "wait")}) },
		"waitUntil": r.waitUntil,
		"app":       map[string]string{"package": target.Package},
		"device":    map[string]string{"serial": string(target.Device)},
		"env":       environment(),
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
	// A file without exports has no recipes.
	exports, ok := vm.Get("__recipes").(*goja.Object)
	if !ok {
		exports = vm.NewObject()
	}
	return exports, nil
}

// recipe is recipe() of the "quack" module, which declares a recipe from its definition.
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
	if params := definition.Get("params"); params != nil && !goja.IsUndefined(params) {
		object := params.ToObject(vm)
		for _, name := range object.Keys() {
			p, err := param(name, object.Get(name).Export())
			if err != nil {
				panic(vm.NewTypeError(err.Error()))
			}
			declared.Params = append(declared.Params, p)
		}
	}
	// The object stands for the recipe, so that Load finds which exports are recipes.
	handle := vm.NewObject()
	r.recipes[handle] = declared
	return handle
}

// param reads the declaration of the param name, as quack.d.ts types it.
func param(name string, declaration any) (wire.Param, error) {
	fields, ok := declaration.(map[string]any)
	if !ok {
		return wire.Param{}, fmt.Errorf("param '%s' must be an object with a type", name)
	}
	p := wire.Param{Name: name, Presence: wire.Optional{}}
	text := func(key string) string { s, _ := fields[key].(string); return s }
	p.Type, p.Description = wire.ParamType(text("type")), text("description")
	if !slices.Contains(paramTypes, p.Type) {
		return wire.Param{}, fmt.Errorf("param '%s' has type '%s', not one of %s", name, p.Type, joinTypes())
	}
	for key, list := range map[string]*[]string{"hints": &p.Hints, "choices": &p.Choices} {
		if fields[key] == nil {
			continue
		}
		values, ok := rawValues(fields[key])
		if !ok {
			return wire.Param{}, fmt.Errorf("param '%s': %s must be an array of strings", name, key)
		}
		*list = values
	}
	if p.Type == wire.ParamTypeEnum && len(p.Choices) == 0 {
		return wire.Param{}, fmt.Errorf("param '%s' is an enum without choices", name)
	}
	if required, _ := fields["required"].(bool); required {
		p.Presence = wire.Required{}
	}
	if value, given := fields["default"]; given && value != nil {
		values, ok := rawValues(value)
		if !ok || slices.ContainsFunc(values, func(v string) bool { return !parses(p, v) }) {
			return wire.Param{}, fmt.Errorf("param '%s': its default is not a %s", name, expected(p))
		}
		p.Presence = wire.Default{Values: values}
	}
	return p, nil
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

// actionResult is what runAction() returns.
type actionResult struct {
	Message       string     `json:"message"`
	Data          goja.Value `json:"data"`
	Notifications []string   `json:"notifications"`
}

// runAction is runAction() of the "quack" module.
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
	var notifications []string
	result := r.do(call, func(message string) { notifications = append(notifications, message) })
	out := actionResult{Message: result.Message, Data: goja.Null(), Notifications: append([]string{}, notifications...)}
	if result.Data != nil {
		// JSON.parse keeps the keys in the app's order, which Go maps do not. The app sends valid JSON.
		parse, _ := goja.AssertFunction(r.vm.Get("JSON").ToObject(r.vm).Get("parse"))
		out.Data, _ = parse(goja.Undefined(), r.vm.ToValue(string(result.Data)))
	}
	return out
}

// step runs step in the app, and throws its error into the script when it fails.
func (r *runtime) step(step Step) wire.MessageResult { return r.do(step, nil) }

func (r *runtime) do(step Step, collect func(string)) wire.MessageResult {
	if r.app == nil {
		panic(r.vm.NewTypeError("the \"quack\" module runs steps only inside a recipe's run"))
	}
	if r.observer.Started != nil {
		r.observer.Started(step)
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
			r.observer.Failed(step, err)
		}
		panic(r.vm.NewGoError(err))
	}
	if r.observer.Ended != nil {
		r.observer.Ended(step, result)
	}
	return result
}

// waitUntil is waitUntil() of the "quack" module. Its waits between checks are no steps of their own.
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

// duration reads a duration of the "quack" module: text such as 2s or 500ms, or a number of milliseconds, above zero.
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

// thrown is the error of what the script threw: the Go error of a failed Step, or the script's own error, with where it
// was thrown. An interrupt is the run's ctx ending.
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

// recipeArgs checks args against the params of r, as the app checks the args of an action, and converts them to the
// values the recipe's run gets: numbers, booleans, strings, and arrays of strings. A param without a value and without a
// default stays out.
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

// argValue is the value of p, which parses, as the recipe's run gets it.
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

// rawValues are the values of an arg as the command line would give them: one for a scalar, and one for each item of
// an array.
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

// bundle bundles the recipes file at path with the local files it imports into one script that sets __recipes to its
// exports, with "quack" bound to __quack. An inline source map lets errors point into the files as written.
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

// environment is env of the "quack" module: quack's environment variables by name.
func environment() map[string]string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			env[name] = value
		}
	}
	return env
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
