package mcpserver

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeviceEnvironmentIsFallbackForToolTargets(t *testing.T) {
	t.Setenv("ANDROID_SERIAL", "from-env")
	source := newFakeSource()
	rpc := connect(t, source)
	call(t, rpc, "list_actions", map[string]any{})
	call(t, rpc, "list_actions", map[string]any{"device": "explicit"})
	if got, want := source.Finds(), []string{"from-env ", "explicit "}; !reflect.DeepEqual(got, want) {
		t.Errorf("targets %v, want %v", got, want)
	}
}

func TestFailedRecipeRetainsCompletedStepsAndNotifications(t *testing.T) {
	writeRecipes(t, `import quack from "quack"
quack.recipe({name:"partial",run(){quack.runAction("account.create",{username:"alice"});quack.runAction("account.delete")}})`)
	result := callTool(t, connect(t, newFakeSource()), "run_recipe", map[string]any{"recipe": "partial"})
	if !result.IsError {
		t.Fatal("failure reported success")
	}
	out := result.StructuredContent.(map[string]any)
	steps := out["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("steps %v", steps)
	}
	first := steps[0].(map[string]any)
	if first["data"].(map[string]any)["id"] != float64(7) {
		t.Errorf("first result %v", first)
	}
	if first["notifications"].([]any)[0].(map[string]any)["data"].(map[string]any)["username"] != "alice" {
		t.Errorf("notifications %v", first)
	}
	if out["error"].(map[string]any)["code"] != "rejected" || steps[1].(map[string]any)["error"].(map[string]any)["code"] != "rejected" {
		t.Errorf("missing errors %v", out)
	}
}

func TestFailedActionRetainsNotificationsAndStructuredError(t *testing.T) {
	result := callTool(t, connect(t, newFakeSource()), "run_action", map[string]any{"action": "account.delete"})
	if !result.IsError {
		t.Fatal("failure reported success")
	}
	out := result.StructuredContent.(map[string]any)
	if out["error"].(map[string]any)["code"] != "rejected" || len(out["notifications"].([]any)) != 1 {
		t.Errorf("lost output %v", out)
	}
}

func TestRecipeOpenAppAndLoadingWarnings(t *testing.T) {
	writeRecipes(t, `import quack from "quack"
quack.recipe({name:"fine",run(){}})
quack.recipe({name:"fails",run(){quack.fail("planned")}})`)
	config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "quack", "config.toml")
	file, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n'other.*' = 'unused.ts'\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	// A second mapped file is missing, while the requested recipes remain usable.
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"io.mehow.quack.sample.ts"`, `["io.mehow.quack.sample.ts", "missing.ts"]`, 1))
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	source := newFakeSource()
	rpc := connect(t, source)
	for _, name := range []string{"fine", "fails"} {
		result := callTool(t, rpc, "run_recipe", map[string]any{"recipe": name, "open_app": true})
		if result.IsError != (name == "fails") {
			t.Errorf("unexpected result %s", text(result))
		}
		problems := result.StructuredContent.(map[string]any)["problems"].([]any)
		if len(problems) != 1 || !strings.Contains(problems[0].(string), "missing.ts") {
			t.Errorf("warnings %v", problems)
		}
	}
	for _, connection := range source.Connects() {
		if !strings.HasSuffix(connection, "open-app") {
			t.Errorf("did not open app: %s", connection)
		}
	}
}
