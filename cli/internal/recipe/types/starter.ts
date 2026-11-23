import quack from "quack"

// Each recipe runs as $<name> in the terminal UI, and as quack --recipe <name>.
quack.recipe({
  name: "open",
  description: "Show the app's main screen",
  run() {
    quack.openApp()
  },
})
