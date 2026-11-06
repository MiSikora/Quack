package wire

import "strings"

// Action is an action as describe lists it.
type Action struct {
	ID          string
	Description string
	Params      []Param
}

type Param struct {
	Name        string
	Description string
	Type        ParamType
	// Choices are the values an enum param takes. Other types have none.
	Choices  []string
	Presence Presence
}

// ParamType is the type of a param's value. An app may send a type this list does not have yet.
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

// IsList is whether the param takes a value each time its key repeats.
func (t ParamType) IsList() bool { return t == ParamTypeStringList }

// Presence is whether a call must give a param: Optional, Required, or Default when the app fills it in.
type Presence interface{ presence() }

type Optional struct{}

type Required struct{}

// Default holds the value the app uses when a call leaves the param out: one value, or any number for a list param.
type Default struct {
	Values []string
}

// String shows the default as a user would type it.
func (d Default) String() string {
	if len(d.Values) == 0 {
		return "none"
	}
	return strings.Join(d.Values, ", ")
}

func (Optional) presence() {}
func (Required) presence() {}
func (Default) presence()  {}
