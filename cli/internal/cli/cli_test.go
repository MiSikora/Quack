package cli

import (
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
