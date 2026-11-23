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
  implementation("io.mehow.quack:quack:0.1.2")
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

A recipe is a small TypeScript or JavaScript function that runs actions and commands, and decides what to do with what they return. Recipes are kept on your computer, one file per app, in `~/.config/quack/recipes/<app package>.ts`. To start the file of an app, along with what your editor needs to autocomplete and check it, run:

```
quack --init-recipes --app <app package>
```

or type `/init-recipes` in `quack`.

Each exported recipe gets its name from its export:

```ts
import quack from "quack"

export const greetAndGoBack = quack.recipe({
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

A param with `env: "QUACK_TOKEN"` takes that environment variable's value when the run leaves it out. quack shows it as `${QUACK_TOKEN}`, not its value, wherever it lists or logs the recipe.

Run it with `quack --recipe greetAndGoBack name=Goose`, or type `$greetAndGoBack` in `quack`.

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
