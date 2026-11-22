package io.mehow.quack

import io.mehow.quack.QuackAction.Param.Presence
import kotlin.coroutines.cancellation.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.buffer
import kotlinx.coroutines.flow.channelFlow
import kotlinx.coroutines.withContext

internal class Endpoint(
  actions: List<QuackAction>,
  hello: Frame.Hello,
  private val codec: Codec,
  private val actionDispatcher: CoroutineDispatcher,
  private val logger: QuackLogger,
) {
  private val actions = actions.associateBy(QuackAction::id)

  val hello = codec.encode(hello)

  // channelFlow, not flow, so that actions can send notifications from any context. The rendezvous buffer
  // hands each line over before the action continues.
  fun serve(request: String): Flow<String> = channelFlow {
    val reply =
      when (val decoded = codec.decodeRequest(request)) {
        Request.Describe -> describe()
        is Request.Call -> call(decoded) { frame -> send(codec.encode(frame)) }
        is Request.Malformed -> Frame.Error(ErrorCode.BadFrame, decoded.reason)
      }
    send(codec.encode(reply))
  }
    .buffer(Channel.RENDEZVOUS)

  private fun describe(): Frame = Frame.Result.Actions(actions.values.toList())

  private suspend fun call(request: Request.Call, send: suspend (Frame) -> Unit): Frame {
    val action = actions[request.action] ?: return Frame.Error(ErrorCode.UnknownAction, "no action '${request.action}'")
    val args =
      when (val parsed = parseArgs(action, request.args)) {
        is ParsedArgs.Valid -> parsed.args
        is ParsedArgs.Invalid -> return Frame.Error(ErrorCode.InvalidArgs, parsed.message)
      }
    return try {
      execute(action, args, send)
    } catch (e: CancellationException) {
      // Only a canceled request ends the coroutine. An action's own timeout is its failure.
      currentCoroutineContext().ensureActive()
      failed(action, e)
    } catch (e: VirtualMachineError) {
      throw e
    } catch (e: Throwable) {
      // An Error such as TODO()'s fails the action too, so that it cannot crash the app.
      failed(action, e)
    }
  }

  private fun failed(action: QuackAction, e: Throwable): Frame {
    logger.log(QuackLogger.Level.Error, "Action '${action.id}' failed", e)
    return Frame.Error(ErrorCode.Failed, e.message ?: e.javaClass.name)
  }

  private fun parseArgs(action: QuackAction, rawArgs: Map<String, RawArg>): ParsedArgs {
    val unknown = rawArgs.keys.firstOrNull { key -> action.params.none { it.name == key } }
    if (unknown != null) return ParsedArgs.Invalid("unknown argument '$unknown'")
    val values = mutableMapOf<String, Any>()
    for (param in action.params) {
      val raw = rawArgs[param.name]
      if (raw == null) {
        if (param.presence is Presence.Required) return ParsedArgs.Invalid("missing argument '${param.name}'")
        continue
      }
      if (raw is RawArg.Repeated && !param.type.isList) {
        return ParsedArgs.Invalid("'${param.name}' given more than once")
      }
      values[param.name] =
        param.parse(raw.values)
          ?: return ParsedArgs.Invalid("'${param.name}' expects ${param.type.expected}, got " + raw.values.joinToString { "'$it'" })
    }
    return ParsedArgs.Valid(QuackAction.Args(action, values))
  }

  private suspend fun execute(action: QuackAction, args: QuackAction.Args, send: suspend (Frame) -> Unit): Frame =
    when (val outcome = withContext(actionDispatcher) { action.execute(args) { message -> send(Frame.Notification(message)) } }) {
      is QuackAction.Outcome.Ok -> result(action, outcome)
      is QuackAction.Outcome.Error -> Frame.Error(ErrorCode.Rejected, outcome.message)
    }

  private fun result(action: QuackAction, ok: QuackAction.Outcome.Ok): Frame {
    val text = ok.json ?: return Frame.Result.Message(ok.message, data = null)
    val data = codec.parseJson(text)
    if (data == null) {
      logger.log(QuackLogger.Level.Error, "Action '${action.id}' returned json that does not parse: $text", throwable = null)
      return Frame.Error(ErrorCode.Failed, "the action returned json that does not parse")
    }
    return Frame.Result.Message(ok.message, data)
  }
}

private sealed interface ParsedArgs {
  class Valid(val args: QuackAction.Args) : ParsedArgs

  class Invalid(val message: String) : ParsedArgs
}
