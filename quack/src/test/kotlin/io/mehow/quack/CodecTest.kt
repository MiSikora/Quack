package io.mehow.quack

import io.kotest.assertions.json.shouldEqualJson
import io.kotest.matchers.shouldBe
import io.kotest.matchers.types.shouldBeInstanceOf
import org.json.JSONObject
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [28, 35], manifest = Config.NONE)
class CodecTest {
  private val codec = Codec()

  @Test
  fun `reject incomplete JSON and trailing values`() {
    for (text in listOf("", " ", "{", "[", "\"unterminated", "{\"a\":}", "[1", "{} {}", "[] false", "1 2")) {
      codec.parseJson(text) shouldBe null
    }
  }

  @Test
  fun `accept platform JSON syntax and emit ordinary JSON`() {
    val data = checkNotNull(codec.parseJson("/* comment */ {unquoted: 'value', items: [1; 2]}"))
    codec.encode(Frame.Result.Message("done", data)) shouldEqualJson
      """{"result":{"message":"done","data":{"unquoted":"value","items":[1,2]}}}"""
  }

  @Test
  fun `encode objects arrays and primitives in one frame`() {
    for (text in
      listOf(
        "null",
        "true",
        "false",
        "[]",
        "\"hello \\n world\"",
        "[null,true,\"\",\" spaced \"]",
        "{\n\"id\":9007199254740993,\"ratio\":0.5\n}",
      )) {
      val data = checkNotNull(codec.parseJson(text))
      val result = codec.encode(Frame.Result.Message("done", data))
      result shouldEqualJson "{\"result\":{\"message\":\"done\",\"data\":$text}}"
      result.contains('\n') shouldBe false
      codec.encode(Frame.Notification("done", data)) shouldEqualJson "{\"notification\":{\"message\":\"done\",\"data\":$text}}"
    }
  }

  @Test
  fun `use platform number types without converting ordinary longs to doubles`() {
    val parsed = checkNotNull(codec.parseJson("""{"id":9007199254740993,"n":0.1234567890123456789012345}"""))
    val data = JSONObject(codec.encode(Frame.Result.Message("done", parsed))).getJSONObject("result").getJSONObject("data")
    data.get("id") shouldBe 9007199254740993L
    data.get("n") shouldBe 0.12345678901234568
  }

  @Test
  fun `absent data and explicit JSON null remain distinct`() {
    JSONObject(codec.encode(Frame.Result.Message("done", null))).getJSONObject("result").has("data") shouldBe false
    val explicit = JSONObject(codec.encode(Frame.Result.Message("done", codec.parseJson("null")))).getJSONObject("result")
    explicit.has("data") shouldBe true
    explicit.get("data") shouldBe JSONObject.NULL
  }

  @Test
  fun `use defaults only when request fields are missing`() {
    codec.decodeRequest("""{"method":"describe"}""") shouldBe Request.Describe
    codec.decodeRequest("""{"method":"call"}""") shouldBe Request.Call("", emptyMap())
  }

  @Test
  fun `request fields never coerce primitives to strings`() {
    for (request in
      listOf(
        """{"method":1}""",
        """{"method":null}""",
        """{"method":"describe","action":2}""",
        """{"method":"call","args":null}""",
        """{"method":"call","args":[]}""",
        """{"method":"call","args":{"x":3}}""",
        """{"method":"call","args":{"x":true}}""",
        """{"method":"call","args":{"x":null}}""",
        """{"method":"call","args":{"x":["a",null]}}""",
      )) codec.decodeRequest(request).shouldBeInstanceOf<Request.Malformed>()
  }

  @Test
  fun `ignore extra fields but retain string and list argument values`() {
    codec.decodeRequest(
      """{"method":"call","action":"a","extra":{"nested":[1]},"args":{"empty":"","tags":[""," spaced ","a,b"]}}""",
    ) shouldBe Request.Call("a", mapOf("empty" to RawArg.Single(""), "tags" to RawArg.Repeated(listOf("", " spaced ", "a,b"))))
    codec.decodeRequest("""{"method":"call","action":"a","args":{"tags":[]}}""") shouldBe
      Request.Call("a", mapOf("tags" to RawArg.Repeated(emptyList())))
  }
}
