package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The files in the repository's protocol directory are the protocol both the CLI and the Kotlin library speak. The
// library's ProtocolTest reads the same files.

func TestDecodeGoldenFrames(t *testing.T) {
	cases := []struct {
		file string
		want Frame
	}{
		{"hello.json", Hello{Protocol: 1, Package: "io.mehow.quack.sample", Version: "1.0"}},
		{"hello-without-version.json", Hello{Protocol: 1, Package: "io.mehow.quack.sample"}},
		{"notification.json", Notification{"Started"}},
		{"message-result.json", MessageResult{Message: "Hello, Bob!"}},
		{"message-result-with-data.json", MessageResult{
			Message: "Signed in as alice",
			Data:    json.RawMessage(`{"username":"alice","tags":["a","b"],"verified":true,"age":7}`),
		}},
		{"error.json", Error{Code: ErrorCodeInvalidArgs, Message: "missing argument 'name'"}},
		{"actions-result.json", ActionsResult{[]Action{
			{ID: "app.greet", Description: "Say hello", Params: []Param{
				{Name: "name", Description: "Who to greet", Type: ParamTypeString, Presence: Required{}},
				{Name: "times", Type: ParamTypeInt, Presence: Default{[]string{"1"}}},
			}},
			{ID: "app.params", Params: []Param{
				{Name: "text", Type: ParamTypeString, Presence: Optional{}},
				{Name: "size", Type: ParamTypeLong, Presence: Default{[]string{"5000000000"}}},
				{Name: "ratio", Type: ParamTypeDouble, Presence: Default{[]string{"0.5"}}},
				{Name: "flag", Type: ParamTypeBoolean, Presence: Default{[]string{"false"}}},
				{Name: "theme", Type: ParamTypeEnum, Choices: []string{"light", "dark"}, Presence: Default{[]string{"dark"}}},
				{Name: "tag", Type: ParamTypeStringList, Presence: Default{[]string{"a", "b"}}},
			}},
			{ID: "account.delete", Description: "Delete the account", Params: []Param{}},
		}}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			if got := DecodeFrame(golden(t, "frames", c.file)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestEncodeGoldenRequests(t *testing.T) {
	cases := []struct {
		file string
		line []byte
	}{
		{"describe.json", EncodeDescribe()},
		{"call.json", EncodeCall(Call{Action: "app.params", Args: map[string]RawArg{"text": {"hi"}, "tag": {"a", "b"}}})},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			var got, want any
			if err := json.Unmarshal(c.line, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(golden(t, "requests", c.file), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %s, want %v", c.line, want)
			}
		})
	}
}

func golden(t *testing.T, dir, file string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "protocol", dir, file))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
