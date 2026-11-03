package io.mehow.quack.android

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import io.mehow.quack.R
import kotlinx.coroutines.Job
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch

internal class QuackService : Service() {
  private val scope = MainScope()
  private var stopping: Job? = null

  override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
    // Android kills an app whose foreground service skips startForeground.
    enterForeground()
    stopOnceIdle()
    return START_NOT_STICKY
  }

  override fun onBind(intent: Intent?): IBinder? = null

  override fun onDestroy() {
    scope.cancel()
    super.onDestroy()
  }

  private fun enterForeground() {
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
      startForeground(notificationId(), notification(), ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
    } else {
      startForeground(notificationId(), notification())
    }
  }

  private fun stopOnceIdle() {
    if (stopping != null) return
    stopping = scope.launch {
      KeepAlive.Shared.awaitIdle()
      stopSelf()
    }
  }

  private fun notification(): Notification {
    val manager = getSystemService(NotificationManager::class.java)
    manager.createNotificationChannel(NotificationChannel(channelId(), "Quack", NotificationManager.IMPORTANCE_LOW))
    return Notification.Builder(this, channelId())
      .setContentTitle("Quack")
      .setContentText("Running a long action")
      .setSmallIcon(android.R.drawable.stat_notify_sync)
      .build()
  }

  private fun channelId(): String = getString(R.string.io_mehow_quack_notification_channel_id)

  private fun notificationId(): Int = resources.getInteger(R.integer.io_mehow_quack_notification_id)
}
