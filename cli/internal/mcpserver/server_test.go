package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"quack/internal/adb"
	"quack/internal/client"
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
		`{"name":"tags","type":"string_list","description":"","required":false,"default":["a","b"]}]}]}`
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

	if want := []client.StartMode{client.Wake, client.Open}; !reflect.DeepEqual(source.modes, want) {
		t.Errorf("modes are %q, want %q", source.modes, want)
	}
}

func TestRunActionGivesTheNotificationsTheMessageAndTheData(t *testing.T) {
	session := connect(t, newFakeSource())

	result := call(t, session, "run_action", map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}})

	assertStructured(t, result, `{"notifications":["Creating alice"],"message":"Created alice","data":{"id":7}}`)
}

func TestRunActionSendsNotificationsAsProgressWhenAskedTo(t *testing.T) {
	var mu sync.Mutex
	var progress []string
	session := connectWith(t, newFakeSource(), &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			defer mu.Unlock()
			progress = append(progress, req.Params.Message)
		},
	})
	params := &mcp.CallToolParams{Name: "run_action", Arguments: map[string]any{"action": "account.create", "args": map[string]any{"username": "alice"}}}
	params.SetProgressToken("run-1")

	if _, err := session.CallTool(t.Context(), params); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if want := []string{"Creating alice"}; !reflect.DeepEqual(progress, want) {
		t.Errorf("progress is %q, want %q", progress, want)
	}
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

// fakeApp has account.create, which creates the account its username names, and account.delete, which the app
// rejects.
type fakeApp struct {
	calls []wire.Call
}

func (a *fakeApp) Describe(context.Context) (wire.Hello, []wire.Action, error) {
	return wire.Hello{Package: "io.mehow.quack.sample", Version: "1.0"}, []wire.Action{
		{ID: "app.greet", Description: "Greet someone"},
		{ID: "account.create", Description: "Create an account", Params: []wire.Param{
			{Name: "username", Type: wire.ParamTypeString, Description: "Who signs in", Hints: []string{"alice", "bob"}, Presence: wire.Required{}},
			{Name: "age", Type: wire.ParamTypeInt, Presence: wire.Default{Values: []string{"30"}}},
			{Name: "tags", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}},
		}},
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
