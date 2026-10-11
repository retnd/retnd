/**
 * The `retnd` command each assurance control is equivalent to, in ONE place
 * (issue #985, checklist section 3: no web-only behaviour).
 *
 * Every verb and flag here is one the CLI has today (core/cmd/retnd), and the
 * spelling is pinned by assurance.test.tsx against the usage text. CommandEcho
 * prints exactly what is returned, so nothing on screen can disagree with it.
 *
 * There is deliberately NO recovery-test command and no operations-cancel
 * command: the CLI has neither. A recovery test runs from the schedule, and
 * the screens say that instead of echoing a command that would fail.
 */
import type { RecoveryTargetSpec } from "@shared/api/contracts";

/** `<source>/<set>`, the same id the web uses for a backup set. */
export function scrubCommand(setId: string): string {
  return "retnd scrub " + setId;
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
