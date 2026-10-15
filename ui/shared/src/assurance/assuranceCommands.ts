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

/** The progress bar and its step count, in a terminal: `--follow` prints each
 *  stage the test enters, in the engine's order, on standard error as it
 *  happens, and the exit status is the test's (#1176). */
export function recoveryTestFollowCommand(setId: string): string {
  return "retnd recovery-test " + setId + " --follow";
}

/** The set's terminal, in a terminal: the live feed narrowed to this set,
 *  which is where a run's output lives (#1176, #1177). `--json` prints the
 *  wire events, stage and stage_label fields included. */
export function setTerminalCommand(setId: string): string {
  return "retnd activity --follow --backup-set " + setId;
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

/**
 * `retnd recover` for several chosen artifacts to ONE destination (issue
 * #1107). There is no browser form: a stream of several files has no terminal
 * spelling, and the page does not offer one.
 */
export function recoverManyCommand(artifactIds: string[], target: "staging" | "storage", where: string, acknowledgeCost: boolean): string {
  const to = target === "staging" ? "--to " + (where || "<name>") : "--to medium:" + (where || "<id>");
  return "retnd recover " + artifactIds.join(" ") + " " + to + (acknowledgeCost ? " --acknowledge-cost" : "");
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

/** The flags `retnd settings assurance patch` and `retnd backup-set assurance`
 *  share, in the order core/cliecho prints them. Only the fields a patch names
 *  are printed, which is also what the PATCH body carries. */
interface AssuranceFlags {
  scrubCadenceSeconds?: number;
  scrubStrategy?: string;
  recoveryTestMode?: string;
  recoveryTestCadenceSeconds?: number;
  recoveryTestValidatorId?: string;
  recoveryTestBuiltinCheck?: string;
}

/** core/cliecho's seconds(): 172800 is 48h and 90 is 1m30s, built from the
 *  hours, minutes and seconds so a trailing zero is never eaten. */
export function durationFlag(total: number): string {
  if (total === 0) return "0s";
  const h = Math.floor(total / 3600), m = Math.floor((total % 3600) / 60), s = total % 60;
  return (h ? h + "h" : "") + (m ? m + "m" : "") + (s ? s + "s" : "");
}

/** core/cliecho's shellQuote. */
function shellQuote(v: string): string {
  if (v === "") return "''";
  return /^[A-Za-z0-9_@%+=:,./-]+$/.test(v) ? v : "'" + v.replace(/'/g, "'\\''") + "'";
}

function assuranceFlags(p: AssuranceFlags): string[] {
  const out: string[] = [];
  const flag = (name: string, value: string) => out.push("--" + name + " " + shellQuote(value));
  if (p.scrubCadenceSeconds !== undefined) flag("scrub-cadence", durationFlag(p.scrubCadenceSeconds));
  if (p.scrubStrategy !== undefined) flag("scrub-strategy", p.scrubStrategy);
  if (p.recoveryTestMode !== undefined) flag("recovery-test-mode", p.recoveryTestMode);
  if (p.recoveryTestCadenceSeconds !== undefined) flag("recovery-test-cadence", durationFlag(p.recoveryTestCadenceSeconds));
  if (p.recoveryTestValidatorId !== undefined) flag("recovery-test-validator", p.recoveryTestValidatorId);
  if (p.recoveryTestBuiltinCheck !== undefined) flag("recovery-test-builtin-check", p.recoveryTestBuiltinCheck);
  return out;
}

/** `retnd settings assurance patch` for the changes the Settings card would
 *  save. With none pending it shows the shape of the command. */
export function settingsCommand(patch: AssuranceFlags = {}): string {
  const flags = assuranceFlags(patch);
  return ["retnd settings assurance patch", ...(flags.length > 0 ? flags : ["--scrub-cadence <duration>"])].join(" ");
}

/** `retnd backup-set assurance <set>` for the own policy the per-set Save
 *  would write (#1111), or its `--reset` when the set goes back to following. */
export function setAssuranceCommand(setId: string, patch: AssuranceFlags | "reset"): string {
  return ["retnd backup-set assurance " + setId, ...(patch === "reset" ? ["--reset"] : assuranceFlags(patch))].join(" ");
}
