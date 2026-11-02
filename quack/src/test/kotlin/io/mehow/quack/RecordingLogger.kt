package io.mehow.quack

import io.mehow.quack.QuackLogger.Level

internal class RecordingLogger : QuackLogger {
  val entries = mutableListOf<Entry>()

  override fun log(level: Level, message: String, throwable: Throwable?) {
    entries += Entry(level, message, throwable)
  }

  data class Entry(val level: Level, val message: String, val throwable: Throwable?)
}
