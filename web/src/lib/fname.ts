const bidi = new Set([0x061c, 0x200e, 0x200f, 0xfeff])

/**
 * Makes an escaped server name safe to read: control characters become visible
 * symbols and bidi/format controls become \u{HEX}. The result is still plain
 * text; the renderer must never use it as HTML (security invariant 8).
 */
export function displayName(escaped: string): string {
  let out = ''
  for (const ch of escaped) {
    const c = ch.codePointAt(0)!
    if (c < 0x20) out += String.fromCodePoint(0x2400 + c)
    else if (c === 0x7f) out += '␡'
    else if ((c >= 0x80 && c <= 0x9f) || bidi.has(c) || (c >= 0x202a && c <= 0x202e) || (c >= 0x2066 && c <= 0x2069))
      out += `\\u{${c.toString(16).toUpperCase()}}`
    else out += ch
  }
  return out
}
