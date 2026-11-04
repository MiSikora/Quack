plugins {
  alias(libs.plugins.android.library)
  alias(libs.plugins.kotlin.serialization)
  alias(libs.plugins.spotless)
}

android {
  namespace = "io.mehow.quack"
  resourcePrefix = "io_mehow_quack_"
  compileSdk { version = release(37) }

  defaultConfig {
    minSdk = 28
  }

  val protocol = rootProject.file("protocol")
  testOptions.unitTests.all {
    it.systemProperty("quack.protocol", protocol.path)
    it.inputs.dir(protocol).withPropertyName("protocol").withPathSensitivity(PathSensitivity.RELATIVE)
  }
}

dependencies {
  implementation(libs.kotlinx.coroutines.android)
  implementation(libs.kotlinx.serialization.json)
  implementation(libs.okio)

  testImplementation(libs.junit)
  testImplementation(libs.kotest.assertions)
  testImplementation(libs.kotest.assertions.json)
  testImplementation(libs.kotlinx.coroutines.test)
}
