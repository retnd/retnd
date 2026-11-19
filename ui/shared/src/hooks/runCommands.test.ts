import { describe, expect, it } from "vitest";
import { localRunCommand, noticeCommand, runCommand } from "./runCommands";

/**
 * The line a run button is equivalent to (#1253's review). The Dashboard's
 * "Run all enabled sets" and the set page's "Run this backup set" POST
 * /operations, which core/cliecho echoes as `retnd operation run` since
 * #1250. The browser printed `retnd run` and `retnd fetch --backup-set`
 * for the same press, so a served run was logged two ways in one export.
 */
describe("the run buttons' terminal lines", () => {
  it("is operation run, the verb that asks the serving engine", () => {
    expect(runCommand({ kind: "all" })).toBe("retnd operation run");
    expect(runCommand({ kind: "set", id: "api-server/var-backups" })).toBe("retnd operation run --backup-set api-server/var-backups");
  });

  it("quotes a set id", () => {
    expect(runCommand({ kind: "set", id: "a/b; id" })).toBe("retnd operation run --backup-set 'a/b; id'");
  });

  it("keeps the local verbs for the one case they are the answer to", () => {
    // `run` and `fetch` open the service in the operator's own process, so
    // they work when the engine's destructive gate refuses the HTTP route,
    // and `operation run` goes through that same route and is refused too.
    expect(localRunCommand({ kind: "all" })).toBe("retnd run");
    expect(localRunCommand({ kind: "set", id: "api-server/var-backups" })).toBe("retnd fetch --backup-set api-server/var-backups");
  });

  it("names the local verb only for the gate's refusal, and the twin everywhere else", () => {
    const set = { kind: "set", id: "api-server/var-backups" } as const;
    expect(noticeCommand("DESTRUCTIVE_OPERATIONS_DISABLED", set)).toBe("retnd fetch --backup-set api-server/var-backups");
    for (const code of ["unknown", "OPERATION_ALREADY_RUNNING", "CONFIG_REVISION_STALE", "BACKUP_SET_HELD_FOR_EDITING"] as const) {
      expect(noticeCommand(code, set)).toBe("retnd operation run --backup-set api-server/var-backups");
    }
    expect(noticeCommand("unknown", { kind: "all" })).toBe("retnd operation run");
  });
});
