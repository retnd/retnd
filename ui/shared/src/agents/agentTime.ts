/**
 * Display-only time words for the Agents card (design 1350, sections 5 and 6).
 *
 * Nothing here decides anything: the dates are the engine's, the browser only
 * says how far they are from its own clock, and no control is enabled or
 * disabled by the answer. The engine is the judge of expiry, because a
 * browser's clock can be wrong.
 */
import { useEffect, useState } from "react";

/** "in 76 days" or "49 days ago", the nearest whole unit. */
export function relativeTime(iso: string, now: number): string {
  const ms = Date.parse(iso) - now;
  const abs = Math.abs(ms);
  const day = 24 * 3600_000;
  const [n, unit] = abs >= day ? [Math.round(abs / day), "day"] : abs >= 3600_000 ? [Math.round(abs / 3600_000), "hour"] : [Math.max(1, Math.round(abs / 60_000)), "minute"];
  const words = n + " " + unit + (n === 1 ? "" : "s");
  return ms >= 0 ? "in " + words : words + " ago";
}

/** "0:41" or "14:03": minutes and seconds to a time, never negative. */
export function countdown(iso: string, now: number): string {
  const s = Math.max(0, Math.floor((Date.parse(iso) - now) / 1000));
  return Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0");
}

/** The browser's clock, re-read every `everyMs` while `active`. */
export function useNow(active: boolean, everyMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), everyMs);
    return () => clearInterval(t);
  }, [active, everyMs]);
  return now;
}
