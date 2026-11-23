package recipe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"quack/internal/client"
)

func TestLoadMergesTheFilesOfEveryPatternThatMatchesTheApp(t *testing.T) {
	dir := writeProject(t, `[recipes]
'io\.mehow\..*' = "project.ts"
'com\.other' = "other.ts"
`, map[string]string{
		"project.ts": recipeNamed("fromProject"),
		"other.ts":   "export const broken = (",
	})
	writeFile(t, "a.ts", recipeNamed("fromA"))
	writeFile(t, "b.ts", recipeNamed("fromB"))
	writeConfig(t, `show-data = false
[recipes]
'io\.mehow\.quack\.sample' = ["a.ts", "b.ts"]
'.*' = "a.ts"
`)

	c, warnings, err := Load(dir, pkg)

	if err != nil || warnings != nil {
		t.Fatalf("got %v and %q", err, warnings)
	}
	want := map[string]string{
		"fromA":       filepath.Join(Dir(), "a.ts"),
		"fromB":       filepath.Join(Dir(), "b.ts"),
		"fromProject": filepath.Join(dir, ".quack", "recipes", "project.ts"),
	}
	if got := files(c); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if names := names(c); !slices.IsSorted(names) {
		t.Errorf("got %v, want them sorted", names)
	}
}

func TestLoadRunsEachRecipeFromItsOwnFile(t *testing.T) {
	writeFile(t, "a.ts", `import quack from "quack"
export const first = quack.recipe({ run() { console.log("a") } })`)
	writeFile(t, "b.ts", `import quack from "quack"
export const second = quack.recipe({ run() { console.log("b") } })`)
	writeConfig(t, "[recipes]\n'.*' = [\"a.ts\", \"b.ts\"]\n")
	c, err := loadApp(t)
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	observer := Observer{Logged: func(text string) { logged = append(logged, text) }}

	for _, name := range []string{"second", "first"} {
		if _, err := c.Run(context.Background(), &fakeApp{}, name, nil, observer); err != nil {
			t.Fatal(err)
		}
	}

	if want := []string{"b", "a"}; !reflect.DeepEqual(logged, want) {
		t.Errorf("got %v, want %v", logged, want)
	}
}

func TestAPatternMatchesTheWholePackage(t *testing.T) {
	for pattern, matches := range map[string]bool{
		`io\.mehow`:                 false,
		`quack\.sample`:             false,
		`io\.mehow.*`:               true,
		`io\.mehow\.quack\.sample`:  true,
		`com\.other|io\.mehow\..*`:  true,
		`io\.mehow\.quack\.sample2`: false,
	} {
		writeFile(t, "a.ts", recipeNamed("a"))
		writeConfig(t, "[recipes]\n'"+pattern+"' = \"a.ts\"\n")

		c, err := loadApp(t)

		if err != nil || (len(names(c)) == 1) != matches {
			t.Errorf("%s: got %v, %v, want a match %v", pattern, names(c), err, matches)
		}
	}
}

func TestAProjectThatMapsAnotherAppGivesNothing(t *testing.T) {
	dir := writeProject(t, "[recipes]\n'com\\.other' = \"recipes.ts\"\n", map[string]string{"recipes.ts": recipeNamed("open")})

	c, warnings, err := Load(dir, pkg)

	if err != nil || warnings != nil || len(c.Recipes) != 0 {
		t.Errorf("got %v, %q, %v, want no recipes", names(c), warnings, err)
	}
}

func TestLoadLeavesOutAFileNoPatternMapsWhichAMappedFileImports(t *testing.T) {
	writeFile(t, "common.ts", `import quack from "quack"
export const helper = quack.recipe({ run() {} })
export function greet() { quack.runAction("app.greet", { name: "Bob" }) }`)
	write(t, `import quack from "quack"
import { greet } from "./common"
export const hello = quack.recipe({ run() { greet() } })`)

	c, err := loadApp(t)

	if err != nil || !reflect.DeepEqual(names(c), []string{"hello"}) {
		t.Fatalf("got %v, %v, want only hello", names(c), err)
	}
	app := &fakeApp{}
	if _, err := c.Run(context.Background(), app, "hello", nil, Observer{}); err != nil || !reflect.DeepEqual(app.calls, []string{"app.greet name=Bob"}) {
		t.Errorf("run: got %v and calls %v", err, app.calls)
	}
}

func TestAMappedFileThatAnotherImportsHasItsRecipesOnlyItself(t *testing.T) {
	common := writeFile(t, "common.ts", `import quack from "quack"
export const login = quack.recipe({ name: "login", run() { quack.runAction("app.login", { name: "Ann" }) } })
export const seed = quack.recipe({ run() {} })
export function greet() { quack.runAction("app.greet", { name: "Bob" }) }`)
	debug := writeFile(t, "debug.ts", `import quack from "quack"
import { greet, login } from "./common"
export { seed } from "./common"
export const reset = quack.recipe({ run() { greet(); quack.runRecipe(login) } })`)
	writeConfig(t, "[recipes]\n'.*' = [\"common.ts\", \"debug.ts\"]\n")

	c, err := loadApp(t)

	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, r := range c.Recipes {
		files[r.Name] = r.File
	}
	if want := map[string]string{"login": common, "reset": debug, "seed": common}; !reflect.DeepEqual(files, want) {
		t.Fatalf("got %v, want %v", files, want)
	}
	app := &fakeApp{}
	if _, err := c.Run(context.Background(), app, "reset", nil, Observer{}); err != nil || !reflect.DeepEqual(app.calls, []string{"app.greet name=Bob", "app.login name=Ann"}) {
		t.Errorf("run: got %v and calls %v", err, app.calls)
	}
}

func TestAMappedFileThatDoesNotExistIsAWarning(t *testing.T) {
	writeFile(t, "a.ts", recipeNamed("a"))
	writeConfig(t, "[recipes]\n'.*' = [\"gone.ts\", \"a.ts\"]\n")

	c, warnings, err := Load(t.TempDir(), pkg)

	want := []string{configFile() + ": [recipes] maps io.mehow.quack.sample to gone.ts, which does not exist"}
	if err != nil || !reflect.DeepEqual(warnings, want) || !reflect.DeepEqual(names(c), []string{"a"}) {
		t.Errorf("got %v, %q, %v, want a and %q", names(c), warnings, err, want)
	}
}

func TestEntriesThatDoNotParseAreWarningsAndTheOthersApply(t *testing.T) {
	dir := writeProject(t, `colors = 1
[recipes]
'io\.mehow(' = "a.ts"
'.*' = "a.ts"
'io.*' = "../escape.ts"
'i.*' = "sub/x.ts"
'.' = ".."
'io\..*' = 3
'io\.mehow\..*' = ["a.ts", 4]
[other]
x = 1
`, map[string]string{"a.ts": recipeNamed("a")})
	file := filepath.Join(dir, ".quack", "quack.toml")

	c, warnings, err := Load(dir, pkg)

	want := []string{
		file + ":1: unknown key 'colors'",
		file + ":10: unknown key 'other'",
		file + ": [recipes] '.' maps to '..', which is not the name of a file in the recipes dir",
		file + ": [recipes] 'i.*' maps to 'sub/x.ts', which is not the name of a file in the recipes dir",
		file + ": [recipes] 'io.*' maps to '../escape.ts', which is not the name of a file in the recipes dir",
		file + ": [recipes] 'io\\..*' must map to a file name or an array of file names",
		file + ": [recipes] 'io\\.mehow(' is not a valid regex: missing closing ): `io\\.mehow(`",
		file + ": [recipes] 'io\\.mehow\\..*' must map to a file name or an array of file names",
	}
	if err != nil || !reflect.DeepEqual(warnings, want) || !reflect.DeepEqual(names(c), []string{"a"}) {
		t.Errorf("got %v, %v and warnings\n%s\nwant a and\n%s", names(c), err, strings.Join(warnings, "\n"), strings.Join(want, "\n"))
	}
}

func TestTheUserConfigsOtherKeysAreNoWarningsOfRecipes(t *testing.T) {
	write(t, recipeNamed("a"))
	writeConfig(t, "show-data = false\ncolors = 1\n[recipes]\n'.*' = \"io.mehow.quack.sample.ts\"\n")

	if _, err := loadApp(t); err != nil {
		t.Error(err)
	}
}

func TestATOMLFileThatDoesNotParseFailsTheLoad(t *testing.T) {
	writeFile(t, "a.ts", recipeNamed("a"))
	writeConfig(t, "[recipes]\n'.*' =\n")
	project := writeProject(t, "[recipes\n", nil)

	for dir, want := range map[string]string{
		project:     filepath.Join(project, ".quack", "quack.toml") + ":1:",
		t.TempDir(): configFile() + ":2:7: ",
	} {
		if _, _, err := Load(dir, pkg); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("got %v, want %s…", err, want)
		}
	}
}

func TestARecipeNameInTwoFilesFailsTheLoadNamingBoth(t *testing.T) {
	dir := writeProject(t, "[recipes]\n'.*' = \"recipes.ts\"\n", map[string]string{"recipes.ts": recipeNamed("open")})
	writeFile(t, "mine.ts", recipeNamed("open"))
	writeConfig(t, "[recipes]\n'.*' = \"mine.ts\"\n")

	_, _, err := Load(dir, pkg)

	want := "recipe 'open' is in both " + filepath.Join(dir, ".quack", "recipes", "recipes.ts") + " and " + filepath.Join(Dir(), "mine.ts")
	if err == nil || err.Error() != want {
		t.Errorf("got %v, want %s", err, want)
	}
}

func TestFindProjectGoesUpToHomeButNoHigher(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(home, "work", "app")
	for _, dir := range []string{filepath.Join(root, ".quack"), work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if got := findProject(work, home); got != "" {
		t.Errorf("from inside home: got %s, want none", got)
	}
	if got := findProject(filepath.Join(root, "elsewhere"), home); got != filepath.Join(root, ".quack") {
		t.Errorf("from outside home: got %s, want the one above", got)
	}
	if err := os.Mkdir(filepath.Join(home, ".quack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findProject(work, home); got != filepath.Join(home, ".quack") {
		t.Errorf("with one in home: got %s, want it", got)
	}
	if err := os.MkdirAll(filepath.Join(home, "work", ".quack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findProject(work, home); got != filepath.Join(home, "work", ".quack") {
		t.Errorf("with one nearer: got %s, want it", got)
	}
}

func TestLoadWritesNothingAndWarnsOfOutOfDateTypesInEachDirItLoadedFrom(t *testing.T) {
	dir := writeProject(t, "[recipes]\n'.*' = \"recipes.ts\"\n'com\\.other' = \"other.ts\"\n", map[string]string{"recipes.ts": recipeNamed("open")})
	write(t, recipeNamed("mine"))
	os.WriteFile(filepath.Join(Dir(), "quack.d.ts"), []byte("old"), 0o644)
	os.Remove(filepath.Join(dir, ".quack", "recipes", "tsconfig.json"))
	before := tree(t, filepath.Dir(dir))

	_, warnings, err := Load(dir, pkg)

	want := []string{
		"the recipe types in " + filepath.Join(dir, ".quack", "recipes") + " are out of date: init-recipes updates them",
		"the recipe types in " + Dir() + " are out of date: init-recipes updates them",
	}
	if err != nil || !reflect.DeepEqual(warnings, want) {
		t.Errorf("got %v and\n%s\nwant\n%s", err, strings.Join(warnings, "\n"), strings.Join(want, "\n"))
	}
	if after := tree(t, filepath.Dir(dir)); !reflect.DeepEqual(after, before) {
		t.Errorf("the files changed from %v to %v", before, after)
	}
	if _, warnings, _ := Load(t.TempDir(), "com.other"); warnings != nil {
		t.Errorf("for an app without files: got %q, want no warnings", warnings)
	}
}

// The hash of each version of the recipe types. A change to quack.d.ts or tsconfig.json raises the version in both and
// adds its hash here, so that quacks sharing a project tell older types from newer ones.
var typesHashes = map[int]string{
	1: "089d231843cb3df7c40ef09c0e652b08c0615f7e72b6eeda739a653c0276d7f7",
}

func TestEachChangeOfTheRecipeTypesRaisesTheirVersion(t *testing.T) {
	version := typesVersion(quackTypes)
	if got := typesVersion(tsconfig); got != version {
		t.Fatalf("quack.d.ts is recipe types %d and tsconfig.json %d, want the same", version, got)
	}
	hash := sha256.Sum256(append(slices.Clone(quackTypes), tsconfig...))
	if got := hex.EncodeToString(hash[:]); typesHashes[version] != got {
		t.Errorf("the recipe types changed: raise 'recipe types %d' in quack.d.ts and tsconfig.json, and add %q to typesHashes", version, got)
	}
}

func TestLoadSaysToUpgradeQuackForTypesANewerQuackWrote(t *testing.T) {
	dir := writeProject(t, "[recipes]\n'.*' = \"recipes.ts\"\n", map[string]string{"recipes.ts": recipeNamed("open")})
	recipes := filepath.Join(dir, ".quack", "recipes")
	newer := strings.Replace(string(quackTypes), "recipe types 1.", "recipe types 99.", 1)
	os.WriteFile(filepath.Join(recipes, "quack.d.ts"), []byte(newer), 0o644)

	_, warnings, err := Load(dir, pkg)

	want := []string{"the recipe types in " + recipes + " are from a newer quack: upgrade quack"}
	if err != nil || !reflect.DeepEqual(warnings, want) {
		t.Errorf("got %q, %v, want %q", warnings, err, want)
	}
	if _, err := Init(dir, ""); err == nil || err.Error() != want[0] {
		t.Errorf("Init: got %v, want %s", err, want[0])
	}
	if got, _ := os.ReadFile(filepath.Join(recipes, "quack.d.ts")); string(got) != newer {
		t.Error("Init wrote over the newer types")
	}
}

func TestInitWithAnAppStartsAProjectWhoseRecipeRuns(t *testing.T) {
	dir := emptyDir(t)

	written, err := Init(dir, pkg)

	project := filepath.Join(dir, ".quack")
	want := []string{
		filepath.Join(Dir(), "quack.d.ts"),
		filepath.Join(Dir(), "tsconfig.json"),
		filepath.Join(project, "recipes", "quack.d.ts"),
		filepath.Join(project, "recipes", "tsconfig.json"),
		filepath.Join(project, "quack.toml"),
		filepath.Join(project, "recipes", "recipes.ts"),
	}
	if err != nil || !reflect.DeepEqual(written, want) {
		t.Fatalf("got %v, %v, want\n%s", written, err, strings.Join(want, "\n"))
	}
	if got, _ := os.ReadFile(filepath.Join(project, "quack.toml")); string(got) != "[recipes]\n'io\\.mehow\\.quack\\.sample' = \"recipes.ts\"\n" {
		t.Errorf("quack.toml: got %q", got)
	}
	c, warnings, err := Load(dir, pkg)
	if err != nil || warnings != nil || !reflect.DeepEqual(names(c), []string{"open"}) {
		t.Fatalf("load: got %v, %q, %v", names(c), warnings, err)
	}
	app := &fakeApp{}
	if _, err := c.Run(context.Background(), app, "open", nil, Observer{}); err != nil || !reflect.DeepEqual(app.starts, []client.StartMode{client.OpenApp}) {
		t.Errorf("open: got %v and starts %v", err, app.starts)
	}
	if written, err := Init(dir, pkg); err != nil || len(written) != 0 {
		t.Errorf("again: got %v, %v, want nothing written", written, err)
	}
}

func TestInitLeavesAProjectsQuackTomlAndRecipesAloneAndUpdatesItsTypes(t *testing.T) {
	mapping := "[recipes]\n'com\\.other' = \"recipes.ts\"\n"
	dir := writeProject(t, mapping, map[string]string{"recipes.ts": "// mine", "quack.d.ts": "old"})
	sub := filepath.Join(dir, "src", "main")
	os.MkdirAll(sub, 0o755)

	written, err := Init(sub, pkg)

	recipes := filepath.Join(dir, ".quack", "recipes")
	want := []string{filepath.Join(Dir(), "quack.d.ts"), filepath.Join(Dir(), "tsconfig.json"), filepath.Join(recipes, "quack.d.ts")}
	if err != nil || !reflect.DeepEqual(written, want) {
		t.Errorf("got %v, %v, want %v", written, err, want)
	}
	for path, want := range map[string]string{filepath.Join(dir, ".quack", "quack.toml"): mapping, filepath.Join(recipes, "recipes.ts"): "// mine"} {
		if got, _ := os.ReadFile(path); string(got) != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(sub, ".quack")); err == nil {
		t.Error("Init started a second project")
	}
}

func TestInitWithoutAnAppWritesTheTypesOfTheUserAndOfAProjectIfThereIsOne(t *testing.T) {
	outside := emptyDir(t)
	if written, err := Init(outside, ""); err != nil || !reflect.DeepEqual(written, []string{filepath.Join(Dir(), "quack.d.ts"), filepath.Join(Dir(), "tsconfig.json")}) {
		t.Errorf("outside a project: got %v, %v", written, err)
	}
	if _, err := os.Stat(filepath.Join(outside, ".quack")); err == nil {
		t.Error("outside a project: Init started one")
	}
	for name, want := range types {
		if got, _ := os.ReadFile(filepath.Join(Dir(), name)); string(got) != string(want) {
			t.Errorf("%s: got %q", name, got)
		}
	}

	dir := writeProject(t, "", nil)
	recipes := filepath.Join(dir, ".quack", "recipes")
	for name := range types {
		os.Remove(filepath.Join(recipes, name))
	}
	if written, err := Init(dir, ""); err != nil || !reflect.DeepEqual(written, []string{filepath.Join(recipes, "quack.d.ts"), filepath.Join(recipes, "tsconfig.json")}) {
		t.Errorf("in a project: got %v, %v", written, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".quack", "quack.toml")); err == nil {
		t.Error("in a project: Init wrote quack.toml")
	}
}

// writeProject makes a dir whose .quack holds quackToml and the files in its recipes dir, with up-to-date types, and
// gives the user a config dir of the test's own.
func writeProject(t *testing.T, quackToml string, files map[string]string) string {
	t.Helper()
	dir := emptyDir(t)
	recipes := filepath.Join(dir, ".quack", "recipes")
	if _, err := writeTypes(recipes); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(recipes, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if quackToml != "" {
		if err := os.WriteFile(filepath.Join(dir, ".quack", "quack.toml"), []byte(quackToml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// emptyDir is a dir outside any project and outside home, with a config dir of the test's own.
func emptyDir(t *testing.T) string {
	t.Helper()
	if !strings.HasPrefix(os.Getenv("XDG_CONFIG_HOME"), os.TempDir()) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	t.Setenv("HOME", t.TempDir())
	return t.TempDir()
}

func recipeNamed(name string) string {
	return `import quack from "quack"
export const ` + name + ` = quack.recipe({ run() {} })`
}

func configFile() string { return filepath.Join(filepath.Dir(Dir()), "config.toml") }

func names(c *Cookbook) []string {
	if c == nil {
		return nil
	}
	var names []string
	for _, r := range c.Recipes {
		names = append(names, r.Name)
	}
	return names
}

func files(c *Cookbook) map[string]string {
	files := map[string]string{}
	for _, r := range c.Recipes {
		files[r.Name] = r.File
	}
	return files
}

// tree is every file under dir with its content.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		files[path] = string(content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
