package e2e

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProjectFlagBoundariesAndUnrelatedMCPActions(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "quack-mcp")
	if out, err := exec.Command("go", "build", "-o", executable, "../cmd/quack-mcp").CombinedOutput(); err != nil {
		t.Fatalf("build quack-mcp: %v\n%s", err, out)
	}
	t.Run("empty CLI root", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "--project-dir", "", "--list-recipes")
		cmd.Env = append(cleanEnv(), runAsQuack+"=1")
		out, err := cmd.CombinedOutput()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || !strings.Contains(string(out), "project-dir") {
			t.Fatalf("empty root: %v, %s", err, out)
		}
	})
	t.Run("empty MCP root", func(t *testing.T) {
		cmd := exec.Command(executable, "--project-dir", "")
		cmd.Env = cleanEnv()
		out, err := cmd.CombinedOutput()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || !strings.Contains(string(out), "project-dir") {
			t.Fatalf("empty root: %v, %s", err, out)
		}
	})
	t.Run("invalid initializer root is usage", func(t *testing.T) {
		config := t.TempDir()
		cmd := exec.Command(os.Args[0], "--project-dir", filepath.Join(t.TempDir(), "missing"), "--init-recipes", "--app", "io.mehow.quack.sample")
		cmd.Env = append(cleanEnv(), runAsQuack+"=1", "XDG_CONFIG_HOME="+config)
		out, err := cmd.CombinedOutput()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
			t.Fatalf("invalid initialization: %v, %s", err, out)
		}
		if _, err := os.Stat(filepath.Join(config, "quack", "recipes")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("invalid initialization wrote personal types: %v", err)
		}
	})
	t.Run("deleted cwd allows action tools", func(t *testing.T) {
		adb := startFakeAdb(t, []*fakeDevice{defaultDevice()})
		gone := t.TempDir()
		cmd := exec.Command("sh", "-c", `rmdir "$1"; exec "$2"`, "quack-mcp-test", gone, executable)
		cmd.Dir = gone
		cmd.Env = append(cleanEnv(), "ANDROID_ADB_SERVER_PORT="+adb.port(), "XDG_CONFIG_HOME="+t.TempDir())
		client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
		session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
			Name: "run_action", Arguments: map[string]any{"action": "app.greet", "args": map[string]any{"name": "Duck"}},
		})
		if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "Hello, Duck!") {
			t.Fatalf("action from deleted cwd: %v, %v", result, err)
		}
	})
	t.Run("bad project only blocks recipe tools", func(t *testing.T) {
		adb := startFakeAdb(t, []*fakeDevice{defaultDevice()})
		cmd := exec.Command(executable, "--project-dir", filepath.Join(t.TempDir(), "missing"))
		cmd.Dir = t.TempDir()
		cmd.Env = append(cleanEnv(), "ANDROID_ADB_SERVER_PORT="+adb.port(), "XDG_CONFIG_HOME="+t.TempDir())
		client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
		session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_recipes"})
		if err != nil || !result.IsError {
			t.Fatalf("recipe lookup with invalid root: %v, %v", result, err)
		}
		result, err = session.CallTool(t.Context(), &mcp.CallToolParams{
			Name: "run_action", Arguments: map[string]any{"action": "app.greet", "args": map[string]any{"name": "Duck"}},
		})
		if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "Hello, Duck!") {
			t.Fatalf("action with invalid root: %v, %v", result, err)
		}
	})
}
