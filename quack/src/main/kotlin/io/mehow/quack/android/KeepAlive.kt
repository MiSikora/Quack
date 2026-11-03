package io.mehow.quack.android

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.update

// Counts the calls in progress. The CLI starts QuackService once a call runs for a few seconds, because Android freezes a background
// app soon after the CLI wakes it. The service keeps the app running until no call is held here.
internal class KeepAlive {
  private val calls = MutableStateFlow(0)

  suspend fun <T> hold(block: suspend () -> T): T {
    calls.update { it + 1 }
    try {
      return block()
    } finally {
      calls.update { it - 1 }
    }
  }

  suspend fun awaitIdle() {
    calls.first { it == 0 }
  }

  companion object {
    // Android creates QuackService, so it cannot get the server's instance through a constructor.
    val Shared = KeepAlive()
  }
}
