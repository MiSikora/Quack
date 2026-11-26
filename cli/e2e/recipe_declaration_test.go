package e2e

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRecipeWhoseNameChangesAtRuntimeFailsWithoutCrashing(t *testing.T) {
	for _, json := range []bool{false, true} {
		name := "text"
		args := []string{"--recipe", "setup"}
		if json {
			name = "json"
			args = append([]string{"--json"}, args...)
		}
		t.Run(name, func(t *testing.T) {
			adb := startFakeAdb(t, []*fakeDevice{defaultDevice()})
			out := run(t, adb, scenario{args: args, recipes: `import quack from "quack"
quack.recipe({name: quack.app.package || "setup", run() { console.log("wrong-recipe-ran") }})`})
			if !strings.Contains(out, "--- exit 1\n") || !strings.Contains(out, "recipe 'setup' is not declared") || !strings.Contains(out, "io.mehow.quack.sample.ts") {
				t.Fatalf("want an ordinary failure naming the missing recipe and file:\n%s", out)
			}
			if strings.Contains(out, "panic:") || strings.Contains(out, "wrong-recipe-ran") {
				t.Fatalf("recipe crashed or ran a different declaration:\n%s", out)
			}
			t.Log(out)
		})
	}
}

func TestMCPStaysAvailableAfterARecipeNameChangesAtRuntime(t *testing.T) {
	config, project := t.TempDir(), t.TempDir()
	writeRecipes(t, scenario{inProject: true, recipes: `import quack from "quack"
quack.recipe({name: quack.app.package || "setup", run() { console.log("wrong-recipe-ran") }})
quack.recipe({name: "healthy", run() { return "still-alive" }})`}, filepath.Join(config, "quack"), filepath.Join(project, ".quack"))
	adb := startFakeAdb(t, []*fakeDevice{defaultDevice()})
	executable := filepath.Join(t.TempDir(), "quack-mcp")
	if out, err := exec.Command("go", "build", "-o", executable, "../cmd/quack-mcp").CombinedOutput(); err != nil {
		t.Fatalf("build quack-mcp: %v\n%s", err, out)
	}
	cmd := exec.Command(executable, "--project-dir", project)
	cmd.Env = append(cleanEnv(), "ANDROID_ADB_SERVER_PORT="+adb.port(), "XDG_CONFIG_HOME="+config)
	cmd.Dir = project
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, test := range []struct {
		name, want string
		isError    bool
	}{
		{"setup", "recipe 'setup' is not declared", true},
		{"healthy", "still-alive", false},
	} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "run_recipe", Arguments: map[string]any{"recipe": test.name}})
		if err != nil {
			t.Fatalf("run_recipe %s: %v", test.name, err)
		}
		var text strings.Builder
		for _, content := range result.Content {
			if content, ok := content.(*mcp.TextContent); ok {
				text.WriteString(content.Text)
			}
		}
		if result.IsError != test.isError || !strings.Contains(text.String(), test.want) || strings.Contains(text.String(), "wrong-recipe-ran") {
			t.Fatalf("run_recipe %s: isError %v, content %s", test.name, result.IsError, text.String())
		}
		t.Logf("run_recipe %s: isError %v, content %s", test.name, result.IsError, text.String())
	}
	if got := adb.recorded(); strings.Contains(strings.Join(got, "\n"), `"method":"call"`) {
		t.Fatalf("unexpected action calls: %v", got)
	}
}
