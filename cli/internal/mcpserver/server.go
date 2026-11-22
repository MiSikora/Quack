package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/recipe"
	"quack/internal/wire"
)

type Source interface {
	Devices() ([]client.Device, error)
	// device and pkg may be "" for any, as in client.Find.
	Find(device adb.Device, pkg string) (client.Target, error)
	Connect(target client.Target, mode client.StartMode) (App, error)
	// Recipes is nil without a recipes file.
	Recipes(pkg string) (*recipe.Script, string, error)
}

type App interface {
	recipe.App
	Describe(ctx context.Context) (wire.Hello, []wire.Action, error)
}

func Serve(ctx context.Context, version string, startTimeout time.Duration, status io.Writer) error {
	return New(clientSource{startTimeout, status}, version).Run(ctx, &mcp.StdioTransport{})
}

func New(source Source, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "quack", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	s := tools{source}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_devices",
		Description: "List the devices adb knows, each with its state and the quack-enabled apps on it.",
	}, s.listDevices)
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_actions",
		Description: "List the actions of a quack-enabled app with their params. A string param may give hints: values " +
			"the app suggests, such as known names, though others work too. " + targetHelp,
	}, s.listActions)
	mcp.AddTool(server, &mcp.Tool{
		Name: "run_action",
		Description: "Run an action of a quack-enabled app and return its notifications, its message and its data. " +
			"Get the action ids and their params from list_actions. The args reach the app as given: ${NAME} stays as " +
			"it is. " + askHelp + targetHelp,
		InputSchema:  schemaFor[runActionInput](),
		OutputSchema: schemaFor[runActionOutput](),
	}, s.runAction)
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_recipes",
		Description: "List the recipes of a quack-enabled app with their params: scripts that the user keeps in a file " +
			"on this computer, which run the app's actions, commands on the device or on this computer, and waits, and " +
			"decide what to do with what they return. " + targetHelp,
	}, s.listRecipes)
	mcp.AddTool(server, &mcp.Tool{
		Name: "run_recipe",
		Description: "Run a recipe of a quack-enabled app with args for its params, and return what each of its steps " +
			"gave, a call of an action, /open-app, a wait, or a shell or run command: its notifications, and its message and " +
			"its data or the error it failed with, and the lines the recipe logged. The message of a command is what it " +
			"printed. A step that fails ends the recipe unless the recipe catches it. Get the recipe names and params from list_recipes. " +
			askHelp + targetHelp,
		InputSchema:  schemaFor[runRecipeInput](),
		OutputSchema: schemaFor[runRecipeOutput](),
	}, s.runRecipe)
	return server
}

type tools struct {
	source Source
}

type listDevicesInput struct{}

type listDevicesOutput struct {
	Devices []deviceOutput `json:"devices"`
}

type deviceOutput struct {
	Serial string `json:"serial"`
	Model  string `json:"model"`
	// adb's state: device when ready, or else why not, such as offline or unauthorized.
	State string   `json:"state"`
	Apps  []string `json:"apps"`
}

type listActionsInput struct {
	Device string `json:"device,omitempty" jsonschema:"the device's serial, as list_devices gives it"`
	App    string `json:"app,omitempty" jsonschema:"the app's package, as list_devices gives it"`
	Filter string `json:"filter,omitempty" jsonschema:"a regular expression that the ids of the listed actions match"`
}

type listActionsOutput struct {
	Device  string         `json:"device"`
	App     string         `json:"app"`
	Version string         `json:"version,omitempty"`
	Actions []actionOutput `json:"actions"`
}

type actionOutput struct {
	ID          string        `json:"id"`
	Description string        `json:"description"`
	Params      []paramOutput `json:"params"`
}

type paramOutput struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Choices     []string `json:"choices,omitempty"`
	// Other values work too.
	Hints    []string `json:"hints,omitempty"`
	Required bool     `json:"required"`
	// A string, an array of strings for a list param, or absent without a default.
	Default any `json:"default,omitempty"`
}

type runActionInput struct {
	Action string `json:"action" jsonschema:"the id of the action, as list_actions gives it"`
	// The SDK's validation turns numbers into float64, so a long beyond 2^53 loses precision unless it comes as a
	// string.
	Args    map[string]json.RawMessage `json:"args,omitempty" jsonschema:"the action's params by name: a string, a number or a boolean, or an array of them for a string_list param. Leave out params to use their defaults. Pass a long beyond 2^53 as a string, since JSON numbers that large lose precision on the way."`
	Device  string                     `json:"device,omitempty" jsonschema:"the device's serial, as list_devices gives it"`
	App     string                     `json:"app,omitempty" jsonschema:"the app's package, as list_devices gives it"`
	OpenApp bool                       `json:"open_app,omitempty" jsonschema:"whether to show the app's main screen before the action runs, instead of starting the app in the background"`
}

type listRecipesInput struct {
	Device string `json:"device,omitempty" jsonschema:"the device's serial, as list_devices gives it"`
	App    string `json:"app,omitempty" jsonschema:"the app's package, as list_devices gives it"`
	Filter string `json:"filter,omitempty" jsonschema:"a regular expression that the names of the listed recipes match"`
}

type listRecipesOutput struct {
	Device string `json:"device"`
	App    string `json:"app"`
	// Also where to write recipes when there are none.
	File    string         `json:"file"`
	Recipes []recipeOutput `json:"recipes"`
}

type recipeOutput struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Params      []paramOutput `json:"params"`
}

type runRecipeInput struct {
	Recipe string                     `json:"recipe" jsonschema:"the name of the recipe, as list_recipes gives it"`
	Args   map[string]json.RawMessage `json:"args,omitempty" jsonschema:"the recipe's params by name, as the args of run_action"`
	Device string                     `json:"device,omitempty" jsonschema:"the device's serial, as list_devices gives it"`
	App    string                     `json:"app,omitempty" jsonschema:"the app's package, as list_devices gives it"`
}

type runRecipeOutput struct {
	Steps []stepOutput `json:"steps"`
	Log   []string     `json:"log"`
}

// A failed step has an error instead, which the recipe caught when it went on.
type stepOutput struct {
	Step          string          `json:"step"`
	Notifications []string        `json:"notifications"`
	Message       string          `json:"message"`
	Data          json.RawMessage `json:"data,omitempty"`
	Error         *errorOutput    `json:"error,omitempty"`
}

// The app's error code, command for a command that exited with another status than 0, no_reply for a call that
// ended without the app's answer, and error for anything else.
type errorOutput struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type runActionOutput struct {
	Notifications []string        `json:"notifications"`
	Message       string          `json:"message"`
	Data          json.RawMessage `json:"data,omitempty"`
}

func (s tools) listDevices(context.Context, *mcp.CallToolRequest, listDevicesInput) (*mcp.CallToolResult, listDevicesOutput, error) {
	devices, err := s.source.Devices()
	if err != nil {
		return nil, listDevicesOutput{}, err
	}
	out := listDevicesOutput{Devices: []deviceOutput{}}
	for _, d := range devices {
		apps := d.Packages
		if apps == nil {
			apps = []string{}
		}
		out.Devices = append(out.Devices, deviceOutput{string(d.Device), d.Model, d.State, apps})
	}
	return nil, out, nil
}

func (s tools) listActions(ctx context.Context, _ *mcp.CallToolRequest, in listActionsInput) (*mcp.CallToolResult, listActionsOutput, error) {
	filter, err := regexp.Compile(in.Filter)
	if err != nil {
		return nil, listActionsOutput{}, fmt.Errorf("invalid filter: %v", err)
	}
	target, app, err := s.connect(in.Device, in.App, client.Wake)
	if err != nil {
		return nil, listActionsOutput{}, err
	}
	hello, actions, err := app.Describe(ctx)
	if err != nil {
		return nil, listActionsOutput{}, err
	}
	out := listActionsOutput{Device: string(target.Device), App: target.Package, Version: hello.Version, Actions: []actionOutput{}}
	for _, a := range actions {
		if filter.MatchString(a.ID) {
			out.Actions = append(out.Actions, action(a))
		}
	}
	return nil, out, nil
}

// The client canceling the request cancels the action. A failed action keeps the notifications it sent before
// the error.
func (s tools) runAction(ctx context.Context, req *mcp.CallToolRequest, in runActionInput) (*mcp.CallToolResult, runActionOutput, error) {
	args, err := rawArgs(in.Args)
	if err != nil {
		return nil, runActionOutput{}, err
	}
	mode := client.Wake
	if in.OpenApp {
		mode = client.OpenApp
	}
	_, app, err := s.connect(in.Device, in.App, mode)
	if err != nil {
		return nil, runActionOutput{}, err
	}
	if canElicit(req.Session) {
		_, actions, err := app.Describe(ctx)
		if err != nil {
			return nil, runActionOutput{}, err
		}
		for _, a := range actions {
			if a.ID == in.Action {
				if form, err := askForParams(req, in.Action, a.Params, args); form != nil || err != nil {
					return form, runActionOutput{}, err
				}
			}
		}
	}
	notifications := []string{}
	report := progress(ctx, req)
	result, err := app.Call(ctx, wire.Call{Action: in.Action, Args: args}, func(message string) {
		notifications = append(notifications, message)
		report(message)
	})
	if err != nil {
		if len(notifications) > 0 {
			err = fmt.Errorf("%s\n%w", strings.Join(notifications, "\n"), err)
		}
		return nil, runActionOutput{}, err
	}
	return nil, runActionOutput{Notifications: notifications, Message: result.Message, Data: result.Data}, nil
}

func (s tools) listRecipes(_ context.Context, _ *mcp.CallToolRequest, in listRecipesInput) (*mcp.CallToolResult, listRecipesOutput, error) {
	filter, err := regexp.Compile(in.Filter)
	if err != nil {
		return nil, listRecipesOutput{}, fmt.Errorf("invalid filter: %v", err)
	}
	// Recipes need only the package, so the app does not start.
	target, err := s.source.Find(adb.Device(in.Device), in.App)
	if err != nil {
		return nil, listRecipesOutput{}, err
	}
	script, file, err := s.source.Recipes(target.Package)
	if err != nil {
		return nil, listRecipesOutput{}, err
	}
	out := listRecipesOutput{Device: string(target.Device), App: target.Package, File: file, Recipes: []recipeOutput{}}
	if script == nil {
		return nil, out, nil
	}
	for _, r := range script.Recipes {
		if filter.MatchString(r.Name) {
			params := action(wire.Action{Params: r.Params}).Params
			out.Recipes = append(out.Recipes, recipeOutput{Name: r.Name, Description: r.Description, Params: params})
		}
	}
	return nil, out, nil
}

// Each step's line goes out before its notifications. The client canceling the request interrupts the recipe.
func (s tools) runRecipe(ctx context.Context, req *mcp.CallToolRequest, in runRecipeInput) (*mcp.CallToolResult, runRecipeOutput, error) {
	args, err := rawArgs(in.Args)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	target, app, err := s.connect(in.Device, in.App, client.Wake)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	script, file, err := s.source.Recipes(target.Package)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	if script == nil {
		return nil, runRecipeOutput{}, fmt.Errorf("no recipe named %s in %s", in.Recipe, file)
	}
	_, actions, err := app.Describe(ctx)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	if canElicit(req.Session) {
		for _, r := range script.Recipes {
			if r.Name == in.Recipe {
				if form, err := askForParams(req, "recipe "+in.Recipe, r.Params, args); form != nil || err != nil {
					return form, runRecipeOutput{}, err
				}
			}
		}
	}
	report := progress(ctx, req)
	out := runRecipeOutput{Steps: []stepOutput{}, Log: []string{}}
	var printed []string
	print := func(line string) {
		printed = append(printed, line)
		report(line)
	}
	var done stepOutput
	err = script.Run(ctx, app, in.Recipe, args, recipe.Observer{
		Started: func(step recipe.Step) {
			done = stepOutput{Step: recipe.Line(step, actions), Notifications: []string{}}
			print("› " + done.Step)
		},
		Notified: func(message string) {
			done.Notifications = append(done.Notifications, message)
			print(message)
		},
		Ended: func(_ recipe.Step, result wire.MessageResult) {
			if result.Message != "" {
				printed = append(printed, result.Message)
			}
			done.Message, done.Data = result.Message, result.Data
			out.Steps = append(out.Steps, done)
		},
		Failed: func(_ recipe.Step, err error) {
			report(err.Error())
			done.Error = stepError(err)
			out.Steps = append(out.Steps, done)
		},
		Logged: func(text string) {
			out.Log = append(out.Log, text)
			print(text)
		},
	})
	if err != nil {
		if len(printed) > 0 {
			err = fmt.Errorf("%s\n%w", strings.Join(printed, "\n"), err)
		}
		return nil, runRecipeOutput{}, err
	}
	return nil, out, nil
}

func stepError(err error) *errorOutput {
	var refused wire.Error
	var command *recipe.CommandError
	var call *client.CallError
	switch {
	case errors.As(err, &refused):
		return &errorOutput{string(refused.Code), refused.Message}
	case errors.As(err, &command):
		return &errorOutput{"command", err.Error()}
	case errors.As(err, &call):
		return &errorOutput{"no_reply", err.Error()}
	}
	return &errorOutput{"error", err.Error()}
}

// Decides as the SDK's Elicit does.
func canElicit(session *mcp.ServerSession) bool {
	init := session.InitializeParams()
	if init == nil || init.Capabilities == nil || init.Capabilities.Elicitation == nil {
		return false
	}
	caps := init.Capabilities.Elicitation
	return caps.Form != nil || caps.URL == nil
}

const paramsForm = "params"

// A first call returns the form, which the handler returns instead of running. The client retries with the
// values, and a declined or canceled form fails the retry so that it does not run without them.
func askForParams(req *mcp.CallToolRequest, what string, params []wire.Param, args map[string]wire.RawArg) (*mcp.CallToolResult, error) {
	schema := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}}
	missing := map[string]wire.Param{}
	for _, p := range params {
		if _, required := p.Presence.(wire.Required); required && len(args[p.Name]) == 0 {
			schema.Properties[p.Name] = fieldSchema(p)
			schema.Required = append(schema.Required, p.Name)
			missing[p.Name] = p
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	names := strings.Join(schema.Required, ", ")
	response, retried := req.Params.InputResponses[paramsForm].(*mcp.ElicitResult)
	if !retried {
		return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{paramsForm: &mcp.ElicitParams{
			Message:         fmt.Sprintf("%s needs %s.", what, names),
			RequestedSchema: schema,
		}}}, nil
	}
	switch response.Action {
	case "accept":
	case "decline":
		return nil, fmt.Errorf("the user declined to give %s for %s", names, what)
	default:
		return nil, fmt.Errorf("the user canceled giving %s for %s", names, what)
	}
	for name, value := range response.Content {
		p, ok := missing[name]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			if p.Type.IsList() {
				args[name] = wire.SplitList(v)
			} else {
				args[name] = wire.RawArg{v}
			}
		case float64:
			args[name] = wire.RawArg{strconv.FormatFloat(v, 'f', -1, 64)}
		case bool:
			args[name] = wire.RawArg{strconv.FormatBool(v)}
		}
	}
	return nil, nil
}

// A long loses precision beyond 2^53, as the args of run_action do. A string_list takes comma-separated values,
// since a form's arrays only pick from choices.
func fieldSchema(p wire.Param) *jsonschema.Schema {
	field := &jsonschema.Schema{Title: p.Name, Description: p.Description, Type: "string"}
	switch p.Type {
	case wire.ParamTypeInt, wire.ParamTypeLong:
		field.Type = "integer"
	case wire.ParamTypeDouble:
		field.Type = "number"
	case wire.ParamTypeBoolean:
		field.Type = "boolean"
	case wire.ParamTypeEnum:
		for _, c := range p.Choices {
			field.Enum = append(field.Enum, c)
		}
	case wire.ParamTypeStringList:
		field.Description = strings.TrimSpace(p.Description + " Separate the values with commas.")
	}
	return field
}

func (s tools) connect(device, pkg string, mode client.StartMode) (client.Target, App, error) {
	target, err := s.source.Find(adb.Device(device), pkg)
	if err != nil {
		return client.Target{}, nil, err
	}
	app, err := s.source.Connect(target, mode)
	return target, app, err
}

func progress(ctx context.Context, req *mcp.CallToolRequest) func(message string) {
	token := req.Params.GetProgressToken()
	sent := 0
	return func(message string) {
		if token == nil {
			return
		}
		sent++
		// A failed progress notification only loses the message's early copy, since the result holds it too.
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token,
			Message:       message,
			Progress:      float64(sent),
		})
	}
}

func action(a wire.Action) actionOutput {
	params := []paramOutput{}
	for _, p := range a.Params {
		param := paramOutput{Name: p.Name, Type: string(p.Type), Description: p.Description, Choices: p.Choices, Hints: p.Hints}
		switch presence := p.Presence.(type) {
		case wire.Required:
			param.Required = true
		case wire.Default:
			if p.Type.IsList() {
				param.Default = presence.Values
			} else if len(presence.Values) > 0 {
				param.Default = presence.Values[0]
			}
		}
		params = append(params, param)
	}
	return actionOutput{ID: a.ID, Description: a.Description, Params: params}
}

// A number stays as written. An array gives a list param its values, and null leaves the param out.
func rawArgs(values map[string]json.RawMessage) (map[string]wire.RawArg, error) {
	args := map[string]wire.RawArg{}
	for key, raw := range values {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("argument %q is not JSON: %v", key, err)
		}
		items, isList := value.([]any)
		if !isList {
			items = []any{value}
		}
		for _, item := range items {
			switch v := item.(type) {
			case nil:
			case string:
				args[key] = append(args[key], v)
			case json.Number, bool:
				args[key] = append(args[key], fmt.Sprint(v))
			default:
				return nil, fmt.Errorf("argument %q must be a string, a number, a boolean or an array of them", key)
			}
		}
	}
	return args, nil
}

type clientSource struct {
	startTimeout time.Duration
	status       io.Writer
}

func (clientSource) Devices() ([]client.Device, error) {
	listings, err := adb.Devices()
	if err != nil {
		return nil, err
	}
	var devices []client.Device
	for _, l := range listings {
		d, err := client.Inspect(l)
		if err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, nil
}

func (clientSource) Find(device adb.Device, pkg string) (client.Target, error) {
	if pkg != "" && !client.IsPackageName(pkg) {
		return client.Target{}, errors.New(pkg + " is not a package name")
	}
	return client.Find(device, pkg)
}

func (s clientSource) Connect(target client.Target, mode client.StartMode) (App, error) {
	app, err := client.Resolve(client.Options{Target: target, Mode: mode, StartTimeout: s.startTimeout, Status: s.status})
	return app, err
}

func (clientSource) Recipes(pkg string) (*recipe.Script, string, error) {
	script, err := recipe.Load(pkg)
	return script, recipe.Path(pkg), err
}

// Raw JSON would otherwise infer as an array of bytes.
func schemaFor[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}},
	})
	if err != nil {
		// T is one of this package's types, so this fails only on a mistake in them, which every test run finds.
		panic(err)
	}
	return schema
}

const instructions = "quack runs debug actions in Android apps on devices that adb reaches. list_devices shows the devices " +
	"and their quack-enabled apps, list_actions an app's actions, and run_action runs one. list_recipes shows the " +
	"recipes the user keeps for an app, and run_recipe runs one. Devices and apps change while you work, so call " +
	"list_devices again when a call says that a device or app is missing or that several match."

const askHelp = "When the client can show the user a form, the user is asked for the required params that args leave " +
	"out, so leave out the ones that only the user knows. A user who declines or cancels fails the call. "

const targetHelp = "Leave out device and app when only one quack-enabled app is reachable. When several are, the error " +
	"lists them as --device and --app pairs: pass those values as device and app."
