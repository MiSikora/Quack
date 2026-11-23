package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quack/internal/wire"
)

func TestWriteActionWrapsLongTextUnderItsIndent(t *testing.T) {
	action := wire.Action{
		ID:          "app.long",
		Description: "Do something that takes a long sentence to explain, so that it cannot fit on one line\nA second paragraph",
		Params: []wire.Param{
			{Name: "first", Type: wire.ParamTypeString, Presence: wire.Required{}, Description: "A param whose description is far too long to fit in the width that help allows"},
			{Name: "second", Type: wire.ParamTypeInt},
			{Name: "third", Type: wire.ParamTypeBoolean},
			{Name: "labels", Type: wire.ParamTypeStringList, Presence: wire.Default{Values: []string{"a", "b"}}, Description: "Labels to add"},
		},
	}
	var out strings.Builder

	writeAction(&out, action)

	want := `app.long
    Do something that takes a long sentence to explain, so that it cannot fit on
    one line
    A second paragraph.

    first=<string>
        Required. A param whose description is far too long to fit in the width
        that help allows.

    second=<int>
    third=<boolean>
    labels=<string>...
        Default: a, b. Labels to add.

`
	if got := out.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if len(line) > helpWidth {
			t.Errorf("line longer than %d: %q", helpWidth, line)
		}
	}
}

func TestTUIOpensOnlyForAPersonAtATerminal(t *testing.T) {
	cases := []struct {
		name     string
		terminal bool
		env      map[string]string
		want     bool
	}{
		{"terminal", true, nil, true},
		{"no terminal", false, nil, false},
		{"agent", true, map[string]string{"AI_AGENT": "claude-code"}, false},
		{"ci", true, map[string]string{"CI": "true"}, false},
		{"ci turned off", true, map[string]string{"CI": "false"}, true},
		{"ci zero", true, map[string]string{"CI": "0"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			getenv := func(key string) string { return c.env[key] }
			if got := opensTUI(c.terminal, getenv); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestInitRecipesSaysWhenNothingMapsTheApp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".quack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".quack", "quack.toml"), []byte("[recipes]\n'io\\.a' = \"recipes.ts\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for pkg, want := range map[string]string{
		"io.a": "The recipes of io.a are in the files that [recipes] maps it to.\n",
		"io.b": "Nothing maps io.b to a recipes file yet. Add a line under [recipes] in .quack/quack.toml, such as\n'io\\.b' = \"recipes.ts\"\n",
	} {
		var out strings.Builder

		err := initRecipes(&out, pkg)

		if err != nil || !strings.HasSuffix(out.String(), want) {
			t.Errorf("%s: got %q, %v, want it to end with %q", pkg, out.String(), err, want)
		}
	}
}
