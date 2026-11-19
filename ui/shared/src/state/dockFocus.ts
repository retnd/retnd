/**
 * "Show in Terminal" (#1505): a set's activity on the Dashboard asks the
 * docked terminal to open with that set's chip selected, which is what
 * pressing Show terminal and then the set's chip does by hand.
 *
 * An event rather than shared state because the strip and the dock never
 * render in the same tree position and neither owns the other's open flag or
 * filter. The dock listens once and does the same two things its own
 * controls do.
 */
export const DOCK_FOCUS_EVENT = "retnd:dock-focus";

export function showSetInTerminal(setId: string): void {
  window.dispatchEvent(new CustomEvent<{ setId: string }>(DOCK_FOCUS_EVENT, { detail: { setId } }));
}
