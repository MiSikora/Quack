package wire

import "strings"

type Action struct {
	ID          string
	Description string
	Params      []Param
}

type Param struct {
	Name        string
	Description string
	Type        ParamType
	Choices     []string
	// Offered in the app's order. Any other value works too.
	Hints    []string
	Presence Presence
}

// An app may send a type this list does not have yet.
type ParamType string

const (
	ParamTypeString     ParamType = "string"
	ParamTypeInt        ParamType = "int"
	ParamTypeLong       ParamType = "long"
	ParamTypeDouble     ParamType = "double"
	ParamTypeBoolean    ParamType = "boolean"
	ParamTypeStringList ParamType = "string_list"
	ParamTypeEnum       ParamType = "enum"
)

func (t ParamType) IsList() bool { return t == ParamTypeStringList }

type Presence interface{ presence() }

type Optional struct{}

type Required struct{}

type Default struct {
	Values []string
}

func (d Default) String() string {
	if len(d.Values) == 0 {
		return "none"
	}
	return strings.Join(d.Values, ", ")
}

func (Optional) presence() {}
func (Required) presence() {}
func (Default) presence()  {}
