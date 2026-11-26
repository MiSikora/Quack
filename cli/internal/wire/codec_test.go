package wire

import (
	"reflect"
	"testing"
)

func TestDecodeFrame(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Frame
	}{
		{
			name: "hello",
			line: `{"hello":{"protocol":1,"app":{"package":"io.mehow.quack.sample","version":"1.0"}}}`,
			want: Hello{Protocol: 1, Package: "io.mehow.quack.sample", Version: "1.0"},
		},
		{
			name: "notification",
			line: `{"notification":{"message":"Started"}}`,
			want: Notification{Message: "Started"},
		},
		{
			name: "notification with null data",
			line: `{"notification":{"message":"Started","data":null}}`,
			want: Notification{Message: "Started"},
		},
		{
			name: "call result",
			line: `{"result":{"message":"Hello, Bob!"}}`,
			want: MessageResult{Message: "Hello, Bob!"},
		},
		{
			name: "describe result with every presence",
			line: `{"result":{"actions":[{"id":"app.greet","description":"Say hello","params":[` +
				`{"name":"name","type":"string","required":true,"description":"Who to greet"},` +
				`{"name":"times","type":"int","required":false,"default":"1","description":""},` +
				`{"name":"theme","type":"enum","choices":["light","dark"],"required":false,"description":""},` +
				`{"name":"tag","type":"string_list","required":false,"default":["a","b"],"description":""}]}]}}`,
			want: ActionsResult{[]Action{{ID: "app.greet", Description: "Say hello", Params: []Param{
				{Name: "name", Description: "Who to greet", Type: ParamTypeString, Presence: Required{}},
				{Name: "times", Type: ParamTypeInt, Presence: Default{[]string{"1"}}},
				{Name: "theme", Type: ParamTypeEnum, Choices: []string{"light", "dark"}, Presence: Optional{}},
				{Name: "tag", Type: ParamTypeStringList, Presence: Default{[]string{"a", "b"}}},
			}}}},
		},
		{
			name: "describe result without actions",
			line: `{"result":{"actions":[]}}`,
			want: ActionsResult{[]Action{}},
		},
		{
			name: "null default is no default",
			line: `{"result":{"actions":[{"id":"a","params":[{"name":"p","type":"string","default":null}]}]}}`,
			want: ActionsResult{[]Action{{ID: "a", Params: []Param{{Name: "p", Type: ParamTypeString, Presence: Optional{}}}}}},
		},
		{
			name: "error with a code this CLI does not know",
			line: `{"error":{"code":"teapot","message":"short and stout"}}`,
			want: Error{Code: "teapot", Message: "short and stout"},
		},
		{
			name: "not JSON",
			line: `not json`,
			want: Malformed{"invalid character 'o' in literal null (expecting 'u')"},
		},
		{
			name: "no known frame",
			line: `{}`,
			want: Malformed{"not a hello, notification, result or error"},
		},
		{
			name: "result without a body",
			line: `{"result":{}}`,
			want: Malformed{"a result without a message or actions"},
		},
		{
			name: "default of the wrong type",
			line: `{"result":{"actions":[{"id":"a","params":[{"name":"p","type":"int","default":1}]}]}}`,
			want: Malformed{"param 'p' of 'a': default is not a string or an array of strings"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DecodeFrame([]byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestEnvironmentBindingNamesAreValidatedAtDecode(t *testing.T) {
	for _, env := range []string{`""`, `"1TOKEN"`, `"A-B"`, `"A B"`, `"é"`, `"Aé"`, `"A١"`, `"TOKEN\n"`, `"A\nB"`, `42`} {
		got := DecodeFrame([]byte(`{"result":{"actions":[{"id":"a","params":[{"name":"p","env":` + env + `}]}]}}`))
		if _, ok := got.(Malformed); !ok {
			t.Errorf("env %s accepted: %#v", env, got)
		}
	}
	for _, env := range []string{`"TOKEN"`, `"_token2"`, `"_"`, `"z"`, `"a0_Z9"`, `null`} {
		got := DecodeFrame([]byte(`{"result":{"actions":[{"id":"a","params":[{"name":"p","env":` + env + `}]}]}}`))
		if _, ok := got.(ActionsResult); !ok {
			t.Errorf("env %s rejected: %#v", env, got)
		}
	}
}
