package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"quack/internal/client"
	"quack/internal/recipe"
	"quack/internal/wire"
)

type stepError struct {
	step int
	err  error
}

func (e *stepError) Error() string { return e.err.Error() }

func (e *stepError) Unwrap() error { return e.err }

// A step error that the recipe catches prints as that step's outcome only once the recipe goes on.
func runRecipe(app client.App, s *recipe.Script, name string, args map[string]wire.RawArg, terminal Terminal, json bool) error {
	ctx := context.Background()
	_, actions, err := app.Describe(ctx)
	if err != nil {
		return err
	}
	step := 0
	// failure is the error of the step that failed last, until the recipe shows whether it caught it.
	var failure *stepError
	caught := func() {
		if failure != nil {
			printFailure(terminal, json, failure)
			failure = nil
		}
	}
	err = s.Run(ctx, app, name, args, recipe.Observer{
		Started: func(started recipe.Step) {
			caught()
			step++
			if !json {
				fmt.Fprintln(terminal.Out, "› "+recipe.Line(started, actions))
			}
		},
		Notified: func(notification wire.Notification) { printNotifications(terminal, json, step)(notification) },
		Ended: func(ended recipe.Step, result wire.MessageResult) {
			if json || hasMessage(ended) {
				printResult(terminal, json, result, step)
			}
		},
		Failed: func(_ recipe.Step, err error) { failure = &stepError{step, err} },
		Logged: func(text string) {
			caught()
			if json {
				printJSON(terminal.Out, notificationJSON{Code: codeLog, Message: text, Step: step})
			} else {
				fmt.Fprintln(terminal.Out, text)
			}
		},
	})
	if failure != nil && errors.Is(err, failure.err) {
		return &stepError{failure.step, err}
	}
	caught()
	return err
}

func printFailure(terminal Terminal, json bool, failed *stepError) {
	code, message, _ := failure(failed.err)
	if json {
		printJSON(terminal.Out, outcomeJSON{Code: code, Message: message, Step: failed.step})
		return
	}
	fmt.Fprintln(terminal.Out, failed.err.Error())
}

func hasMessage(step recipe.Step) bool {
	switch step.(type) {
	case recipe.OpenApp, recipe.Wait:
		return false
	}
	return true
}

// A file that does not load is bad usage, like a name it does not have.
func findRecipe(pkg, name string) (*recipe.Script, error) {
	s, err := recipe.Load(pkg)
	if err != nil {
		return nil, usageErrorf("%v", err)
	}
	if s == nil || !slices.ContainsFunc(s.Recipes, func(r recipe.Recipe) bool { return r.Name == name }) {
		return nil, usageErrorf("no recipe named %s in %s", name, recipe.Path(pkg))
	}
	return s, nil
}

func printRecipes(w io.Writer, pkg string, filter *regexp.Regexp, asJSON bool) error {
	s, err := recipe.Load(pkg)
	if err != nil {
		return usageErrorf("%v", err)
	}
	var recipes, kept []recipe.Recipe
	if s != nil {
		recipes = s.Recipes
	}
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
			out[i] = recipeJSON{Name: r.Name, Description: r.Description, Params: paramsJSON(r.Params)}
		}
		printJSON(w, out)
	case len(kept) == 0:
		fmt.Fprintln(w, "No recipes. Create "+recipe.Path(pkg)+", and run quack --init-recipes for editors to check it.")
	default:
		var out strings.Builder
		for _, r := range kept {
			writeAction(&out, wire.Action{ID: r.Name, Description: r.Description, Params: r.Params})
		}
		fmt.Fprint(w, strings.TrimSuffix(out.String(), "\n"))
	}
	return nil
}

func initRecipes(w io.Writer, pkg string) error {
	written, err := recipe.Init(pkg)
	if err != nil {
		return err
	}
	for _, path := range written {
		fmt.Fprintln(w, "wrote "+path)
	}
	if len(written) == 0 {
		fmt.Fprintln(w, "The files that let editors check recipes are up to date in "+recipe.Dir()+".")
	}
	if pkg == "" {
		fmt.Fprintln(w, "Write the recipes of an app in "+filepath.Join(recipe.Dir(), "<package>.ts")+".")
	} else {
		fmt.Fprintln(w, "Write the recipes of "+pkg+" in "+recipe.Path(pkg)+".")
	}
	return nil
}
