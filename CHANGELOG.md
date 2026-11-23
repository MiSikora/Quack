# Changelog

All notable changes to this project will be documented in this document.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.4] - 2026-11-23

### Added
- Library: `Terminal.send` takes optional `json`, data that the CLI prints after the notification's message, MCP returns with it, and recipes read. JSON that does not parse fails the call.
- MCP: `start_action` starts an action without waiting for it to end, for actions that wait on something outside the call such as another device. `read_run` follows the run's notifications while it runs and gives its end, `cancel_run` cancels it, and `list_runs` lists the runs of the session.
- CLI: `/toggle-data` in the terminal UI hides the JSON data of notifications and results, or shows it again.

### Changed
- Library: `QuackAction.Terminal` is a plain interface, so a test builds one with an `object` expression instead of a lambda.
- CLI: a recipe's `ActionResult.notifications` holds `{ message, data }` objects instead of strings.
- MCP: `run_action` and `run_recipe` return each notification as `{ message, data }` instead of a string.

## [0.1.3] - 2026-11-22

### Added
- CLI: a recipe can declare its `name`, so that it needs no export.
- CLI: a recipe param with `env: "NAME"` takes that environment variable's value when the run leaves it out, and shows as `${NAME}`.

### Changed
- CLI: enter on an action or a recipe with params in the terminal UI opens its params box even when they are all optional.

### Fixed
- CLI: a recipe's steps show the environment variables it reads as `${NAME}` again, instead of their values, in the terminal UI, the CLI's output and MCP.

## [0.1.2] - 2026-11-22

### Added
- MCP: when the client can show a form, `run_action` and `run_recipe` ask the user for the required params that their args leave out.

### Changed
- CLI: recipes are TypeScript or JavaScript scripts that can take params, branch on what actions return, and share code. `quack --init-recipes --app <package>` and `/init-recipes` in the terminal UI start an app's recipes file and set up editors to check it.
- CLI: the terminal UI commands `/quit`, `/stop-action`, `/switch-app` and `/switch-device` are now `/exit`, `/cancel`, `/pick-app` and `/pick-device`.
- CLI: the terminal UI and the CLI's messages say app where they said package, except for the package name itself.
- MCP: `list_recipes` gives each recipe's params instead of its steps, and `run_recipe` takes `args` and returns what the recipe logged and the error of each step that failed.
- CLI: `/clear` in the terminal UI keeps the duck at the top of the log.
- CLI and MCP: actions, recipes and terminal UI commands are listed sorted by name, not in the order the app declares actions.

## [0.1.1] - 2026-11-22

### Added
- CLI: use environment variables as `${NAME}` in recipes, in action args and in the terminal UI.
- CLI: write `$${NAME}` to pass `${NAME}` unchanged, for example to a shell command that should expand it on the device.
- CLI: a recipe fails before its first step if it uses a variable that is not set.
- CLI: run shell commands in recipes, on the device with `shell` or on your computer with `run`.
- CLI: use `${QUACK_PACKAGE}` and `${ANDROID_SERIAL}` in recipes for the app and device the recipe runs on.
- MCP: recipes use environment variables. Args of `run_action` do not.

### Fixed
- Library: apps with `compileSdk` 35 or 36 no longer fail the build in `checkAarMetadata`.

## [0.1.0] - 2026-11-22

### Added

- Initial release.

[Unreleased]: https://github.com/MiSikora/Quack/compare/0.1.4...HEAD
[0.1.4]: https://github.com/MiSikora/Quack/releases/tag/0.1.4
[0.1.3]: https://github.com/MiSikora/Quack/releases/tag/0.1.3
[0.1.2]: https://github.com/MiSikora/Quack/releases/tag/0.1.2
[0.1.1]: https://github.com/MiSikora/Quack/releases/tag/0.1.1
[0.1.0]: https://github.com/MiSikora/Quack/releases/tag/0.1.0
