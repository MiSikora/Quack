package io.mehow.quack.android

import android.app.Application
import android.content.Context
import android.net.LocalServerSocket
import android.net.LocalSocket
import android.os.Process
import android.system.Os
import android.system.OsConstants
import io.mehow.quack.ActionProblem
import io.mehow.quack.Codec
import io.mehow.quack.Endpoint
import io.mehow.quack.Frame
import io.mehow.quack.QuackAction
import io.mehow.quack.QuackLogger
import io.mehow.quack.QuackLogger.Level
import io.mehow.quack.checkActions
import java.io.IOException
import java.util.concurrent.CountDownLatch
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.DelicateCoroutinesApi
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.launch

/**
 * [start] and [stop] are safe to call from any thread, concurrently, and any number of times. An app overrides the keep-alive
 * notification's channel and id with the resources `io_mehow_quack_notification_channel_id` and `io_mehow_quack_notification_id`.
 */
public class QuackServer internal constructor(builder: Builder) {
  private val context = builder.context.applicationContext
  private val logger = builder.logger
  private val socketName = socketName()
  private val endpoint =
    Endpoint(
      actions = servedActions(builder.actions.toList(), builder.throwOnMisconfiguration),
      hello = Frame.Hello(packageName = context.packageName, version = appVersion()),
      codec = Codec(),
      actionDispatcher = builder.actionDispatcher,
      logger = logger,
    )
  private val keepAlive = builder.keepAlive
  private val scope = CoroutineScope(SupervisorJob() + builder.ioDispatcher)
  private val lock = Any()

  // Guarded by lock.
  private var acceptLoop: AcceptLoop? = null

  /**
   * Call it from `Application.onCreate`, so the CLI can reach a stopped app. Does nothing outside the app's main process. Logs and stays
   * stopped when the socket cannot be bound.
   */
  public fun start() {
    if (!isMainProcess()) {
      logger.log(Level.Info, "Not serving in ${Application.getProcessName()}, only in the app's main process", throwable = null)
      return
    }
    synchronized(lock) {
      if (acceptLoop != null) return
      val socket = bindSocket() ?: return
      acceptLoop = startAcceptLoop(socket)
    }
  }

  /** Cancels the running actions and returns once the socket is unbound. */
  public fun stop() {
    synchronized(lock) {
      val current = acceptLoop ?: return
      current.close()
      acceptLoop = null
      current.ended.await()
    }
  }

  private fun servedActions(actions: List<QuackAction>, throwOnMisconfiguration: Boolean): List<QuackAction> {
    val checked = checkActions(actions)
    reportProblems(checked.problems, throwOnMisconfiguration)
    return checked.served
  }

  private fun reportProblems(problems: List<ActionProblem>, throwOnMisconfiguration: Boolean) {
    val lines = problems.map { "The CLI cannot run ${it.action.javaClass.name} as '${it.action.id}': ${it.message}" }
    require(!throwOnMisconfiguration || lines.isEmpty()) { lines.joinToString("\n") }
    lines.forEach { logger.log(Level.Error, it, throwable = null) }
  }

  private fun bindSocket(): LocalServerSocket? =
    try {
      LocalServerSocket(socketName)
    } catch (e: IOException) {
      logger.log(Level.Error, "Not serving: binding socket $socketName failed", e)
      null
    }

  private fun startAcceptLoop(socket: LocalServerSocket): AcceptLoop {
    val ended = CountDownLatch(1)
    val job = scope.launch {
      val failure =
        try {
          acceptConnections(socket)
        } catch (e: IOException) {
          e
        } finally {
          ended.countDown()
        }
      // stop() closes the socket, which also ends accept with an IOException.
      ensureActive()
      logger.log(Level.Error, "Stopped serving: accepting a connection failed", failure)
      // Only after ended opens: stop() waits for it while holding the lock that this needs.
      stopAfterFailure(socket)
    }
    // A job canceled before it runs skips the finally above.
    job.invokeOnCompletion { ended.countDown() }
    return AcceptLoop(socket, job, ended)
  }

  private fun CoroutineScope.acceptConnections(socket: LocalServerSocket): Nothing {
    while (true) launchConnection(socket.accept())
  }

  // ATOMIC runs the body even when stop() cancels first, so LocalConnection closes the socket.
  @OptIn(DelicateCoroutinesApi::class)
  private fun CoroutineScope.launchConnection(connection: LocalSocket) {
    launch(start = CoroutineStart.ATOMIC) {
      try {
        LocalConnection(connection, context, endpoint, keepAlive, logger).serve()
      } catch (_: IOException) {}
    }
  }

  private fun stopAfterFailure(socket: LocalServerSocket) {
    synchronized(lock) {
      val current = acceptLoop
      if (current?.socket !== socket) return
      current.close()
      acceptLoop = null
    }
  }

  // The CLI connects to <package>.quack.<user id>. Abstract socket names are shared across the device, so the user id keeps apps in
  // different users, such as a work profile, apart. The public API hides UserHandle.getIdentifier(). Android gives each user a block of
  // 100,000 ids, so the app's uid divided by 100,000 is its user id.
  private fun socketName(): String {
    val userId = Process.myUid() / 100_000
    return "${context.packageName}.quack.$userId"
  }

  private fun appVersion(): String? = context.packageManager.getPackageInfo(context.packageName, 0).versionName

  private fun isMainProcess(): Boolean = Application.getProcessName() == context.applicationInfo.processName

  /** Not safe to use from several threads at once. */
  public class Builder(internal val context: Context) {
    internal val actions = mutableListOf<QuackAction>()
    internal var actionDispatcher: CoroutineDispatcher = Dispatchers.IO
      private set

    internal var logger: QuackLogger = AndroidLogger()
      private set

    internal var throwOnMisconfiguration = false
      private set

    internal var keepAlive: KeepAlive = KeepAlive.Shared
      private set

    internal var ioDispatcher: CoroutineDispatcher = Dispatchers.IO
      private set

    public fun action(action: QuackAction): Builder = apply { actions += action }

    public fun actions(actions: Iterable<QuackAction>): Builder = apply { this.actions += actions }

    /** Runs actions on [dispatcher] instead of `Dispatchers.IO`. Socket work stays on `Dispatchers.IO`. */
    public fun actionDispatcher(dispatcher: CoroutineDispatcher): Builder = apply { actionDispatcher = dispatcher }

    /** Logs through [logger] instead of `android.util.Log` with the tag `Quack`. */
    public fun logger(logger: QuackLogger): Builder = apply { this.logger = logger }

    /** Makes [build] throw instead of leaving out misconfigured actions. */
    public fun throwOnMisconfiguration(): Builder = apply { throwOnMisconfiguration = true }

    /**
     * Leaves out and logs the actions the CLI cannot run, such as duplicate ids or malformed param names.
     *
     * @throws IllegalArgumentException listing them, after [throwOnMisconfiguration].
     */
    public fun build(): QuackServer = QuackServer(this)

    internal fun keepAlive(keepAlive: KeepAlive): Builder = apply { this.keepAlive = keepAlive }

    internal fun ioDispatcher(dispatcher: CoroutineDispatcher): Builder = apply { ioDispatcher = dispatcher }
  }
}

private class AcceptLoop(val socket: LocalServerSocket, val job: Job, val ended: CountDownLatch) {
  fun close() {
    job.cancel()
    // close() alone does not wake a thread blocked in accept. shutdown() does.
    Os.shutdown(socket.fileDescriptor, OsConstants.SHUT_RDWR)
    socket.close()
  }
}
