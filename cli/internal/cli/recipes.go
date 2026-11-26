package cli

import (
	"context"
	encodingjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"quack/internal/config"
	"quack/internal/recipe"
	"quack/internal/session"
	"quack/internal/step"
	"quack/internal/wire"
)

func runPrinted(app session.App, cookbook *recipe.Cookbook, r session.Runnable, args, shownArgs map[string]wire.RawArg, terminal Terminal, json bool) error {
	ctx := context.Background()
	_, actions, err := app.Describe(ctx)
	if err != nil {
		return err
	}
	var caught *step.Failed
	printCaught := func() {
		if caught != nil {
			printFailure(terminal, json, *caught)
			caught = nil
		}
	}
	record := func(e step.Event) {
		switch e := e.(type) {
		case step.Started:
			printCaught()
			if r.Recipe && !json {
				fmt.Fprintln(terminal.Out, "› "+e.Line)
			}
		case step.Notified:
			printNotification(terminal, json, e.Notification, e.Number)
		case step.Ended:
			if json || hasMessage(e.Step) {
				printResult(terminal, json, e.Result, e.Number)
			}
		case step.Failed:
			caught = &e
		case step.Logged:
			printCaught()
			if json {
				printJSON(terminal.Out, notificationJSON{Code: codeLog, Message: e.Text, Step: e.Number})
			} else {
				fmt.Fprintln(terminal.Out, e.Text)
			}
		}
	}
	var data encodingjson.RawMessage
	if !r.Recipe {
		err = session.RunPrepared(ctx, app, actions, step.PrepareCall(actions, wire.Call{Action: r.Name, Args: args}, shownArgs, os.Getenv), record)
	} else {
		data, err = session.Run(ctx, app, actions, cookbook, r, args, record)
	}
	var failed *session.StepError
	if errors.As(err, &failed) {
		return err
	}
	printCaught()
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

func printFailure(terminal Terminal, json bool, failed step.Failed) {
	code, message, _ := failure(failed.Err)
	if json {
		printJSON(terminal.Out, outcomeJSON{Code: code, Message: message, Step: failed.Number})
		return
	}
	fmt.Fprintln(terminal.Out, failed.Err.Error())
}

func hasMessage(s step.Step) bool {
	switch s.(type) {
	case step.OpenApp, step.Wait, step.RunRecipe:
		return false
	}
	return true
}

func loadRecipes(location recipe.Location, pkg string, terminal Terminal) (*recipe.Cookbook, error) {
	c, warnings, err := location.Load(pkg)
	if err != nil {
		return nil, usageErrorf("%v", err)
	}
	for _, warning := range warnings {
		fmt.Fprintln(terminal.Err, "quack: "+warning)
	}
	return c, nil
}

func findRecipe(location recipe.Location, pkg, name string, terminal Terminal) (*recipe.Cookbook, error) {
	c, err := loadRecipes(location, pkg, terminal)
	if err != nil {
		return nil, err
	}
	if _, ok := c.Find(name); !ok {
		return nil, usageErrorf("no recipe named %s for %s", name, pkg)
	}
	return c, nil
}

func printRecipes(terminal Terminal, location recipe.Location, pkg string, filter *regexp.Regexp, asJSON bool) error {
	c, err := loadRecipes(location, pkg, terminal)
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
		fmt.Fprintln(w, "No recipes for "+pkg+". quack --init-recipes --app "+pkg+" sets up a project, or map the app under [recipes] in "+config.Path()+".")
	default:
		var out strings.Builder
		for _, r := range kept {
			writeAction(&out, wire.Action{ID: r.Name, Description: r.Description, Params: r.Params})
		}
		fmt.Fprint(w, strings.TrimSuffix(out.String(), "\n"))
	}
	return nil
}

func initRecipes(w io.Writer, location recipe.Location, pkg string) error {
	written, err := location.Init(pkg)
	for _, path := range written {
		fmt.Fprintln(w, "wrote "+path)
	}
	var selection *recipe.SelectionError
	if errors.As(err, &selection) {
		return usageErrorf("%v", err)
	}
	if err != nil {
		return err
	}
	if len(written) == 0 {
		fmt.Fprintln(w, "The files that let editors check recipes are up to date.")
	}
	if pkg == "" {
		mapped, err := location.MapsAny()
		if err != nil || mapped {
			return err
		}
		fmt.Fprintln(w, "Map apps to recipe files under [recipes] in .quack/quack.toml, or in "+config.Path()+".\n"+
			"quack --init-recipes --app <package> sets up a project.")
		return nil
	}
	mapped, err := location.Maps(pkg)
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
