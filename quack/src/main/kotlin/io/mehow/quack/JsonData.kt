package io.mehow.quack

import android.util.JsonReader
import android.util.JsonToken
import java.io.IOException
import java.io.StringReader

/** A single validated JSON value, compacted without changing strings or number literals. */
internal class JsonData private constructor(val encoded: String) {
  companion object {
    fun parse(text: String): JsonData? {
      val compact = StringBuilder(text.length)
      val structure = StringBuilder(text.length + 2).append('[')
      var at = 0
      while (at < text.length) {
        val start = at
        when (val c = text[at]) {
          ' ',
          '\t',
          '\r',
          '\n' -> {
            at++
            continue
          }
          '"' -> at = stringEnd(text, at) ?: return null
          '{',
          '}',
          '[',
          ']',
          ',',
          ':' -> at++
          't',
          'f',
          'n' -> {
            val literal =
              when (c) {
                't' -> "true"
                'f' -> "false"
                else -> "null"
              }
            if (!text.startsWith(literal, at)) return null
            at += literal.length
          }
          '-',
          in '0'..'9' -> {
            at = numberEnd(text, at) ?: return null
          }
          else -> return null
        }
        compact.append(text, start, at)
        // JsonReader validates nesting and separators. Number grammar was checked above;
        // a placeholder avoids its token-length limit and any conversion of numeric data.
        if (text[start] == '-' || text[start] in '0'..'9') structure.append('0') else structure.append(text, start, at)
      }
      structure.append(']')
      try {
        JsonReader(StringReader(structure.toString())).use { reader ->
          reader.beginArray()
          reader.skipValue()
          reader.endArray()
          if (reader.peek() != JsonToken.END_DOCUMENT) return null
        }
      } catch (_: IOException) {
        return null
      } catch (_: IllegalStateException) {
        return null
      } catch (_: NumberFormatException) {
        return null
      }
      return JsonData(compact.toString())
    }

    // Android's JSON readers accept unknown escapes and literal control characters.
    // Check quoted tokens explicitly rather than enabling those extensions on the wire.
    private fun stringEnd(text: String, start: Int): Int? {
      var at = start + 1
      while (at < text.length) {
        when (val c = text[at++]) {
          '"' -> return at
          '\\' -> {
            if (at == text.length) return null
            when (text[at++]) {
              '"',
              '\\',
              '/',
              'b',
              'f',
              'n',
              'r',
              't' -> Unit
              'u' -> {
                if (
                  at + 4 > text.length || (at until at + 4).any { text[it] !in '0'..'9' && text[it] !in 'a'..'f' && text[it] !in 'A'..'F' }
                )
                  return null
                at += 4
              }
              else -> return null
            }
          }
          else -> if (c < ' ') return null
        }
      }
      return null
    }

    private fun numberEnd(text: String, start: Int): Int? {
      var at = start
      if (text[at] == '-') at++
      when (text.getOrNull(at)) {
        '0' -> at++
        in '1'..'9' -> while (text.getOrNull(at) in '0'..'9') at++
        else -> return null
      }
      if (text.getOrNull(at) == '.') {
        at++
        if (text.getOrNull(at) !in '0'..'9') return null
        while (text.getOrNull(at) in '0'..'9') at++
      }
      if (text.getOrNull(at) == 'e' || text.getOrNull(at) == 'E') {
        at++
        if (text.getOrNull(at) == '+' || text.getOrNull(at) == '-') at++
        if (text.getOrNull(at) !in '0'..'9') return null
        while (text.getOrNull(at) in '0'..'9') at++
      }
      return at
    }
  }
}
