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
