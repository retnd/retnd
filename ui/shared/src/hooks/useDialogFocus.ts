/**
 * The keyboard behaviour of the custom-validator dialogs (issue #1203): focus
 * moves into the dialog on open and back to what opened it on close, Escape
 * closes it unless it is busy submitting, and Tab stays inside it (aria-modal
 * says the page behind is inert; this makes Tab honour that).
 *
 * Other dialogs in the app do this by hand today. This hook is where one that
 * wants the same behaviour can pick it up, not a claim that they all have.
 */
import { useEffect, useRef } from "react";
import type { RefObject } from "react";

export interface DialogFocusOptions {
  /** Where focus goes on close, when that is not the element that was focused on open. */
  returnFocus?: RefObject<HTMLElement>;
  /** True while a request is in flight: Escape then does nothing, so a dialog
   *  cannot be dismissed over an upload it cannot take back. */
  busy?: boolean;
}

export function useDialogFocus(box: RefObject<HTMLElement>, onClose: () => void, options: DialogFocusOptions = {}) {
  const { returnFocus, busy = false } = options;
  // The listener below is added once. It reads the newest of these when a key
  // arrives, so a parent that passes a new closure on every render does not
  // make the window listener come and go.
  const closeRef = useRef(onClose);
  const busyRef = useRef(busy);
  useEffect(() => {
    closeRef.current = onClose;
    busyRef.current = busy;
  });

  useEffect(() => {
    const back = returnFocus?.current ?? (document.activeElement as HTMLElement | null);
    box.current?.focus();
    return () => back?.focus?.();
  }, [box, returnFocus]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        if (!busyRef.current) closeRef.current();
        return;
      }
      if (e.key !== "Tab" || !box.current) return;
      const items = [...box.current.querySelectorAll<HTMLElement>("button:not(:disabled), input:not(:disabled), a[href], select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex='-1'])")];
      if (items.length === 0) return;
      const first = items[0], last = items[items.length - 1];
      const at = document.activeElement;
      if (e.shiftKey && (at === first || at === box.current)) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && at === last) { e.preventDefault(); first.focus(); }
      else if (!box.current.contains(at)) { e.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [box]);
}
