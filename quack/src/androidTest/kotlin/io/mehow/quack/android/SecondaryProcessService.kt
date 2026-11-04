package io.mehow.quack.android

import android.app.Service
import android.content.Intent
import android.os.Binder
import android.os.IBinder

class SecondaryProcessService : Service() {
  private lateinit var server: QuackServer

  override fun onCreate() {
    super.onCreate()
    server = QuackServer.Builder(this).build()
    server.start()
  }

  override fun onBind(intent: Intent?): IBinder = Binder()

  override fun onDestroy() {
    server.stop()
    super.onDestroy()
  }
}
