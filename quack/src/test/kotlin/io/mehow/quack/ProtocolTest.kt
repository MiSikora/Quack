package io.mehow.quack

import io.kotest.assertions.json.shouldEqualJson
import io.kotest.matchers.shouldBe
import io.mehow.quack.QuackAction.Param
import java.io.File
import org.junit.Test

class ProtocolTest {
  private val codec = Codec()

  @Test
  fun `encode hello`() {
    codec.encode(Frame.Hello(packageName = "io.mehow.quack.sample", version = "1.0")) shouldEqualJson golden("frames/hello.json")
  }

  @Test
  fun `encode hello without a version`() {
    codec.encode(Frame.Hello(packageName = "io.mehow.quack.sample", version = null)) shouldEqualJson
      golden("frames/hello-without-version.json")
  }

  @Test
  fun `encode a notification`() {
    codec.encode(Frame.Notification("Started", data = null)) shouldEqualJson golden("frames/notification.json")
  }

  @Test
  fun `encode a notification with data`() {
    val data = codec.parseJson("""{"code": "abc123"}""")

    codec.encode(Frame.Notification("Pairing code ready", data)) shouldEqualJson golden("frames/notification-with-data.json")
  }

  @Test
  fun `encode a message result`() {
    codec.encode(Frame.Result.Message("Hello, Bob!", data = null)) shouldEqualJson golden("frames/message-result.json")
  }

  @Test
  fun `encode a message result with data`() {
    val data = codec.parseJson("""{"username": "alice", "tags": ["a", "b"], "verified": true, "age": 7}""")

    codec.encode(Frame.Result.Message("Signed in as alice", data)) shouldEqualJson golden("frames/message-result-with-data.json")
  }

  @Test
  fun `encode an error`() {
    codec.encode(Frame.Error(ErrorCode.InvalidArgs, "missing argument 'name'")) shouldEqualJson golden("frames/error.json")
  }

  @Test
  fun `encode an actions result with every param type, presence and hints`() {
    val greet =
      FakeAction(
        id = "app.greet",
        description = "Say hello",
        params =
          listOf(
            Param.string("name", description = "Who to greet").hints(listOf("Alice", "Bob")).env("QUACK_NAME").required(),
            Param.int("times").withDefault(1).env("QUACK_TIMES"),
          ),
      )
    val params =
      FakeAction(
        id = "app.params",
        description = "",
        params =
          listOf(
            Param.string("text"),
            Param.long("size").withDefault(5_000_000_000),
            Param.double("ratio").withDefault(0.5),
            Param.boolean("flag").withDefault(false),
            Param.enum<Theme>("theme").withDefault(Theme.Dark),
            Param.stringList("tag").withDefault(listOf("a", "b")),
          ),
      )
    val delete = FakeAction(id = "account.delete", description = "Delete the account")

    codec.encode(Frame.Result.Actions(listOf(greet, params, delete))) shouldEqualJson golden("frames/actions-result.json")
  }

  @Test
  fun `decode a describe request`() {
    codec.decodeRequest(golden("requests/describe.json")) shouldBe Request.Describe
  }

  @Test
  fun `decode a call request`() {
    codec.decodeRequest(golden("requests/call.json")) shouldBe
      Request.Call(
        action = "app.params",
        args = mapOf("text" to RawArg.Single("hi"), "tag" to RawArg.Repeated(listOf("a", "b"))),
      )
  }

  private enum class Theme {
    Light,
    Dark,
  }
}

private fun golden(path: String): String = File(System.getProperty("quack.protocol"), path).readText()
