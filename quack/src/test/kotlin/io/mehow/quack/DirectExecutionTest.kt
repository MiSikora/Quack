package io.mehow.quack

import io.kotest.assertions.throwables.shouldThrow
import io.kotest.matchers.collections.shouldContainExactly
import io.kotest.matchers.nulls.shouldBeNull
import io.kotest.matchers.shouldBe
import io.mehow.quack.QuackAction.Outcome
import io.mehow.quack.QuackAction.Param
import io.mehow.quack.QuackAction.Terminal
import kotlinx.coroutines.test.runTest
import org.junit.Test

class DirectExecutionTest {
  private val name = Param.string("name").required()
  private val times = Param.int("times").withDefault(2)
  private val loud = Param.boolean("loud")
  private val action = FakeAction(params = listOf(name, times, loud))

  @Test
  fun `read a given value`() {
    action.args().with(name, "Bob")[name] shouldBe "Bob"
  }

  @Test
  fun `fall back to the default of a param without a value`() {
    action.args()[times] shouldBe 2
  }

  @Test
  fun `read null for an optional param without a value`() {
    action.args()[loud].shouldBeNull()
  }

  @Test
  fun `fail to read a required param without a value`() {
    shouldThrow<IllegalArgumentException> { action.args()[name] }
  }

  @Test
  fun `fail to give a value for a param the action does not declare`() {
    shouldThrow<IllegalArgumentException> { action.args().with(Param.string("other"), "value") }
  }

  @Test
  fun `keep the last value given for a param`() {
    action.args().with(times, 3).with(times, 5)[times] shouldBe 5
  }

  @Test
  fun `leave the original args unchanged`() {
    val original = action.args()

    original.with(loud, true)

    original[loud].shouldBeNull()
  }

  @Test
  fun `execute an action with a terminal of the test's own`() = runTest {
    val greeting =
      FakeAction(
        params = listOf(name, times),
        execute = { args, terminal ->
          terminal.send("Greeting ${args[name]}")
          Outcome.Ok("Hello ${args[name]} x${args[times]}")
        },
      )
    val notifications = mutableListOf<String>()
    val terminal =
      object : Terminal {
        override suspend fun send(message: String, json: String?) {
          notifications += message
        }
      }

    val outcome = greeting.execute(greeting.args().with(name, "Bob"), terminal)

    outcome shouldBe Outcome.Ok("Hello Bob x2")
    notifications shouldContainExactly listOf("Greeting Bob")
  }
}
