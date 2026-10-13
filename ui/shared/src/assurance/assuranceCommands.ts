/**
 * The `retnd` command each assurance control is equivalent to, in ONE place
 * (issue #985, checklist section 3: no web-only behaviour).
 *
 * Every verb and flag here is one the CLI has today (core/cmd/retnd), and the
 * spelling is pinned by assurance.test.tsx against the usage text. CommandEcho
 * prints exactly what is returned, so nothing on screen can disagree with it.
 *
 * Every control has a twin: Scrub now is `retnd scrub`, Run recovery test is
 * `retnd recovery-test`, Cancel is `retnd assurance cancel` (which, like the
 * route, needs the serving process, so it is reached over RETND_API_URL), and
 * Show validator output is `retnd recovery-test --show-output`.
 */
import type { RecoveryTargetSpec } from "@shared/api/contracts";

/** `<source>/<set>`, the same id the web uses for a backup set. */
export function scrubCommand(setId: string): string {
  return "retnd scrub " + setId;
}

/** `retnd recovery-test <source/backup-set>`: the same test the Run recovery
 *  test control starts, with no cost acknowledged unless the flag says so. */
export function recoveryTestCommand(setId: string): string {
  return "retnd recovery-test " + setId;
}

/** What Cancel does: `retnd assurance cancel <operation-id>`, the same call
 *  as POST /assurance/operations/{op}/cancel. The id is the one the scrub or
 *  recovery test returned when it started. */
export function assuranceCancelCommand(operationId: string): string {
  return "retnd assurance cancel " + operationId;
}

/** What Show validator output does: print the newest test's stored output,
 *  the body GET .../recovery-test/output serves. It runs no test. */
export function showOutputCommand(setId: string): string {
  return "retnd recovery-test " + setId + " --show-output";
}

/** A read of every copy's bytes on a storage medium costs egress, so the CLI
 *  makes it two flags rather than one. */
export function scrubContentCommand(setId: string): string {
  return "retnd scrub " + setId + " --content --allow-egress";
}

/**
 * `retnd recover` for the target a dialog has chosen. A browser download has
 * no terminal spelling of its own: the terminal equivalent writes the bytes to
 * standard output, so it is shown redirected into a file.
 */
export function recoverCommand(artifactId: string, target: RecoveryTargetSpec["kind"], where: string, acknowledgeCost: boolean): string {
  const to =
    target === "download" ? "--to - > recovered-file"
    : target === "staging" ? "--to " + (where || "<name>")
    : "--to medium:" + (where || "<id>");
  // The flag goes before a shell redirection, never after it.
  const ack = acknowledgeCost ? " --acknowledge-cost" : "";
  return target === "download"
    ? "retnd recover " + artifactId + ack + " " + to
    : "retnd recover " + artifactId + " " + to + ack;
}

/**
 * `retnd recover --set` for the choices the Recover this backup set dialog has
 * made (#1106). The flags are the ones the CLI has: --newest-only, --since,
 * --to NAME or medium:ID and --acknowledge-cost, in that order.
 */
export function recoverSetCommand(setId: string, narrow: "all" | "newest" | "since", since: string, target: "staging" | "storage", where: string, acknowledgeCost: boolean): string {
  const parts = ["retnd recover --set " + setId];
  if (narrow === "newest") parts.push("--newest-only");
  if (narrow === "since") parts.push("--since " + (since || "<date>"));
  parts.push(target === "staging" ? "--to " + (where || "<name>") : "--to medium:" + (where || "<id>"));
  if (acknowledgeCost) parts.push("--acknowledge-cost");
  return parts.join(" ");
}

/** The same pre-flight on its own: nothing is read and nothing is written. */
export function recoverSetPreflightCommand(setId: string, narrow: "all" | "newest" | "since", since: string, target: "staging" | "storage", where: string): string {
  return recoverSetCommand(setId, narrow, since, target, where, false) + " --preflight";
}

/** A backup set stored by the snapshot engine has restore points, not
 *  artifacts, and `recover` refuses it with this verb's name. */
export function snapshotRestoreCommand(setId: string): string {
  return "retnd snapshot restore " + setId + " --to <dir>";
}

/** The provider restore on its own, without recovering anything yet. */
export function thawCommand(artifactId: string): string {
  return "retnd restore " + artifactId + " --medium <id> --acknowledge";
}

export function settingsCommand(): string {
  return "retnd settings assurance patch --scrub-cadence <duration>";
}
