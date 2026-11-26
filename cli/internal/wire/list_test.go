package wire

import (
	"reflect"
	"testing"
)

func TestListInputPreservesExactJSONAndCommaConvenience(t *testing.T) {
	for _, tc := range []struct {
		text string
		want RawArg
	}{
		{`["", " spaced ", "a,b", "[bracket", "a\\b"]`, RawArg{"", " spaced ", "a,b", "[bracket", `a\b`}},
		{` [] `, RawArg{}},
		{`a, b\,c, ,d `, RawArg{"a", "b,c", "d"}},
		{"", nil},
	} {
		got, err := ParseList(tc.text)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q = %#v, %v; want %#v", tc.text, got, err, tc.want)
		}
	}
}

func TestInvalidJSONListsFailInsteadOfDroppingOrCoercingValues(t *testing.T) {
	for _, text := range []string{`[`, `[null]`, `[1]`, `[true]`, `[{}]`, `[["a"]]`, `["a"] trailing`} {
		if _, err := ParseList(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
}
