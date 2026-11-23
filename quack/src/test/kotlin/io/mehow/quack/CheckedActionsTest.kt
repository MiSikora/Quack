package io.mehow.quack

import io.kotest.matchers.collections.shouldBeEmpty
import io.kotest.matchers.collections.shouldContainExactly
import io.mehow.quack.QuackAction.Param
import org.junit.Test

class CheckedActionsTest {
  @Test
  fun `serve well-formed actions in order`() {
    val actions = listOf(FakeAction(id = "app.greet"), FakeAction(id = "account.create"))

    val checked = checkActions(actions)

    checked.served shouldContainExactly actions
    checked.problems.shouldBeEmpty()
  }

  @Test
  fun `serve ids with capitals, digits, dots, underscores and dashes`() {
    val actions =
      listOf("App.greet", "account-create", "app..greet", ".app", "_app", "app_greet", "app2", "1st").map { FakeAction(id = it) }

    val checked = checkActions(actions)

    checked.served shouldContainExactly actions
    checked.problems.shouldBeEmpty()
  }

  @Test
  fun `do not serve actions with ids the CLI cannot pass`() {
    val ids = listOf("", " ", "-help", "/exit", "\$login", "app greet", "app\tgreet", "app:greet", "zażółć")
    val malformed = ids.map { FakeAction(id = it) }
    val valid = FakeAction(id = "app.greet")

    val checked = checkActions(malformed + valid)

    checked.served shouldContainExactly listOf(valid)
    checked.problems shouldContainExactly malformed.map { ActionProblem.MalformedId(it) }
  }

  @Test
  fun `serve the first of actions that share an id`() {
    val first = FakeAction(id = "app.greet")
    val second = FakeAction(id = "app.greet")

    val checked = checkActions(listOf(first, second))

    checked.served shouldContainExactly listOf(first)
    checked.problems shouldContainExactly listOf(ActionProblem.DuplicateId(second))
  }

  @Test
  fun `serve param names with capitals, dashes and other characters`() {
    val action = FakeAction(params = listOf("Name", "1st", "_name", "dry-run", "名前").map { Param.string(it) })

    val checked = checkActions(listOf(action))

    checked.served shouldContainExactly listOf(action)
    checked.problems.shouldBeEmpty()
  }

  @Test
  fun `do not serve actions with param names the CLI cannot pass`() {
    val names = listOf("", "-name", "first name", "name=", "a=b")
    val malformed = names.map { FakeAction(params = listOf(Param.string(it))) }
    val valid = FakeAction(id = "app.greet", params = listOf(Param.string("first_name")))

    val checked = checkActions(malformed + valid)

    checked.served shouldContainExactly listOf(valid)
    checked.problems shouldContainExactly malformed.zip(names) { action, name -> ActionProblem.MalformedParamName(action, name) }
  }

  @Test
  fun `do not serve an action with an enum param without choices`() {
    val action = FakeAction(params = listOf(Param.enum("size", emptyList<Size>())))

    val checked = checkActions(listOf(action))

    checked.served.shouldBeEmpty()
    checked.problems shouldContainExactly listOf(ActionProblem.NoEnumChoices(action, "size"))
  }

  @Test
  fun `do not serve an action with enum choices that differ only in case`() {
    val action = FakeAction(params = listOf(Param.enum<Size>("size")))

    val checked = checkActions(listOf(action))

    checked.served.shouldBeEmpty()
    checked.problems shouldContainExactly listOf(ActionProblem.CaseClashingEnumChoices(action, "size", listOf("S", "s", "M")))
  }

  @Test
  fun `do not serve an action that declares a param name twice`() {
    val action = FakeAction(params = listOf(Param.string("name"), Param.int("name")))

    val checked = checkActions(listOf(action))

    checked.served.shouldBeEmpty()
    checked.problems shouldContainExactly listOf(ActionProblem.DuplicateParamName(action, "name"))
  }
}

@Suppress("EnumEntryName")
private enum class Size {
  S,
  s,
  M,
}
