package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	for _, test := range []struct {
		name     string
		doc      string
		want     Config
		problems []string
	}{
		{
			name: "no keys",
			doc:  "",
			want: Config{ShowData: false, GroupNamespaces: true},
		},
		{
			name: "both keys",
			doc:  "show-data = true\ngroup-namespaces = false\n",
			want: Config{ShowData: true, GroupNamespaces: false},
		},
		{
			name: "a recipes table, which package recipe reads",
			doc:  "show-data = true\n[recipes]\n'io\\.example' = \"recipes.ts\"\n",
			want: Config{ShowData: true, GroupNamespaces: true},
		},
		{
			name:     "an unknown key",
			doc:      "# settings\nshow-data = true\nshow-dta = true\n",
			want:     Config{ShowData: true, GroupNamespaces: true},
			problems: []string{"config.toml:3: unknown key 'show-dta'"},
		},
		{
			name:     "an unknown table",
			doc:      "group-namespaces = false\n[tui]\nshow-data = true\n",
			want:     Config{ShowData: false, GroupNamespaces: false},
			problems: []string{"config.toml:2: unknown key 'tui'"},
		},
		{
			name:     "a value of the wrong type before a valid key",
			doc:      "show-data = \"no\"\ngroup-namespaces = false\n",
			want:     Config{ShowData: false, GroupNamespaces: false},
			problems: []string{"config.toml:1: 'show-data' must be true or false"},
		},
		{
			name: "every problem at once",
			doc:  "colors = 1\nshow-data = true\ngroup-namespaces = 0\n",
			want: Config{ShowData: true, GroupNamespaces: true},
			problems: []string{
				"config.toml:1: unknown key 'colors'",
				"config.toml:3: 'group-namespaces' must be true or false",
			},
		},
		{
			name:     "a file that does not parse",
			doc:      "show-data = true\ngroup-namespaces =\n",
			want:     Config{ShowData: false, GroupNamespaces: true},
			problems: []string{"config.toml:2:19: unexpected character U+000A at start of value"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, problems := Parse([]byte(test.doc))
			if got != test.want {
				t.Errorf("config: got %+v, want %+v", got, test.want)
			}
			if !reflect.DeepEqual(problems, test.problems) {
				t.Errorf("problems: got %q, want %q", problems, test.problems)
			}
		})
	}
}

func TestLoadWithoutAFileGivesTheDefaultsSilently(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	got, problems := Load()

	if got != Default() || problems != nil {
		t.Errorf("got %+v and %q, want the defaults and no problems", got, problems)
	}
}

func TestLoadReadsTheFileInTheXDGConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "quack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quack", "config.toml"), []byte("show-data = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, problems := Load()

	if want := (Config{ShowData: true, GroupNamespaces: true}); got != want || problems != nil {
		t.Errorf("got %+v and %q, want %+v and no problems", got, problems, want)
	}
}

func TestDirFallsBackToTheHomeConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	if got, want := Path(), filepath.Join(home, ".config", "quack", "config.toml"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
