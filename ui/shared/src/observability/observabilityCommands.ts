/**
 * The `retnd` commands the Observability card echoes (EPIC O, #899). Each is
 * the real command and the string core/cliecho prints for the same request
 * (routes.go: PATCH and POST /settings/observability...), held to it by
 * observabilityCommands.899.test.ts. None carries a secret: a boolean, a
 * duration and a verb that prints its token on standard output once.
 */

/** `retnd settings observability patch --metrics-enabled=true|false`. */
export function metricsEnabledCommand(enabled: boolean): string {
  return "retnd settings observability patch --metrics-enabled=" + String(enabled);
}

/** Seconds as an operator types a duration: 90 is 1m30s, 30 is 30s, 3600 is 1h. */
function durationText(totalSeconds: number): string {
  if (totalSeconds === 0) return "0s";
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  return (h > 0 ? h + "h" : "") + (m > 0 ? m + "m" : "") + (s > 0 ? s + "s" : "");
}

/** `retnd settings observability patch --metrics-min-rebuild-interval D`. */
export function metricsIntervalCommand(seconds: number): string {
  return "retnd settings observability patch --metrics-min-rebuild-interval " + durationText(seconds);
}

/** `retnd settings observability rotate-scrape-token`. */
export function mintScrapeTokenCommand(): string {
  return "retnd settings observability rotate-scrape-token";
}
