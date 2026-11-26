# Changelog

All notable changes to this project will be documented in this document.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### SDK

#### Changed
- Use platform JSON APIs instead of kotlinx.serialization. The SDK no longer needs its runtime dependency or compiler plugin.
- Parse and encode JSON with the platform defaults, and compact multiline data into a single protocol frame.
- Run codec and endpoint tests with Robolectric while keeping pure Kotlin tests on the JVM runner.

### CLI and MCP

#### Added
- `-r` is an alias for `--recipe`.
- TUI device and app pickers mark the current connection and initially select it.
- TUI string and list fields accept explicit empty values with Ctrl+E and reset with Ctrl+U.
- TUI and MCP list forms accept JSON string arrays to preserve empty entries, whitespace, and commas.
- TUI and MCP arguments expand host environment variables with `${NAME}` and escape them with `$${NAME}`.
- MCP recipes accept `open_app` and return recipe-loading warnings in `problems`.

#### Fixed
- MCP preserves exact JSON numbers in action arguments, notifications, and results. Long form fields accept decimal text.
- MCP empty list form values send `[]` instead of `null`.
- MCP uses `ANDROID_SERIAL` when a tool call omits `device`.
- Failed MCP actions and recipes retain structured notifications, completed steps, and errors.
- Recipe commands and action arguments derived from expanded user input retain environment placeholders in their displayed values.

## [0.1.5] - 2026-11-24

### SDK

#### Changed
- Action ids may hold only ASCII letters, digits, `_`, `.` and `-`.

### CLI

#### Added
- `show-data` and `group-namespaces` settings in `config.toml`.
- `quack.runRecipe()` runs another recipe.
- Recipes can return JSON data.
- `quack.fail(message)` ends a recipe with a message.

#### Changed
- Recipes live in `.quack/recipes/` or `~/.config/quack/recipes/`, and a `[recipes]` table maps apps to files or globs there.
- A recipe needs a `name`, following the action id rules. Only recipes declared in a mapped file are listed.
- `quack.waitUntil` returns the first truthy value.
- Only `--init-recipes` writes `quack.d.ts` and `tsconfig.json`.
- The terminal UI lists recipes with the actions, and groups them by namespace when browsing.
- The terminal UI hides JSON data until `/toggle-data`.
- Typing in a param with hints selects the best match.
- The terminal UI keeps its prompt while no app is connected, and notices when the app is uninstalled.

#### Fixed
- Recipes files with non-ASCII strings no longer crash quack.

### MCP

#### Added
- `run_recipe` returns the recipe's data.

#### Changed
- Recipes load as in the CLI, and `list_recipes` shows each recipe's file.

#### Fixed
- Recipes files with non-ASCII strings no longer crash `quack-mcp`.

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

[Unreleased]: https://github.com/MiSikora/Quack/compare/0.1.5...HEAD
[0.1.5]: https://github.com/MiSikora/Quack/releases/tag/0.1.5
[0.1.4]: https://github.com/MiSikora/Quack/releases/tag/0.1.4
[0.1.3]: https://github.com/MiSikora/Quack/releases/tag/0.1.3
[0.1.2]: https://github.com/MiSikora/Quack/releases/tag/0.1.2
[0.1.1]: https://github.com/MiSikora/Quack/releases/tag/0.1.1
[0.1.0]: https://github.com/MiSikora/Quack/releases/tag/0.1.0
