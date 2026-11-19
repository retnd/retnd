/**
 * Durations in the one spelling the cadence fields take (issue #985): a whole
 * number and a unit of s, m, h or d. The server stores seconds and refuses a
 * cadence under a floor that differs by claim (an hour for a scrub, a day for
 * a recovery test), so this only translates and the floor is checked where
 * the person typed it. The floors repeat config.MinScrubCadence and
 * config.MinRecoveryTestCadence; a test reads them out of the Go source so
 * they cannot drift apart.
 */
export const MIN_CADENCE_SECONDS = 3600;
export const MIN_RECOVERY_TEST_CADENCE_SECONDS = 86_400;
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

/** A floor in hours when it is a whole number of them, because "24h" reads
 *  better than "1d" in a sentence about the shortest allowed cadence. */
export function formatFloor(seconds: number): string {
  return seconds % UNIT.h === 0 ? seconds / UNIT.h + "h" : formatCadence(seconds);
}

/** Why a typed cadence cannot be saved, or null when it can. */
export function cadenceProblem(text: string, minSeconds: number = MIN_CADENCE_SECONDS): string | null {
  const s = parseCadence(text);
  if (s === null) return "Use a number and a unit, like 12h or 7d.";
  if (s < minSeconds) return `The shortest cadence is ${formatFloor(minSeconds)}.`;
  return null;
}
