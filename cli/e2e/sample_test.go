package e2e

import (
	"fmt"
	"strings"
	"time"
)

// sampleDescribe is what the fake sample app answers to describe. It is not a recording of the real sample: its actions
// misbehave on purpose and its params cover what help prints. protocol/ pins the frame format against the Kotlin library.
const sampleDescribe = `{"result":{"actions":[` +
	`{"id":"app.greet","description":"Say hello","params":[{"name":"name","type":"string","required":true,"description":"Who to greet"},{"name":"times","type":"int","required":false,"default":"1","description":"How many times to say hello"}]},` +
	`{"id":"app.params","description":"Show the parsed value of each param type","params":[{"name":"text","type":"string","required":false,"description":""},{"name":"count","type":"int","required":false,"description":""},{"name":"size","type":"long","required":false,"description":""},{"name":"ratio","type":"double","required":false,"description":""},{"name":"flag","type":"boolean","required":false,"description":""},{"name":"theme","type":"enum","choices":["light","dark"],"required":false,"description":""},{"name":"tag","type":"string_list","required":false,"default":["a","b"],"description":"Labels to add, which this description explains at enough length that help has to wrap it onto a second line"}]},` +
	`{"id":"account.create","description":"Create the account","params":[{"name":"username","type":"string","required":true,"description":""}]},` +
	`{"id":"account.delete","description":"Delete the account","params":[]},` +
	`{"id":"account.sync","description":"Sync the account, which takes about 4 seconds","params":[]},` +
	`{"id":"app.hangup","description":"Close the connection without a reply","params":[]},` +
	`{"id":"app.dropout","description":"Close the connection after a notification","params":[]},` +
	`{"id":"app.garbled","description":"Reply with a line that is not JSON","params":[]},` +
	`{"id":"app.teapot","description":"Fail with an error code the CLI does not know","params":[]}` +
	`]}}`

func sampleApp() *fakeApp {
	return &fakeApp{
		pkg:         "io.mehow.quack.sample",
		version:     "1.0",
		installed:   true,
		running:     true,
		protocol:    1,
		hasLauncher: true,
		reply:       sampleReply,
	}
}

func sampleReply(request map[string]any, write func(string)) {
	if request["method"] == "describe" {
		write(sampleDescribe)
		return
	}
	args, _ := request["args"].(map[string]any)
	switch request["action"] {
	case "app.greet":
		name, ok := args["name"].(string)
		if !ok {
			write(`{"error":{"code":"invalid_args","message":"missing argument 'name'"}}`)
			return
		}
		write(fmt.Sprintf(`{"result":{"message":"Hello, %s!"}}`, name))
	case "app.params":
		write(fmt.Sprintf(`{"result":{"message":"tag=%v"}}`, args["tag"]))
	case "account.create":
		write(`{"notification":{"message":"Started"}}`)
		write(`{"notification":{"message":"Creating account"}}`)
		write(`{"result":{"message":"Created"}}`)
	case "account.show":
		write(`{"result":{"message":"Signed in as alice","data":{"username":"alice","tags":["a","b"]}}}`)
	case "account.delete":
		write(`{"error":{"code":"rejected","message":"there is no account"}}`)
	case "account.sync":
		time.Sleep(3500 * time.Millisecond)
		write(`{"result":{"message":"Synced"}}`)
	case "app.hangup":
	case "app.dropout":
		write(`{"notification":{"message":"Working"}}`)
	case "app.garbled":
		write(`not json`)
	case "app.teapot":
		write(`{"error":{"code":"teapot","message":"short and stout"}}`)
	case "app.reject":
		write(`{"error":{"code":"rejected","message":"not today"}}`)
	case "app.crash":
		write(`{"error":{"code":"failed","message":"boom"}}`)
	case "app.misread":
		write(`{"error":{"code":"bad_frame","message":"malformed request"}}`)
	default:
		action, _ := request["action"].(string)
		write(`{"error":{"code":"unknown_action","message":"no action '` + strings.ReplaceAll(action, "'", "") + `'"}}`)
	}
}
