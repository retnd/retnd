/**
 * The `retnd` command each gated screen act is equivalent to (issue #1157),
 * spelled the way the engine's own echo spells it (core/cliecho/routes.go,
 * POST /operations), so the line in a refusal and the line the docked
 * terminal prints for the same press are the same string.
 *
 * Every verb and flag is one the CLI has today (core/cmd/retnd usage()). The
 * values are shell-quoted by the same rule as cliecho's shellQuote: a value
 * made of safe characters is left alone, anything else is single-quoted.
 */

const SAFE = /^[A-Za-z0-9_@%+=:,./-]+$/;

/** The same quoting cliecho.shellQuote applies. */
export function shellQuote(s: string): string {
  if (s === "") return "''";
  return SAFE.test(s) ? s : "'" + s.replace(/'/g, "'\\''") + "'";
}

export function snapshotVerifyCommand(setId: string, runId?: string): string {
  return "retnd snapshot verify " + shellQuote(setId) + (runId ? " --run " + shellQuote(runId) : "");
}

export function snapshotHoldCommand(setId: string, reason: string, runId?: string): string {
  return (
    "retnd snapshot hold " + shellQuote(setId) + " --reason " + shellQuote(reason) +
    (runId ? " --run " + shellQuote(runId) : "")
  );
}

export function snapshotUnholdCommand(setId: string, holdId: string): string {
  return "retnd snapshot unhold " + shellQuote(setId) + " " + shellQuote(holdId);
}

export function snapshotRestoreCommand(args: {
  setId: string;
  targetPath: string;
  snapshotId?: string;
  sourcePath?: string;
  conflict?: string;
}): string {
  return (
    "retnd snapshot restore " + shellQuote(args.setId) + " --to " + shellQuote(args.targetPath) +
    (args.snapshotId ? " --snapshot " + shellQuote(args.snapshotId) : "") +
    (args.sourcePath ? " --path " + shellQuote(args.sourcePath) : "") +
    (args.conflict ? " --conflict " + shellQuote(args.conflict) : "")
  );
}

/** The snapshot restore page's notice when the gate is shut (#1253, moved
 *  here from SnapshotRestorePage): the verb, with the target still to name. */
export function snapshotRestoreShapeCommand(setId: string): string {
  return "retnd snapshot restore " + shellQuote(setId) + " --to <directory>";
}

export function retentionApplyCommand(setId: string): string {
  return "retnd retention apply " + shellQuote(setId) + " --acknowledge";
}
