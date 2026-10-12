/**
 * retnd#1049: a surface that says "retnd could not be reached" should not
 * need a person to notice that it can be reached again.
 *
 * serve-ui proxies /health/* to the engine unchanged, so a GET of
 * /health/live from the browser asks the one question that matters here and
 * is answered by the engine itself: 200 when the service is up, and the
 * gateway's own 502/503/504 (or no response at all) when it is not. It needs
 * no session, so it works on the page that replaces the sign-in form too.
 *
 * Polling stops the moment it hears 200 and hands control back through
 * `onRecovered`; the caller does whatever "try again" means for it, which is
 * the same thing its Try again button does. If that retry fails again the
 * caller's `rearmKey` changes (a new error, a new "tried again at" time) and
 * the poll starts over, so a service that answers /health/live while the
 * read still fails is not hammered in a tight loop.
 */
import { useEffect, useRef } from "react";

/** Between two asks. Long enough that a stopped service is not hammered,
 *  short enough that a restart (seconds on a NAS) is noticed promptly. */
export const SERVICE_POLL_MS = 3_000;

export const LIVENESS_PATH = "/health/live";

export function useServiceRecovery(
  active: boolean,
  onRecovered: () => void,
  rearmKey?: unknown
): void {
  const latest = useRef(onRecovered);
  useEffect(() => {
    latest.current = onRecovered;
  });

  useEffect(() => {
    if (!active || typeof fetch !== "function") return undefined;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const ask = async () => {
      let alive: boolean;
      try {
        const res = await fetch(LIVENESS_PATH, { cache: "no-store", credentials: "same-origin" });
        alive = res.ok;
      } catch {
        // Nothing arrived: still unreachable.
        alive = false;
      }
      if (stopped) return;
      if (alive) {
        stopped = true;
        latest.current();
        return;
      }
      timer = setTimeout(() => void ask(), SERVICE_POLL_MS);
    };

    timer = setTimeout(() => void ask(), SERVICE_POLL_MS);
    return () => {
      stopped = true;
      if (timer !== undefined) clearTimeout(timer);
    };
  }, [active, rearmKey]);
}
