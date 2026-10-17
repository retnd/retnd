/**
 * What every modal in the assurance surfaces does with the keyboard, in one
 * place: focus moves into the dialog on open and back to what opened it on
 * close, Escape closes it, and Tab stays inside it (aria-modal says the page
 * behind is inert; this makes Tab honour that).
 */
import { useEffect } from "react";
import type { RefObject } from "react";

export function useDialogFocus(box: RefObject<HTMLElement>, onClose: () => void, returnFocus?: RefObject<HTMLElement>) {
  useEffect(() => {
    const back = returnFocus?.current ?? (document.activeElement as HTMLElement | null);
    box.current?.focus();
    return () => back?.focus?.();
  }, [box, returnFocus]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { onClose(); return; }
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
  }, [box, onClose]);
}
