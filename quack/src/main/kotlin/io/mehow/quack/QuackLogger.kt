package io.mehow.quack

public fun interface QuackLogger {
  public fun log(level: Level, message: String, throwable: Throwable?)

  public enum class Level {
    Info,
    Warn,
    Error,
  }
}
