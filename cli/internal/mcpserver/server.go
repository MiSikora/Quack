// Package mcpserver serves quack-enabled apps to MCP clients, such as Claude, over stdin and stdout. Its tools list the
// devices and their apps, list an app's actions and recipes, and run one. Devices and apps come and go, so every call
// finds them again: a tool picks the only app there is, or the one its device and app arguments name.
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
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/recipe"
	"quack/internal/wire"
)

// Source is where the server finds the devices and the apps on them. clientSource is the real one.
type Source interface {
	Devices() ([]client.Device, error)
	// Find returns the one app that device and pkg leave, either of which may be "" for any, as client.Find does.
	Find(device adb.Device, pkg string) (client.Target, error)
	// Connect gets the app of target running the way mode says before each request.
	Connect(target client.Target, mode client.StartMode) (App, error)
	// Recipes are the recipes of pkg, as recipe.Load reads them, and the file they come from.
	Recipes(pkg string) ([]recipe.Recipe, string, error)
}

// App is what the server needs from a quack app. client.App satisfies it.
type App interface {
	Describe(ctx context.Context) (wire.Hello, []wire.Action, error)
	Call(ctx context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error)
	Start(mode client.StartMode) error
	Command(ctx context.Context, cmd string) (adb.Output, error)
	Target() client.Target
}

// Serve serves the apps that adb reaches until the client disconnects. A started app waits up to startTimeout to
// answer, and what the server says about starting apps goes to status.
func Serve(ctx context.Context, version string, startTimeout time.Duration, status io.Writer) error {
	return New(clientSource{startTimeout, status}, version).Run(ctx, &mcp.StdioTransport{})
}

// New is a server for the apps of source.
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
			"Get the action ids and their params from list_actions. " + targetHelp,
		InputSchema:  runActionSchema,
		OutputSchema: schemaFor[runActionOutput](),
	}, s.runAction)
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_recipes",
		Description: "List the recipes of a quack-enabled app: named lists of steps with fixed args that the user keeps " +
			"in a file on this computer. Each step is a call of an action as the command line writes it, /open-app, which " +
			"shows the app's main screen, a wait, shell with a command that runs on the device, or run with a command that " +
			"runs on this computer. An arg or a command written ${NAME} takes the environment variable NAME of quack-mcp, " +
			"except ${QUACK_PACKAGE} and ${ANDROID_SERIAL}, which are the app and the device the recipe runs against. " +
			targetHelp,
	}, s.listRecipes)
	mcp.AddTool(server, &mcp.Tool{
		Name: "run_recipe",
		Description: "Run a recipe of a quack-enabled app, step by step, and return what each step gave: its " +
			"notifications, its message and its data. The message of a command is what it printed. The recipe runs no " +
			"step when one of its calls would not pass the app's checks, and stops at the first step that fails. Get the " +
			"recipe names from list_recipes. " + targetHelp,
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
	// State is adb's: device when it is ready, or else why it is not, such as offline or unauthorized.
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
	// Hints are values the app offers for a string param, in its order. Other values work too.
	Hints    []string `json:"hints,omitempty"`
	Required bool     `json:"required"`
	// Default is a string, or an array of strings for a list param, or absent without a default.
	Default any `json:"default,omitempty"`
}

type runActionInput struct {
	Action string `json:"action"`
	// Args are JSON values, so that a number reaches the app as the client wrote it. The SDK's validation still turns
	// numbers into float64 first, so a long beyond 2^53 loses precision unless it comes as a string.
	Args    map[string]json.RawMessage `json:"args,omitempty"`
	Device  string                     `json:"device,omitempty"`
	App     string                     `json:"app,omitempty"`
	OpenApp bool                       `json:"open_app,omitempty"`
}

type listRecipesInput struct {
	Device string `json:"device,omitempty" jsonschema:"the device's serial, as list_devices gives it"`
	App    string `json:"app,omitempty" jsonschema:"the app's package, as list_devices gives it"`
	Filter string `json:"filter,omitempty" jsonschema:"a regular expression that the names of the listed recipes match"`
}

type listRecipesOutput struct {
	Device string `json:"device"`
	App    string `json:"app"`
	// File is where the recipes come from, and where to write them when there are none.
	File    string         `json:"file"`
	Recipes []recipeOutput `json:"recipes"`
}

type recipeOutput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Steps       []string `json:"steps"`
}

type runRecipeInput struct {
	Recipe string `json:"recipe" jsonschema:"the name of the recipe, as list_recipes gives it"`
	Device string `json:"device,omitempty" jsonschema:"the device's serial, as list_devices gives it"`
	App    string `json:"app,omitempty" jsonschema:"the app's package, as list_devices gives it"`
}

type runRecipeOutput struct {
	Steps []stepOutput `json:"steps"`
}

// stepOutput is what a step of a recipe gave. Only a call of an action has a message and data.
type stepOutput struct {
	Step          string          `json:"step"`
	Notifications []string        `json:"notifications"`
	Message       string          `json:"message"`
	Data          json.RawMessage `json:"data,omitempty"`
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
	var filter *regexp.Regexp
	if in.Filter != "" {
		var err error
		if filter, err = regexp.Compile(in.Filter); err != nil {
			return nil, listActionsOutput{}, fmt.Errorf("invalid filter: %v", err)
		}
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
		if filter == nil || filter.MatchString(a.ID) {
			out.Actions = append(out.Actions, action(a))
		}
	}
	return nil, out, nil
}

// runAction runs the action and sends each of its notifications as a progress notification when the client asked for
// them. The client canceling the request cancels the action. A failed action keeps the notifications it sent before the
// error.
func (s tools) runAction(ctx context.Context, req *mcp.CallToolRequest, in runActionInput) (*mcp.CallToolResult, runActionOutput, error) {
	args, err := rawArgs(in.Args)
	if err != nil {
		return nil, runActionOutput{}, err
	}
	mode := client.Wake
	if in.OpenApp {
		mode = client.Open
	}
	_, app, err := s.connect(in.Device, in.App, mode)
	if err != nil {
		return nil, runActionOutput{}, err
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
	var filter *regexp.Regexp
	if in.Filter != "" {
		var err error
		if filter, err = regexp.Compile(in.Filter); err != nil {
			return nil, listRecipesOutput{}, fmt.Errorf("invalid filter: %v", err)
		}
	}
	// Recipes need only the package, so the app does not start.
	target, err := s.find(in.Device, in.App)
	if err != nil {
		return nil, listRecipesOutput{}, err
	}
	recipes, file, err := s.source.Recipes(target.Package)
	if err != nil {
		return nil, listRecipesOutput{}, err
	}
	out := listRecipesOutput{Device: string(target.Device), App: target.Package, File: file, Recipes: []recipeOutput{}}
	for _, r := range recipes {
		if filter != nil && !filter.MatchString(r.Name) {
			continue
		}
		steps := make([]string, len(r.Steps))
		for i, step := range r.Steps {
			steps[i] = recipe.Line(step, nil)
		}
		out.Recipes = append(out.Recipes, recipeOutput{Name: r.Name, Description: r.Description, Steps: steps})
	}
	return nil, out, nil
}

// runRecipe runs the steps of the recipe, after checking its calls against the app's actions, so that a recipe the app
// would refuse runs no step. Each step's line and then its notifications go out as progress notifications when the
// client asked for them. The client canceling the request cancels the step that runs. A failed step ends the recipe
// with an error that holds what the steps before it gave, as the command line prints them.
func (s tools) runRecipe(ctx context.Context, req *mcp.CallToolRequest, in runRecipeInput) (*mcp.CallToolResult, runRecipeOutput, error) {
	target, app, err := s.connect(in.Device, in.App, client.Wake)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	recipes, file, err := s.source.Recipes(target.Package)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	at := slices.IndexFunc(recipes, func(r recipe.Recipe) bool { return r.Name == in.Recipe })
	if at < 0 {
		return nil, runRecipeOutput{}, fmt.Errorf("no recipe named %s in %s", in.Recipe, file)
	}
	r := recipes[at]
	_, actions, err := app.Describe(ctx)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	if err := recipe.Check(r, actions, app.Target()); err != nil {
		return nil, runRecipeOutput{}, err
	}
	report := progress(ctx, req)
	out := runRecipeOutput{Steps: []stepOutput{}}
	var printed []string
	for _, step := range r.Steps {
		done := stepOutput{Step: recipe.Line(step, actions), Notifications: []string{}}
		printed = append(printed, "› "+done.Step)
		report(printed[len(printed)-1])
		result, err := recipe.Do(ctx, app, step, func(message string) {
			done.Notifications = append(done.Notifications, message)
			printed = append(printed, message)
			report(message)
		})
		if err != nil {
			return nil, runRecipeOutput{}, fmt.Errorf("%s\n%w", strings.Join(printed, "\n"), err)
		}
		if result.Message != "" {
			printed = append(printed, result.Message)
		}
		done.Message, done.Data = result.Message, result.Data
		out.Steps = append(out.Steps, done)
	}
	return nil, out, nil
}

func (s tools) find(device, pkg string) (client.Target, error) {
	return s.source.Find(adb.Device(device), pkg)
}

func (s tools) connect(device, pkg string, mode client.StartMode) (client.Target, App, error) {
	target, err := s.find(device, pkg)
	if err != nil {
		return client.Target{}, nil, err
	}
	app, err := s.source.Connect(target, mode)
	return target, app, err
}

// progress sends each message as a progress notification when the client asked for them, counting them from 1.
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

// rawArgs turns JSON arguments into the text the app parses, as a user would type it: a string as it is, a number as
// written and a boolean as true or false. An array gives a list param its values, and null leaves the param out.
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

// clientSource finds devices through adb, and the apps on them through the client.
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
	if err != nil {
		return nil, err
	}
	return app, nil
}

func (clientSource) Recipes(pkg string) ([]recipe.Recipe, string, error) {
	recipes, err := recipe.Load(pkg)
	return recipes, recipe.Path(pkg), err
}

// runActionSchema is run_action's input schema, with the descriptions of its properties.
var runActionSchema = func() *jsonschema.Schema {
	schema := schemaFor[runActionInput]()
	schema.Properties["action"].Description = "the id of the action, as list_actions gives it"
	schema.Properties["args"].Description = "the action's params by name: a string, a number or a boolean, or an array of them for a " +
		"string_list param. Leave out params to use their defaults. Pass a long beyond 2^53 as a string, since JSON numbers " +
		"that large lose precision on the way."
	schema.Properties["device"].Description = "the device's serial, as list_devices gives it"
	schema.Properties["app"].Description = "the app's package, as list_devices gives it"
	schema.Properties["open_app"].Description = "whether to show the app's main screen before the action runs, instead of " +
		"starting the app in the background"
	return schema
}()

// schemaFor is the JSON schema of T, where raw JSON may be any value. It would otherwise infer as an array of bytes.
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

const targetHelp = "Leave out device and app when only one quack-enabled app is reachable. When several are, the error " +
	"lists them as --device and --app pairs: pass those values as device and app."
