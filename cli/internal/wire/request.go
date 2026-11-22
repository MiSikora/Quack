package wire

import (
	"encoding/json"
	"strings"
)

type Call struct {
	Action string
	Args   map[string]RawArg
}

// Goes on the wire as a string when the key appears once, and as an array when it repeats.
type RawArg []string

func (a RawArg) MarshalJSON() ([]byte, error) {
	if len(a) == 1 {
		return json.Marshal(a[0])
	}
	return json.Marshal([]string(a))
}

// Spaces around values are trimmed and empty ones dropped. \, is a comma inside a value.
func SplitList(text string) RawArg {
	var values RawArg
	var value strings.Builder
	add := func() {
		if v := strings.TrimSpace(value.String()); v != "" {
			values = append(values, v)
		}
		value.Reset()
	}
	for i := 0; i < len(text); i++ {
		switch {
		case strings.HasPrefix(text[i:], `\,`):
			value.WriteByte(',')
			i++
		case text[i] == ',':
			add()
		default:
			value.WriteByte(text[i])
		}
	}
	add()
	return values
}
