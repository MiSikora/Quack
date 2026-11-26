package io.mehow.quack

import io.mehow.quack.QuackAction.Param.Presence
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.jsonPrimitive

internal class Codec {
  private val json = Json {
    explicitNulls = false
    ignoreUnknownKeys = true
  }

  fun decodeRequest(line: String): Request {
    val element = parseObject(line) ?: return Request.Malformed("request is not a JSON object")
    val frame =
      try {
        json.decodeFromJsonElement<RequestFrame>(element)
      } catch (_: SerializationException) {
        return Request.Malformed("malformed request")
      }
    return when (frame.method) {
      "describe" -> Request.Describe
      "call" -> {
        val args = mutableMapOf<String, RawArg>()
        for ((key, value) in frame.args) {
          args[key] = rawArg(value) ?: return Request.Malformed("argument '$key' is not a string or an array of strings")
        }
        Request.Call(frame.action, args)
      }
      else -> Request.Malformed("unknown method '${frame.method}'")
    }
  }

  fun encode(frame: Frame): String {
    val (key, body) =
      when (frame) {
        is Frame.Hello -> "hello" to json.encodeToJsonElement(helloBody(frame))
        is Frame.Notification -> "notification" to json.encodeToJsonElement(MessageBody(frame.message, frame.data))
        is Frame.Result.Actions -> "result" to json.encodeToJsonElement(ActionsBody(frame.actions.map(::actionBody)))
        is Frame.Result.Message -> "result" to json.encodeToJsonElement(MessageBody(frame.message, frame.data))
        is Frame.Error -> "error" to json.encodeToJsonElement(ErrorBody(frame.code.wireName, frame.message))
      }
    return JsonObject(mapOf(key to body)).toString()
  }

  fun parseJson(text: String): JsonElement? =
    try {
      json.parseToJsonElement(text)
    } catch (_: SerializationException) {
      null
    }

  private fun parseObject(line: String): JsonObject? = parseJson(line) as? JsonObject

  private fun rawArg(value: JsonElement): RawArg? =
    when {
      value is JsonPrimitive && value.isString -> RawArg.Single(value.content)
      value is JsonArray && value.all { it is JsonPrimitive && it.isString } -> RawArg.Repeated(value.map { it.jsonPrimitive.content })
      else -> null
    }

  private fun helloBody(hello: Frame.Hello): HelloBody {
    val app = AppBody(packageName = hello.packageName, version = hello.version)
    return HelloBody(protocol = PROTOCOL_VERSION, app = app)
  }

  private fun actionBody(action: QuackAction): ActionBody {
    return ActionBody(
      id = action.id,
      description = action.description,
      params =
        action.params.map { param ->
          ParamBody(
            name = param.name,
            type = param.type.wireName,
            choices = (param.type as? QuackAction.Param.Type.Enum)?.wireChoices,
            hints = param.hints.ifEmpty { null },
            env = param.env,
            required = param.presence is Presence.Required,
            default = (param.presence as? Presence.Default)?.value?.let(::defaultBody),
            description = param.description,
          )
        },
    )
  }

  private fun defaultBody(value: Any): JsonElement =
    when (value) {
      is List<*> -> JsonArray(value.map { JsonPrimitive(it.toString()) })
      is Enum<*> -> JsonPrimitive(value.name.lowercase())
      else -> JsonPrimitive(value.toString())
    }
}

@Serializable
private class RequestFrame(
  val method: String = "",
  val action: String = "",
  val args: Map<String, JsonElement> = emptyMap(),
)

@Serializable
private class HelloBody(
  val protocol: Int,
  val app: AppBody,
)

@Serializable
private class AppBody(
  @SerialName("package") val packageName: String,
  val version: String?,
)

@Serializable
private class MessageBody(
  val message: String,
  val data: JsonElement?,
)

@Serializable
private class ActionsBody(
  val actions: List<ActionBody>,
)

@Serializable
private class ActionBody(
  val id: String,
  val description: String,
  val params: List<ParamBody>,
)

@Serializable
private class ParamBody(
  val name: String,
  val type: String,
  val choices: List<String>?,
  val hints: List<String>?,
  val env: String?,
  val required: Boolean,
  val default: JsonElement?,
  val description: String,
)

@Serializable
private class ErrorBody(
  val code: String,
  val message: String,
)

private const val PROTOCOL_VERSION = 1
