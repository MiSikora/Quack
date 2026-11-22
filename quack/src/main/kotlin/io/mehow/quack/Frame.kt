package io.mehow.quack

import kotlinx.serialization.json.JsonElement

internal sealed interface Frame {
  data class Hello(val packageName: String, val version: String?) : Frame

  data class Notification(val message: String) : Frame

  sealed interface Result : Frame {
    data class Actions(val actions: List<QuackAction>) : Result

    data class Message(val message: String, val data: JsonElement?) : Result
  }

  data class Error(val code: ErrorCode, val message: String) : Frame
}

internal enum class ErrorCode(val wireName: String) {
  BadFrame("bad_frame"),
  UnknownAction("unknown_action"),
  InvalidArgs("invalid_args"),
  Rejected("rejected"),
  Failed("failed"),
}
