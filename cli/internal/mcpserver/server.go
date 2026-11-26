package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"quack/internal/adb"
	"quack/internal/device"
	"quack/internal/recipe"
	"quack/internal/session"
	"quack/internal/step"
	"quack/internal/wire"
)

type tools struct {
	backend  session.Backend
	runs     *runs
	location recipe.Location
}

func (s tools) listDevices(context.Context, *mcp.CallToolRequest, listDevicesInput) (*mcp.CallToolResult, listDevicesOutput, error) {
	devices, err := s.backend.Devices("", "")
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
	target, app, err := s.connect(in.Device, in.App, false)
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

func (s tools) runAction(ctx context.Context, req *mcp.CallToolRequest, in runActionInput) (*mcp.CallToolResult, runActionOutput, error) {
	prepared, form, err := s.prepareCall(ctx, req, in)
	if form != nil || err != nil {
		return form, runActionOutput{}, err
	}
	t := newTranscript(progress(ctx, req))
	err = session.RunPrepared(ctx, prepared.app, prepared.actions, prepared.call, t.record)
	done := t.steps[0]
	out := runActionOutput{Notifications: done.Notifications, Message: done.Message, Data: done.Data}
	if err != nil {
		out.Error = stepError(err)
		return t.failure(err), out, nil
	}
	return nil, out, nil
}

func (s tools) startAction(ctx context.Context, req *mcp.CallToolRequest, in runActionInput) (*mcp.CallToolResult, startActionOutput, error) {
	prepared, form, err := s.prepareCall(ctx, req, in)
	if form != nil || err != nil {
		return form, startActionOutput{}, err
	}
	// Preflight checks argument names and presence. Display has the prepared keys without host values.
	if err := checkCall(prepared.actions, wire.Call(prepared.call.Display)); err != nil {
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
	target, err := s.find(in.Device, in.App)
	if err != nil {
		return nil, listRecipesOutput{}, err
	}
	cookbook, problems, err := s.location.Load(target.Package)
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

func (s tools) runRecipe(ctx context.Context, req *mcp.CallToolRequest, in runRecipeInput) (*mcp.CallToolResult, runRecipeOutput, error) {
	args, err := rawArgs(in.Args)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	target, app, err := s.connect(in.Device, in.App, in.OpenApp)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	cookbook, problems, err := s.location.Load(target.Package)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	r, ok := cookbook.Find(in.Recipe)
	if !ok {
		return nil, runRecipeOutput{}, errors.New(strings.Join(append([]string{"no recipe named " + in.Recipe + " for " + target.Package}, problems...), "\n"))
	}
	_, actions, err := app.Describe(ctx)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	if canElicit(req.Session) {
		if form, err := askForParams(req, "recipe "+in.Recipe, r.Params, args); form != nil || err != nil {
			return form, runRecipeOutput{}, err
		}
	}
	expanded, err := step.ExpandArgs(args, target)
	if err != nil {
		return nil, runRecipeOutput{}, err
	}
	t := newTranscript(progress(ctx, req))
	data, err := session.Run(ctx, app, actions, cookbook, session.Runnable{Name: in.Recipe, Recipe: true}, expanded, t.record)
	out := runRecipeOutput{Steps: t.steps, Log: t.log, Data: data, Problems: problems}
	if err != nil {
		out.Error = stepError(err)
		return t.failure(err), out, nil
	}
	return nil, out, nil
}

type transcript struct {
	report  func(string)
	steps   []stepOutput
	log     []string
	printed []string
}

func newTranscript(report func(string)) *transcript {
	return &transcript{report: report, steps: []stepOutput{}, log: []string{}}
}

func (t *transcript) record(e step.Event) {
	switch e := e.(type) {
	case step.Started:
		t.steps = append(t.steps, stepOutput{Step: e.Line, Notifications: []notificationOutput{}})
		if e.Number > 0 {
			t.print("› " + e.Line)
		}
	case step.Notified:
		done := &t.steps[len(t.steps)-1]
		done.Notifications = append(done.Notifications, notificationOutput(e.Notification))
		t.print(e.Notification.Message)
	case step.Ended:
		if e.Result.Message != "" {
			t.printed = append(t.printed, e.Result.Message)
		}
		done := &t.steps[len(t.steps)-1]
		done.Message, done.Data = e.Result.Message, e.Result.Data
	case step.Failed:
		t.report(e.Err.Error())
		t.steps[len(t.steps)-1].Error = stepError(e.Err)
	case step.Logged:
		t.log = append(t.log, e.Text)
		t.print(e.Text)
	}
}

func (t *transcript) print(line string) {
	t.printed = append(t.printed, line)
	t.report(line)
}

func (t *transcript) failure(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: t.failed(err).Error()}}}
}

func (t *transcript) failed(err error) error {
	if len(t.printed) > 0 {
		return fmt.Errorf("%s\n%w", strings.Join(t.printed, "\n"), err)
	}
	return err
}

func stepError(err error) *errorOutput {
	code, message := session.Classify(err)
	return &errorOutput{code, message}
}

type preparedCall struct {
	target  device.Target
	app     session.App
	call    step.PreparedCall
	actions []wire.Action
}

func (s tools) prepareCall(ctx context.Context, req *mcp.CallToolRequest, in runActionInput) (preparedCall, *mcp.CallToolResult, error) {
	args, err := rawArgs(in.Args)
	if err != nil {
		return preparedCall{}, nil, err
	}
	target, app, err := s.connect(in.Device, in.App, in.OpenApp)
	if err != nil {
		return preparedCall{}, nil, err
	}
	prepared := preparedCall{target: target, app: app}
	_, prepared.actions, err = app.Describe(ctx)
	if err != nil {
		return preparedCall{}, nil, err
	}
	if a, ok := wire.FindAction(prepared.actions, in.Action); ok && canElicit(req.Session) {
		if form, err := askForParams(req, in.Action, a.Params, args); form != nil || err != nil {
			return preparedCall{}, form, err
		}
	}
	expanded, err := step.ExpandArgs(args, target)
	if err != nil {
		return preparedCall{}, nil, err
	}
	prepared.call = step.PrepareCall(prepared.actions, wire.Call{Action: in.Action, Args: expanded.Values}, expanded.Display, os.Getenv)
	return prepared, nil, nil
}

func (s tools) connect(serial, pkg string, openApp bool) (device.Target, session.App, error) {
	target, err := s.find(serial, pkg)
	if err != nil {
		return device.Target{}, nil, err
	}
	app, err := s.backend.Connect(target, openApp)
	return target, app, err
}

func (s tools) find(serial, pkg string) (device.Target, error) {
	if serial == "" {
		serial = os.Getenv("ANDROID_SERIAL")
	}
	if pkg != "" && !device.IsPackageName(pkg) {
		return device.Target{}, errors.New(pkg + " is not a package name")
	}
	return s.backend.Find(adb.Device(serial), pkg)
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
		params = append(params, paramOutput{
			Name:        p.Name,
			Env:         p.Env,
			Type:        string(p.Type),
			Description: p.Description,
			Choices:     p.Choices,
			Hints:       p.Hints,
			Required:    p.Required(),
			Default:     p.DefaultValue(),
		})
	}
	return actionOutput{ID: a.ID, Description: a.Description, Params: params}
}

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
		if isList && len(items) == 0 {
			args[key] = wire.RawArg{}
		}
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
