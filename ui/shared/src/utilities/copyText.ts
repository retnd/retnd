/**
 * Copy text to the clipboard on any origin, and say honestly what happened
 * (retnd#1049).
 *
 * Every NAS deployment is served from a plain-http LAN address, and in
 * Chromium and Firefox such an origin has no `navigator.clipboard` at all.
 * A button whose entire job is getting a diagnostic off the screen cannot
 * stop there, so this is a chain:
 *
 *  1. When the browser offers the async clipboard (secure contexts only). "copied" on success.
 *  2. Otherwise, or if that throws, a hidden read-only textarea selected
 *     and copied with `document.execCommand("copy")`. That call needs the
 *     user gesture, so on an insecure origin it runs before the first
 *     await. "copied" only when it reports true.
 *  3. Otherwise, when the caller passed a node that shows the text, that
 *     node's contents are selected with a Range so Ctrl+C or Cmd+C finishes
 *     the job. "selected": nothing was copied yet, and the caller must say
 *     so rather than claim success.
 *
 * "failed" means even the selection was not possible.
 */
export type CopyOutcome = "copied" | "selected" | "failed";

function execCommandCopy(text: string): boolean {
  if (typeof document === "undefined" || typeof document.execCommand !== "function") return false;
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.setAttribute("aria-hidden", "true");
  area.tabIndex = -1;
  // Off-screen rather than display:none, which cannot be selected. Fixed so
  // that appending it does not scroll the page.
  area.style.position = "fixed";
  area.style.top = "0";
  area.style.left = "-9999px";
  area.style.opacity = "0";
  const previouslyFocused = document.activeElement as HTMLElement | null;
  document.body.appendChild(area);
  try {
    area.focus();
    area.select();
    area.setSelectionRange(0, text.length);
    return document.execCommand("copy") === true;
  } catch {
    return false;
  } finally {
    document.body.removeChild(area);
    try {
      previouslyFocused?.focus?.();
    } catch {
      /* focus restore is a courtesy */
    }
  }
}

function selectNode(node: Element | null | undefined): boolean {
  if (!node || typeof window === "undefined" || typeof document === "undefined") return false;
  try {
    const selection = window.getSelection?.();
    if (!selection) return false;
    const range = document.createRange();
    range.selectNodeContents(node);
    selection.removeAllRanges();
    selection.addRange(range);
    return true;
  } catch {
    return false;
  }
}

export async function copyText(text: string, fallbackNode?: Element | null): Promise<CopyOutcome> {
  // Presence is the test. A browser only defines navigator.clipboard in a
  // secure context, so on a plain-http origin this is false without this
  // function having to ask (and a stub in a test is honoured the same way).
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return "copied";
    } catch {
      /* fall through to the next link in the chain */
    }
  }
  if (execCommandCopy(text)) return "copied";
  if (selectNode(fallbackNode)) return "selected";
  return "failed";
}
