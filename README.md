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
  implementation("io.mehow.quack:quack:0.1.4")
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

While it runs, an action can report progress with `terminal.send("Syncing")`, and hand over data as JSON with `terminal.send("Pairing code", json = """{"code": "$code"}""")`. `Outcome.Ok` takes `json` the same way.

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

## Run them from your computer

Install the `quack` command with Homebrew:

```
brew install MiSikora/tap/quack
```

Connect your phone or start an emulator, then run `quack`. It lets you pick a device and an app, search the actions, and run them. Your app doesn't need to be open.

You can also run an action directly, which is handy in scripts:

```
quack app.greet name=Duck
```

Run `quack --help` to see everything else.

## Script your setups as recipes

A recipe is a small TypeScript or JavaScript function that runs actions and commands, and decides what to do with what they return. A project keeps its recipes in a `.quack/` dir, which quack finds in the dir you run it from or above it, up to your home dir. To start one, with a first recipe and what your editor needs to autocomplete and check it, run this in your project:

```
quack --init-recipes --app <app package>
```

or type `/init-recipes` in `quack`. Commit the whole `.quack/` dir, type files included, so that your team shares the recipes.

`.quack/quack.toml` maps apps to files in `.quack/recipes/`:

```toml
[recipes]
'com\.example\.app' = "recipes.ts"
'com\.example\..*' = ["accounts.ts", "sync.ts"]
```

Each key is a regex that has to match the whole package. Keep it in single quotes, which TOML leaves as written, so that `\.` reaches the regex. An app gets the recipes of every file that a matching key names. Files no key names can still be imported by those that are. Recipes of your own go in the same kind of `[recipes]` table in `~/.config/quack/config.toml`, with the files in `~/.config/quack/recipes/`. quack merges both, and fails to load an app's recipes when two of them share a name. Run `/init-recipes` again after updating quack, so that the type files match it.

Each recipe gets the name it declares, or else the name of its export:

```ts
import quack from "quack"

quack.recipe({
  name: "greetAndGoBack",
  description: "Open the app, say hello, then press back",
  params: { name: { type: "string", default: "Duck", hints: ["Duck", "Goose"] } },
  run({ name }) {
    quack.openApp()
    quack.wait("1s")
    const greeting = quack.runAction("app.greet", { name })
    console.log(greeting.message)
    quack.shell("input keyevent KEYCODE_BACK")
  },
})
```

A recipe can run actions (`quack.runAction`), open the app (`quack.openApp`), wait (`quack.wait`, `quack.waitUntil`), and run commands on the phone (`quack.shell`) or on your computer (`quack.run`). A step that fails throws, so the recipe stops unless it catches the error. Recipes can import your own files, but not npm packages.

Recipes build on each other. `quack.runRecipe` runs another recipe with args and returns what it returned. Its steps show under a `recipe <name>` line, as if the caller took them. A recipe can return any value JSON holds, and `quack --recipe` prints it after the last step. `quack.waitUntil` returns the first truthy value of its check, and `quack.fail("message")` ends the run with that message:

```ts
export const signIn = quack.recipe({
  params: { user: { type: "string", required: true } },
  run({ user }) {
    return quack.runAction<{ token: string }>("account.signIn", { user }).data
  },
})

export const seed = quack.recipe({
  run() {
    const { token } = quack.runRecipe(signIn, { user: "duck" })
    const id = quack.waitUntil(() => quack.runAction<{ id?: string }>("sync.status").data.id)
    if (!token) quack.fail("signing in gave no token")
    return { token, id }
  },
})
```

A param with `env: "QUACK_TOKEN"` takes that environment variable's value when the run leaves it out. quack shows it as `${QUACK_TOKEN}`, not its value, wherever it lists or logs the recipe.

Run it with `quack --recipe greetAndGoBack name=Goose`, or find it in `quack`, which lists recipes with the actions and marks them `ƒ`. Type `$` there to list only the recipes.

## Use it with AI agents

`quack-mcp` lets AI agents like Claude Code find and run your actions:

```
brew install MiSikora/tap/quack-mcp
claude mcp add quack -- quack-mcp
```

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
