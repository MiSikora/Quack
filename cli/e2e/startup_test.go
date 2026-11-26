package e2e

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStartupTimeoutEndsAStalledShell(t *testing.T) {
	d := defaultDevice()
	d.startupHangs = true
	adb := startFakeAdb(t, []*fakeDevice{d})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "--start-timeout", "100ms", "app.greet", "name=Duck")
	cmd.Dir = t.TempDir()
	cmd.Env = append(cleanEnv(), runAsQuack+"=1", "ANDROID_ADB_SERVER_PORT="+adb.port(), "XDG_CONFIG_HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("startup ignored --start-timeout and needed forced termination: %v\n%s", err, out)
	}
	if err == nil || !strings.Contains(string(out), "did not answer within 100ms") {
		t.Fatalf("expected an app startup timeout, got %v\n%s", err, out)
	}
	if !slices.Contains(adb.recorded(), "shell emulator-5554: startup stalled") {
		t.Fatalf("did not exercise the stalled startup shell: %v", adb.recorded())
	}
}

func TestStartupTimeoutDoesNotLimitActionExecution(t *testing.T) {
	adb := startFakeAdb(t, []*fakeDevice{defaultDevice()})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "--start-timeout", "1s", "account.sync")
	cmd.Dir = t.TempDir()
	cmd.Env = append(cleanEnv(), runAsQuack+"=1", "ANDROID_ADB_SERVER_PORT="+adb.port(), "XDG_CONFIG_HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("action should be allowed to finish after its startup deadline: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Synced") {
		t.Fatalf("action did not finish: %s", out)
	}
}
