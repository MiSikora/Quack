package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"quack/internal/recipe"
	"quack/internal/session"
)

// The session ends with stdin, which cancels the runs that start_action started.
func Serve(ctx context.Context, version string, location recipe.Location, startTimeout time.Duration, status io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	return New(ctx, session.ADB{StartTimeout: startTimeout, Status: status}, version, location).Run(ctx, &mcp.StdioTransport{})
}

// The runs that start_action starts end with ctx.
func New(ctx context.Context, backend session.Backend, version string, location recipe.Location) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "quack", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	s := tools{backend: backend, runs: newRuns(ctx), location: location}
	addTool(server, &mcp.Tool{
		Name:        "list_devices",
		Description: "List the devices adb knows, each with its state and the quack-enabled apps on it.",
	}, s.listDevices)
	addTool(server, &mcp.Tool{
		Name: "list_actions",
		Description: "List the actions of a quack-enabled app with their params. A string param may give hints: values " +
			"the app suggests, such as known names, though others work too. " + targetHelp,
	}, s.listActions)
	addTool(server, &mcp.Tool{
		Name: "run_action",
		Description: "Run an action of a quack-enabled app and return its notifications, each a message with optional " +
			"data, and its message and its data. Get the action ids and their params from list_actions. " +
			"For an action that waits for something outside the call before " +
			"it ends, such as another device, use start_action instead. " + askHelp + targetHelp,
	}, s.runAction)
	addTool(server, &mcp.Tool{
		Name: "start_action",
		Description: "Start an action as run_action does, without waiting for it to end, and return the id of its run. " +
			"Use it for an action that waits for something outside the call before it ends, such as another device, " +
			"and read its notifications with read_run while it runs. An unknown action, an unknown arg or a missing " +
			"required one fails this call. Any other refusal of the app ends the run with an error. " + askHelp + targetHelp,
	}, s.startAction)
	addTool(server, &mcp.Tool{
		Name: "read_run",
		Description: "Read a run that start_action started: its notifications from index after on, and next to pass " +
			"as after to the following read. Once the run has ended, done is true and the run's message and data, or " +
			"its error, come too. With wait, the call waits for a notification or the end when there is nothing new " +
			"yet, and returns without notifications when the wait runs out. A run keeps its latest " +
			strconv.Itoa(maxRunNotifications) + " notifications, and dropped counts the ones between after and the " +
			"oldest one kept.",
	}, s.readRun)
	addTool(server, &mcp.Tool{
		Name: "cancel_run",
		Description: "Cancel a run that start_action started. The app sees the action canceled, and read_run gives " +
			"the run's end as the error canceled. A run that has ended stays as it was.",
	}, s.cancelRun)
	addTool(server, &mcp.Tool{
		Name: "list_runs",
		Description: "List the runs that start_action started in this session, oldest first, with their action, " +
			"device and app, whether they are done, and next, their count of notifications so far. Use it to find a " +
			"run whose id you no longer have. Finished runs past the latest " + strconv.Itoa(maxFinishedRuns) +
			" are gone.",
	}, s.listRuns)
	addTool(server, &mcp.Tool{
		Name: "list_recipes",
		Description: "List the recipes of a quack-enabled app with their params and files: scripts that the user keeps " +
			"on this computer, in the .quack dir of a project or in their config, which run the app's actions, commands " +
			"on the device or on this computer, and waits, and decide what to do with what they return. problems lists " +
			"the recipe files and settings that quack left out. " + targetHelp,
	}, s.listRecipes)
	addTool(server, &mcp.Tool{
		Name: "run_recipe",
		Description: "Run a recipe of a quack-enabled app with args for its params, and return what each of its steps " +
			"gave, a call of an action, /open-app, a wait, or a shell or run command: its notifications, and its message and " +
			"its data or the error it failed with, and the lines the recipe logged. The message of a command is what it " +
			"printed. A step that fails ends the recipe unless the recipe catches it. Failures retain completed steps and their data. problems lists recipe-loading warnings even when this recipe succeeds. Use open_app to foreground the app before running. Get the recipe names and params from list_recipes. " +
			askHelp + targetHelp,
	}, s.runRecipe)
	return server
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

const askHelp = "User arguments, including form responses, expand ${NAME} from the server environment once without " +
	"shell evaluation. Use $${NAME} for literal ${NAME}. Missing variables fail the call. When the client can show " +
	"the user a form, the user is asked for the required params that args leave out, so leave out the ones that " +
	"only the user knows. A user who declines or cancels fails the call. "

const targetHelp = "When device is omitted, ANDROID_SERIAL from the server environment selects the device if set. " +
	"Otherwise leave out device and app when only one quack-enabled app is reachable. When several are, the error " +
	"lists them as --device and --app pairs: pass those values as device and app."
