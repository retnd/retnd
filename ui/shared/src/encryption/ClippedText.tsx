/**
 * The 80-character clip (EPIC T, issue #1318; design
 * docs/design/1318-suite-agility-ui.html, section 6).
 *
 * A post-quantum recipient is about 2000 characters. No suite surface draws a
 * recipient, and the client drops one from every answer it maps, so this is
 * the second line: every runtime string the suite surfaces print (an id, a
 * sentence quoted from the engine) passes through here, and anything longer
 * than 80 characters is drawn as its first 80 and an ellipsis, with a Copy
 * button that puts the whole value on the clipboard and nowhere in the page.
 * Not in an attribute, not in a title, not in an aria-label: the whole value
 * lives in this component's props for as long as the row is drawn, and the
 * DOM only ever holds the clipped form.
 */
import { useState } from "react";

export const CLIP_AT = 80;

/** The value as a surface may print it: whole up to 80 characters, else the
 *  first 80 and an ellipsis. For aria-labels and other attribute text too. */
export function clipText(value: string): string {
  return value.length <= CLIP_AT ? value : value.slice(0, CLIP_AT) + "…";
}

export function ClippedText({ value, mono = false }: { value: string; mono?: boolean }) {
  const [copied, setCopied] = useState(false);
  if (value.length <= CLIP_AT) return <span className={mono ? "mono" : undefined}>{value}</span>;
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
    } catch {
      // A browser that refuses the clipboard leaves the clipped text as it is;
      // the whole value is never drawn as a fallback.
      setCopied(false);
    }
  };
  return (
    <span className="eclip" data-clipped="">
      <span className={mono ? "mono" : undefined}>{clipText(value)}</span>
      <button type="button" className="btn btn--sm btn--quiet" onClick={() => void copy()}>
        Copy
      </button>
      {copied ? <span role="status" className="eclip__note">Copied</span> : null}
    </span>
  );
}
