package tui

import (
	tea "charm.land/bubbletea/v2"

	"quack/internal/device"
	"quack/internal/recipe"
	"quack/internal/session"
	"quack/internal/wire"
)

type connection struct {
	backend    session.Backend
	app        session.App
	target     device.Target
	hello      wire.Hello
	generation int
	cookbook   *recipe.Cookbook
	opensApp   bool
}

type connected struct {
	target device.Target
	app    session.App
	described
}

type described struct {
	hello      wire.Hello
	actions    []wire.Action
	err        error
	recipes    recipes
	connection int
}

type recipes struct {
	cookbook *recipe.Cookbook
	warnings []string
	err      error
}

func (c *connection) connect(target device.Target, activity activity, location recipe.Location) tea.Cmd {
	backend, openApp := c.backend, c.opensApp
	return func() tea.Msg {
		app, err := backend.Connect(target, openApp)
		msg := connected{target: target, app: app, described: described{err: err}}
		if err == nil {
			msg.hello, msg.actions, msg.err = app.Describe(activity.ctx)
			msg.recipes = loadRecipes(location, target.Package)
		}
		activity.send(msg)
		return nil
	}
}

func (c *connection) open(msg connected) {
	c.app, c.target, c.hello, c.opensApp = msg.app, msg.target, msg.hello, false
	c.generation++
}

func (c *connection) drop() {
	c.app = nil
	c.generation++
}

func (c *connection) reload(activity activity, location recipe.Location) tea.Cmd {
	app, pkg, generation := c.app, c.target.Package, c.generation
	return func() tea.Msg {
		hello, actions, err := app.Describe(activity.ctx)
		activity.send(described{hello, actions, err, loadRecipes(location, pkg), generation})
		return nil
	}
}

func loadRecipes(location recipe.Location, pkg string) recipes {
	cookbook, warnings, err := location.Load(pkg)
	return recipes{cookbook, warnings, err}
}

func (r recipes) list() []recipe.Recipe {
	if r.cookbook == nil {
		return nil
	}
	return r.cookbook.Recipes
}
