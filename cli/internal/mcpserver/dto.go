package mcpserver

import "encoding/json"

type listDevicesInput struct{}

type listDevicesOutput struct {
	Devices []deviceOutput `json:"devices"`
}

type deviceOutput struct {
	Serial string   `json:"serial"`
	Model  string   `json:"model"`
	State  string   `json:"state"`
	Apps   []string `json:"apps"`
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
	Env         string   `json:"env,omitempty"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Choices     []string `json:"choices,omitempty"`
	Hints       []string `json:"hints,omitempty"`
	Required    bool     `json:"required"`
	Default     any      `json:"default,omitempty"`
}

type runActionInput struct {
	Action  string                     `json:"action" jsonschema:"the id of the action, as list_actions gives it"`
	Args    map[string]json.RawMessage `json:"args,omitempty" jsonschema:"the action's params by name: a string, a number or a boolean, or an array of them for a string_list param. Leave out params to use their defaults."`
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
	File        string        `json:"file"`
}

type runRecipeInput struct {
	OpenApp bool                       `json:"open_app,omitempty" jsonschema:"whether to show the app main screen before the recipe runs"`
	Recipe  string                     `json:"recipe" jsonschema:"the name of the recipe, as list_recipes gives it"`
	Args    map[string]json.RawMessage `json:"args,omitempty" jsonschema:"the recipe's params by name, as the args of run_action"`
	Device  string                     `json:"device,omitempty" jsonschema:"the device's serial, as list_devices gives it"`
	App     string                     `json:"app,omitempty" jsonschema:"the app's package, as list_devices gives it"`
}

type runRecipeOutput struct {
	Problems []string        `json:"problems,omitempty"`
	Error    *errorOutput    `json:"error,omitempty"`
	Steps    []stepOutput    `json:"steps"`
	Log      []string        `json:"log"`
	Data     json.RawMessage `json:"data,omitempty"`
}

type stepOutput struct {
	Step          string               `json:"step"`
	Notifications []notificationOutput `json:"notifications"`
	Message       string               `json:"message"`
	Data          json.RawMessage      `json:"data,omitempty"`
	Error         *errorOutput         `json:"error,omitempty"`
}

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
	Error         *errorOutput         `json:"error,omitempty"`
	Notifications []notificationOutput `json:"notifications"`
	Message       string               `json:"message"`
	Data          json.RawMessage      `json:"data,omitempty"`
}

type notificationOutput struct {
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}
