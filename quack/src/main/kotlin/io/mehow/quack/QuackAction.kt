package io.mehow.quack

import kotlin.enums.enumEntries

public interface QuackAction {
  /** Holds only ASCII letters, '_', '.' and '-', and does not start with '-'. The server leaves out other actions. */
  public val id: String

  public val description: String

  public val params: List<Param<*>>

  /** Runs on `QuackServer.Builder.actionDispatcher`. Disconnecting the CLI cancels it, and a thrown exception fails the call. */
  public suspend fun execute(args: Args, terminal: Terminal): Outcome

  /** Optional, so its value is null when the argument is absent. Use [required] or [withDefault] for a non-null value. */
  public class Param<T>
  internal constructor(
    public val name: String,
    public val description: String,
    internal val type: Type,
    internal val presence: Presence,
    internal val hints: List<String> = emptyList(),
    internal val parse: (List<String>) -> T?,
  ) {

    internal sealed class Type(val wireName: kotlin.String) {
      open val expected: kotlin.String
        get() = wireName

      open val isList: kotlin.Boolean
        get() = false

      data object String : Type("string")

      data object Int : Type("int")

      data object Long : Type("long")

      data object Double : Type("double")

      data object Boolean : Type("boolean")

      data object StringList : Type("string_list") {
        override val isList: kotlin.Boolean
          get() = true
      }

      class Enum(val choices: List<kotlin.String>) : Type("enum") {
        val wireChoices = choices.map(kotlin.String::lowercase)

        override val expected: kotlin.String
          get() = "one of ${wireChoices.joinToString()}"
      }
    }

    internal sealed interface Presence {
      data object Optional : Presence

      data object Required : Presence

      data class Default(val value: Any) : Presence
    }

    public companion object {
      public fun string(name: String, description: String = ""): Param<String?> = scalar(name, Type.String, description) { it }

      public fun int(name: String, description: String = ""): Param<Int?> = scalar(name, Type.Int, description, String::toIntOrNull)

      public fun long(name: String, description: String = ""): Param<Long?> = scalar(name, Type.Long, description, String::toLongOrNull)

      /** Rejects `NaN` and `Infinity`. */
      public fun double(name: String, description: String = ""): Param<Double?> =
        scalar(name, Type.Double, description) { it.toDoubleOrNull()?.takeIf(Double::isFinite) }

      public fun boolean(name: String, description: String = ""): Param<Boolean?> =
        scalar(name, Type.Boolean, description, String::toBooleanStrictOrNull)

      /** Matches constant names ignoring case. */
      public inline fun <reified T : Enum<T>> enum(
        name: String,
        description: String = "",
      ): Param<T?> = enum(name, enumEntries<T>(), description)

      /** Offers only [entries], matching their names ignoring case. */
      public fun <T : Enum<T>> enum(
        name: String,
        entries: List<T>,
        description: String = "",
      ): Param<T?> {
        return scalar(name, Type.Enum(entries.map { it.name }), description) { raw ->
          entries.firstOrNull { it.name.equals(raw, ignoreCase = true) }
        }
      }

      /** The user repeats the key once per element, as in `tag=a tag=b`. */
      public fun stringList(name: String, description: String = ""): Param<List<String>?> =
        Param(name = name, type = Type.StringList, presence = Presence.Optional, description = description) { it }

      private fun <T> scalar(
        name: String,
        type: Type,
        description: String,
        parse: (String) -> T?,
      ): Param<T?> = Param(name = name, type = type, presence = Presence.Optional, description = description) { parse(it.single()) }
    }
  }

  public class Args internal constructor(private val action: QuackAction, private val values: Map<String, Any>) {
    /**
     * Returns the given value, else the default, else null.
     *
     * @throws IllegalArgumentException when [param] is not one of the action's [params], or is required and has no value.
     */
    public operator fun <T> get(param: Param<T>): T {
      requireDeclared(param)
      val value =
        values[param.name]
          ?: when (val presence = param.presence) {
            Param.Presence.Optional -> null
            Param.Presence.Required -> throw IllegalArgumentException("Required param '${param.name}' has no value")
            is Param.Presence.Default -> presence.value
          }
      @Suppress("UNCHECKED_CAST")
      return value as T
    }

    /**
     * Returns a copy with [value] for [param].
     *
     * @throws IllegalArgumentException when [param] is not one of the action's [params].
     */
    public fun <T> with(param: Param<T>, value: T): Args {
      requireDeclared(param)
      return Args(action, values = if (value == null) values - param.name else values + (param.name to value))
    }

    private fun requireDeclared(param: Param<*>) {
      require(action.params.any { it === param }) { "Param '${param.name}' is not in the params of action '${action.id}'" }
    }
  }

  public interface Terminal {
    /** The CLI prints [json] after [message]. A [json] that does not parse fails the call. */
    public suspend fun send(message: String, json: String? = null)
  }

  public sealed interface Outcome {
    /** The CLI prints [json] after [message]. A [json] that does not parse fails the call. */
    public data class Ok(val message: String, val json: String? = null) : Outcome

    public data class Error(val message: String) : Outcome
  }
}

public fun <T : Any> QuackAction.Param<T?>.required(): QuackAction.Param<T> =
  QuackAction.Param(
    name = name,
    type = type,
    presence = QuackAction.Param.Presence.Required,
    description = description,
    parse = parse,
    hints = hints,
  )

public fun <T : Any> QuackAction.Param<T?>.withDefault(value: T): QuackAction.Param<T> =
  QuackAction.Param(
    name = name,
    type = type,
    presence = QuackAction.Param.Presence.Default(value),
    description = description,
    parse = parse,
    hints = hints,
  )

/** Values the CLI offers while the user types, in this order and without duplicates. The user can still type any value. */
public fun QuackAction.Param<String?>.hints(values: List<String>): QuackAction.Param<String?> = withHints(values)

/** Values the CLI offers while the user types, in this order and without duplicates. The user can still type any value. */
@JvmName("hintsOfNonNull") public fun QuackAction.Param<String>.hints(values: List<String>): QuackAction.Param<String> = withHints(values)

private fun <T> QuackAction.Param<T>.withHints(values: List<String>): QuackAction.Param<T> =
  QuackAction.Param(name = name, type = type, presence = presence, description = description, parse = parse, hints = values.distinct())

public fun QuackAction.args(): QuackAction.Args = QuackAction.Args(this, emptyMap())
