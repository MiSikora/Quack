# Changelog

All notable changes to this project will be documented in this document.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### SDK

#### Changed
- An action's id holds only ASCII letters, digits, `_`, `.` and `-`, and does not start with `-`. The server leaves out other actions, or throws after `throwOnMisconfiguration()`.

### CLI

#### Added
- The terminal UI reads `show-data` and `group-namespaces` from `$XDG_CONFIG_HOME/quack/config.toml`, the first state of `/toggle-data` and whether browsing groups the ids by namespace. It logs each problem with the file when it starts, and goes on with the defaults for the keys it could not read.
- `quack.runRecipe(recipe, args)` runs another recipe and returns what it returned. Its steps follow a `recipe <name>` step, and what it throws, the call throws.
- A recipe can return a value JSON holds. The CLI prints it after the last step, `--json` as a last `ok` line with `data`, and the terminal UI as the run's data.
- `quack.fail(message)` throws an error that ends the run with the message alone, without the recipe's stack.
- A `[recipes]` value can name a file in a subdir of `recipes/`, such as `features/login.ts`, or a glob, such as `features/*.ts`. A glob that matches no files is a warning.

#### Changed
- Typing in a param with hints in the terminal UI selects the best matching hint, so that enter picks it. ↓ goes back to the text as typed.
- The terminal UI lists recipes with the actions, marked `ƒ` after their names, and `$` lists only the recipes. A recipe and an action of the same name both show. The terminal UI writes a recipe's command line as `--recipe <name>` instead of `$<name>`.
- ↑ on an empty prompt in the terminal UI lists the actions and recipes sorted by id, and groups the ones that share a namespace, such as `sync.`. Enter or → opens a group, and ← or backspace goes back. Typing still searches them all.
- A recipe's name follows the same rule as an action's id, and a recipes file with another name fails to load.
- A recipe needs a `name`. `quack.recipe()` throws without one, and an export no longer names a recipe.
- An app gets only the recipes that a mapped file declares itself. A recipe that a file imports runs through `quack.runRecipe()` and is not listed for the importing file.
- `quack.waitUntil` keeps checking while the check returns a falsy value, and returns the first truthy one instead of nothing.
- Recipes live in a project's `.quack/recipes/` or in `$XDG_CONFIG_HOME/quack/recipes/`, and a `[recipes]` table in `.quack/quack.toml` or `config.toml` maps the apps whose packages a regex matches to files there, instead of one `<package>.ts` file per app. An app gets the recipes of every file it matches. `--init-recipes` and `/init-recipes` start `.quack/`.
- Only `--init-recipes` and `/init-recipes` write `quack.d.ts` and `tsconfig.json`. Loading recipes warns when they are out of date instead of rewriting them. They carry a version that changes only with the recipe API, so a project that a newer quack wrote them in says to upgrade quack, and `--init-recipes` leaves them alone.
- While no app is connected, the terminal UI keeps its prompt with the commands that need no app, such as `/pick-device`, instead of waiting in the device picker. The line over the prompt says why there is no app, in red when there is no device or no quack-enabled app.
- The terminal UI picks a device and an app by itself only until the first app connects. After a device disconnects, `/pick-device` picks the next one.
- The terminal UI notices when the connected app is uninstalled, and goes on as when its device disconnects.

#### Fixed
- A recipes file that exports a string with letters outside ASCII loads instead of crashing quack.

### MCP

#### Added
- `run_recipe` gives what the recipe returned as `data`.

#### Changed
- Recipes load from `.quack/` and `config.toml`, as in the CLI. `list_recipes` gives the file each recipe is in and the problems that left files out, instead of one `file` for the app.

#### Fixed
- A recipes file that exports a string with letters outside ASCII loads instead of crashing `quack-mcp`.

## [0.1.4] - 2026-11-23

### SDK

#### Added
- `Terminal.send` takes optional `json`, data that the CLI prints after the notification's message, MCP returns with it, and recipes read. JSON that does not parse fails the call.

#### Changed
- `QuackAction.Terminal` is a plain interface, so a test builds one with an `object` expression instead of a lambda.

### CLI

#### Added
- `/toggle-data` in the terminal UI hides the JSON data of notifications and results, or shows it again.

#### Changed
- A recipe's `ActionResult.notifications` holds `{ message, data }` objects instead of strings.

### MCP

#### Added
- `start_action` starts an action without waiting for it to end, for actions that wait on something outside the call such as another device. `read_run` follows the run's notifications while it runs and gives its end, `cancel_run` cancels it, and `list_runs` lists the runs of the session.

#### Changed
- `run_action` and `run_recipe` return each notification as `{ message, data }` instead of a string.

## [0.1.3] - 2026-11-22

### CLI

#### Added
- A recipe can declare its `name`, so that it needs no export.
- A recipe param with `env: "NAME"` takes that environment variable's value when the run leaves it out, and shows as `${NAME}`.

#### Changed
- Enter on an action or a recipe with params in the terminal UI opens its params box even when they are all optional.

#### Fixed
- A recipe's steps show the environment variables it reads as `${NAME}` again, instead of their values, in the terminal UI and the CLI's output.

### MCP

#### Fixed
- A recipe's steps show the environment variables it reads as `${NAME}` again, instead of their values.

## [0.1.2] - 2026-11-22

### CLI

#### Changed
- Recipes are TypeScript or JavaScript scripts that can take params, branch on what actions return, and share code. `quack --init-recipes --app <package>` and `/init-recipes` in the terminal UI start an app's recipes file and set up editors to check it.
- The terminal UI commands `/quit`, `/stop-action`, `/switch-app` and `/switch-device` are now `/exit`, `/cancel`, `/pick-app` and `/pick-device`.
- The terminal UI and the CLI's messages say app where they said package, except for the package name itself.
- `/clear` in the terminal UI keeps the duck at the top of the log.
- Actions, recipes and terminal UI commands are listed sorted by name, not in the order the app declares actions.

### MCP

#### Added
- When the client can show a form, `run_action` and `run_recipe` ask the user for the required params that their args leave out.

#### Changed
- `list_recipes` gives each recipe's params instead of its steps, and `run_recipe` takes `args` and returns what the recipe logged and the error of each step that failed.
- Actions and recipes are listed sorted by name, not in the order the app declares actions.

## [0.1.1] - 2026-11-22

### SDK

#### Fixed
- Apps with `compileSdk` 35 or 36 no longer fail the build in `checkAarMetadata`.

### CLI

#### Added
- Use environment variables as `${NAME}` in recipes, in action args and in the terminal UI.
- Write `$${NAME}` to pass `${NAME}` unchanged, for example to a shell command that should expand it on the device.
- A recipe fails before its first step if it uses a variable that is not set.
- Run shell commands in recipes, on the device with `shell` or on your computer with `run`.
- Use `${QUACK_PACKAGE}` and `${ANDROID_SERIAL}` in recipes for the app and device the recipe runs on.

### MCP

#### Added
- Recipes use environment variables. Args of `run_action` do not.

## [0.1.0] - 2026-11-22

### Added

- Initial release.

[Unreleased]: https://github.com/MiSikora/Quack/compare/0.1.4...HEAD
[0.1.4]: https://github.com/MiSikora/Quack/releases/tag/0.1.4
[0.1.3]: https://github.com/MiSikora/Quack/releases/tag/0.1.3
[0.1.2]: https://github.com/MiSikora/Quack/releases/tag/0.1.2
[0.1.1]: https://github.com/MiSikora/Quack/releases/tag/0.1.1
[0.1.0]: https://github.com/MiSikora/Quack/releases/tag/0.1.0
