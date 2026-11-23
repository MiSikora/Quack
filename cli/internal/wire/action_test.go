package wire

import (
	"reflect"
	"testing"
)

func TestFindActionFindsTheActionWithTheId(t *testing.T) {
	actions := []Action{{ID: "app.greet"}, {ID: "account.create", Description: "Create"}}
	if got, ok := FindAction(actions, "account.create"); !ok || got.Description != "Create" {
		t.Errorf("got %+v, %v, want account.create", got, ok)
	}
	if got, ok := FindAction(actions, "account"); ok {
		t.Errorf("got %+v for an id no action has", got)
	}
}

func TestOnlyARequiredParamIsRequired(t *testing.T) {
	cases := []struct {
		presence Presence
		want     bool
	}{
		{Required{}, true},
		{Optional{}, false},
		{Default{[]string{"1"}}, false},
	}
	for _, c := range cases {
		if got := (Param{Presence: c.presence}).Required(); got != c.want {
			t.Errorf("%T: got %v, want %v", c.presence, got, c.want)
		}
	}
}

func TestDefaultValueIsAStringOrAListForAListParam(t *testing.T) {
	cases := []struct {
		param Param
		want  any
	}{
		{Param{Type: ParamTypeInt, Presence: Default{[]string{"1"}}}, "1"},
		{Param{Type: ParamTypeStringList, Presence: Default{[]string{"a", "b"}}}, []string{"a", "b"}},
		{Param{Type: ParamTypeStringList, Presence: Default{[]string{}}}, []string{}},
		{Param{Type: ParamTypeString, Presence: Default{}}, nil},
		{Param{Type: ParamTypeString, Presence: Required{}}, nil},
		{Param{Type: ParamTypeString, Presence: Optional{}}, nil},
	}
	for _, c := range cases {
		if got := c.param.DefaultValue(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: got %#v, want %#v", c.param, got, c.want)
		}
	}
}

func TestParsesTakesOnlyValuesOfTheParamsType(t *testing.T) {
	cases := []struct {
		param  Param
		value  string
		parses bool
	}{
		{Param{Type: ParamTypeInt}, "2147483647", true},
		{Param{Type: ParamTypeInt}, "2147483648", false},
		{Param{Type: ParamTypeInt}, "abc", false},
		{Param{Type: ParamTypeLong}, "5000000000", true},
		{Param{Type: ParamTypeLong}, "1.5", false},
		{Param{Type: ParamTypeDouble}, "0.5", true},
		{Param{Type: ParamTypeDouble}, "NaN", false},
		{Param{Type: ParamTypeDouble}, "-Infinity", false},
		{Param{Type: ParamTypeBoolean}, "false", true},
		{Param{Type: ParamTypeBoolean}, "yes", false},
		{Param{Type: ParamTypeEnum, Choices: []string{"light", "dark"}}, "DARK", true},
		{Param{Type: ParamTypeEnum, Choices: []string{"light", "dark"}}, "drak", false},
		{Param{Type: ParamTypeString}, "", true},
		{Param{Type: "uuid"}, "anything", true},
	}
	for _, c := range cases {
		if got := c.param.Parses(c.value); got != c.parses {
			t.Errorf("%s %q: got %v, want %v", c.param.Type, c.value, got, c.parses)
		}
	}
}

func TestExpectedNamesTheTypeOrTheChoices(t *testing.T) {
	if got := (Param{Type: ParamTypeLong}).Expected(); got != "long" {
		t.Errorf("long: got %q", got)
	}
	if got := (Param{Type: ParamTypeEnum, Choices: []string{"light", "dark"}}).Expected(); got != "one of light, dark" {
		t.Errorf("enum: got %q", got)
	}
}
