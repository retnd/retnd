/**
 * Durations in the one spelling the cadence fields take (issue #985): a whole
 * number and a unit of s, m, h or d. The server stores seconds and refuses
 * anything under an hour, so this only translates and the floor is checked
 * where the person typed it.
 */
export const MIN_CADENCE_SECONDS = 3600;
const UNIT: Record<string, number> = { s: 1, m: 60, h: 3600, d: 86_400 };

/** Seconds for "12h" or "7d", or null when it is not that shape. */
export function parseCadence(text: string): number | null {
  const m = /^\s*(\d+)\s*([smhd])\s*$/i.exec(text);
  if (!m) return null;
  const n = Number(m[1]) * UNIT[m[2].toLowerCase()];
  return n > 0 ? n : null;
}

/** The largest unit that divides the seconds exactly, so what is shown parses
 *  back to the same number. */
export function formatCadence(seconds: number): string {
  if (seconds <= 0) return "";
  if (seconds % UNIT.d === 0) return seconds / UNIT.d + "d";
  if (seconds % UNIT.h === 0) return seconds / UNIT.h + "h";
  if (seconds % UNIT.m === 0) return seconds / UNIT.m + "m";
  return seconds + "s";
}

/** Why a typed cadence cannot be saved, or null when it can. */
export function cadenceProblem(text: string): string | null {
  const s = parseCadence(text);
  if (s === null) return "Use a number and a unit, like 12h or 7d.";
  if (s < MIN_CADENCE_SECONDS) return "The shortest cadence is 1h.";
  return null;
}
