plugins {
  alias(libs.plugins.android.library)
  alias(libs.plugins.kotlin.serialization)
}

android {
  namespace = "io.mehow.quack"
  resourcePrefix = "io_mehow_quack_"
  compileSdk { version = release(37) }

  defaultConfig {
    minSdk = 28
  }
}

dependencies {
  implementation(libs.kotlinx.coroutines.android)
  implementation(libs.kotlinx.serialization.json)
}
