# Changelog

All notable changes to this project will be documented in this document.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/MiSikora/Quack/compare/0.1.1...HEAD
[0.1.1]: https://github.com/MiSikora/Quack/releases/tag/0.1.1
[0.1.0]: https://github.com/MiSikora/Quack/releases/tag/0.1.0
