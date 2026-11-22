import com.diffplug.gradle.spotless.SpotlessExtension
import com.diffplug.spotless.LineEnding
import org.jetbrains.kotlin.gradle.plugin.getKotlinPluginVersion

plugins {
  alias(libs.plugins.android.library) apply false
  alias(libs.plugins.kotlin.serialization) apply false
  alias(libs.plugins.maven.publish) apply false
  alias(libs.plugins.sort.dependencies)
  alias(libs.plugins.spotless)
  alias(libs.plugins.tapmoc) apply false
}

// AGP's built-in Kotlin compiles with the newer of its own Kotlin and the catalog's, so a catalog version behind AGP's is ignored silently.
check(getKotlinPluginVersion() == libs.versions.kotlin.get()) {
  "The build compiles with Kotlin ${getKotlinPluginVersion()}. Set kotlin to it in gradle/libs.versions.toml."
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
