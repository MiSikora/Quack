package io.mehow.quack

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
  fun `reject platform JSON extensions and malformed values`() {
    for (text in
      listOf(
        "",
        " ",
        "{} {}",
        "null true",
        "1 2",
        "[1 2]",
        "[1,,2]",
        "\"\\u١٢٣٤\"",
        "[01]",
        "[1,]",
        "{\"a\":1,}",
        "{unquoted:1}",
        "{'single':'quotes'}",
        "/*comment*/{}",
        "{\"a\"=1}",
        "[1;2]",
        "[NaN]",
        "[Infinity]",
        "[TRUE]",
        "[NULL]",
        "[+1]",
        "[.5]",
        "[1.]",
        "[1e]",
        "[-]",
        "[0x10]",
        "\"\\q\"",
        "\"\\uZZZZ\"",
        "\"\\u123\"",
        "\"literal\nnewline\"",
      )) codec.parseJson(text) shouldBe null
  }

  @Test
  fun `preserve arbitrary JSON values and exact numbers inside one frame`() {
    for ((input, compact) in
      listOf(
        "null" to "null",
        " true " to "true",
        "false" to "false",
        "[]" to "[]",
        "\"hello \\n world\"" to "\"hello \\n world\"",
        "[ null, true, false, \"\", \" spaced \" ]" to "[null,true,false,\"\",\" spaced \"]",
        "{\n\"id\":9223372036854775807,\n\"n\":0.1234567890123456789012345,\"huge\":1e9999\n}" to
          "{\"id\":9223372036854775807,\"n\":0.1234567890123456789012345,\"huge\":1e9999}",
        "-0.25E+100" to "-0.25E+100",
      )) {
      val data = checkNotNull(codec.parseJson(input))
      codec.encode(Frame.Result.Message("done", data)) shouldBe "{\"result\":{\"message\":\"done\",\"data\":$compact}}"
      codec.encode(Frame.Notification("done", data)) shouldBe "{\"notification\":{\"message\":\"done\",\"data\":$compact}}"
    }
  }

  @Test
  fun `absent data and explicit JSON null remain distinct`() {
    JSONObject(codec.encode(Frame.Result.Message("done", null))).getJSONObject("result").has("data") shouldBe false
    val explicit = JSONObject(codec.encode(Frame.Result.Message("done", codec.parseJson("null")))).getJSONObject("result")
    explicit.has("data") shouldBe true
    explicit.get("data") shouldBe JSONObject.NULL
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

  @Test
  fun `long string and number tokens do not depend on parser buffer size`() {
    for (text in listOf("\"" + "x".repeat(32000) + "\"", "9".repeat(4000))) {
      codec.parseJson(text)?.encoded shouldBe text
    }
  }
}
