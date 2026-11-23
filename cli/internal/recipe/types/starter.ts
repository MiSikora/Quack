import quack from "quack"

// Each recipe shows as <name> ƒ in the terminal UI, and runs as quack --recipe <name>.
quack.recipe({
  name: "open",
  description: "Show the app's main screen",
  run() {
    quack.openApp()
  },
})
