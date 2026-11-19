/**
 * The `retnd` line the custom validator dialog prints (issue #1203), kept
 * with the other twin modules so ui/shared/src/test/cliTwins.test.ts finds
 * it and core/cmd/retnd parses it (#1253). It used to live in
 * customValidators.ts, which nothing parsed.
 */
import type { ValidatorEnv } from "@shared/api/contracts";
import { shellQuote } from "./assuranceCommands";

/** The command that does the same upload from a shell on the host, for the
 *  notice a shut gate gets. Every operand a person typed goes through
 *  shellQuote, so the line can be pasted as it stands. */
export function addCommand(name: string, pattern: string, backupSetId: string, env: ValidatorEnv = "minimal"): string {
  return (
    "retnd assurance validators add " + shellQuote(name || "NAME") + " --pattern " + shellQuote(pattern || "PATTERN") +
    (backupSetId ? " --backup-set " + shellQuote(backupSetId) : "") + " --script FILE" +
    (env === "workflow" ? " --env workflow" : "")
  );
}
