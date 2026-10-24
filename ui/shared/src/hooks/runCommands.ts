/**
 * The `retnd` line the run buttons are equivalent to, kept with the other
 * twin modules so ui/shared/src/test/cliTwins.test.ts finds it and
 * core/cmd/retnd parses it (#1253). It used to live in useRunControls.ts,
 * which nothing parsed.
 */
import { shellQuote } from "@shared/assurance/assuranceCommands";

/** What an operator asked for: every enabled set, or exactly one. */
export type RunScope = { kind: "all" } | { kind: "set"; id: string };

/** The command line each scope is equivalent to: `retnd operation run`
 *  (#1250), which asks the serving engine the way the button does, so the
 *  run takes the engine's lock and shows in its feeds. Naming the set by
 *  its full source/backup-set id, quoted, copy-pasteable as written.
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
  return scope.kind === "all" ? "retnd operation run" : "retnd operation run --backup-set " + shellQuote(scope.id);
}

/** The local verbs, `retnd run` and `retnd fetch --backup-set`: they open
 *  the service in the operator's own process and run the cycle there.
 *
 *  They are not the twin of the button and are only ever a remedy. When
 *  the engine's destructive gate refuses the HTTP route,
 *  `operation run` goes through that same route and is refused too, so
 *  these are the commands that still work (#662). */
export function localRunCommand(scope: RunScope): string {
  return scope.kind === "all" ? "retnd run" : "retnd fetch --backup-set " + shellQuote(scope.id);
}

/** The line a notice for this press carries: the twin, except for the
 *  gate's refusal, which carries the local verb it can still be answered
 *  with. */
export function noticeCommand(code: string, scope: RunScope): string {
  return code === "DESTRUCTIVE_OPERATIONS_DISABLED" ? localRunCommand(scope) : runCommand(scope);
}
