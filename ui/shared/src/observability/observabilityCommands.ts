/**
 * The `retnd` commands the Observability card echoes (EPIC O, #899). Each is
 * the real command and the string core/cliecho prints for the same request
 * (routes.go: PATCH and POST /settings/observability...), held to it by
 * observability-card-899.test.tsx, observability-logs-900.test.tsx and
 * observability-card-1604.test.tsx. None carries a secret: booleans,
 * durations, a level, a channel, a format and a verb that prints its token on
 * standard output once. The webhook URL has no builder, because it is never a
 * value here: config.yaml names where it is kept.
 *
 * The alert builders (#1604) are held byte for byte to what core/cliecho
 * prints for the same PATCH by core/cmd/retnd's TestAlertTwinsAreTheEcho_1604,
 * over the lines cliTwins.test.ts writes to the fixture.
 *
 * LOG_LEVELS is the select's list and the input the twin fixture is written
 * from, so core/cmd/retnd's TestLogsLevelTwinIsTheSchema_900 holds the
 * levels the browser offers to the levels config.yaml accepts, and each line
 * to the one core/cliecho prints for PATCH /settings/observability.
 */
import { shellQuote } from "@shared/assurance/assuranceCommands";

/** observability.logs.level's values, in the order the select shows them. */
export const LOG_LEVELS = ["debug", "info", "warn", "error"] as const;

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

/** `retnd settings observability patch --logs-level L`. */
export function logsLevelCommand(level: string): string {
  return "retnd settings observability patch --logs-level " + shellQuote(level);
}

/** `retnd settings observability patch --alerts-delivery platform|webhook`. */
export function alertsDeliveryCommand(delivery: string): string {
  return "retnd settings observability patch --alerts-delivery " + shellQuote(delivery);
}

/** `retnd settings observability patch --webhook-dialect generic|alertmanager`. */
export function webhookDialectCommand(dialect: string): string {
  return "retnd settings observability patch --webhook-dialect " + shellQuote(dialect);
}

/** `retnd settings observability patch --webhook-allow-insecure-http=true|false`,
 *  assigned the way cliecho prints every boolean in this block. */
export function webhookAllowInsecureHttpCommand(allow: boolean): string {
  return "retnd settings observability patch --webhook-allow-insecure-http=" + String(allow);
}

/** `retnd settings observability patch --webhook-refresh-interval D`. */
export function webhookRefreshIntervalCommand(seconds: number): string {
  return "retnd settings observability patch --webhook-refresh-interval " + durationText(seconds);
}

/** `retnd settings observability patch --webhook-resolve-ttl D`. */
export function webhookResolveTtlCommand(seconds: number): string {
  return "retnd settings observability patch --webhook-resolve-ttl " + durationText(seconds);
}
