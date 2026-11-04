package io.mehow.quack.android

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

// Does nothing on purpose. Delivering the CLI's wake broadcast starts the app's process, and Application.onCreate then starts the
// server. The CLI also finds quack-enabled apps by listing the receivers of that broadcast.
internal class QuackWakeReceiver : BroadcastReceiver() {
  override fun onReceive(context: Context, intent: Intent) = Unit
}
