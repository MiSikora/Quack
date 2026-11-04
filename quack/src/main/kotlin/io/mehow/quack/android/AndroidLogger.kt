package io.mehow.quack.android

import android.util.Log
import io.mehow.quack.QuackLogger
import io.mehow.quack.QuackLogger.Level

internal class AndroidLogger : QuackLogger {
  override fun log(level: Level, message: String, throwable: Throwable?) {
    when (level) {
      Level.Info -> Log.i(TAG, message, throwable)
      Level.Warn -> Log.w(TAG, message, throwable)
      Level.Error -> Log.e(TAG, message, throwable)
    }
  }
}

private const val TAG = "Quack"
