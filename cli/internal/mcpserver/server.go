// Package mcpserver serves quack-enabled apps to MCP clients, such as Claude, over stdin and stdout. Its tools list the
// devices and their apps, list an app's actions, and run one. Devices and apps come and go, so every call finds them
// again: a tool picks the only app there is, or the one its device and app arguments name.
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
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/wire"
)

// Source is where the server finds the devices and the apps on them. clientSource is the real one.
type Source interface {
	Devices() ([]client.Device, error)
	// Find returns the one app that device and pkg leave, either of which may be "" for any, as client.Find does.
	Find(device adb.Device, pkg string) (client.Target, error)
	Connect(target client.Target) (App, error)
}

// App is what the server needs from a quack app. client.App satisfies it.
type App interface {
	Describe(ctx context.Context) (wire.Hello, []wire.Action, error)
	Call(ctx context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error)
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
		Name:        "list_actions",
		Description: "List the actions of a quack-enabled app with their params. " + targetHelp,
	}, s.listActions)
	mcp.AddTool(server, &mcp.Tool{
		Name: "run_action",
		Description: "Run an action of a quack-enabled app and return its notifications, its message and its data. " +
			"Get the action ids and their params from list_actions. " + targetHelp,
		InputSchema:  runActionSchema,
		OutputSchema: schemaFor[runActionOutput](),
	}, s.runAction)
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
	Required    bool     `json:"required"`
	// Default is a string, or an array of strings for a list param, or absent without a default.
	Default any `json:"default,omitempty"`
}

type runActionInput struct {
	Action string `json:"action"`
	// Args are JSON values, so that a number reaches the app as the client wrote it. The SDK's validation still turns
	// numbers into float64 first, so a long beyond 2^53 loses precision unless it comes as a string.
	Args   map[string]json.RawMessage `json:"args,omitempty"`
	Device string                     `json:"device,omitempty"`
	App    string                     `json:"app,omitempty"`
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
	target, app, err := s.connect(in.Device, in.App)
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
	_, app, err := s.connect(in.Device, in.App)
	if err != nil {
		return nil, runActionOutput{}, err
	}
	notifications := []string{}
	token := req.Params.GetProgressToken()
	result, err := app.Call(ctx, wire.Call{Action: in.Action, Args: args}, func(message string) {
		notifications = append(notifications, message)
		if token != nil {
			// A failed progress notification only loses the notification's early copy, since the result holds it too.
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Message:       message,
				Progress:      float64(len(notifications)),
			})
		}
	})
	if err != nil {
		if len(notifications) > 0 {
			err = fmt.Errorf("%s\n%w", strings.Join(notifications, "\n"), err)
		}
		return nil, runActionOutput{}, err
	}
	return nil, runActionOutput{Notifications: notifications, Message: result.Message, Data: result.Data}, nil
}

func (s tools) connect(device, pkg string) (client.Target, App, error) {
	target, err := s.source.Find(adb.Device(device), pkg)
	if err != nil {
		return client.Target{}, nil, err
	}
	app, err := s.source.Connect(target)
	return target, app, err
}

func action(a wire.Action) actionOutput {
	params := []paramOutput{}
	for _, p := range a.Params {
		param := paramOutput{Name: p.Name, Type: string(p.Type), Description: p.Description, Choices: p.Choices}
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

func (s clientSource) Connect(target client.Target) (App, error) {
	app, err := client.Resolve(client.Options{Target: target, Mode: client.Wake, StartTimeout: s.startTimeout, Status: s.status})
	if err != nil {
		return nil, err
	}
	return app, nil
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
	"and their quack-enabled apps, list_actions an app's actions, and run_action runs one. Devices and apps change while " +
	"you work, so call list_devices again when a call says that a device or app is missing or that several match."

const targetHelp = "Leave out device and app when only one quack-enabled app is reachable. When several are, the error " +
	"lists them as --device and --app pairs: pass those values as device and app."
