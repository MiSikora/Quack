package e2e

import (
	"context"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPRecipeCancellationClosesStalledShellNegotiation(t *testing.T) {
	config, project := t.TempDir(), t.TempDir()
	writeRecipes(t, scenario{inProject: true, recipes: `import quack from "quack"
quack.recipe({name: "blocked", run() { quack.shell("echo hello") }})`}, filepath.Join(config, "quack"), filepath.Join(project, ".quack"))
	started, disconnected := make(chan struct{}), make(chan struct{})
	d := defaultDevice()
	d.commandHandshake = func(conn net.Conn) {
		close(started)
		io.Copy(io.Discard, conn)
		close(disconnected)
	}
	adb := startFakeAdb(t, []*fakeDevice{d})
	executable := filepath.Join(t.TempDir(), "quack-mcp")
	if out, err := exec.Command("go", "build", "-o", executable, "../cmd/quack-mcp").CombinedOutput(); err != nil {
		t.Fatalf("build quack-mcp: %v\n%s", err, out)
	}
	cmd := exec.Command(executable, "--project-dir", project)
	cmd.Dir = project
	cmd.Env = append(cleanEnv(), "ANDROID_ADB_SERVER_PORT="+adb.port(), "XDG_CONFIG_HOME="+config)
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	defer cmd.Process.Kill()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		session.CallTool(ctx, &mcp.CallToolParams{Name: "run_recipe", Arguments: map[string]any{"recipe": "blocked"}})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("recipe did not reach shell negotiation")
	}
	cancel()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("MCP cancellation left shell negotiation connected")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("canceled MCP call did not return")
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "run_action", Arguments: map[string]any{"action": "app.greet", "args": map[string]any{"name": "Duck"}}})
	if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "Hello, Duck!") {
		t.Fatalf("server did not remain usable after cancellation: %v, %v", result, err)
	}
}
