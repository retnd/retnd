/** Replaces every control character and bidi override with its visible
 *  escape, so untrusted terminal text can be shown and cannot act. */
export function inert(text: string): string {
  let out = "";
  for (const ch of text) {
    const c = ch.codePointAt(0)!;
    const control = (c < 0x20 && c !== 0x0a && c !== 0x09) || c === 0x7f || (c >= 0x80 && c <= 0x9f);
    const bidi = (c >= 0x200b && c <= 0x200f) || (c >= 0x202a && c <= 0x202e) || (c >= 0x2066 && c <= 0x2069);
    if (control) out += "\\x" + c.toString(16).padStart(2, "0");
    else if (bidi) out += "\\u" + c.toString(16).padStart(4, "0");
    else out += ch;
  }
  return out;
}

