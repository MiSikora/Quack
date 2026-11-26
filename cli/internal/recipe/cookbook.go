package recipe

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"

	"quack/internal/config"
	"quack/internal/step"
	"quack/internal/wire"
)

// Cookbook holds the recipes of one app: those that every file a [recipes] table maps the app to declares itself.
type Cookbook struct {
	// Sorted by name.
	Recipes []Recipe
	scripts map[string]*script
}

// Run returns what the recipe's run returned as JSON, or nil when it returned undefined. Ending ctx interrupts the
// recipe wherever it is. The events' lines show the args of a Call in the order of its action's params in actions.
func (c *Cookbook) Run(ctx context.Context, app step.App, actions []wire.Action, name string, args step.Arguments, emit func(step.Event)) (json.RawMessage, error) {
	r, ok := c.Find(name)
	if !ok {
		return nil, fmt.Errorf("no recipe named %s for %s", name, app.Target().Package)
	}
	return c.scripts[r.File].run(ctx, app, actions, name, args, emit)
}

func (c *Cookbook) Find(name string) (Recipe, bool) {
	at := slices.IndexFunc(c.Recipes, func(r Recipe) bool { return r.Name == name })
	if at < 0 {
		return Recipe{}, false
	}
	return c.Recipes[at], true
}

// Load reads the recipes of pkg from the selected project and the user's config. It never writes files.
// A problem that leaves out only an entry or a file comes back as a warning, and one that leaves the recipes unknown
// fails the load.
func (l Location) Load(pkg string) (*Cookbook, []string, error) {
	tables, warnings, err := l.readTables(pkg)
	if err != nil {
		return nil, nil, err
	}
	var paths, dirs []string
	for _, t := range tables {
		for _, mapped := range t.files(pkg) {
			names := []string{mapped}
			if strings.ContainsAny(mapped, "*?[") {
				// parseEntry checked the pattern, which is the only error of Glob.
				names, _ = fs.Glob(os.DirFS(t.dir), mapped)
				names = slices.DeleteFunc(names, func(name string) bool {
					info, err := os.Stat(filepath.Join(t.dir, name))
					return err == nil && info.IsDir()
				})
				if len(names) == 0 {
					warnings = append(warnings, fmt.Sprintf("%s: [recipes] maps %s to %s, which matches no files", t.file, pkg, mapped))
				}
			}
			for _, name := range names {
				path := filepath.Join(t.dir, name)
				// A file can be a link, so files compare by their real paths.
				if slices.ContainsFunc(paths, func(other string) bool { return realPath(other) == realPath(path) }) {
					continue
				}
				if problem := unreadable(path); problem != "" {
					warnings = append(warnings, fmt.Sprintf("%s: [recipes] maps %s to %s, %s", t.file, pkg, name, problem))
					continue
				}
				paths = append(paths, path)
				if !slices.Contains(dirs, t.dir) {
					dirs = append(dirs, t.dir)
				}
			}
		}
	}
	cookbook := &Cookbook{Recipes: []Recipe{}, scripts: map[string]*script{}}
	for _, path := range paths {
		s, err := loadScript(path)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range s.recipes {
			if at := slices.IndexFunc(cookbook.Recipes, func(other Recipe) bool { return other.Name == r.Name }); at >= 0 {
				return nil, nil, fmt.Errorf("recipe '%s' is in both %s and %s", r.Name, cookbook.Recipes[at].File, path)
			}
			cookbook.Recipes = append(cookbook.Recipes, r)
		}
		cookbook.scripts[path] = s
	}
	slices.SortFunc(cookbook.Recipes, func(a, b Recipe) int { return strings.Compare(a.Name, b.Name) })
	for _, dir := range dirs {
		switch typesIn(dir) {
		case olderTypes:
			warnings = append(warnings, "the recipe types in "+dir+" are out of date: init-recipes updates them")
		case newerTypes:
			warnings = append(warnings, newerTypesProblem(dir))
		}
	}
	return cookbook, warnings, nil
}

// Init writes editor types in the user's recipes directory and the selected project. With a package and
// no existing project, it starts a project in the caller's directory. It returns the paths it wrote.
func (l Location) Init(pkg string) ([]string, error) {
	project, _, err := l.sources(pkg, true)
	if err != nil {
		return nil, err
	}
	written, err := writeTypes(Dir())
	if err != nil || project == "" {
		return written, err
	}
	projectTypes, err := writeTypes(filepath.Join(project, "recipes"))
	written = append(written, projectTypes...)
	quackToml := filepath.Join(project, "quack.toml")
	if err != nil || pkg == "" || exists(quackToml) {
		return written, err
	}
	mapping := "[recipes]\n'" + regexp.QuoteMeta(pkg) + "' = \"recipes.ts\"\n"
	if err := os.WriteFile(quackToml, []byte(mapping), 0o644); err != nil {
		return written, err
	}
	written = append(written, quackToml)
	recipes := filepath.Join(project, "recipes", "recipes.ts")
	if exists(recipes) {
		return written, nil
	}
	if err := os.WriteFile(recipes, starter, 0o644); err != nil {
		return written, err
	}
	return append(written, recipes), nil
}

// Maps reports whether a [recipes] table of the selected project, or of the user's config, maps pkg to a file.
func (l Location) Maps(pkg string) (bool, error) {
	tables, _, err := l.readTables(pkg)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(tables, func(t table) bool { return len(t.files(pkg)) > 0 }), nil
}

// MapsAny reports whether a [recipes] table of the selected project, or of the user's config, maps any app.
func (l Location) MapsAny() (bool, error) {
	tables, _, err := l.readTables("")
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(tables, func(t table) bool { return len(t.entries) > 0 }), nil
}

var (
	//go:embed types/quack.d.ts
	quackTypes []byte
	//go:embed types/tsconfig.json
	tsconfig []byte
	//go:embed types/starter.ts
	starter []byte
)

var types = map[string][]byte{"quack.d.ts": quackTypes, "tsconfig.json": tsconfig}

const projectDir = ".quack"

type table struct {
	file    string
	dir     string
	entries []entry
}

type entry struct {
	pattern *regexp.Regexp
	// Paths in the table's dir, each a file or a glob of files.
	files []string
}

func (t table) files(pkg string) []string {
	var names []string
	for _, e := range t.entries {
		if e.pattern.MatchString(pkg) {
			names = append(names, e.files...)
		}
	}
	return names
}

func (l Location) readTables(pkg string) ([]table, []string, error) {
	project, user, err := l.sources(pkg, false)
	if err != nil {
		return nil, nil, err
	}
	var tables []table
	var warnings []string
	if project != "" {
		file, err := readTOML(filepath.Join(project, "quack.toml"))
		if err != nil {
			return nil, nil, err
		}
		t, problems, err := file.recipeTable(true)
		if err != nil {
			return nil, nil, err
		}
		tables, warnings = append(tables, t), append(warnings, problems...)
	}
	t, problems, err := user.recipeTable(false)
	if err != nil {
		return nil, nil, err
	}
	return append(tables, t), append(warnings, problems...), nil
}

type tomlFile struct {
	path string
	doc  []byte
}

func readTOML(path string) (tomlFile, error) {
	doc, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return tomlFile{}, err
	}
	return tomlFile{path, doc}, nil
}

func (f tomlFile) recipeTable(onlyRecipes bool) (table, []string, error) {
	t := table{file: f.path, dir: filepath.Join(filepath.Dir(f.path), "recipes")}
	var decoded struct {
		Recipes any `toml:"recipes"`
	}
	warnings, err := config.Decode(f.doc, f.path, onlyRecipes, &decoded)
	if err != nil {
		return table{}, nil, err
	}
	if decoded.Recipes == nil {
		return t, warnings, nil
	}
	recipes, ok := decoded.Recipes.(map[string]any)
	if !ok {
		return t, append(warnings, f.path+": recipes must be a table of patterns"), nil
	}
	for _, pattern := range slices.Sorted(maps.Keys(recipes)) {
		e, problem := parseEntry(pattern, recipes[pattern])
		if problem != "" {
			warnings = append(warnings, fmt.Sprintf("%s: [recipes] '%s' %s", f.path, pattern, problem))
			continue
		}
		t.entries = append(t.entries, e)
	}
	return t, warnings, nil
}

// packagePattern matches an entire package, including when the regex contains alternatives.
func packagePattern(pattern string) (*regexp.Regexp, error) {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, err
	}
	return regexp.Compile("^(?:" + parsed.String() + ")$")
}

func parseEntry(pattern string, value any) (entry, string) {
	re, err := packagePattern(pattern)
	if err != nil {
		return entry{}, "is not a valid regex: " + strings.TrimPrefix(err.Error(), "error parsing regexp: ")
	}
	e := entry{pattern: re}
	switch value := value.(type) {
	case string:
		e.files = []string{value}
	case []any:
		for _, item := range value {
			name, ok := item.(string)
			if !ok {
				return entry{}, "must map to a file name or an array of file names"
			}
			e.files = append(e.files, name)
		}
	default:
		return entry{}, "must map to a file name or an array of file names"
	}
	for i, name := range e.files {
		if !filepath.IsLocal(name) || slices.Contains(strings.Split(filepath.ToSlash(name), "/"), "..") {
			return entry{}, fmt.Sprintf("maps to '%s', which is not a path in the recipes dir", name)
		}
		if _, err := filepath.Match(name, ""); err != nil {
			return entry{}, fmt.Sprintf("maps to '%s', which is not a valid glob", name)
		}
		// fs.Glob takes only clean paths, such as a.ts for ./a.ts.
		e.files[i] = filepath.ToSlash(filepath.Clean(name))
	}
	return e, ""
}

func findProject(start, home string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, projectDir)); err == nil && info.IsDir() {
			return filepath.Join(dir, projectDir)
		}
		if dir == home || dir == filepath.Dir(dir) {
			return ""
		}
	}
}

func home() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Clean(dir)
}

func writeTypes(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if typesIn(dir) == newerTypes {
		return nil, errors.New(newerTypesProblem(dir))
	}
	var written []string
	for _, name := range slices.Sorted(maps.Keys(types)) {
		path := filepath.Join(dir, name)
		if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, types[name]) {
			continue
		}
		if err := os.WriteFile(path, types[name], 0o644); err != nil {
			return nil, err
		}
		written = append(written, path)
	}
	return written, nil
}

func newerTypesProblem(dir string) string {
	return "the recipe types in " + dir + " are from a newer quack: upgrade quack"
}

type typesState int

const (
	currentTypes typesState = iota
	olderTypes
	newerTypes
)

// The version of the recipe types goes up only when quack.d.ts or tsconfig.json change, so that quack's releases
// leave the files of a project alone, and quacks of different versions that share a project tell which is newer.
var typesVersionPattern = regexp.MustCompile(`recipe types (\d+)`)

func typesVersion(content []byte) int {
	match := typesVersionPattern.FindSubmatch(content)
	if match == nil {
		return 0
	}
	version, _ := strconv.Atoi(string(match[1]))
	return version
}

func typesIn(dir string) typesState {
	state := currentTypes
	for name, content := range types {
		current, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case err == nil && typesVersion(current) > typesVersion(content):
			return newerTypes
		case err != nil || !bytes.Equal(current, content):
			state = olderTypes
		}
	}
	return state
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

func unreadable(path string) string {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "which does not exist"
	case err == nil && info.IsDir():
		return "which is a directory"
	}
	file, err := os.Open(path)
	if err != nil {
		return "which quack cannot read: " + err.Error()
	}
	file.Close()
	return ""
}

func realPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}
