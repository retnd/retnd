/**
 * Issue #985: the parts of the assurance UI that are copies of, or derived
 * from, something the server owns, each checked against that source.
 */
import { describe, expect, it } from "vitest";
import { MIN_CADENCE_SECONDS, MIN_RECOVERY_TEST_CADENCE_SECONDS, cadenceProblem, formatCadence, formatFloor, parseCadence } from "@shared/assurance/duration";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { contentClaim } from "@shared/assurance/realAssurance";
import { RECOVERY_STAGES } from "@shared/assurance/stages";
import { recoverCommand, recoveryTestCommand, scrubCommand, snapshotRestoreCommand, thawCommand } from "@shared/assurance/assuranceCommands";
import { recoveryRefusal } from "@shared/assurance/recoveryMessages";
import { RetndError } from "@shared/api/contracts";
import type { ApiErrorCode } from "@shared/api/contracts";
import type { BackupArtifact, BackupPlacement } from "@shared/types/backup";

// Raw text through Vite, because tsconfig keeps @types/node out of this program.
import obsEvents from "../../../../core/internal/obs/assurance_events.go?raw";
import cliMain from "../../../../core/cmd/retnd/main.go?raw";
import openapi from "../../../../api/v1/openapi.json?raw";
import assuranceConfig from "../../../../core/internal/config/assurance.go?raw";

describe("the stage names are the engine's", () => {
  it("lists exactly obs.RecoveryStages, in order, with the same labels", () => {
    const src = obsEvents;
    const block = src.slice(src.indexOf("var RecoveryStages"), src.indexOf("// StageLabel"));
    const go = [...block.matchAll(/\{ID: "([a-z_]+)", Label: "([^"]+)"\}/g)].map((m) => ({ id: m[1], label: m[2] }));
    expect(go.length).toBeGreaterThan(5);
    expect(RECOVERY_STAGES.map((s) => ({ id: s.id, label: s.label }))).toEqual(go);
  });
});

describe("the terminal commands are ones the CLI has", () => {
  const usage = cliMain;
  it("spells the cost flag the way recover does and never as accept-cost", () => {
    expect(usage).toContain("--acknowledge-cost");
    expect(recoverCommand("s/set/a.zst", "staging", "name", true)).toBe("retnd recover s/set/a.zst --to name --acknowledge-cost");
    expect(recoverCommand("s/set/a.zst", "staging", "name", false)).not.toContain("cost");
    for (const t of ["download", "staging", "storage"] as const) {
      expect(recoverCommand("s/set/a.zst", t, "x", true)).not.toContain("accept-cost");
    }
  });
  it("a browser download is shown as its standard-output form, which is a target recover has", () => {
    expect(recoverCommand("s/set/a.zst", "download", "", false)).toBe("retnd recover s/set/a.zst --to - > recovered-file");
    expect(recoverCommand("s/set/a.zst", "download", "", true)).toBe("retnd recover s/set/a.zst --acknowledge-cost --to - > recovered-file");
    expect(recoverCommand("s/set/a.zst", "storage", "offsite", false)).toBe("retnd recover s/set/a.zst --to medium:offsite");
    expect(usage).toContain("--to - | NAME | medium:ID[/KEY]");
  });
  it("scrub, recovery-test, restore and snapshot restore exist as verbs", () => {
    expect(usage).toMatch(/\n {2}scrub \[<source\/backup-set>/);
    expect(scrubCommand("s/set")).toBe("retnd scrub s/set");
    expect(usage).toMatch(/\n {2}restore <source\/backup-set\/artifact> --medium M \[--days N\] --acknowledge/);
    expect(thawCommand("s/set/a")).toContain("--acknowledge");
    expect(usage).toMatch(/\n {2}snapshot restore <source\/backup-set> --to DIR/);
    expect(snapshotRestoreCommand("s/set")).toMatch(/^retnd snapshot restore s\/set --to /);
    expect(usage).toMatch(/\n {2}recovery-test <source\/backup-set>/);
    expect(recoveryTestCommand("s/set")).toBe("retnd recovery-test s/set");
  });
});

describe("cadences", () => {
  it("parse, format back to the same seconds, and refuse under an hour", () => {
    expect(parseCadence("7d")).toBe(604800);
    expect(parseCadence("12h")).toBe(43200);
    expect(parseCadence("7 days")).toBeNull();
    expect(formatCadence(604800)).toBe("7d");
    expect(formatCadence(5400)).toBe("90m");
    for (const s of [3600, 5400, 43200, 604800, 2592000]) expect(parseCadence(formatCadence(s))).toBe(s);
    expect(cadenceProblem("30m")).toMatch(/1h/);
    expect(cadenceProblem("soon")).toMatch(/number and a unit/);
    expect(cadenceProblem("1h")).toBeNull();
  });

  // The floors are the config package's, read out of its source so neither the
  // checks nor the help copy can drift from what the server refuses. The
  // recovery test's was once described as an hour here while the server held
  // it to a day.
  const goFloorSeconds = (name: string): number => {
    const m = new RegExp(`${name}\\s*=\\s*(?:(\\d+)\\s*\\*\\s*)?time\\.Hour`).exec(assuranceConfig);
    if (!m) throw new Error(`${name} is not an hours constant in core/internal/config/assurance.go any more`);
    return (m[1] ? Number(m[1]) : 1) * 3600;
  };
  it("hold each claim to the floor the server holds it to", () => {
    expect(MIN_CADENCE_SECONDS).toBe(goFloorSeconds("MinScrubCadence"));
    expect(MIN_RECOVERY_TEST_CADENCE_SECONDS).toBe(goFloorSeconds("MinRecoveryTestCadence"));
    expect(cadenceProblem("12h", MIN_RECOVERY_TEST_CADENCE_SECONDS)).toMatch(/24h/);
    expect(cadenceProblem("12h")).toBeNull();
    expect(cadenceProblem("24h", MIN_RECOVERY_TEST_CADENCE_SECONDS)).toBeNull();
  });
  it("describe the recovery-test floor in the help copy as the server's floor", () => {
    const floor = formatFloor(goFloorSeconds("MinRecoveryTestCadence"));
    expect(FIELD_HELP.assuranceRecoveryTestEvery.effect).toContain(`nothing under ${floor}`);
    expect(FIELD_HELP.assuranceRecoveryTestEvery.effect).not.toContain("nothing under 1h");
    expect(FIELD_HELP.assuranceScrubEvery.effect).toContain(`nothing under ${formatFloor(goFloorSeconds("MinScrubCadence"))}`);
  });
});

function placement(over: Partial<BackupPlacement>): BackupPlacement {
  return {
    medium: "local", mediumType: "local", location: "/x", sizeBytes: 1, storageClass: "",
    verificationClass: null, verifiedAt: null, access: "immediate", status: "ACTIVE", ...over
  };
}
const artifact = (placements: BackupPlacement[], over: Partial<BackupArtifact> = {}) =>
  ({ id: "s/set/a", setId: "s/set", placements, quarantine: null, ...over }) as unknown as BackupArtifact;

describe("the Content claim is derived from what the copies recorded", () => {
  const T = "2026-10-01T00:00:00Z";
  it("takes the strongest class any copy achieved, with that copy's time", () => {
    const c = contentClaim(artifact([
      placement({ verificationClass: "existence", verifiedAt: "2026-10-05T00:00:00Z" }),
      placement({ medium: "s3", verificationClass: "content", verifiedAt: T })
    ]), { overdue: false, never: false });
    expect(c).toEqual({ status: "pass", achievedClass: "content", at: T });
  });
  it("a class with no time is not evidence", () => {
    expect(contentClaim(artifact([placement({ verificationClass: "content", verifiedAt: null })]), undefined).status).toBe("never_run");
  });
  it("an existence check stays an existence check", () => {
    expect(contentClaim(artifact([placement({ verificationClass: "existence", verifiedAt: T })]), undefined).achievedClass).toBe("existence");
  });
  it("overdue comes from the schedule and keeps the time it last passed", () => {
    const c = contentClaim(artifact([placement({ verificationClass: "content", verifiedAt: T })]), { overdue: true, never: false });
    expect(c).toEqual({ status: "overdue", achievedClass: "content", at: T });
  });
  it("no copy is never run, an archived-only copy needs a restore, an unreachable one is unreadable", () => {
    expect(contentClaim(artifact([]), undefined).status).toBe("never_run");
    expect(contentClaim(artifact([placement({ access: "requires_restore" })]), undefined).status).toBe("requires_restore");
    expect(contentClaim(artifact([placement({ access: "restoring" })]), undefined).status).toBe("requires_restore");
    expect(contentClaim(artifact([placement({ access: "unreachable" })]), undefined).status).toBe("no_readable_copy");
  });
  it("a copy whose removal is recorded does not count", () => {
    expect(contentClaim(artifact([placement({ status: "DELETE_PENDING", verificationClass: "content", verifiedAt: T })]), undefined).status).toBe("never_run");
  });
  it("a quarantined backup failed a check the product ran", () => {
    expect(contentClaim(artifact([placement({ verificationClass: "content", verifiedAt: T })], { quarantine: { reason: "x" } as never }), undefined).status).toBe("failed");
  });
});

describe("recovery refusals say what to do", () => {
  const refuse = (code: string, message = "service sentence") =>
    new RetndError({ code: code as ApiErrorCode, message, correlationId: "c" });
  it("keeps the service's sentence and adds the next step", () => {
    const r = recoveryRefusal(refuse("RECOVERY_GRANT_EXPIRED", "expired"), "s/set");
    expect(r.message).toBe("expired");
    expect(r.retryable).toBe(true);
    expect(r.next).toMatch(/new one/);
  });
  it("a full staging filesystem is not something asking again fixes", () => {
    const r = recoveryRefusal(refuse("RECOVERY_NO_SPACE"), "s/set");
    expect(r.retryable).toBe(false);
    expect(r.next).toMatch(/Nothing was written/);
  });
  it("a snapshot-engine set is pointed at the verb that restores it", () => {
    const r = recoveryRefusal(refuse("RECOVERY_USE_SNAPSHOT_RESTORE"), "s/set");
    expect(r.command).toBe("retnd snapshot restore s/set --to <dir>");
  });
  it("every recovery code the contract can emit has a next step or the service's own words", () => {
    const codes = [...openapi.matchAll(/"(RECOVERY_[A-Z_]+)"/g)].map((m) => m[1]);
    const unique = [...new Set(codes)].filter((c) => c !== "RECOVERY_PERF_BYTES");
    expect(unique.length).toBeGreaterThan(15);
    for (const code of unique) {
      const r = recoveryRefusal(refuse(code), "s/set");
      expect(r.message.length, code).toBeGreaterThan(0);
      if (code !== "RECOVERY_COST_NOT_ACKNOWLEDGED") expect(r.next ?? "", code).not.toBe("");
    }
  });
});
