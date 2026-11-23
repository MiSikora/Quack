// Package config reads quack's settings file. quack never writes it. Its settings are the terminal UI's, and its
// [recipes] table belongs to package recipe, which reads it on its own.
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
		Recipes         any `toml:"recipes"`
	}
	problems, err := Decode(doc, filepath.Base(Path()), true, &known)
	if err != nil {
		return Default(), []string{err.Error()}
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

// Decode decodes doc, which file names, into v. Strict reports each key that v lacks as a problem at its row, and the
// rest of doc still decodes. A syntax error says where in file it is.
func Decode(doc []byte, file string, strict bool, v any) ([]string, error) {
	decoder := toml.NewDecoder(bytes.NewReader(doc))
	if strict {
		decoder.DisallowUnknownFields()
	}
	var problems []string
	var unknown *toml.StrictMissingError
	var syntax *toml.DecodeError
	switch err := decoder.Decode(v); {
	case errors.As(err, &unknown):
		for _, e := range unknown.Errors {
			row, _ := e.Position()
			problems = append(problems, fmt.Sprintf("%s:%d: unknown key '%s'", file, row, strings.Join(e.Key(), ".")))
		}
		// A plain decode reads whatever the strict one left out.
		if err := toml.Unmarshal(doc, v); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
	case errors.As(err, &syntax):
		row, column := syntax.Position()
		return nil, fmt.Errorf("%s:%d:%d: %s", file, row, column, strings.TrimPrefix(syntax.Error(), "toml: "))
	case err != nil:
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return problems, nil
}

func at(err *toml.DecodeError, text string) string {
	row, _ := err.Position()
	return fmt.Sprintf("%s:%d: %s", filepath.Base(Path()), row, text)
}
