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
	"slices"
	"strconv"
	"strings"

	"quack/internal/config"
	"quack/internal/wire"
)

// Cookbook holds the recipes of one app: those of every file that a [recipes] table maps the app to.
type Cookbook struct {
	// Sorted by name.
	Recipes []Recipe
	// By path.
	scripts map[string]*script
}

// Run returns what the recipe's run returned as JSON, or nil when it returned undefined. Ending ctx interrupts the
// recipe wherever it is.
func (c *Cookbook) Run(ctx context.Context, app App, name string, args map[string]wire.RawArg, observer Observer) (json.RawMessage, error) {
	at := slices.IndexFunc(c.Recipes, func(r Recipe) bool { return r.Name == name })
	if at < 0 {
		return nil, fmt.Errorf("no recipe named %s for %s", name, app.Target().Package)
	}
	return c.scripts[c.Recipes[at].File].run(ctx, app, name, args, observer)
}

// Load reads the recipes of pkg from the project that dir is in, and from the user's config. It never writes files.
// A problem that leaves out only an entry or a file comes back as a warning, and one that leaves the recipes unknown
// fails the load.
func Load(dir, pkg string) (*Cookbook, []string, error) {
	tables, warnings, err := readTables(dir)
	if err != nil {
		return nil, nil, err
	}
	var paths, dirs []string
	for _, t := range tables {
		for _, path := range t.files(pkg) {
			switch {
			case slices.Contains(paths, path):
			case !exists(path):
				warnings = append(warnings, fmt.Sprintf("%s: [recipes] maps %s to %s, which does not exist", t.file, pkg, filepath.Base(path)))
			default:
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
			// A mapped file that another one imports has its recipes itself.
			if source := s.sources[r.Name]; source != path && slices.Contains(paths, source) {
				continue
			}
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

// Init writes the files that let editors check recipes into the user's recipes dir, and into the project's when dir
// is in one or pkg is given. With pkg and no quack.toml in the project, it starts one that maps pkg to a starter
// recipes file, and creates the project in dir when there is none. It returns the paths it wrote.
func Init(dir, pkg string) ([]string, error) {
	written, err := writeTypes(Dir())
	if err != nil {
		return nil, err
	}
	start, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	project := findProject(start, home())
	switch {
	case project == "" && pkg == "":
		return written, nil
	case project == "":
		project = filepath.Join(start, projectDir)
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

// Maps reports whether a [recipes] table of the project that dir is in, or of the user's config, maps pkg to a file.
func Maps(dir, pkg string) (bool, error) {
	tables, _, err := readTables(dir)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(tables, func(t table) bool { return len(t.files(pkg)) > 0 }), nil
}

var (
	//go:embed types/quack.d.ts
	quackTypes []byte
	//go:embed types/tsconfig.json
	tsconfig []byte
	//go:embed types/starter.ts
	starter []byte
)

// The files quack owns in each recipes dir.
var types = map[string][]byte{"quack.d.ts": quackTypes, "tsconfig.json": tsconfig}

const projectDir = ".quack"

// table is a [recipes] table: the files in dir that hold the recipes of the apps whose packages match each pattern.
type table struct {
	// The TOML file it is in.
	file    string
	dir     string
	entries []entry
}

type entry struct {
	pattern *regexp.Regexp
	// Names of files in the table's dir.
	files []string
}

// Each path once, in the order of the entries.
func (t table) files(pkg string) []string {
	var paths []string
	for _, e := range t.entries {
		if !e.pattern.MatchString(pkg) {
			continue
		}
		for _, name := range e.files {
			if path := filepath.Join(t.dir, name); !slices.Contains(paths, path) {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

// The project's table comes first, then the user's.
func readTables(dir string) ([]table, []string, error) {
	start, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	userFile, err := filepath.Abs(config.Path())
	if err != nil {
		return nil, nil, err
	}
	var tables []table
	var warnings []string
	if project := findProject(start, home()); project != "" {
		t, problems, err := readTable(filepath.Join(project, "quack.toml"), filepath.Join(project, "recipes"), true)
		if err != nil {
			return nil, nil, err
		}
		tables, warnings = append(tables, t), append(warnings, problems...)
	}
	t, problems, err := readTable(userFile, filepath.Join(filepath.Dir(userFile), "recipes"), false)
	if err != nil {
		return nil, nil, err
	}
	return append(tables, t), append(warnings, problems...), nil
}

// A missing file is an empty table.
func readTable(file, dir string, onlyRecipes bool) (table, []string, error) {
	doc, err := os.ReadFile(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return table{file: file, dir: dir}, nil, nil
	case err != nil:
		return table{}, nil, err
	}
	return parseTable(doc, file, dir, onlyRecipes)
}

// onlyRecipes reports the keys besides [recipes] as unknown.
func parseTable(doc []byte, file, dir string, onlyRecipes bool) (table, []string, error) {
	t := table{file: file, dir: dir}
	var decoded struct {
		Recipes any `toml:"recipes"`
	}
	warnings, err := config.Decode(doc, file, onlyRecipes, &decoded)
	if err != nil {
		return table{}, nil, err
	}
	if decoded.Recipes == nil {
		return t, warnings, nil
	}
	recipes, ok := decoded.Recipes.(map[string]any)
	if !ok {
		return t, append(warnings, file+": recipes must be a table of patterns"), nil
	}
	for _, pattern := range slices.Sorted(maps.Keys(recipes)) {
		e, problem := parseEntry(pattern, recipes[pattern])
		if problem != "" {
			warnings = append(warnings, fmt.Sprintf("%s: [recipes] '%s' %s", file, pattern, problem))
			continue
		}
		t.entries = append(t.entries, e)
	}
	return t, warnings, nil
}

// The problem is "" for an entry that parses.
func parseEntry(pattern string, value any) (entry, string) {
	if _, err := regexp.Compile(pattern); err != nil {
		return entry{}, "is not a valid regex: " + strings.TrimPrefix(err.Error(), "error parsing regexp: ")
	}
	e := entry{pattern: regexp.MustCompile("^(?:" + pattern + ")$")}
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
	for _, name := range e.files {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/"+string(filepath.Separator)) {
			return entry{}, fmt.Sprintf("maps to '%s', which is not the name of a file in the recipes dir", name)
		}
	}
	return e, ""
}

// findProject returns the first .quack dir from start up. It checks home, but goes no higher when start is in it.
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

// "" when the home dir is unknown, which lets findProject go up to the root.
func home() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Clean(dir)
}

// It leaves a dir whose types a newer quack wrote, so that a project's types do not go back to an older version.
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

// 0 for a file without a version, such as one from before versions.
func typesVersion(content []byte) int {
	match := typesVersionPattern.FindSubmatch(content)
	if match == nil {
		return 0
	}
	version, _ := strconv.Atoi(string(match[1]))
	return version
}

// A missing file, or one edited by hand, counts as older.
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
