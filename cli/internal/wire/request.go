package wire

import "encoding/json"

// Call asks the app to run an action.
type Call struct {
	Action string
	Args   map[string]RawArg
}

// RawArg holds the values of one key in command-line order. It goes on the wire as a string when the key appears
// once, and as an array when it repeats.
type RawArg []string

func (a RawArg) MarshalJSON() ([]byte, error) {
	if len(a) == 1 {
		return json.Marshal(a[0])
	}
	return json.Marshal([]string(a))
}
