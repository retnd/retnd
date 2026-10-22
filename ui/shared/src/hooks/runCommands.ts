/**
 * The `retnd` line the run buttons are equivalent to, kept with the other
 * twin modules so ui/shared/src/test/cliTwins.test.ts finds it and
 * core/cmd/retnd parses it (#1253). It used to live in useRunControls.ts,
 * which nothing parsed.
 */
import { shellQuote } from "@shared/assurance/assuranceCommands";

/** What an operator asked for: every enabled set, or exactly one. */
export type RunScope = { kind: "all" } | { kind: "set"; id: string };

/** The command line each scope is equivalent to, copy-pasteable exactly
 *  as written and naming the set by its full source/backup-set id.
 *
 *  Neither form can carry a credential: the connection details live in
 *  the configuration, and the browser holds a session cookie rather than
 *  the administrator's password.
 *
 *  Neither carries `--config` either, and that is a gap rather than a
 *  decision: nothing on the wire tells this build where the deployment's
 *  configuration actually is, so the line shown is the packaged-default
 *  form. On an install that moved it, the command still names the right
 *  work and would need the flag added by hand. */
export function runCommand(scope: RunScope): string {
  return scope.kind === "all" ? "retnd run" : "retnd fetch --backup-set " + shellQuote(scope.id);
}
