package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"quack/internal/adb"
	"quack/internal/client"
	"quack/internal/recipe"
	"quack/internal/wire"
)

func TestListDevicesGivesEachDeviceWithItsApps(t *testing.T) {
	source := &fakeSource{devices: []client.Device{
		{Listing: adb.Listing{Device: "emulator-5554", State: "device"}, Model: "Pixel 8", Packages: []string{"io.mehow.quack.sample"}},
		{Listing: adb.Listing{Device: "R5CT20ABCDE", State: "unauthorized"}},
	}}
	session := connect(t, source)

	result := call(t, session, "list_devices", nil)

	want := `{"devices":[` +
		`{"serial":"emulator-5554","model":"Pixel 8","state":"device","apps":["io.mehow.quack.sample"]},` +
		`{"serial":"R5CT20ABCDE","model":"","state":"unauthorized","apps":[]}]}`
	assertStructured(t, result, want)
}

func TestListActionsGivesTheParamsOfEachActionThatMatchesTheFilter(t *testing.T) {
	session := connect(t, newFakeSource())

	result := call(t, session, "list_actions", map[string]any{"filter": "^account\\."})

	want := `{"device":"emulator-5554","app":"io.mehow.quack.sample","version":"1.0","actions":[` +
		`{"id":"account.create","description":"Create an account","params":[` +
		`{"name":"username","type":"string","description":"Who signs in","hints":["alice","bob"],"required":true},` +
		`{"name":"age","type":"int","description":"","required":false,"default":"30"},` +
		`{"name":"tags","type":"string_list","description":"","required":false,"default":["a","b"]}]},` +
		`{"id":"account.delete","description":"Delete the account","params":[]}]}`
	assertStructured(t, result, want)
}

func TestListActionsAsksForTheDeviceAndAppItWasGiven(t *testing.T) {
	source := newFakeSource()
	session := connect(t, source)

	call(t, session, "list_actions", map[string]any{"device": "emulator-5554", "app": "io.mehow.quack.sample"})

	if want := []string{"emulator-5554 io.mehow.quack.sample"}; !reflect.DeepEqual(source.finds, want) {
		t.Errorf("finds are %q, want %q", source.finds, want)
	}
}

func TestRunActionPassesArgsAsTheAppParsesThem(t *testing.T) {
	source := newFakeSource()
	session := connect(t, source)

	call(t, session, "run_action", map[string]any{
		"action": "account.create",
		"args": map[string]any{
			"username": "alice",
			"age":      42,
			"id":       "12345678901234567890",
			"rate":     json.Number("0.1"),
			"admin":    true,
			"tags":     []any{"a", "b"},
			"note":     nil,
		},
	})

	want := wire.Call{Action: "account.create", Args: map[string]wire.RawArg{
		"username": {"alice"},
		"age":      {"42"},
		"id":       {"12345678901234567890"},
		"rate":     {"0.1"},
		"admin":    {"true"},
		"tags":     {"a", "b"},
	}}
	if got := source.app.calls; !reflect.DeepEqual(got, []wire.Call{want}) {
		t.Errorf("calls are %+v, want %+v", got, want)
	}
}

func TestRunActionStartsTheAppInTheBackgroundUnlessToldToOpenIt(t *testing.T) {
	source := newFakeSource()
	session := connect(t, source)

	call(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}})
	call(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": "bob"}, "open_app": true})

	if want := []client.StartMode{client.Wake, client.OpenApp}; !reflect.DeepEqual(source.modes, want) {
		t.Errorf("modes are %q, want %q", source.modes, want)
	}
}

func TestRunActionGivesTheNotificationsTheMessageAndTheData(t *testing.T) {
	session := connect(t, newFakeSource())

	result := call(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}})

	assertStructured(t, result, `{"notifications":["Creating alice"],"message":"Created alice","data":{"id":7}}`)
}

func TestRunActionSendsNotificationsAsProgressWhenAskedTo(t *testing.T) {
	progress := &progressLog{}
	session := connectWith(t, newFakeSource(), &mcp.ClientOptions{ProgressNotificationHandler: progress.handle})
	params := &mcp.CallToolParams{Name: "run_action", Arguments: map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}}}
	params.SetProgressToken("run-1")

	if _, err := session.CallTool(t.Context(), params); err != nil {
		t.Fatal(err)
	}

	progress.waitFor(t, []string{"Creating alice"})
}

func TestRunActionReportsTheAppsErrorAfterTheNotifications(t *testing.T) {
	session := connect(t, newFakeSource())

	result := callTool(t, session, "run_action", map[string]any{"action": "account.delete"})

	assertError(t, result, "Deleting\n[rejected] there is no account")
}

func TestToolsReportWhyNoAppWasFound(t *testing.T) {
	source := newFakeSource()
	source.findErr = &client.AppError{Message: "several quack packages on emulator-5554, pick one:\n  --device emulator-5554 --app a.b\n  --device emulator-5554 --app c.d"}
	session := connect(t, source)

	for tool, args := range map[string]map[string]any{"list_actions": nil, "run_action": {"action": "account.create"}} {
		result := callTool(t, session, tool, args)

		assertError(t, result, source.findErr.Error())
	}
}

func TestRunActionRefusesArgsThatAreNotTextNumbersOrBooleans(t *testing.T) {
	source := newFakeSource()
	session := connect(t, source)

	result := callTool(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": map[string]any{"first": "alice"}}})

	assertError(t, result, `argument "username" must be a string, a number, a boolean or an array of them`)
	if len(source.app.calls) > 0 {
		t.Errorf("the app got calls %+v, want none", source.app.calls)
	}
}

func TestListActionsRefusesAnInvalidFilter(t *testing.T) {
	session := connect(t, newFakeSource())

	result := callTool(t, session, "list_actions", map[string]any{"filter": "("})

	if !result.IsError || !strings.HasPrefix(text(result), "invalid filter: ") {
		t.Errorf("the result is %q with isError %v, want an invalid filter error", text(result), result.IsError)
	}
}

func TestListRecipesGivesTheParamsOfEachRecipeThatMatchesTheFilter(t *testing.T) {
	source := newFakeSource()
	path := writeRecipes(t, `
import quack from "quack"
export const fresh = quack.recipe({
  description: "Start over",
  params: { user: { type: "string", default: "alice", hints: ["alice", "bob"] }, count: { type: "int", required: true } },
  run() {},
})
export const gone = quack.recipe({ run() {} })`)
	session := connect(t, source)

	result := call(t, session, "list_recipes", map[string]any{"filter": "^fr"})

	want := `{"device":"emulator-5554","app":"io.mehow.quack.sample","file":` + quoted(path) + `,"recipes":[` +
		`{"name":"fresh","description":"Start over","params":[` +
		`{"name":"user","type":"string","description":"","hints":["alice","bob"],"required":false,"default":"alice"},` +
		`{"name":"count","type":"int","description":"","required":true}]}]}`
	assertStructured(t, result, want)
	if len(source.modes) > 0 {
		t.Errorf("the app started %q, want it left alone", source.modes)
	}
}

func TestListRecipesWithoutAFileGivesWhereToWriteThem(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	session := connect(t, newFakeSource())

	result := call(t, session, "list_recipes", map[string]any{})

	assertStructured(t, result, `{"device":"emulator-5554","app":"io.mehow.quack.sample","file":`+quoted(recipe.Path("io.mehow.quack.sample"))+`,"recipes":[]}`)
}

func TestRunRecipeGivesWhatEachStepGaveAndWhatItLogged(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const fresh = quack.recipe({
  params: { user: { type: "string", required: true } },
  run({ user }) {
    const created = quack.runAction<{ id: number }>("account.create", { username: user })
    console.log("id " + created.data.id)
    quack.openApp()
    quack.wait("1ms")
    quack.shell("pm clear com.example")
    quack.run("echo seeded")
  },
})`)
	session := connect(t, source)

	result := call(t, session, "run_recipe", map[string]any{"recipe": "fresh", "args": map[string]any{"user": "alice"}})

	assertStructured(t, result, `{"steps":[`+
		`{"step":"account.create username=alice","notifications":["Creating alice"],"message":"Created alice","data":{"id":7}},`+
		`{"step":"/open-app","notifications":[],"message":""},`+
		`{"step":"wait 1ms","notifications":[],"message":""},`+
		`{"step":"shell pm clear com.example","notifications":[],"message":"ran pm clear com.example"},`+
		`{"step":"run echo seeded","notifications":[],"message":"seeded"}],"log":["id 7"]}`)
	if want := []client.StartMode{client.OpenApp}; !reflect.DeepEqual(source.app.starts, want) {
		t.Errorf("starts are %q, want %q", source.app.starts, want)
	}
}

func TestRunRecipeSendsStepsNotificationsAndLogsAsProgressWhenAskedTo(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const fresh = quack.recipe({
  run() {
    quack.openApp()
    quack.runAction("account.create", { username: "alice" })
    console.log("done")
  },
})`)
	progress := &progressLog{}
	session := connectWith(t, source, &mcp.ClientOptions{ProgressNotificationHandler: progress.handle})
	params := &mcp.CallToolParams{Name: "run_recipe", Arguments: map[string]any{"recipe": "fresh"}}
	params.SetProgressToken("run-1")

	if _, err := session.CallTool(t.Context(), params); err != nil {
		t.Fatal(err)
	}

	progress.waitFor(t, []string{"› /open-app", "› account.create username=alice", "Creating alice", "done"})
}

func TestRunRecipeEndsAtAStepThatFailsSayingWhere(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const redo = quack.recipe({
  run() {
    quack.runAction("account.create", { username: "alice" })
    quack.runAction("account.delete")
    quack.runAction("account.create", { username: "bob" })
  },
})`)
	session := connect(t, source)

	result := callTool(t, session, "run_recipe", map[string]any{"recipe": "redo"})

	assertError(t, result, "› account.create username=alice\nCreating alice\nCreated alice\n› account.delete\nDeleting\n"+
		"[rejected] there is no account\n    at run (io.mehow.quack.sample.ts:5:20)")
	if got := len(source.app.calls); got != 2 {
		t.Errorf("the app got %d calls, want 2", got)
	}
}

func TestRunRecipeGivesTheErrorOfAStepTheRecipeCaught(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const careful = quack.recipe({
  run() {
    try {
      quack.runAction("account.delete")
    } catch (e) {}
    quack.runAction("account.create", { username: "alice" })
  },
})`)
	session := connect(t, source)

	result := call(t, session, "run_recipe", map[string]any{"recipe": "careful"})

	assertStructured(t, result, `{"steps":[`+
		`{"step":"account.delete","notifications":["Deleting"],"message":"","error":{"code":"rejected","message":"there is no account"}},`+
		`{"step":"account.create username=alice","notifications":["Creating alice"],"message":"Created alice","data":{"id":7}}],"log":[]}`)
}

func TestRunRecipeRefusesArgsItsParamsRefuse(t *testing.T) {
	source := newFakeSource()
	writeRecipes(t, `
import quack from "quack"
export const fresh = quack.recipe({ params: { user: { type: "string", required: true } }, run() { quack.runAction("app.greet") } })`)
	session := connect(t, source)

	result := callTool(t, session, "run_recipe", map[string]any{"recipe": "fresh"})

	assertError(t, result, "[invalid_args] recipe 'fresh': missing argument 'user'")
	if len(source.app.calls) > 0 {
		t.Errorf("the app got calls %+v, want none", source.app.calls)
	}
}

func TestRunRecipeNamesTheFileWithoutTheRecipe(t *testing.T) {
	path := writeRecipes(t, `export {}`)
	session := connect(t, newFakeSource())

	result := callTool(t, session, "run_recipe", map[string]any{"recipe": "fresh"})

	assertError(t, result, "no recipe named fresh in "+path)
}

// writeRecipes writes file as the recipes of the sample app, in a config dir of the test's own, and returns its path.
func writeRecipes(t *testing.T, file string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := recipe.Path("io.mehow.quack.sample")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimPrefix(file, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func quoted(text string) string {
	out, _ := json.Marshal(text)
	return string(out)
}

// progressLog keeps the messages of the progress notifications a client gets. The client handles notifications on a
// goroutine of their own, so a call can return before the handler has seen the notifications sent ahead of its result.
type progressLog struct {
	mu       sync.Mutex
	messages []string
}

func (l *progressLog) handle(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, req.Params.Message)
}

// waitFor fails unless the messages come to want within a few seconds.
func (l *progressLog) waitFor(t *testing.T, want []string) {
	t.Helper()
	var got []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		l.mu.Lock()
		got = slices.Clone(l.messages)
		l.mu.Unlock()
		if len(got) >= len(want) {
			break
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("progress is %q, want %q", got, want)
	}
}

// connect runs a server for source and connects a client to it in memory.
func connect(t *testing.T, source *fakeSource) *mcp.ClientSession {
	return connectWith(t, source, nil)
}

func connectWith(t *testing.T, source *fakeSource, options *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(source, "test").Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, options).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		session.Close()
		serverSession.Wait()
	})
	return session
}

// call calls a tool and fails the test when the tool fails.
func call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result := callTool(t, session, tool, args)
	if result.IsError {
		t.Fatalf("%s failed: %s", tool, text(result))
	}
	return result
}

func callTool(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("calling %s: %v", tool, err)
	}
	return result
}

func assertStructured(t *testing.T, result *mcp.CallToolResult, want string) {
	t.Helper()
	got, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var gotValue, wantValue any
	json.Unmarshal(got, &gotValue)
	json.Unmarshal([]byte(want), &wantValue)
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("the structured content is\n%s\nwant\n%s", got, want)
	}
}

func assertError(t *testing.T, result *mcp.CallToolResult, want string) {
	t.Helper()
	if !result.IsError || text(result) != want {
		t.Errorf("the result is %q with isError %v, want the error %q", text(result), result.IsError, want)
	}
}

func text(result *mcp.CallToolResult) string {
	var texts []string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// fakeSource has one ready device with the sample app, unless a test changes it.
type fakeSource struct {
	devices []client.Device
	app     *fakeApp
	findErr error
	// finds are the device and package of each Find, joined by a space.
	finds []string
	// modes are the start modes of each Connect.
	modes []client.StartMode
}

func newFakeSource() *fakeSource {
	return &fakeSource{app: &fakeApp{}}
}

func (s *fakeSource) Devices() ([]client.Device, error) { return s.devices, nil }

func (s *fakeSource) Find(device adb.Device, pkg string) (client.Target, error) {
	s.finds = append(s.finds, string(device)+" "+pkg)
	if s.findErr != nil {
		return client.Target{}, s.findErr
	}
	return client.Target{Device: "emulator-5554", Package: "io.mehow.quack.sample"}, nil
}

func (s *fakeSource) Connect(_ client.Target, mode client.StartMode) (App, error) {
	s.modes = append(s.modes, mode)
	return s.app, nil
}

func (s *fakeSource) Recipes(pkg string) (*recipe.Script, string, error) {
	return clientSource{}.Recipes(pkg)
}

// fakeApp has account.create, which creates the account its username names, and account.delete, which the app
// rejects.
type fakeApp struct {
	calls  []wire.Call
	starts []client.StartMode
}

func (a *fakeApp) Target() client.Target {
	return client.Target{Device: "emulator-5554", Package: "io.mehow.quack.sample"}
}

// Command answers every command with its text.
func (a *fakeApp) Command(_ context.Context, cmd string) (adb.Output, error) {
	return adb.Output{Stdout: "ran " + cmd + "\n"}, nil
}

func (a *fakeApp) Start(mode client.StartMode) error {
	a.starts = append(a.starts, mode)
	return nil
}

func (a *fakeApp) Describe(context.Context) (wire.Hello, []wire.Action, error) {
	return wire.Hello{Package: "io.mehow.quack.sample", Version: "1.0"}, []wire.Action{
		{ID: "app.greet", Description: "Greet someone"},
		{ID: "account.create", Description: "Create an account", Params: []wire.Param{
			{Name: "username", Type: wire.ParamTypeString, Description: "Who signs in", Hints: []string{"alice", "bob"}, Presence: wire.Required{}},
			{Name: "age", Type: wire.ParamTypeInt, Presence: wire.Default{Values: []string{"30"}}},
			{Name: "tags", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}},
		}},
		{ID: "account.delete", Description: "Delete the account"},
	}, nil
}

func (a *fakeApp) Call(_ context.Context, call wire.Call, notify client.NotificationFunc) (wire.MessageResult, error) {
	a.calls = append(a.calls, call)
	switch call.Action {
	case "account.create":
		username := call.Args["username"][0]
		notify("Creating " + username)
		return wire.MessageResult{Message: "Created " + username, Data: json.RawMessage(`{"id":7}`)}, nil
	case "account.delete":
		notify("Deleting")
		return wire.MessageResult{}, wire.Error{Code: wire.ErrorCodeRejected, Message: "there is no account"}
	default:
		return wire.MessageResult{}, errors.New("unknown action")
	}
}
