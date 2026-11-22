package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"quack/internal/client"
	"quack/internal/recipe"
	"quack/internal/wire"
)

// stepError is the error that ended step of a recipe, numbered from 1.
type stepError struct {
	step int
	err  error
}

func (e *stepError) Error() string { return e.err.Error() }

func (e *stepError) Unwrap() error { return e.err }

// runRecipe runs the steps of r, after checking its calls against the app's actions, so that a recipe the app would
// refuse runs no step. Each step prints its line, then what the call of an action prints, and the first step that
// fails ends the recipe with its error.
func runRecipe(app client.App, r recipe.Recipe, terminal Terminal, json bool) error {
	ctx := context.Background()
	_, actions, err := app.Describe(ctx)
	if err != nil {
		return err
	}
	if err := recipe.Check(r, actions, app.Target()); err != nil {
		// An environment variable that is not set is the user's to fix, like an arg the app would refuse.
		var refused wire.Error
		if !errors.As(err, &refused) {
			return usageErrorf("%v", err)
		}
		return err
	}
	for i, step := range r.Steps {
		if !json {
			fmt.Fprintln(terminal.Out, "› "+recipe.Line(step, actions))
		}
		result, err := recipe.Do(ctx, app, step, printNotifications(terminal, json, i+1))
		if err != nil {
			return &stepError{i + 1, err}
		}
		// Only a call or a command has a message to print, which may be empty.
		if json || hasMessage(step) {
			printResult(terminal, json, result, i+1)
		}
	}
	return nil
}

// hasMessage is whether step ends with a message to print: what an action returns, or what a command prints.
func hasMessage(step recipe.Step) bool {
	switch step.(type) {
	case recipe.OpenApp, recipe.Wait:
		return false
	}
	return true
}

// findRecipe reads the recipes of pkg and returns the one named name. A file that does not read is bad usage, like a
// name it does not have.
func findRecipe(pkg, name string) (recipe.Recipe, error) {
	recipes, err := recipe.Load(pkg)
	if err != nil {
		return recipe.Recipe{}, usageErrorf("%v", err)
	}
	at := slices.IndexFunc(recipes, func(r recipe.Recipe) bool { return r.Name == name })
	if at < 0 {
		return recipe.Recipe{}, usageErrorf("no recipe named %s in %s", name, recipe.Path(pkg))
	}
	return recipes[at], nil
}

// printRecipes lists the recipes of pkg whose name filter matches, or all of them without a filter, each like a man
// page entry: its name, then its description and its steps indented below it. With asJSON they are an array of
// recipeJSON instead. Without any recipes, it says where to write them. It fails when filter matches none.
func printRecipes(w io.Writer, pkg string, filter *regexp.Regexp, asJSON bool) error {
	recipes, err := recipe.Load(pkg)
	if err != nil {
		return usageErrorf("%v", err)
	}
	var kept []recipe.Recipe
	for _, r := range recipes {
		if filter == nil || filter.MatchString(r.Name) {
			kept = append(kept, r)
		}
	}
	switch {
	case len(recipes) > 0 && len(kept) == 0:
		return usageErrorf("no recipe matches '%s'", filter)
	case asJSON:
		out := make([]recipeJSON, len(kept))
		for i, r := range kept {
			out[i] = recipeJSON{Name: r.Name, Description: r.Description, Steps: make([]string, len(r.Steps))}
			for j, step := range r.Steps {
				out[i].Steps[j] = recipe.Line(step, nil)
			}
		}
		printJSON(w, out)
	case len(kept) == 0:
		fmt.Fprintln(w, "No recipes. Create "+recipe.Path(pkg))
	default:
		var out strings.Builder
		for _, r := range kept {
			out.WriteString(r.Name + "\n")
			if r.Description != "" {
				writeWrapped(&out, sentence(r.Description), helpIndent)
				out.WriteString("\n")
			}
			for _, step := range r.Steps {
				out.WriteString(helpIndent + recipe.Line(step, nil) + "\n")
			}
			out.WriteString("\n")
		}
		fmt.Fprint(w, strings.TrimSuffix(out.String(), "\n"))
	}
	return nil
}
