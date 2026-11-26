package io.mehow.quack

import io.mehow.quack.QuackAction.Param.Presence
import org.json.JSONArray
import org.json.JSONException
import org.json.JSONObject
import org.json.JSONTokener

internal class Codec {
  fun decodeRequest(line: String): Request {
    val frame = parseJson(line) as? JSONObject ?: return Request.Malformed("request is not a JSON object")
    val method = frame.opt("method", "") ?: return Request.Malformed("malformed request")
    val action = frame.opt("action", "") ?: return Request.Malformed("malformed request")
    val args = frame.opt("args", JSONObject()) ?: return Request.Malformed("malformed request")
    return when (method) {
      "describe" -> decodeDescribe()
      "call" -> decodeCall(action, args)
      else -> Request.Malformed("unknown method '$method'")
    }
  }

  fun encode(frame: Frame): String =
    when (frame) {
      is Frame.Hello -> encodeHello(frame)
      is Frame.Notification -> encodeNotification(frame)
      is Frame.Result.Message -> encodeMessage(frame)
      is Frame.Result.Actions -> encodeActions(frame)
      is Frame.Error -> encodeError(frame)
    }.toString()

  fun parseJson(text: String): Any? =
    try {
      val parser = JSONTokener(text)
      val value = parser.nextValue()
      if (parser.nextClean() == '\u0000') value else null
    } catch (_: JSONException) {
      null
    }

  private fun decodeDescribe(): Request.Describe = Request.Describe

  private fun decodeCall(action: String, args: JSONObject): Request {
    val values = mutableMapOf<String, RawArg>()
    for (key in args.keys()) {
      values[key] = rawArg(args.get(key)) ?: return Request.Malformed("argument '$key' is not a string or an array of strings")
    }
    return Request.Call(action, values)
  }

  private fun encodeHello(frame: Frame.Hello): JSONObject =
    JSONObject()
      .put(
        "hello",
        JSONObject()
          .put("protocol", PROTOCOL_VERSION)
          .put("app", JSONObject().put("package", frame.packageName).put("version", frame.version)),
      )

  private fun encodeNotification(frame: Frame.Notification): JSONObject =
    JSONObject().put("notification", messageBody(frame.message, frame.data))

  private fun encodeMessage(frame: Frame.Result.Message): JSONObject = JSONObject().put("result", messageBody(frame.message, frame.data))

  private fun encodeActions(frame: Frame.Result.Actions): JSONObject =
    JSONObject().put("result", JSONObject().put("actions", JSONArray(frame.actions.map(::actionBody))))

  private fun encodeError(frame: Frame.Error): JSONObject =
    JSONObject().put("error", JSONObject().put("code", frame.code.wireName).put("message", frame.message))

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

  private fun messageBody(message: String, data: Any?): JSONObject = JSONObject().put("message", message).put("data", data)

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
      is List<*> -> JSONArray(value)
      is Enum<*> -> value.name.lowercase()
      else -> value.toString()
    }
}

// A missing field uses its declared default. Explicit JSON null and wrong types are malformed.
private inline fun <reified T : Any> JSONObject.opt(name: String, default: T): T? {
  val value = opt(name)
  return if (value === null) default else value as? T
}

private const val PROTOCOL_VERSION = 1
