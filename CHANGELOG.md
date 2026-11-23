# Changelog

All notable changes to this project will be documented in this document.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/MiSikora/Quack/compare/0.1.2...HEAD
[0.1.2]: https://github.com/MiSikora/Quack/releases/tag/0.1.2
[0.1.1]: https://github.com/MiSikora/Quack/releases/tag/0.1.1
[0.1.0]: https://github.com/MiSikora/Quack/releases/tag/0.1.0
