package io.mehow.quack

internal sealed interface Request {
  data object Describe : Request

  data class Call(val action: String, val args: Map<String, RawArg>) : Request

  data class Malformed(val reason: String) : Request
}

internal sealed interface RawArg {
  val values: List<String>

  data class Single(val value: String) : RawArg {
    override val values: List<String>
      get() = listOf(value)
  }

  data class Repeated(override val values: List<String>) : RawArg
}
