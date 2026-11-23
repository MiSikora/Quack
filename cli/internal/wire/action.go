package wire

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

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

func FindAction(actions []Action, id string) (Action, bool) {
	i := slices.IndexFunc(actions, func(a Action) bool { return a.ID == id })
	if i < 0 {
		return Action{}, false
	}
	return actions[i], true
}

func (p Param) Required() bool {
	_, required := p.Presence.(Required)
	return required
}

// The default as JSON shows it: a string, a []string for a list param, or nil without a default.
func (p Param) DefaultValue() any {
	presence, ok := p.Presence.(Default)
	switch {
	case !ok:
		return nil
	case p.Type.IsList():
		return presence.Values
	case len(presence.Values) > 0:
		return presence.Values[0]
	}
	return nil
}

// Matches the Kotlin library's parsing. A type this CLI does not know takes any value.
func (p Param) Parses(value string) bool {
	switch p.Type {
	case ParamTypeInt:
		_, err := strconv.ParseInt(value, 10, 32)
		return err == nil
	case ParamTypeLong:
		_, err := strconv.ParseInt(value, 10, 64)
		return err == nil
	case ParamTypeDouble:
		f, err := strconv.ParseFloat(value, 64)
		return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
	case ParamTypeBoolean:
		return value == "true" || value == "false"
	case ParamTypeEnum:
		return slices.ContainsFunc(p.Choices, func(c string) bool { return strings.EqualFold(c, value) })
	}
	return true
}

func (p Param) Expected() string {
	if p.Type == ParamTypeEnum {
		return "one of " + strings.Join(p.Choices, ", ")
	}
	return string(p.Type)
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
