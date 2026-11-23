// Package config reads quack's settings file, which only the terminal UI uses. quack never writes it.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	// The first state of /toggle-data.
	ShowData bool
	// Browsing gathers the ids that share a namespace into its group.
	GroupNamespaces bool
}

func Default() Config { return Config{ShowData: true, GroupNamespaces: true} }

// Dir holds quack's files: $XDG_CONFIG_HOME/quack, or ~/.config/quack when XDG_CONFIG_HOME is unset or empty.
func Dir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "quack")
}

func Path() string { return filepath.Join(Dir(), "config.toml") }

// Load never fails: a missing file gives the defaults, and each problem with the file comes back as a line to show
// while the rest of the file applies.
func Load() (Config, []string) {
	doc, err := os.ReadFile(Path())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Default(), nil
	case err != nil:
		return Default(), []string{err.Error()}
	}
	return Parse(doc)
}

// Parse decodes each key on its own, so that a value of the wrong type leaves only its key at the default.
func Parse(doc []byte) (Config, []string) {
	config := Default()
	// Values of any type pass, so that this decode fails only on syntax and on unknown keys.
	var known struct {
		ShowData        any `toml:"show-data"`
		GroupNamespaces any `toml:"group-namespaces"`
	}
	decoder := toml.NewDecoder(bytes.NewReader(doc))
	decoder.DisallowUnknownFields()
	var problems []string
	var unknown *toml.StrictMissingError
	var syntax *toml.DecodeError
	switch err := decoder.Decode(&known); {
	case errors.As(err, &unknown):
		for _, e := range unknown.Errors {
			problems = append(problems, at(&e, fmt.Sprintf("unknown key '%s'", strings.Join(e.Key(), "."))))
		}
	case errors.As(err, &syntax):
		row, column := syntax.Position()
		return Default(), []string{fmt.Sprintf("%s:%d:%d: %s", filepath.Base(Path()), row, column, strings.TrimPrefix(syntax.Error(), "toml: "))}
	case err != nil:
		return Default(), []string{filepath.Base(Path()) + ": " + err.Error()}
	}
	var showData struct {
		Value *bool `toml:"show-data"`
	}
	problems = append(problems, decodeKey(doc, &showData, &showData.Value, &config.ShowData)...)
	var groupNamespaces struct {
		Value *bool `toml:"group-namespaces"`
	}
	problems = append(problems, decodeKey(doc, &groupNamespaces, &groupNamespaces.Value, &config.GroupNamespaces)...)
	return config, problems
}

// holder is a struct of the key's one field, decoded points into it, and setting takes its value when it decodes.
func decodeKey(doc []byte, holder any, decoded **bool, setting *bool) []string {
	var wrong *toml.DecodeError
	switch err := toml.Unmarshal(doc, holder); {
	case errors.As(err, &wrong):
		return []string{at(wrong, fmt.Sprintf("'%s' must be true or false", strings.Join(wrong.Key(), ".")))}
	case err != nil:
		return []string{filepath.Base(Path()) + ": " + err.Error()}
	case *decoded != nil:
		*setting = **decoded
	}
	return nil
}

func at(err *toml.DecodeError, text string) string {
	row, _ := err.Position()
	return fmt.Sprintf("%s:%d: %s", filepath.Base(Path()), row, text)
}
