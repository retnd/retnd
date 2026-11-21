import { useEffect, useRef } from "react";

/**
 * Issue #1646: a pop-up the pointer or the keyboard opened on a button stayed
 * up over the dialog that same button opened. Nothing told it to go: the
 * pointer is still resting on the host (the scrim that now covers it fires no
 * mouseleave until the mouse moves), and a keyboard open has no event at all
 * once the dialog takes focus in a later frame.
 *
 * So a modal dialog arriving closes every pop-up that is up. This is done at
 * the DOM rather than in each dialog on purpose: there is no one dialog
 * primitive (ConfirmationDialog, MintDialog, RetireDialog, the recovery and
 * validator dialogs, the SSH wizard each render their own `role="dialog"`),
 * and the tooltip layer is the one place every one of them already goes
 * through. A MutationObserver on `document.body` sees a dialog however it was
 * mounted, in place or portalled, with no opt-in and no edit to a dialog file.
 * It only runs while a pop-up is showing, so with none up it costs nothing.
 *
 * The other half is `behindModal`: a host asks it before opening, so focus or
 * the pointer landing on a control a dialog covers (the Retire dialog hands
 * focus back to its opener on a timer whenever focus has fallen to the body)
 * opens nothing. Without it the observer alone would close the pop-up and the
 * next focus event would bring it straight back.
 *
 * A dialog that CONTAINS the host (a field help inside the dialog that has just
 * opened) leaves that pop-up alone: the dialog is not covering it.
 */
export const MODAL_DIALOG = '[role="dialog"][aria-modal="true"], [role="alertdialog"], dialog[open]';

/** Whether a modal dialog is open that does not contain `host`: the host is
 *  covered, and nobody can be asking it for help. */
export function behindModal(host: Element | null): boolean {
  if (typeof document === "undefined") return false;
  return Array.from(document.querySelectorAll(MODAL_DIALOG)).some((dialog) => !host || !dialog.contains(host));
}

export function useCloseOnModal(active: boolean, close: () => void, host: () => Element | null) {
  // Read through refs so the observer is made once per showing, not once per render.
  const closeRef = useRef(close);
  const hostRef = useRef(host);
  useEffect(() => {
    closeRef.current = close;
    hostRef.current = host;
  });

  useEffect(() => {
    if (!active || typeof MutationObserver === "undefined") return;
    const watch = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of Array.from(record.addedNodes)) {
          if (!(node instanceof Element)) continue;
          const dialog = node.matches(MODAL_DIALOG) ? node : node.querySelector(MODAL_DIALOG);
          if (!dialog) continue;
          const here = hostRef.current();
          if (here && dialog.contains(here)) continue;
          closeRef.current();
          return;
        }
      }
    });
    watch.observe(document.body, { childList: true, subtree: true });
    return () => watch.disconnect();
  }, [active]);
}
