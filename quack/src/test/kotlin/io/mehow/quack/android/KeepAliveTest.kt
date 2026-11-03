package io.mehow.quack.android

import io.kotest.matchers.booleans.shouldBeFalse
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.yield
import org.junit.Test

class KeepAliveTest {
  @Test
  fun `be idle without calls`() = runTest {
    KeepAlive().awaitIdle()
  }

  @Test
  fun `stay busy until every held call ends, even a failing one`() = runTest {
    val keepAlive = KeepAlive()
    val finishFirst = CompletableDeferred<Unit>()
    val finishSecond = CompletableDeferred<Unit>()
    launch { keepAlive.hold { finishFirst.await() } }
    launch {
      try {
        keepAlive.hold {
          finishSecond.await()
          error("Sync failed")
        }
      } catch (_: IllegalStateException) {}
    }
    yield()
    val idle = async { keepAlive.awaitIdle() }

    finishFirst.complete(Unit)
    yield()
    idle.isCompleted.shouldBeFalse()
    finishSecond.complete(Unit)
    idle.await()
  }
}
