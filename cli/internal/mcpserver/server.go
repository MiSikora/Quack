package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"slices"
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
	// Recipes gives the app's recipes, and the problems that left some files or entries out.
	Recipes(pkg string) (*recipe.Cookbook, []string, error)
}

type App interface {
	recipe.App
	Describe(ctx context.Context) (wire.Hello, []wire.Action, error)
}

// The session ends with stdin, which cancels the runs that start_action started.
func Serve(ctx context.Context, version string, startTimeout time.Duration, status io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	return New(ctx, clientSource{startTimeout, status}, version).Run(ctx, &mcp.StdioTransport{})
}

// The runs that start_action starts end with ctx.
func New(ctx context.Context, source Source, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "quack", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	s := tools{source, newRuns(ctx)}
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
		Description: "Run an action of a quack-enabled app and return its notifications, each a message with optional " +
			"data, and its message and its data. Get the action ids and their params from list_actions. The args reach " +
			"the app as given: ${NAME} stays as it is. For an action that waits for something outside the call before " +
			"it ends, such as another device, use start_action instead. " + askHelp + targetHelp,
		InputSchema:  schemaFor[runActionInput](),
		OutputSchema: schemaFor[runActionOutput](),
	}, s.runAction)
	mcp.AddTool(server, &mcp.Tool{
		Name: "start_action",
		Description: "Start an action as run_action does, without waiting for it to end, and return the id of its run. " +
			"Use it for an action that waits for something outside the call before it ends, such as another device, " +
			"and read its notifications with read_run while it runs. An unknown action, an unknown arg or a missing " +
			"required one fails this call. Any other refusal of the app ends the run with an error. " + askHelp + targetHelp,
		InputSchema:  schemaFor[runActionInput](),
		OutputSchema: schemaFor[startActionOutput](),
	}, s.startAction)
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_run",
		Description: "Read a run that start_action started: its notifications from index after on, and next to pass " +
			"as after to the following read. Once the run has ended, done is true and the run's message and data, or " +
			"its error, come too. With wait, the call waits for a notification or the end when there is nothing new " +
			"yet, and returns without notifications when the wait runs out. A run keeps its latest " +
			strconv.Itoa(maxRunNotifications) + " notifications, and dropped counts the ones between after and the " +
			"oldest one kept.",
		OutputSchema: schemaFor[readRunOutput](),
	}, s.readRun)
	mcp.AddTool(server, &mcp.Tool{
		Name: "cancel_run",
		Description: "Cancel a run that start_action started. The app sees the action canceled, and read_run gives " +
			"the run's end as the error canceled. A run that has ended stays as it was.",
	}, s.cancelRun)
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_runs",
		Description: "List the runs that start_action started in this session, oldest first, with their action, " +
			"device and app, whether they are done, and next, their count of notifications so far. Use it to find a " +
			"run whose id you no longer have. Finished runs past the latest " + strconv.Itoa(maxFinishedRuns) +
			" are gone.",
		OutputSchema: schemaFor[listRunsOutput](),
	}, s.listRuns)
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_recipes",
		Description: "List the recipes of a quack-enabled app with their params and files: scripts that the user keeps " +
			"on this computer, in the .quack dir of a project or in their config, which run the app's actions, commands " +
			"on the device or on this computer, and waits, and decide what to do with what they return. problems lists " +
			"the recipe files and settings that quack left out. " + targetHelp,
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
	runs   *runs
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
	Device   string         `json:"device"`
	App      string         `json:"app"`
	Recipes  []recipeOutput `json:"recipes"`
	Problems []string       `json:"problems,omitempty"`
}

type recipeOutput struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Params      []paramOutput `json:"params"`
	// The file whose code declares it, which may be one that a mapped file imports.
	File string `json:"file"`
}

type runRecipeInput struct {
	Recipe string                     `json:"recipe" jsonschema:"the name of the recipe, as list_recipes gives it"`
	Args   map[string]json.RawMessage `json:"args,omitempty" jsonschema:"the recipe's params by name, as the args of run_action"`
	Device string                     `json:"device,omitempty" jsonschema:"the device's serial, as list_devices gives it"`
	App    string                     `json:"app,omitempty" jsonschema:"the app's package, as list_devices gives it"`
}

// Data is what the recipe returned, and absent when it returned nothing.
type runRecipeOutput struct {
	Steps []stepOutput    `json:"steps"`
	Log   []string        `json:"log"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// A failed step has an error instead, which the recipe caught when it went on.
type stepOutput struct {
	Step          string               `json:"step"`
	Notifications []notificationOutput `json:"notifications"`
	Message       string               `json:"message"`
	Data          json.RawMessage      `json:"data,omitempty"`
	Error         *errorOutput         `json:"error,omitempty"`
}

// The app's error code, command for a command that exited with another status than 0, no_reply for a call that
// ended without the app's answer, canceled for a run that cancel_run or the session's end canceled, and error for
// anything else.
type errorOutput struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type startActionOutput struct {
	Run string `json:"run"`
}

type readRunInput struct {
	Run   string `json:"run" jsonschema:"the id of the run, as start_action gives it"`
	After int    `json:"after,omitempty" jsonschema:"how many of the run's notifications you have read, as the last read gave it in next: the read gives the ones from that index on"`
	Wait  string `json:"wait,omitempty" jsonschema:"how long to wait for a notification or the end when there is nothing new yet, such as 30s, at most 1m. Leave it out to return at once."`
}

type cancelRunInput struct {
	Run string `json:"run" jsonschema:"the id of the run, as start_action gives it"`
}

type cancelRunOutput struct{}

type listRunsInput struct{}

type listRunsOutput struct {
	Runs []runOutput `json:"runs"`
}

type runActionOutput struct {
	Notifications []notificationOutput `json:"notifications"`
	Message       string               `json:"message"`
	Data          json.RawMessage      `json:"data,omitempty"`
}

type notificationOutput struct {
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
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
	prepared, form, err := s.prepareCall(ctx, req, in, canElicit(req.Session))
	if form != nil || err != nil {
		return form, runActionOutput{}, err
	}
	notifications := []notificationOutput{}
	var messages []string
	report := progress(ctx, req)
	result, err := prepared.app.Call(ctx, prepared.call, func(notification wire.Notification) {
		notifications = append(notifications, notificationOutput(notification))
		messages = append(messages, notification.Message)
		report(notification.Message)
	})
	if err != nil {
		if len(messages) > 0 {
			err = fmt.Errorf("%s\n%w", strings.Join(messages, "\n"), err)
		}
		return nil, runActionOutput{}, err
	}
	return nil, runActionOutput{Notifications: notifications, Message: result.Message, Data: result.Data}, nil
}

func (s tools) startAction(ctx context.Context, req *mcp.CallToolRequest, in runActionInput) (*mcp.CallToolResult, startActionOutput, error) {
	prepared, form, err := s.prepareCall(ctx, req, in, true)
	if form != nil || err != nil {
		return form, startActionOutput{}, err
	}
	if err := checkCall(prepared.actions, prepared.call); err != nil {
		return nil, startActionOutput{}, err
	}
	return nil, startActionOutput{s.runs.start(prepared.target, prepared.app, prepared.call)}, nil
}

func (s tools) readRun(ctx context.Context, _ *mcp.CallToolRequest, in readRunInput) (*mcp.CallToolResult, readRunOutput, error) {
	var wait time.Duration
	if in.Wait != "" {
		parsed, err := time.ParseDuration(in.Wait)
		if err != nil || parsed < 0 {
			return nil, readRunOutput{}, fmt.Errorf("wait %q is not a duration such as 30s", in.Wait)
		}
		wait = min(parsed, maxWait)
	}
	out, err := s.runs.read(ctx, in.Run, in.After, wait)
	return nil, out, err
}

func (s tools) cancelRun(_ context.Context, _ *mcp.CallToolRequest, in cancelRunInput) (*mcp.CallToolResult, cancelRunOutput, error) {
	return nil, cancelRunOutput{}, s.runs.cancel(in.Run)
}

func (s tools) listRuns(context.Context, *mcp.CallToolRequest, listRunsInput) (*mcp.CallToolResult, listRunsOutput, error) {
	return nil, listRunsOutput{s.runs.list()}, nil
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
	cookbook, problems, err := s.source.Recipes(target.Package)
	if err != nil {
		return nil, listRecipesOutput{}, err
	}
	out := listRecipesOutput{Device: string(target.Device), App: target.Package, Recipes: []recipeOutput{}, Problems: problems}
	for _, r := range cookbook.Recipes {
		if filter.MatchString(r.Name) {
			params := action(wire.Action{Params: r.Params}).Params
			out.Recipes = append(out.Recipes, recipeOutput{Name: r.Name, Description: r.Description, Params: params, File: r.File})
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
	cookbook, problems, err := s.source.Recipes(target.Package)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	// The problems may say why the recipe is missing, such as a mapped file that does not exist.
	if !slices.ContainsFunc(cookbook.Recipes, func(r recipe.Recipe) bool { return r.Name == in.Recipe }) {
		return nil, runRecipeOutput{}, errors.New(strings.Join(append([]string{"no recipe named " + in.Recipe + " for " + target.Package}, problems...), "\n"))
	}
	_, actions, err := app.Describe(ctx)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	if canElicit(req.Session) {
		for _, r := range cookbook.Recipes {
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
	out.Data, err = cookbook.Run(ctx, app, in.Recipe, args, recipe.Observer{
		Started: func(step recipe.Step) {
			done = stepOutput{Step: recipe.Line(step, actions), Notifications: []notificationOutput{}}
			print("› " + done.Step)
		},
		Notified: func(notification wire.Notification) {
			done.Notifications = append(done.Notifications, notificationOutput(notification))
			print(notification.Message)
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

type preparedCall struct {
	target client.Target
	app    App
	call   wire.Call
	// Nil unless describe.
	actions []wire.Action
}

// The front half of run_action and start_action. With describe, it asks the app for its actions, and the user for
// the required params that args leave out when the client can ask. A form comes back instead of the call, for the
// client to show.
func (s tools) prepareCall(ctx context.Context, req *mcp.CallToolRequest, in runActionInput, describe bool) (preparedCall, *mcp.CallToolResult, error) {
	args, err := rawArgs(in.Args)
	if err != nil {
		return preparedCall{}, nil, err
	}
	mode := client.Wake
	if in.OpenApp {
		mode = client.OpenApp
	}
	target, app, err := s.connect(in.Device, in.App, mode)
	if err != nil {
		return preparedCall{}, nil, err
	}
	prepared := preparedCall{target: target, app: app, call: wire.Call{Action: in.Action, Args: args}}
	if !describe {
		return prepared, nil, nil
	}
	_, prepared.actions, err = app.Describe(ctx)
	if err != nil {
		return preparedCall{}, nil, err
	}
	if !canElicit(req.Session) {
		return prepared, nil, nil
	}
	for _, a := range prepared.actions {
		if a.ID == in.Action {
			if form, err := askForParams(req, in.Action, a.Params, args); form != nil || err != nil {
				return preparedCall{}, form, err
			}
		}
	}
	return prepared, nil, nil
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

// Recipes come from the project of the dir quack-mcp runs in, as the MCP client started it, and from the user's config.
func (clientSource) Recipes(pkg string) (*recipe.Cookbook, []string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	return recipe.Load(dir, pkg)
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
	"list_devices again when a call says that a device or app is missing or that several match. An action that " +
	"waits for something outside the call before it ends, such as another device, returns only once it ends: start " +
	"it with start_action instead, and read its notifications with read_run while it runs. list_runs finds the runs " +
	"again. Runs belong to this MCP session: reconnecting the server cancels the ones still running, so finish or " +
	"cancel them first."

const askHelp = "When the client can show the user a form, the user is asked for the required params that args leave " +
	"out, so leave out the ones that only the user knows. A user who declines or cancels fails the call. "

const targetHelp = "Leave out device and app when only one quack-enabled app is reachable. When several are, the error " +
	"lists them as --device and --app pairs: pass those values as device and app."
