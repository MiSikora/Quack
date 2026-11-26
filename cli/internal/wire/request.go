package wire

import (
	"encoding/json"
	"errors"
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

// ParseList preserves JSON string arrays exactly and otherwise accepts comma-separated values.
// A blank comma-separated field is omitted; an explicit [] is a nonnil empty list.
func ParseList(text string) (RawArg, error) {
	if !strings.HasPrefix(strings.TrimSpace(text), "[") {
		return splitList(text), nil
	}
	invalid := errors.New(`invalid list: expected a JSON array of strings`)
	var items []any
	if err := json.Unmarshal([]byte(text), &items); err != nil {
		return nil, invalid
	}
	values := make(RawArg, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, invalid
		}
		values = append(values, value)
	}
	return values, nil
}

// Spaces around values are trimmed and empty ones dropped. \, is a comma inside a value.
func splitList(text string) RawArg {
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
