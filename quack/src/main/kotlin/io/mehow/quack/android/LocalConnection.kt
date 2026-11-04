package io.mehow.quack.android

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.net.Credentials
import android.net.LocalSocket
import io.mehow.quack.Endpoint
import io.mehow.quack.QuackLogger
import io.mehow.quack.QuackLogger.Level
import java.io.IOException
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch
import okio.BufferedSink
import okio.BufferedSource
import okio.buffer
import okio.sink
import okio.source

internal class LocalConnection(
  private val socket: LocalSocket,
  private val context: Context,
  private val endpoint: Endpoint,
  private val keepAlive: KeepAlive,
  private val logger: QuackLogger,
) {
  suspend fun serve(): Unit = socket.use {
    val peer = socket.peerCredentials
    if (!peer.holdsDumpPermission()) {
      logger.log(Level.Warn, "Refused connection from uid ${peer.uid}, pid ${peer.pid}: no DUMP permission", throwable = null)
      return@use
    }
    val sink = socket.outputStream.sink().buffer()
    val source = socket.inputStream.source().buffer()
    sink.writeLine(endpoint.hello)
    val request = readRequest(source) ?: return@use
    keepAlive.hold { reply(request, source, sink) }
  }

  private fun readRequest(source: BufferedSource): String? {
    socket.soTimeout = REQUEST_TIMEOUT_MS
    return try {
      if (source.exhausted()) null else source.readUtf8LineStrict(MAX_FRAME_BYTES)
    } catch (_: IOException) {
      null
    } finally {
      socket.soTimeout = 0
    }
  }

  private suspend fun reply(request: String, source: BufferedSource, sink: BufferedSink) {
    coroutineScope {
      val serving = launch { endpoint.serve(request).collect { sink.writeLine(it) } }
      launch {
        source.awaitDisconnect()
        serving.cancel()
      }
      try {
        serving.join()
      } finally {
        // close() does not wake a thread blocked in read. shutdownInput() does.
        socket.shutdownInput()
      }
    }
  }

  // The CLI sends nothing after its request, so reading ends only when it disconnects.
  private fun BufferedSource.awaitDisconnect() {
    try {
      while (!exhausted()) skip(buffer.size)
    } catch (_: IOException) {}
  }

  // Any app can reach an abstract socket. Only adb's shell holds DUMP.
  private fun Credentials.holdsDumpPermission(): Boolean =
    context.checkPermission(Manifest.permission.DUMP, pid, uid) == PackageManager.PERMISSION_GRANTED

  private fun BufferedSink.writeLine(line: String) {
    writeUtf8(line).writeByte('\n'.code).flush()
  }
}

private const val REQUEST_TIMEOUT_MS = 5_000
private const val MAX_FRAME_BYTES = 64 * 1024L
