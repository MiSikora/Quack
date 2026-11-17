import com.diffplug.gradle.spotless.SpotlessExtension
import com.diffplug.spotless.LineEnding

plugins {
  alias(libs.plugins.android.library) apply false
  alias(libs.plugins.kotlin.serialization) apply false
  alias(libs.plugins.sort.dependencies)
  alias(libs.plugins.spotless)
}

allprojects {
  pluginManager.withPlugin(rootProject.libs.plugins.spotless.get().pluginId) {
    configure<SpotlessExtension> {
      lineEndings = LineEnding.UNIX
      val ktfmtVersion = rootProject.libs.versions.ktfmt.get()
      kotlin {
        target("src/**/*.kt")
        trimTrailingWhitespace()
        endWithNewline()
        ktfmt(ktfmtVersion).googleStyle().configure { it.setMaxWidth(140) }
      }
      kotlinGradle {
        target("*.kts")
        trimTrailingWhitespace()
        endWithNewline()
        ktfmt(ktfmtVersion).googleStyle().configure { it.setMaxWidth(140) }
      }
    }
    pluginManager.withPlugin(rootProject.libs.plugins.sort.dependencies.get().pluginId) {
      tasks.named("spotlessCheck") { dependsOn("checkSortDependencies") }
      tasks.named("spotlessApply") { dependsOn("sortDependencies") }
      // ktfmt formats the build scripts that sortDependencies rewrites.
      tasks.named("spotlessKotlinGradle") { mustRunAfter("sortDependencies") }
    }
  }
}
