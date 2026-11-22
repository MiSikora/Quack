import quack from "quack"

// Each exported recipe runs as $<export name> in the terminal UI, and as quack --recipe <export name>.
export const open = quack.recipe({
  description: "Show the app's main screen",
  run() {
    quack.openApp()
  },
})
