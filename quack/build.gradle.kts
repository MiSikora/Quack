import org.jetbrains.kotlin.gradle.dsl.JvmDefaultMode

plugins {
  alias(libs.plugins.android.library)
  alias(libs.plugins.maven.publish)
  alias(libs.plugins.sort.dependencies)
  alias(libs.plugins.spotless)
  alias(libs.plugins.tapmoc)
}

android {
  namespace = "io.mehow.quack"
  resourcePrefix = "io_mehow_quack_"
  compileSdk { version = release(37) }

  defaultConfig {
    minSdk = 28
    aarMetadata { minCompileSdk = 35 }
  }

  lint {
    warningsAsErrors = true
    checkTestSources = false
  }

  val protocol = rootProject.file("protocol")
  testOptions.unitTests.all {
    it.systemProperty("quack.protocol", protocol.path)
    it.inputs.dir(protocol).withPropertyName("protocol").withPathSensitivity(PathSensitivity.RELATIVE)
  }
}

tapmoc {
  java(11)
  kotlin(libs.versions.kotlin.get())
  checkDependencies()
}

mavenPublishing {
  publishToMavenCentral()
  signAllPublications()
}

kotlin {
  explicitApi()
  compilerOptions {
    allWarningsAsErrors = true
    progressiveMode = true
    jvmDefault = JvmDefaultMode.NO_COMPATIBILITY
  }
}

dependencies {
  api(libs.kotlinx.coroutines.android)

  implementation(libs.okio)

  testImplementation(libs.junit)
  testImplementation(libs.kotest.assertions)
  testImplementation(libs.kotest.assertions.json)
  testImplementation(libs.kotlinx.coroutines.test)
  testImplementation(libs.robolectric)
}
