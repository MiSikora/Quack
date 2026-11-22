package io.mehow.quack

import io.kotest.assertions.json.shouldEqualJson
import io.kotest.assertions.throwables.shouldThrow
import io.kotest.matchers.booleans.shouldBeTrue
import io.kotest.matchers.collections.shouldBeEmpty
import io.kotest.matchers.collections.shouldContainExactly
import io.kotest.matchers.collections.shouldHaveSize
import io.mehow.quack.QuackAction.Outcome
import io.mehow.quack.QuackAction.Param
import java.util.concurrent.Executors
import kotlin.coroutines.ContinuationInterceptor
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObjectBuilder
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject
import org.junit.Test

class EndpointTest {
  @Test
  fun `say hello with the app's package and version`() {
    endpoint().hello shouldEqualJson
      """
      {
        "hello": {
          "protocol": 1,
          "app": {
            "package": "io.mehow.test",
            "version": "1.0"
          }
        }
      }
      """
  }

  @Test
  fun `omit a missing app version from hello`() {
    val endpoint =
      Endpoint(
        actions = emptyList(),
        hello = Frame.Hello(packageName = "io.mehow.test", version = null),
        codec = Codec(),
        actionDispatcher = Dispatchers.Unconfined,
        logger = RecordingLogger(),
      )

    endpoint.hello shouldEqualJson
      """
      {
        "hello": {
          "protocol": 1,
          "app": {
            "package": "io.mehow.test"
          }
        }
      }
      """
  }

  @Test
  fun `describe actions with their params`() = runTest {
    val action =
      FakeAction(
        id = "app.greet",
        params =
          listOf(
            Param.string("name", description = "Who to greet").required(),
            Param.int("times").withDefault(1),
            Param.boolean("loud"),
            Param.boolean("dry_run").withDefault(false),
          ),
      )

    endpoint(action).describe() shouldReply
      """
      {
        "result": {
          "actions": [
            {
              "id": "app.greet",
              "description": "A test action",
              "params": [
                {
                  "name": "name",
                  "type": "string",
                  "required": true,
                  "description": "Who to greet"
                },
                {
                  "name": "times",
                  "type": "int",
                  "required": false,
                  "default": "1",
                  "description": ""
                },
                {
                  "name": "loud",
                  "type": "boolean",
                  "required": false,
                  "description": ""
                },
                {
                  "name": "dry_run",
                  "type": "boolean",
                  "required": false,
                  "default": "false",
                  "description": ""
                }
              ]
            }
          ]
        }
      }
      """
  }

  @Test
  fun `describe the wire type of each param`() = runTest {
    val action =
      FakeAction(
        params =
          listOf(
            Param.string("text"),
            Param.int("count"),
            Param.long("size"),
            Param.double("ratio"),
            Param.boolean("flag"),
            Param.enum<Theme>("theme"),
            Param.enum("mode", listOf(Theme.Dark)).withDefault(Theme.Dark),
            Param.stringList("tag"),
            Param.stringList("label").withDefault(listOf("a", "b")),
          ),
      )

    endpoint(action).describe() shouldReply
      """
      {
        "result": {
          "actions": [
            {
              "id": "test.action",
              "description": "A test action",
              "params": [
                {
                  "name": "text",
                  "type": "string",
                  "required": false,
                  "description": ""
                },
                {
                  "name": "count",
                  "type": "int",
                  "required": false,
                  "description": ""
                },
                {
                  "name": "size",
                  "type": "long",
                  "required": false,
                  "description": ""
                },
                {
                  "name": "ratio",
                  "type": "double",
                  "required": false,
                  "description": ""
                },
                {
                  "name": "flag",
                  "type": "boolean",
                  "required": false,
                  "description": ""
                },
                {
                  "name": "theme",
                  "type": "enum",
                  "choices": ["light", "dark"],
                  "required": false,
                  "description": ""
                },
                {
                  "name": "mode",
                  "type": "enum",
                  "choices": ["dark"],
                  "required": false,
                  "default": "dark",
                  "description": ""
                },
                {
                  "name": "tag",
                  "type": "string_list",
                  "required": false,
                  "description": ""
                },
                {
                  "name": "label",
                  "type": "string_list",
                  "required": false,
                  "default": ["a", "b"],
                  "description": ""
                }
              ]
            }
          ]
        }
      }
      """
  }

  @Test
  fun `describe the hints of string params in order and without duplicates`() = runTest {
    val action =
      FakeAction(
        params =
          listOf(
            Param.string("feature").hints(listOf("sync", "autofill", "sync")),
            Param.string("name").required().hints(listOf("Alice")),
            Param.string("theme").withDefault("dark").hints(listOf("light", "dark")),
            Param.string("text").hints(emptyList()),
          ),
      )

    endpoint(action).describe() shouldReply
      """
      {
        "result": {
          "actions": [
            {
              "id": "test.action",
              "description": "A test action",
              "params": [
                {
                  "name": "feature",
                  "type": "string",
                  "hints": ["sync", "autofill"],
                  "required": false,
                  "description": ""
                },
                {
                  "name": "name",
                  "type": "string",
                  "hints": ["Alice"],
                  "required": true,
                  "description": ""
                },
                {
                  "name": "theme",
                  "type": "string",
                  "hints": ["light", "dark"],
                  "required": false,
                  "default": "dark",
                  "description": ""
                },
                {
                  "name": "text",
                  "type": "string",
                  "required": false,
                  "description": ""
                }
              ]
            }
          ]
        }
      }
      """
  }

  @Test
  fun `call an action with parsed args`() = runTest {
    val name = Param.string("name").required()
    val times = Param.int("times").withDefault(2)
    val loud = Param.boolean("loud")
    val polite = Param.boolean("polite")
    val action =
      FakeAction(
        params = listOf(name, times, loud, polite),
        execute = { args, _ ->
          Outcome.Ok("${args[name]} ${args[times]} ${args[loud]} ${args[polite]}")
        },
      )

    endpoint(action).call("test.action", "name" to "Bob", "polite" to "true") shouldReply result("Bob 2 null true")
  }

  @Test
  fun `parse long and double args`() = runTest {
    val size = Param.long("size")
    val ratio = Param.double("ratio")
    val action =
      FakeAction(
        params = listOf(size, ratio),
        execute = { args, _ -> Outcome.Ok("${args[size]} ${args[ratio]}") },
      )

    endpoint(action).call("test.action", "size" to "9000000000", "ratio" to "-0.5") shouldReply result("9000000000 -0.5")
  }

  @Test
  fun `parse an enum arg by constant name ignoring case`() = runTest {
    val theme = Param.enum<Theme>("theme")
    val action =
      FakeAction(
        params = listOf(theme),
        execute = { args, _ -> Outcome.Ok("${args[theme]?.name}") },
      )
    val endpoint = endpoint(action)

    for (raw in listOf("Dark", "dark", "DARK")) {
      endpoint.call("test.action", "theme" to raw) shouldReply result("Dark")
    }
  }

  @Test
  fun `parse a list from one key or a repeated key`() = runTest {
    val tags = Param.stringList("tag")
    val labels = Param.stringList("label").withDefault(emptyList())
    val action =
      FakeAction(
        params = listOf(tags, labels),
        execute = { args, _ -> Outcome.Ok("${args[tags]} ${args[labels]}") },
      )
    val endpoint = endpoint(action)

    endpoint.call("test.action") shouldReply result("null []")
    endpoint.call("test.action", "tag" to "a") shouldReply result("[a] []")
    endpoint.call("test.action", "tag" to listOf("a", "b"), "label" to listOf("c", "c")) shouldReply result("[a, b] [c, c]")
    endpoint.call("test.action", "tag" to emptyList<String>()) shouldReply result("[] []")
  }

  @Test
  fun `prefer a passed argument over the default`() = runTest {
    val times = Param.int("times").withDefault(2)
    val action = FakeAction(params = listOf(times), execute = { args, _ -> Outcome.Ok("${args[times]}") })

    endpoint(action).call("test.action", "times" to "5") shouldReply result("5")
  }

  @Test
  fun `fail an action that reads a param it does not declare`() = runTest {
    val declared = Param.string("name").required()
    val undeclared = Param.string("name").required()
    val action = FakeAction(params = listOf(declared), execute = { args, _ -> Outcome.Ok(args[undeclared]) })

    endpoint(action).call("test.action", "name" to "Bob") shouldReply failed("Param 'name' is not in the params of action 'test.action'")
  }

  @Test
  fun `send notifications before the result`() = runTest {
    val action =
      FakeAction(
        execute = { _, terminal ->
          terminal.send("Started")
          terminal.send("Working")
          Outcome.Ok("Done")
        },
      )

    endpoint(action).call("test.action") shouldReply listOf(notification("Started"), notification("Working"), result("Done"))
  }

  @Test
  fun `stream notifications that an action sends from another dispatcher`() = runTest {
    val action =
      FakeAction(
        execute = { _, terminal ->
          withContext(Dispatchers.Default) { terminal.send("Working") }
          Outcome.Ok("Done")
        },
      )

    endpoint(action).call("test.action") shouldReply listOf(notification("Working"), result("Done"))
  }

  @Test
  fun `reject a request that is not a JSON object`() = runTest {
    val endpoint = endpoint(FakeAction())

    for (line in listOf("not json", "[]", "\"call\"")) {
      endpoint.send(line) shouldReply badFrame("request is not a JSON object")
    }
  }

  @Test
  fun `reject an argument that is not a string or an array of strings`() = runTest {
    val endpoint = endpoint(FakeAction(params = listOf(Param.stringList("tag"))))

    for (value in listOf("1", "null", "{}", """["a", 1]""", """[["a"]]""")) {
      val request =
        """
        {
          "method": "call",
          "action": "test.action",
          "args": {
            "tag": $value
          }
        }
        """

      endpoint.send(request) shouldReply badFrame("argument 'tag' is not a string or an array of strings")
    }
  }

  @Test
  fun `reject an unknown method`() = runTest {
    val request =
      """
      {
        "method": "nope"
      }
      """

    endpoint(FakeAction()).send(request) shouldReply badFrame("unknown method 'nope'")
  }

  @Test
  fun `reject an unknown action`() = runTest {
    endpoint(FakeAction()).call("nope") shouldReply unknownAction("no action 'nope'")
  }

  @Test
  fun `reject a missing required argument`() = runTest {
    val endpoint = endpoint(FakeAction(params = listOf(Param.string("name").required())))

    endpoint.call("test.action") shouldReply invalidArgs("missing argument 'name'")
  }

  @Test
  fun `reject invalid args`() = runTest {
    val endpoint =
      endpoint(
        FakeAction(
          params =
            listOf(
              Param.int("times"),
              Param.long("size"),
              Param.double("ratio"),
              Param.boolean("loud"),
              Param.enum<Theme>("theme"),
              Param.enum("mode", listOf(Theme.Dark)),
            ),
        ),
      )

    endpoint.call("test.action", "foo" to "1") shouldReply invalidArgs("unknown argument 'foo'")
    endpoint.call("test.action", "times" to listOf("1", "2")) shouldReply invalidArgs("'times' given more than once")
    endpoint.call("test.action", "times" to listOf("1")) shouldReply invalidArgs("'times' given more than once")
    endpoint.call("test.action", "times" to "abc") shouldReply invalidArgs("'times' expects int, got 'abc'")
    endpoint.call("test.action", "times" to "9000000000") shouldReply invalidArgs("'times' expects int, got '9000000000'")
    endpoint.call("test.action", "size" to "1.5") shouldReply invalidArgs("'size' expects long, got '1.5'")
    endpoint.call("test.action", "ratio" to "abc") shouldReply invalidArgs("'ratio' expects double, got 'abc'")
    endpoint.call("test.action", "ratio" to "NaN") shouldReply invalidArgs("'ratio' expects double, got 'NaN'")
    endpoint.call("test.action", "ratio" to "Infinity") shouldReply invalidArgs("'ratio' expects double, got 'Infinity'")
    endpoint.call("test.action", "ratio" to "-Infinity") shouldReply invalidArgs("'ratio' expects double, got '-Infinity'")
    endpoint.call("test.action", "loud" to "yes") shouldReply invalidArgs("'loud' expects boolean, got 'yes'")
    endpoint.call("test.action", "theme" to "drak") shouldReply invalidArgs("'theme' expects one of light, dark, got 'drak'")
    endpoint.call("test.action", "mode" to "light") shouldReply invalidArgs("'mode' expects one of dark, got 'light'")
  }

  @Test
  fun `execute on the action dispatcher`() = runTest {
    val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
    val interceptors = mutableListOf<ContinuationInterceptor?>()
    val action =
      FakeAction(
        execute = { _, _ ->
          interceptors += currentCoroutineContext()[ContinuationInterceptor]
          Outcome.Ok("Done")
        },
      )

    dispatcher.use { endpoint(action, actionDispatcher = it).call("test.action") shouldReply result("Done") }

    interceptors shouldContainExactly listOf(dispatcher)
  }

  @Test
  fun `report an action error as rejected`() = runTest {
    val action = FakeAction(execute = { _, _ -> Outcome.Error("no way") })

    endpoint(action).call("test.action") shouldReply rejected("no way")
  }

  @Test
  fun `reply with the data an action returns as json`() = runTest {
    val action = FakeAction(execute = { _, _ -> Outcome.Ok("Signed in", json = """{"username": "alice", "tags": ["a"]}""") })

    endpoint(action).call("test.action") shouldReply
      """
      {
        "result": {
          "message": "Signed in",
          "data": {
            "username": "alice",
            "tags": ["a"]
          }
        }
      }
      """
  }

  @Test
  fun `send the data of a notification as json`() = runTest {
    val action =
      FakeAction(
        execute = { _, terminal ->
          terminal.send("Pairing code ready", json = """{"code": "abc123"}""")
          Outcome.Ok("Paired")
        },
      )

    endpoint(action).call("test.action") shouldReply
      listOf(
        """
        {
          "notification": {
            "message": "Pairing code ready",
            "data": {
              "code": "abc123"
            }
          }
        }
        """,
        result("Paired"),
      )
  }

  @Test
  fun `fail an action that sends json that does not parse`() = runTest {
    val action =
      FakeAction(
        execute = { _, terminal ->
          terminal.send("Pairing code ready", json = "{code: abc123")
          Outcome.Ok("Paired")
        },
      )
    val logger = RecordingLogger()

    endpoint(action, logger = logger).call("test.action") shouldReply failed("the action sent json that does not parse")

    logger.entries shouldContainExactly
      listOf(
        RecordingLogger.Entry(QuackLogger.Level.Error, "Action 'test.action' sent json that does not parse: {code: abc123", null),
      )
  }

  @Test
  fun `fail an action that returns json that does not parse`() = runTest {
    val action = FakeAction(execute = { _, _ -> Outcome.Ok("Signed in", json = "{username: alice") })
    val logger = RecordingLogger()

    endpoint(action, logger = logger).call("test.action") shouldReply failed("the action returned json that does not parse")

    logger.entries shouldContainExactly
      listOf(
        RecordingLogger.Entry(QuackLogger.Level.Error, "Action 'test.action' returned json that does not parse: {username: alice", null),
      )
  }

  @Test
  fun `report a thrown exception as failed`() = runTest {
    val throwing = FakeAction(id = "app.throw", execute = { _, _ -> error("boom") })
    val silent = FakeAction(id = "app.silent", execute = { _, _ -> throw IllegalStateException() })
    val endpoint = endpoint(throwing, silent)

    endpoint.call("app.throw") shouldReply failed("boom")
    endpoint.call("app.silent") shouldReply failed("java.lang.IllegalStateException")
  }

  @Test
  fun `report a thrown error as failed`() = runTest {
    val action = FakeAction(execute = { _, _ -> TODO("later") })

    endpoint(action).call("test.action") shouldReply failed("An operation is not implemented: later")
  }

  @Test
  fun `report an action timing out on its own as failed`() = runTest {
    val action = FakeAction(execute = { _, _ -> withTimeout(1_000) { awaitCancellation() } })

    endpoint(action).call("test.action") shouldReply failed("Timed out waiting for 1000 ms")
  }

  @Test
  fun `let a virtual machine error through`() = runTest {
    val action = FakeAction(execute = { _, _ -> throw OutOfMemoryError("full") })

    shouldThrow<OutOfMemoryError> { endpoint(action).call("test.action") }
  }

  @Test
  fun `log a thrown exception`() = runTest {
    val exception = IllegalStateException("boom")
    val action = FakeAction(execute = { _, _ -> throw exception })
    val logger = RecordingLogger()

    endpoint(action, logger = logger).call("test.action") shouldReply failed("boom")

    logger.entries shouldContainExactly listOf(RecordingLogger.Entry(QuackLogger.Level.Error, "Action 'test.action' failed", exception))
  }

  @Test
  fun `cancel a running action when serving is canceled`() = runTest {
    val waiting = CompletableDeferred<Unit>()
    var canceled = false
    val action =
      FakeAction(
        execute = { _, _ ->
          try {
            waiting.complete(Unit)
            awaitCancellation()
          } finally {
            canceled = true
          }
        },
      )
    val request =
      """
      {
        "method": "call",
        "action": "test.action"
      }
      """
    val replies = mutableListOf<String>()
    val logger = RecordingLogger()
    val serving = launch { endpoint(action, logger = logger).serve(request).toList(replies) }
    waiting.await()

    serving.cancelAndJoin()

    logger.entries.shouldBeEmpty()

    canceled.shouldBeTrue()
    replies.shouldBeEmpty()
  }
}

private enum class Theme {
  Light,
  Dark;

  override fun toString() = name.lowercase()
}

private fun endpoint(
  vararg actions: QuackAction,
  actionDispatcher: CoroutineDispatcher = Dispatchers.Unconfined,
  logger: QuackLogger = RecordingLogger(),
) =
  Endpoint(
    actions = actions.toList(),
    hello = Frame.Hello(packageName = "io.mehow.test", version = "1.0"),
    codec = Codec(),
    actionDispatcher = actionDispatcher,
    logger = logger,
  )

private suspend fun Endpoint.send(line: String) = serve(line).toList()

private suspend fun Endpoint.describe() =
  send(
    """
    {
      "method": "describe"
    }
    """,
  )

private suspend fun Endpoint.call(action: String, vararg args: Pair<String, Any>) =
  send(
    buildJsonObject {
      put("method", "call")
      put("action", action)
      if (args.isNotEmpty()) {
        putJsonObject("args") {
          for ((key, value) in args) {
            val element =
              when (value) {
                is List<*> -> JsonArray(value.map { JsonPrimitive(it as String) })
                else -> JsonPrimitive(value as String)
              }
            put(key, element)
          }
        }
      }
    }
      .toString(),
  )

private infix fun List<String>.shouldReply(reply: String) = shouldReply(listOf(reply))

private infix fun List<String>.shouldReply(replies: List<String>) {
  shouldHaveSize(replies.size)
  zip(replies) { actual, expected -> actual shouldEqualJson expected }
}

private fun result(message: String) = reply("result") { put("message", message) }

private fun notification(message: String) = reply("notification") { put("message", message) }

private fun badFrame(message: String) = errorReply("bad_frame", message)

private fun unknownAction(message: String) = errorReply("unknown_action", message)

private fun invalidArgs(message: String) = errorReply("invalid_args", message)

private fun rejected(message: String) = errorReply("rejected", message)

private fun failed(message: String) = errorReply("failed", message)

private fun errorReply(code: String, message: String) =
  reply("error") {
    put("code", code)
    put("message", message)
  }

private fun reply(key: String, body: JsonObjectBuilder.() -> Unit) = buildJsonObject { putJsonObject(key, body) }.toString()
