package recipe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProjectSelectionKeepsPersonalRecipes(t *testing.T) {
	project := func(name string) string {
		return writeProject(t, "[recipes]\n'.*' = 'r.ts'\n", map[string]string{"r.ts": recipeNamed(name)})
	}
	outer, inner, configured := project("outer"), project("inner"), project("configured")
	nested := filepath.Join(outer, "nested")
	if err := os.Rename(inner, nested); err != nil {
		t.Fatal(err)
	}
	caller := filepath.Join(nested, "src")
	if err := os.Mkdir(caller, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeFile(t, "mine.ts", recipeNamed("mine"))
	const personal = "[recipes]\n'.*' = 'mine.ts'\n"
	rel, err := filepath.Rel(filepath.Dir(configFile()), configured)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", configured)
	t.Setenv("PROJECT", configured)
	if err := os.Symlink(outer, filepath.Join(configured, "android")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outer, filepath.Join(outside, "$PROJECT")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, dir, flag, projects, app string
		want                           []string
	}{
		{"flag overrides nearest", caller, "../..", "projects = 4", pkg, []string{"mine", "outer"}},
		{"nearest overrides mappings", caller, "", "[projects]\n'.*' = 'bad'\n'io.*' = 'bad'", pkg, []string{"inner", "mine"}},
		{"config relative to its file", outside, "", fmt.Sprintf("[projects]\n'io\\.other|io\\.mehow\\..*' = %q", rel), pkg, []string{"configured", "mine"}},
		{"flag home", outside, "~", "", pkg, []string{"configured", "mine"}},
		{"flag home subdirectory", outside, "~/android", "", pkg, []string{"mine", "outer"}},
		{"literal environment variable", outside, "$PROJECT", "", pkg, []string{"mine", "outer"}},
		{"config home", outside, "", "[projects]\n'.*' = '~/'", pkg, []string{"configured", "mine"}},
		{"quoted through end", outside, "", "[projects]\n'\\Qio.mehow.quack.sample' = '~/'", pkg, []string{"configured", "mine"}},
		{"quoted partial package", outside, "", "[projects]\n'\\Qio.mehow' = 'missing'", pkg, []string{"mine"}},
		{"partial alternatives", outside, "", "[projects]\n'io\\.mehow|quack\\.sample' = 'missing'", pkg, []string{"mine"}},
		{"case insensitive pattern", outside, "", "[projects]\n'(?i)IO\\.MEHOW\\.QUACK\\.SAMPLE' = '~/'", pkg, []string{"configured", "mine"}},
		{"unmatched partial package", outside, "", "[projects]\n'io\\.mehow' = 'missing'", pkg, []string{"mine"}},
		{"no mappings", outside, "", "", pkg, []string{"mine"}},
		{"no app ignores mappings", outside, "", "[projects]\n'.*' = 'bad'\n'' = 'bad'", "", []string{"mine"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeConfig(t, tc.projects+"\n"+personal)
			location := Location{Dir: tc.dir, ProjectDir: tc.flag}
			book, warnings, err := location.Load(tc.app)
			if err != nil || len(warnings) != 0 {
				t.Fatalf("load: %v, warnings %v", err, warnings)
			}
			if got := names(book); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInvalidProjectMappingReportsTheProblemBeforeInitialization(t *testing.T) {
	for _, tc := range []struct{ name, doc, problem string }{
		{"ambiguous even with one root", "[projects]\n'io.*' = 'same'\n'.*' = 'same'", "multiple patterns match " + pkg},
		{"ambiguous with a malformed value", "[projects]\n'.*' = 'same'\n'io.*' = ['bad']", "multiple patterns match " + pkg},
		{"bad regex", "[projects]\n'(' = 'a'", "invalid pattern"},
		{"bad table", "projects = 4", "must be a table"},
		{"bad value", "[projects]\n'.*' = ['a']", "project root path"},
		{"empty path", "[projects]\n'.*' = ''", "project root path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			location := Location{Dir: emptyDir(t)}
			if err := os.MkdirAll(filepath.Dir(configFile()), 0o755); err != nil {
				t.Fatal(err)
			}
			writeConfig(t, tc.doc)
			_, _, loadErr := location.Load(pkg)
			_, initErr := location.Init(pkg)
			_, mapsErr := location.Maps(pkg)
			for _, err := range []error{loadErr, initErr, mapsErr} {
				if err == nil || !strings.Contains(err.Error(), tc.problem) || !strings.Contains(err.Error(), configFile()) {
					t.Errorf("got %v, want %s in %s", err, tc.problem, configFile())
				}
			}
			if _, err := os.Stat(Dir()); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("personal recipes dir after failed initialization: %v", err)
			}
			if mapped, err := location.MapsAny(); err != nil || mapped {
				t.Errorf("without an app: got mapped %t, %v", mapped, err)
			}
			if paths, err := location.Init(""); err != nil || len(paths) != 2 {
				t.Errorf("without an app: wrote %v, %v", paths, err)
			}
		})
	}
}

func TestExactProjectValidatesRootsBeforeWriting(t *testing.T) {
	for _, kind := range []string{"missing", "file", ".quack itself", ".quack through link", ".quack file", "dangling .quack", "named home"} {
		t.Run(kind, func(t *testing.T) {
			location := Location{Dir: emptyDir(t)}
			root := t.TempDir()
			var err error
			switch kind {
			case "missing":
				root = filepath.Join(root, "missing")
			case "file":
				root = filepath.Join(root, "file")
				err = os.WriteFile(root, nil, 0o644)
			case ".quack itself":
				root = filepath.Join(root, ".quack")
				err = os.Mkdir(root, 0o755)
			case ".quack through link":
				if err := os.Mkdir(filepath.Join(root, ".quack"), 0o755); err != nil {
					t.Fatal(err)
				}
				root = filepath.Join(root, "link")
				err = os.Symlink(".quack", root)
			case ".quack file":
				err = os.WriteFile(filepath.Join(root, ".quack"), nil, 0o644)
			case "dangling .quack":
				err = os.Symlink("missing", filepath.Join(root, ".quack"))
			case "named home":
				root = "~other/android"
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(configFile()), 0o755); err != nil {
				t.Fatal(err)
			}
			writeConfig(t, fmt.Sprintf("[projects]\n'.*' = %q", root))
			for _, flag := range []string{"", root} {
				location.ProjectDir = flag
				if _, _, err := location.Load(pkg); err == nil {
					t.Error("load ignored invalid root")
				}
				if paths, err := location.Init(pkg); err == nil || len(paths) != 0 {
					t.Errorf("init wrote %v and returned %v", paths, err)
				}
				if _, err := os.Stat(Dir()); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("personal recipes dir: %v", err)
				}
			}
		})
	}
}

func TestProjectRootDistinguishesMetadataAliasesFromDistinctDirectories(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", linked), func(t *testing.T) {
			location := Location{Dir: emptyDir(t)}
			parent := t.TempDir()
			metadata := filepath.Join(parent, ".quack")
			if err := os.Mkdir(metadata, 0o755); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(parent, ".QUACK")
			if err := os.Mkdir(root, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				t.Fatal(err)
			}
			rootInfo, err := os.Stat(root)
			if err != nil {
				t.Fatal(err)
			}
			metadataInfo, err := os.Stat(metadata)
			if err != nil {
				t.Fatal(err)
			}
			alias := os.SameFile(rootInfo, metadataInfo)
			if linked {
				link := filepath.Join(t.TempDir(), "project")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				root = link
			}
			location.ProjectDir = root
			paths, err := location.Init(pkg)
			if alias {
				if err == nil || !strings.Contains(err.Error(), "is the .quack directory") || len(paths) != 0 {
					t.Fatalf("metadata alias initialization wrote %v, error %v", paths, err)
				}
				for _, path := range []string{Dir(), filepath.Join(metadata, ".quack")} {
					if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
						t.Errorf("failed initialization created %s: %v", path, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("distinct root initialization: %v", err)
			}
			for _, path := range []string{Dir(), filepath.Join(root, ".quack", "recipes")} {
				if _, err := os.Stat(filepath.Join(path, "quack.d.ts")); err != nil {
					t.Error(err)
				}
			}
		})
	}
}

func TestInitCreatesOnlyTheSelectedProject(t *testing.T) {
	parent := writeProject(t, "", nil)
	root := filepath.Join(parent, "sub")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(configFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, fmt.Sprintf("[projects]\n'.*' = %q\n", root))
	explicit := Location{Dir: parent, ProjectDir: "sub"}
	configured := Location{Dir: outside}
	for _, location := range []Location{explicit, configured} {
		if _, _, err := location.Load(pkg); err == nil || !strings.Contains(err.Error(), filepath.Join(root, ".quack")) {
			t.Fatalf("exact load without .quack: %v", err)
		}
	}
	if _, err := explicit.Init(""); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{Dir(), filepath.Join(root, ".quack", "recipes")} {
		if _, err := os.Stat(filepath.Join(dir, "quack.d.ts")); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".quack", "quack.toml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("mapping before an app is known: %v", err)
	}
	if _, err := configured.Init(pkg); err != nil {
		t.Fatal(err)
	}
	book, warnings, err := configured.Load(pkg)
	if err != nil || len(warnings) != 0 || !slices.Equal(names(book), []string{"open"}) {
		t.Fatalf("starter: cookbook %v, warnings %v, error %v", book, warnings, err)
	}
	if files, err := explicit.Init(pkg); err != nil || len(files) != 0 {
		t.Errorf("repeat initialization: wrote %v, %v", files, err)
	}
	if _, err := os.Stat(filepath.Join(outside, ".quack")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("calling directory after initialization: %v", err)
	}
}
