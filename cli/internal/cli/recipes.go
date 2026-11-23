package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"quack/internal/client"
	"quack/internal/config"
	"quack/internal/recipe"
	"quack/internal/wire"
)

type stepError struct {
	step int
	err  error
}

func (e *stepError) Error() string { return e.err.Error() }

func (e *stepError) Unwrap() error { return e.err }

// A step error that the recipe catches prints as that step's outcome only once the recipe goes on. What the recipe
// returned prints after its last step, as an action's data does.
func runRecipe(app client.App, c *recipe.Cookbook, name string, args map[string]wire.RawArg, terminal Terminal, json bool) error {
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
	data, err := c.Run(ctx, app, name, args, recipe.Observer{
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
	switch {
	case err != nil || data == nil:
		return err
	case json:
		printJSON(terminal.Out, outcomeJSON{Code: codeOK, Data: data})
	default:
		fmt.Fprintln(terminal.Out, wire.MessageResult{Data: data}.IndentedData())
	}
	return nil
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
	case recipe.OpenApp, recipe.Wait, recipe.RunRecipe:
		return false
	}
	return true
}

// A recipes file that does not load is bad usage, like a name it does not have. The warnings go to stderr.
func loadRecipes(pkg string, terminal Terminal) (*recipe.Cookbook, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	c, warnings, err := recipe.Load(dir, pkg)
	if err != nil {
		return nil, usageErrorf("%v", err)
	}
	for _, warning := range warnings {
		fmt.Fprintln(terminal.Err, "quack: "+warning)
	}
	return c, nil
}

func findRecipe(pkg, name string, terminal Terminal) (*recipe.Cookbook, error) {
	c, err := loadRecipes(pkg, terminal)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(c.Recipes, func(r recipe.Recipe) bool { return r.Name == name }) {
		return nil, usageErrorf("no recipe named %s for %s", name, pkg)
	}
	return c, nil
}

func printRecipes(terminal Terminal, pkg string, filter *regexp.Regexp, asJSON bool) error {
	c, err := loadRecipes(pkg, terminal)
	if err != nil {
		return err
	}
	var kept []recipe.Recipe
	for _, r := range c.Recipes {
		if filter == nil || filter.MatchString(r.Name) {
			kept = append(kept, r)
		}
	}
	w := terminal.Out
	switch {
	case len(c.Recipes) > 0 && len(kept) == 0:
		return usageErrorf("no recipe matches '%s'", filter)
	case asJSON:
		out := make([]recipeJSON, len(kept))
		for i, r := range kept {
			out[i] = recipeJSON{Name: r.Name, Description: r.Description, Params: paramsJSON(r.Params)}
		}
		printJSON(w, out)
	case len(kept) == 0:
		fmt.Fprintln(w, "No recipes for "+pkg+". quack --init-recipes --app "+pkg+" starts .quack/ here, or map the app under [recipes] in "+config.Path()+".")
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
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	written, err := recipe.Init(dir, pkg)
	for _, path := range written {
		fmt.Fprintln(w, "wrote "+path)
	}
	if err != nil {
		return err
	}
	if len(written) == 0 {
		fmt.Fprintln(w, "The files that let editors check recipes are up to date.")
	}
	if pkg == "" {
		fmt.Fprintln(w, "Map apps to recipe files under [recipes] in .quack/quack.toml, or in "+config.Path()+".\n"+
			"quack --init-recipes --app <package> starts .quack/ here.")
		return nil
	}
	mapped, err := recipe.Maps(dir, pkg)
	switch {
	case err != nil:
		return err
	case mapped:
		fmt.Fprintln(w, "The recipes of "+pkg+" are in the files that [recipes] maps it to.")
	default:
		fmt.Fprintln(w, "Nothing maps "+pkg+" to a recipes file yet. Add a line under [recipes] in .quack/quack.toml, such as\n"+
			"'"+regexp.QuoteMeta(pkg)+"' = \"recipes.ts\"")
	}
	return nil
}
