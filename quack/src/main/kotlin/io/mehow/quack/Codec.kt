package io.mehow.quack

import io.mehow.quack.QuackAction.Param.Presence
import org.json.JSONArray
import org.json.JSONException
import org.json.JSONObject

internal class Codec {
  fun decodeRequest(line: String): Request {
    val json = parseJson(line) ?: return Request.Malformed("request is not a JSON object")
    val frame =
      try {
        JSONObject(json.encoded)
      } catch (_: JSONException) {
        return Request.Malformed("request is not a JSON object")
      }
    val method = if (frame.has("method")) frame.get("method") else ""
    val action = if (frame.has("action")) frame.get("action") else ""
    val args = if (frame.has("args")) frame.get("args") else JSONObject()
    if (method !is String || action !is String || args !is JSONObject) return Request.Malformed("malformed request")
    return when (method) {
      "describe" -> Request.Describe
      "call" -> {
        val values = mutableMapOf<String, RawArg>()
        for (key in args.keys()) {
          values[key] = rawArg(args.get(key)) ?: return Request.Malformed("argument '$key' is not a string or an array of strings")
        }
        Request.Call(action, values)
      }
      else -> Request.Malformed("unknown method '$method'")
    }
  }

  fun encode(frame: Frame): String {
    val (key, body) =
      when (frame) {
        is Frame.Hello ->
          "hello" to
            JSONObject()
              .put("protocol", PROTOCOL_VERSION)
              .put("app", JSONObject().put("package", frame.packageName).put("version", frame.version))
              .toString()
        is Frame.Notification -> "notification" to messageBody(frame.message, frame.data)
        is Frame.Result.Message -> "result" to messageBody(frame.message, frame.data)
        is Frame.Result.Actions -> "result" to JSONObject().put("actions", JSONArray(frame.actions.map(::actionBody))).toString()
        is Frame.Error -> "error" to JSONObject().put("code", frame.code.wireName).put("message", frame.message).toString()
      }
    return "{\"$key\":$body}"
  }

  fun parseJson(text: String): JsonData? = JsonData.parse(text)

  private fun rawArg(value: Any): RawArg? {
    return when (value) {
      is String -> RawArg.Single(value)
      is JSONArray -> {
        val values = mutableListOf<String>()
        for (i in 0 until value.length()) values += value.get(i) as? String ?: return null
        RawArg.Repeated(values)
      }
      else -> null
    }
  }

  private fun messageBody(message: String, data: JsonData?): String {
    val body = JSONObject().put("message", message).toString()
    // Validated, compact JSON is embedded unchanged: arbitrary numbers must not go through Double.
    return if (data == null) body else body.dropLast(1) + ",\"data\":" + data.encoded + "}"
  }

  private fun actionBody(action: QuackAction): JSONObject =
    JSONObject()
      .put("id", action.id)
      .put("description", action.description)
      .put(
        "params",
        JSONArray(
          action.params.map { param ->
            JSONObject()
              .put("name", param.name)
              .put("type", param.type.wireName)
              .put("choices", (param.type as? QuackAction.Param.Type.Enum)?.wireChoices?.let(::JSONArray))
              .put("hints", param.hints.takeIf { it.isNotEmpty() }?.let(::JSONArray))
              .put("env", param.env)
              .put("required", param.presence is Presence.Required)
              .put("default", (param.presence as? Presence.Default)?.value?.let(::defaultBody))
              .put("description", param.description)
          },
        ),
      )

  private fun defaultBody(value: Any): Any =
    when (value) {
      is List<*> -> JSONArray(value.map { it.toString() })
      is Enum<*> -> value.name.lowercase()
      else -> value.toString()
    }
}

private const val PROTOCOL_VERSION = 1
