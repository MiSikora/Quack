package io.mehow.quack.android

import android.Manifest
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.net.LocalSocket
import android.net.LocalSocketAddress
import android.os.IBinder
import android.os.ParcelFileDescriptor
import android.os.Process
import androidx.test.platform.app.InstrumentationRegistry
import io.kotest.assertions.json.shouldEqualJson
import io.kotest.assertions.throwables.shouldThrow
import io.kotest.assertions.withClue
import io.kotest.matchers.booleans.shouldBeTrue
import io.kotest.matchers.nulls.shouldBeNull
import io.kotest.matchers.shouldBe
import io.kotest.matchers.string.shouldContain
import io.kotest.matchers.string.shouldStartWith
import io.mehow.quack.QuackAction
import io.mehow.quack.QuackAction.Args
import io.mehow.quack.QuackAction.Outcome
import io.mehow.quack.QuackAction.Param
import io.mehow.quack.QuackAction.Terminal
import io.mehow.quack.QuackLogger
import io.mehow.quack.QuackLogger.Level
import io.mehow.quack.required
import java.io.Closeable
import java.io.IOException
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit.SECONDS
import kotlin.coroutines.ContinuationInterceptor
import kotlin.time.Duration.Companion.milliseconds
import kotlin.time.Duration.Companion.seconds
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.Json
import okio.buffer
import okio.sink
import okio.source
import org.junit.After
import org.junit.Before
import org.junit.Test

class QuackServerTest {
  private val instrumentation = InstrumentationRegistry.getInstrumentation()
  private val context = instrumentation.targetContext
  private val servers = mutableListOf<QuackServer>()
  private val logger = RecordingLogger()

  @Before
  fun grantDump() {
    shell("pm grant ${context.packageName} ${Manifest.permission.DUMP}")
  }

  @After
  fun stopServers() {
    servers.forEach(QuackServer::stop)
  }

  @Test
  fun serve_a_client_that_holds_DUMP() {
    server(GreetAction()).start()

    connect().use { client ->
      client.readLine() shouldEqualJson hello
      client.writeJson(
        """
        {
          "method": "call",
          "action": "app.greet",
          "args": {
            "name": "Bob"
          }
        }
        """,
      )
      client.readLine() shouldEqualJson
        """
        {
          "result": {
            "message": "Hello, Bob!"
          }
        }
        """
      client.shouldBeDisconnected()
    }
  }

  @Test
  fun answer_an_action_that_throws_an_error_and_keep_serving() {
    server(UnfinishedAction(), GreetAction()).start()

    connect().use { client ->
      client.readLine() shouldEqualJson hello
      client.writeJson(
        """
        {
          "method": "call",
          "action": "app.unfinished"
        }
        """,
      )
      client.readLine() shouldEqualJson
        """
        {
          "error": {
            "code": "failed",
            "message": "An operation is not implemented: later"
          }
        }
        """
    }
    connect().use { client ->
      client.readLine() shouldEqualJson hello
      client.writeJson(
        """
        {
          "method": "call",
          "action": "app.greet",
          "args": {
            "name": "Bob"
          }
        }
        """,
      )
      client.readLine() shouldEqualJson
        """
        {
          "result": {
            "message": "Hello, Bob!"
          }
        }
        """
    }
  }

  @Test
  fun refuse_a_client_without_DUMP() {
    shell("pm revoke ${context.packageName} ${Manifest.permission.DUMP}")
    server().start()

    connect().use { client -> client.shouldBeDisconnected() }

    logger.entries.single().level shouldBe Level.Warn
    logger.entries.single().message shouldStartWith "Refused connection from uid"
  }

  @Test
  fun refuse_connections_once_stopped() {
    val server = server()
    server.start()

    server.stop()

    shouldThrow<IOException> { connect() }
  }

  @Test
  fun serve_again_after_a_restart() {
    val server = server()
    server.start()
    server.stop()

    server.start()

    connect().use { client -> client.readLine() shouldEqualJson hello }
  }

  @Test
  fun keep_serving_when_a_second_server_cannot_bind() {
    server().start()

    server().start()

    connect().use { client -> client.readLine() shouldEqualJson hello }
    logger.entries.single().level shouldBe Level.Error
    logger.entries.single().message shouldStartWith "Not serving: binding socket"
  }

  @Test
  fun serve_only_in_the_main_process() {
    val connected = CountDownLatch(1)
    val connection =
      object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName, service: IBinder) = connected.countDown()

        override fun onServiceDisconnected(name: ComponentName) = Unit
      }
    context.bindService(Intent(context, SecondaryProcessService::class.java), connection, Context.BIND_AUTO_CREATE)
    try {
      withClue("Expected the secondary process to start") { connected.await(5, SECONDS).shouldBeTrue() }

      shouldThrow<IOException> { connect() }
    } finally {
      context.unbindService(connection)
    }
  }

  @Test
  fun leave_out_misconfigured_actions_by_default() {
    server(GreetAction(), NamedAction("-help")).start()

    connect().use { client ->
      client.readLine() shouldEqualJson hello
      client.writeJson(
        """
        {
          "method": "describe"
        }
        """,
      )
      client.readLine() shouldEqualJson
        """
        {
          "result": {
            "actions": [
              {
                "id": "app.greet",
                "description": "Say hello",
                "params": [
                  {
                    "name": "name",
                    "type": "string",
                    "required": true,
                    "description": ""
                  }
                ]
              }
            ]
          }
        }
        """
    }

    logger.entries.single().level shouldBe Level.Error
    logger.entries.single().message shouldContain "'-help'"
  }

  @Test
  fun throw_on_misconfigured_actions_when_asked() {
    val builder = QuackServer.Builder(context).actions(listOf(GreetAction(), NamedAction("-help"), NamedAction("/exit")))

    val error = shouldThrow<IllegalArgumentException> { builder.throwOnMisconfiguration().build() }

    error.message shouldContain "'-help'"
    error.message shouldContain "'/exit'"
  }

  @Test
  fun run_actions_on_the_action_dispatcher() {
    val action = DispatcherRecordingAction()
    Executors.newSingleThreadExecutor().asCoroutineDispatcher().use { dispatcher ->
      QuackServer.Builder(context).action(action).actionDispatcher(dispatcher).build().also(servers::add).start()

      connect().use { client ->
        client.readLine() shouldEqualJson hello
        client.writeJson(
          """
          {
            "method": "call",
            "action": "app.record"
          }
          """,
        )
        client.readLine() shouldEqualJson
          """
          {
            "result": {
              "message": "Done"
            }
          }
          """
      }

      action.interceptor shouldBe dispatcher
    }
  }

  @Test
  fun refuse_a_request_longer_than_64_KiB() {
    server(GreetAction()).start()

    connect().use { client ->
      client.readLine() shouldEqualJson hello
      client.writeLine("x".repeat(64 * 1024 + 1))
      client.shouldBeDisconnected()
    }
  }

  @Test
  fun cancel_a_running_action_when_the_client_disconnects() {
    val action = BlockingAction()
    server(action).start()

    connect().use { client ->
      client.readLine() shouldEqualJson hello
      client.writeJson(blockRequest)
      action.shouldHaveStarted()
    }

    action.shouldHaveBeenCanceled()
  }

  @Test
  fun hold_the_keep_alive_while_a_call_runs() {
    val keepAlive = KeepAlive()
    val action = BlockingAction()
    QuackServer.Builder(context).action(action).logger(logger).keepAlive(keepAlive).build().also(servers::add).start()

    connect().use { client ->
      client.readLine() shouldEqualJson hello
      client.writeJson(blockRequest)
      action.shouldHaveStarted()

      runBlocking { withTimeoutOrNull(500.milliseconds) { keepAlive.awaitIdle() } }.shouldBeNull()
    }

    runBlocking { withTimeout(5.seconds) { keepAlive.awaitIdle() } }
  }

  @Test
  fun cancel_a_running_action_on_stop() {
    val action = BlockingAction()
    val server = server(action)
    server.start()

    connect().use { client ->
      client.readLine() shouldEqualJson hello
      client.writeJson(blockRequest)
      action.shouldHaveStarted()

      server.stop()

      action.shouldHaveBeenCanceled()
      client.shouldBeDisconnected()
    }
  }

  private val hello =
    """
    {
      "hello": {
        "protocol": 1,
        "app": {
          "package": "${context.packageName}"
        }
      }
    }
    """

  private val blockRequest =
    """
    {
      "method": "call",
      "action": "app.block"
    }
    """

  private fun server(vararg actions: QuackAction) =
    QuackServer.Builder(context).actions(actions.asIterable()).logger(logger).build().also(servers::add)

  private fun connect(): Client {
    val socket = LocalSocket()
    socket.connect(LocalSocketAddress("${context.packageName}.quack.${Process.myUid() / 100_000}"))
    socket.soTimeout = 5_000
    return Client(socket)
  }

  private fun shell(command: String) {
    val output = instrumentation.uiAutomation.executeShellCommand(command)
    ParcelFileDescriptor.AutoCloseInputStream(output).use { it.readBytes() }
  }
}

private class Client(private val socket: LocalSocket) : Closeable {
  private val source = socket.inputStream.source().buffer()
  private val sink = socket.outputStream.sink().buffer()

  fun readLine(): String = source.readUtf8LineStrict()

  fun shouldBeDisconnected() {
    withClue("Expected the server to close the connection") { source.exhausted().shouldBeTrue() }
  }

  fun writeLine(line: String) {
    sink.writeUtf8(line).writeByte('\n'.code).flush()
  }

  fun writeJson(json: String) = writeLine(Json.parseToJsonElement(json).toString())

  override fun close() = socket.close()
}

private class GreetAction : QuackAction {
  private val name = Param.string("name").required()

  override val id = "app.greet"
  override val description = "Say hello"
  override val params = listOf(name)

  override suspend fun execute(args: Args, terminal: Terminal) = Outcome.Ok("Hello, ${args[name]}!")
}

private class UnfinishedAction : QuackAction {
  override val id = "app.unfinished"
  override val description = "Not written yet"
  override val params = emptyList<Param<*>>()

  override suspend fun execute(args: Args, terminal: Terminal): Outcome = TODO("later")
}

private class RecordingLogger : QuackLogger {
  val entries = CopyOnWriteArrayList<Entry>()

  override fun log(level: Level, message: String, throwable: Throwable?) {
    entries += Entry(level, message, throwable)
  }

  data class Entry(val level: Level, val message: String, val throwable: Throwable?)
}

private class NamedAction(override val id: String) : QuackAction {
  override val description = "An action with a given id"
  override val params = emptyList<Param<*>>()

  override suspend fun execute(args: Args, terminal: Terminal) = Outcome.Ok("Done")
}

private class DispatcherRecordingAction : QuackAction {
  @Volatile var interceptor: ContinuationInterceptor? = null

  override val id = "app.record"
  override val description = "Record the dispatcher it runs on"
  override val params = emptyList<Param<*>>()

  override suspend fun execute(args: Args, terminal: Terminal): Outcome {
    interceptor = currentCoroutineContext()[ContinuationInterceptor]
    return Outcome.Ok("Done")
  }
}

private class BlockingAction : QuackAction {
  private val started = CountDownLatch(1)
  private val canceled = CountDownLatch(1)

  override val id = "app.block"
  override val description = "Block until canceled"
  override val params = emptyList<Param<*>>()

  override suspend fun execute(args: Args, terminal: Terminal): Outcome {
    started.countDown()
    try {
      awaitCancellation()
    } finally {
      canceled.countDown()
    }
  }

  fun shouldHaveStarted() {
    withClue("Expected the action to start") { started.await(5, SECONDS).shouldBeTrue() }
  }

  fun shouldHaveBeenCanceled() {
    withClue("Expected the action to be canceled") { canceled.await(5, SECONDS).shouldBeTrue() }
  }
}
