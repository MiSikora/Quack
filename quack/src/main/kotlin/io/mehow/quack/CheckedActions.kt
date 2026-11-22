package io.mehow.quack

internal class CheckedActions(val served: List<QuackAction>, val problems: List<ActionProblem>)

internal sealed interface ActionProblem {
  val action: QuackAction
  val message: String

  data class MalformedId(override val action: QuackAction) : ActionProblem {
    override val message: String
      get() = "its id must not be empty, contain whitespace, or start with '-', '/' or '$'"
  }

  data class DuplicateId(override val action: QuackAction) : ActionProblem {
    override val message: String
      get() = "an earlier action already uses this id"
  }

  data class MalformedParamName(override val action: QuackAction, val name: String) : ActionProblem {
    override val message: String
      get() = "param '$name' must not be empty, contain whitespace or '=', or start with '-'"
  }

  data class DuplicateParamName(override val action: QuackAction, val name: String) : ActionProblem {
    override val message: String
      get() = "param '$name' is declared more than once"
  }

  data class NoEnumChoices(override val action: QuackAction, val name: String) : ActionProblem {
    override val message: String
      get() = "enum param '$name' has no choices"
  }

  data class CaseClashingEnumChoices(
    override val action: QuackAction,
    val name: String,
    val choices: List<String>,
  ) : ActionProblem {
    override val message: String
      get() = "enum param '$name' has choices that differ only in case: ${choices.joinToString()}"
  }
}

internal fun checkActions(actions: List<QuackAction>): CheckedActions {
  val served = mutableListOf<QuackAction>()
  val problems = mutableListOf<ActionProblem>()
  val servedIds = mutableSetOf<String>()
  for (action in actions) {
    val problem =
      when {
        !action.id.isActionId() -> ActionProblem.MalformedId(action)
        action.id in servedIds -> ActionProblem.DuplicateId(action)
        else -> paramProblem(action)
      }
    if (problem == null) {
      served += action
      servedIds += action.id
    } else {
      problems += problem
    }
  }
  return CheckedActions(served, problems)
}

private fun paramProblem(action: QuackAction): ActionProblem? {
  val names = mutableSetOf<String>()
  for (param in action.params) {
    if (!param.name.isParamName()) {
      return ActionProblem.MalformedParamName(action, param.name)
    }
    if (!names.add(param.name)) {
      return ActionProblem.DuplicateParamName(action, param.name)
    }
    val type = param.type
    if (type is QuackAction.Param.Type.Enum) {
      if (type.choices.isEmpty()) {
        return ActionProblem.NoEnumChoices(action, param.name)
      }
      if (type.choices.distinctBy { it.lowercase() }.size != type.choices.size) {
        return ActionProblem.CaseClashingEnumChoices(action, param.name, type.choices)
      }
    }
  }
  return null
}

// The CLI reads a word that starts with '-' as one of its options, one that starts with '/' as an interactive command, and one that
// starts with '$' as a recipe.
private fun String.isActionId() = isNotEmpty() && none(Char::isWhitespace) && first() !in "-/$"

// The CLI splits an argument into a param name and a value at its first '='.
private fun String.isParamName() = isNotEmpty() && none { it.isWhitespace() || it == '=' } && first() != '-'
