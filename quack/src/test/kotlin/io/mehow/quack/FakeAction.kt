package io.mehow.quack

import io.mehow.quack.QuackAction.Args
import io.mehow.quack.QuackAction.Outcome
import io.mehow.quack.QuackAction.Param
import io.mehow.quack.QuackAction.Terminal

internal class FakeAction(
  override val id: String = "test.action",
  override val description: String = "A test action",
  override val params: List<Param<*>> = emptyList(),
  private val execute: suspend (Args, Terminal) -> Outcome = { _, _ -> Outcome.Ok("Done") },
) : QuackAction {
  override suspend fun execute(args: Args, terminal: Terminal) = execute.invoke(args, terminal)
}
