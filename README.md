# Quack

Run debug actions in your Android app from your computer.

Log in as a test user, flip a feature flag, or seed some data with one command, without tapping through debug menus on the phone.

```
$ quack app.greet name=Duck
Hello, Duck!
```

## Add actions to your app

Add the library:

```kotlin
dependencies {
  implementation("io.mehow.quack:quack:0.1.5")
}
```

Write an action:

```kotlin
class GreetAction : QuackAction {
  private val name = Param.string("name", "Who to greet").required()

  override val id = "app.greet"
  override val description = "Say hello"
  override val params = listOf(name)

  override suspend fun execute(args: Args, terminal: Terminal): Outcome {
    return Outcome.Ok("Hello, ${args[name]}!")
  }
}
```

Dots in an id group actions by namespace, such as `app.`. An action can report progress with `terminal.send("Syncing")` and hand over JSON with `terminal.send("Pairing code", json = """{"code": "$code"}""")`. `Outcome.Ok` takes `json` too.

Start Quack when your app starts:

```kotlin
class MyApplication : Application() {
  override fun onCreate() {
    super.onCreate()
    QuackServer.Builder(this)
      .action(GreetAction())
      .build()
      .start()
  }
}
```

## Use host environment defaults

Bind an action parameter to an environment variable on the computer running `quack` or `quack-mcp`:

```kotlin
import io.mehow.quack.env

private val token = Param.string("token").env("QUACK_TOKEN").required()
private val region = Param.string("region").withDefault("local").env("QUACK_REGION")
```

An explicit argument wins, including an empty string or an empty list. Otherwise, a nonempty host environment value supplies the argument. An unset or empty variable uses the parameter's literal default, optional null, or required check. A list binding supplies one entry, with commas and spaces kept literally. Environment contents are never expanded again.

`.env()` preserves nullability and works before or after `.required()`, `.withDefault()`, and string `.hints()`. Names must match `[A-Za-z_][A-Za-z0-9_]*`. Android does not read the host environment, so direct Kotlin `Args` calls keep their existing behavior.

In a TUI string or list field, press **Ctrl+E** to send an explicit empty value. The field shows `<empty string>` or `<empty list>`. Press **Ctrl+U** to reset the field and use its default again. An ordinary blank field keeps its default.

Bindings apply to direct commands, interactive forms, recipe `quack.runAction()` calls, and synchronous and asynchronous MCP actions. Discovery keeps the declared required status and literal default and includes only the variable name. Forms and help show `${NAME}` when the variable is available.

CLI and TUI arguments expand `${NAME}` using the host environment. Use `$${NAME}` to send literal `${NAME}`. In a shell, quote the argument with single quotes so Quack receives the expression, such as `'name=${USER}'`. Missing variables fail the run before an action executes. Expansion runs once and does not evaluate shell expressions. `${QUACK_PACKAGE}` and `${ANDROID_SERIAL}` refer to the selected app and device. MCP arguments and action arguments written inside recipe code remain literal.

Displayed bound arguments, commands, and history use `${NAME}`. Recipe commands and arguments derived from a consumed environment value keep the existing masking. Returned messages, JSON results, notifications, and errors are unchanged. Typed parsing errors can include the supplied value.

## Run them from your computer

Install the `quack` command with Homebrew:

```
brew install MiSikora/tap/quack
```

Connect a phone or start an emulator, then run `quack` to pick a device and an app, search the actions, and run them. Type `/` for commands and ↑ on an empty prompt to browse.

Settings live in `~/.config/quack/config.toml`:

```toml
show-data = false        # show the JSON data of notifications and results; /toggle-data flips it
group-namespaces = true  # group actions by namespace when browsing
```

Run an action directly, for example in a script, with `quack app.greet name=Duck`. See `quack --help` for the rest.

## Script your setups as recipes

A recipe is a TypeScript or JavaScript function that runs actions and commands and acts on what they return. Start a project's recipes with:

```
quack --init-recipes --app <app package>
```

or `/init-recipes` in `quack`. This creates `.quack/`, which you commit. `.quack/quack.toml` maps apps to files in `.quack/recipes/`:

```toml
[recipes]
'com\.example(\.debug)?' = ["features/*.ts"]
```

Keys are regexes matching the whole package, in single quotes. Values are paths or globs in `recipes/`. Personal recipes go in the same kind of table in `~/.config/quack/config.toml`, with files in `~/.config/quack/recipes/`.

To use a project's recipes from another directory, map the app to its project root in your personal config.

```toml
# ~/.config/quack/config.toml (or $XDG_CONFIG_HOME/quack/config.toml)
[projects]
'com\.example(\.debug)?' = "~/src/android"
```

To choose a project explicitly, pass `--project-dir` to either executable.

```sh
quack --project-dir ../android --list-recipes
quack-mcp --project-dir ~/src/android
```

The flag selects the exact directory containing `.quack/`. Without the flag, Quack uses the nearest `.quack/` in the current directory or its ancestors, stopping at home or the filesystem root. If it finds none and has an app package, Quack looks for a matching `[projects]` entry. The regex must match the whole package. Multiple matches are an error. With no project, Quack uses only personal recipes. Personal recipes also load alongside the selected project's recipes.

Relative flag paths start in your working directory. Relative `[projects]` paths start in the directory containing the personal config file. Both accept `~` and `~/` for your home directory. The root must exist, and loading requires a `.quack/` directory at that exact root. Recipe commands still run in the directory where you started Quack, so relative output paths use that directory.

`--init-recipes` can create a missing `.quack/` in the selected root. With `--project-dir` and no `--app`, it writes the personal and project editor types. With an app, it can also create the project's mapping and starter recipe. The terminal UI uses the same project selection for `/init-recipes` and `/reload`.

```ts
// .quack/recipes/features/greet.ts
import quack from "quack"

quack.recipe({
  name: "greetAndGoBack",
  description: "Open the app, say hello, then press back",
  params: { name: { type: "string", default: "Duck" } },
  run({ name }) {
    quack.openApp()
    console.log(quack.runAction("app.greet", { name }).message)
    quack.shell("input keyevent KEYCODE_BACK")
  },
})
```

Run it with `quack --recipe greetAndGoBack name=Goose`, or find it in `quack`, where recipes are marked `ƒ` and `$` lists only them. A step that fails throws. `quack.d.ts` in `.quack/recipes/` documents the whole API.

Recipes can run other recipes and import your own files, but not npm packages. Only a recipe declared in a mapped file is listed, so shared recipes can live in an unmapped dir:

```ts
// .quack/recipes/lib/accounts.ts
import quack from "quack"

export const signIn = quack.recipe({
  name: "signIn",
  params: { user: { type: "string", required: true } },
  run({ user }) {
    return quack.runAction<{ token: string }>("account.signIn", { user }).data
  },
})
```

```ts
// .quack/recipes/features/checkout.ts
import quack from "quack"
import { signIn } from "../lib/accounts"

quack.recipe({
  name: "checkout",
  run() {
    const { token } = quack.runRecipe(signIn, { user: "duck" })
    quack.runAction("cart.fill", { token })
  },
})
```

## Use it with AI agents

`quack-mcp` lets AI agents like Claude Code find and run your actions:

```
brew install MiSikora/tap/quack-mcp
claude mcp add quack -- quack-mcp
```

MCP honors `ANDROID_SERIAL` when a tool call omits `device`. An explicit `device` takes precedence. Action arguments and result JSON preserve large integers. Forms ask for `long` values as decimal text. `run_recipe` accepts `open_app` to foreground the app before running and includes recipe-loading warnings in `problems`. Failed actions and recipes set `isError` and keep structured notifications, completed steps, and an `error` with a code and message.

## License

    Copyright 2026 Michał Sikora

    Licensed under the Apache License, Version 2.0 (the "License");
    you may not use this file except in compliance with the License.
    You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

    Unless required by applicable law or agreed to in writing, software
    distributed under the License is distributed on an "AS IS" BASIS,
    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
    See the License for the specific language governing permissions and
    limitations under the License.
