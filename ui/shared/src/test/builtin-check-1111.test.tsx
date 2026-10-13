/**
 * Issue #1111: a built-in structure check reaches the UI as recovery_state
 * "readable" (the wire never says "passed" for it) plus recovery_check
 * "builtin". These pin what each surface may say about it.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { BackupSetAssurance, RetndApi } from "@shared/api/contracts";
import { AssuranceSection } from "@shared/assurance/AssuranceSection";
import { ClaimChips } from "@shared/assurance/ClaimChips";
import { renderClaim, tally } from "@shared/assurance/claims";
import { recoveryTestEndText, recoveryTestStartText } from "@shared/assurance/feedText";
import { recoveryClaim, withHttpAssurance } from "@shared/assurance/realAssurance";
import { resetGraphForTests } from "@shared/state/graph";
import type { SetActivityEvent } from "@shared/types/activity";
import type { BackupArtifact, BackupPlacement } from "@shared/types/backup";

const NOW = Date.parse("2026-10-13T12:00:00Z");
const T = "2026-10-12T12:00:00Z";
const BUILTIN = "retnd-builtin-structure";
const SET = "production/auth-config";

describe("#1111 claims: a built-in pass is Recovered, readable", () => {
  const rec = { status: "pass", achievedClass: "content", at: T, check: "builtin" } as const;

  it("renders pass_builtin with its own label, glyph and age", () => {
    const r = renderClaim("recovery", rec, NOW);
    expect(r.state).toBe("pass_builtin");
    expect(r.label).toBe("Recovered, readable");
    expect(r.age).toBe("24h ago");
    expect(r.glyph).toEqual({ id: "pass-builtin", icon: "success" });
  });

  it("never says Recovery test passed for a built-in record, and still does for a validator one", () => {
    expect(renderClaim("recovery", rec, NOW).label).not.toMatch(/Recovery test passed/);
    expect(renderClaim("recovery", { ...rec, check: "validator" }, NOW).label).toBe("Recovery test passed");
    expect(renderClaim("recovery", { status: "pass", achievedClass: "content", at: T }, NOW).state).toBe("pass");
  });

  it("a built-in pass without a time is unknown, like any pass", () => {
    expect(renderClaim("recovery", { ...rec, at: undefined }, NOW).state).toBe("unknown");
  });

  it("an overdue built-in verdict reads as overdue", () => {
    expect(renderClaim("recovery", { status: "overdue", achievedClass: "", at: T, check: "builtin" }, NOW).state).toBe("overdue");
  });

  it("the content claim ignores a check field", () => {
    expect(renderClaim("content", { status: "pass", achievedClass: "content", at: T, check: "builtin" }, NOW).label).toBe("Content verified");
  });

  it("tally counts it as neither due nor degraded", () => {
    const content = { status: "pass", achievedClass: "content", at: T } as const;
    expect(tally([{ content, recovery: rec }], NOW)).toEqual({ due: 0, overdue: 0, degraded: 0, unknown: 0 });
  });

  it("the chip carries data-state=pass_builtin and the label", () => {
    render(<ClaimChips assurance={{
      artifactId: SET + "/a.tar.zst", copies: 1, copyContent: ["content"],
      content: { status: "pass", achievedClass: "content", at: T }, recovery: rec
    }} />);
    const chip = document.querySelector('[data-claim="recovery"]')!;
    expect(chip.getAttribute("data-state")).toBe("pass_builtin");
    expect(chip.textContent).toContain("Recovered, readable");
    expect(chip.textContent).not.toContain("Recovery test passed");
    expect(chip.querySelector('[data-glyph="pass-builtin"]')).toBeTruthy();
    cleanup();
  });
});

const placement = (over: Partial<BackupPlacement> = {}): BackupPlacement => ({
  medium: "local", mediumType: "local", location: "/x", sizeBytes: 1, storageClass: "",
  verificationClass: null, verifiedAt: null, access: "immediate", status: "ACTIVE", ...over
});
const artifact = (placements: BackupPlacement[]) =>
  ({ id: SET + "/a.tar.zst", setId: SET, producedAt: T, placements, quarantine: null }) as unknown as BackupArtifact;

describe("#1111 recoveryClaim reads readable and recovery_check", () => {
  it("readable is a pass whose check is builtin", () => {
    const c = recoveryClaim(artifact([placement({ recoveryState: "readable", recoveryCheck: "builtin", recoveryTestedAt: T })]), false, true);
    expect(c.status).toBe("pass");
    expect(c.check).toBe("builtin");
    expect(c.at).toBe(T);
  });
  it("a validator pass carries no builtin check", () => {
    const c = recoveryClaim(artifact([placement({ recoveryState: "passed", recoveryCheck: "validator", recoveryTestedAt: T })]), false, true);
    expect(c.status).toBe("pass");
    expect(c.check).not.toBe("builtin");
  });
  it("a validator pass on one copy outranks a readable one on another", () => {
    const c = recoveryClaim(artifact([
      placement({ recoveryState: "readable", recoveryCheck: "builtin", recoveryTestedAt: T }),
      placement({ medium: "s3", recoveryState: "passed", recoveryCheck: "validator", recoveryTestedAt: T })
    ]), false, true);
    expect(c.check).not.toBe("builtin");
  });
  it("an overdue built-in verdict stays overdue", () => {
    const c = recoveryClaim(artifact([placement({ recoveryState: "overdue", recoveryCheck: "builtin", recoveryTestedAt: T })]), false, true);
    expect(c.status).toBe("overdue");
  });
  it("failed still outranks readable", () => {
    const c = recoveryClaim(artifact([
      placement({ recoveryState: "readable", recoveryCheck: "builtin", recoveryTestedAt: T }),
      placement({ medium: "s3", recoveryState: "failed", recoveryTestedAt: T })
    ]), false, true);
    expect(c.status).toBe("failed");
  });
});

const ev = (result: SetActivityEvent["result"], fields: Record<string, string>): SetActivityEvent =>
  ({ sequence: 1, scope: "set", at: T, level: "info", event: "recovery_test_end", result, message: "", fields }) as SetActivityEvent;

describe("#1111 feed wording", () => {
  it("start names the built-in check instead of a validator", () => {
    expect(recoveryTestStartText(ev("info", { artifact: SET + "/a.tar.zst", validator: BUILTIN })))
      .toBe("recovery test started: a.tar.zst (built-in structure check)");
  });
  it("start with a validator is unchanged", () => {
    expect(recoveryTestStartText(ev("info", { artifact: SET + "/a.tar.zst", validator: "pg-check" })))
      .toBe("recovery test started: a.tar.zst (validator pg-check)");
  });
  it("success end reads recovered and readable, never passed", () => {
    const t = recoveryTestEndText(ev("success", { artifact: SET + "/a.tar.zst", validator: BUILTIN }));
    expect(t).toBe("recovered and readable: a.tar.zst (built-in check)");
    expect(t).not.toMatch(/passed/);
  });
  it("success with a validator still says passed", () => {
    expect(recoveryTestEndText(ev("success", { artifact: SET + "/a.tar.zst", validator: "pg-check" }))).toBe("recovery test passed: a.tar.zst");
  });
  it("error and warn are unchanged and carry the stored reason", () => {
    expect(recoveryTestEndText(ev("error", { artifact: SET + "/a.tar.zst", validator: BUILTIN, reason: "unexpected EOF" })))
      .toBe("recovery test failed: a.tar.zst: unexpected EOF");
    expect(recoveryTestEndText(ev("warn", { artifact: SET + "/a.tar.zst", validator: BUILTIN, reason: "budget hit" })))
      .toBe("recovery test did not complete: a.tar.zst: budget hit");
  });
});

const policy = (recoveryTest: Partial<BackupSetAssurance["recoveryTest"]>): BackupSetAssurance => ({
  backupSetId: SET, isOverride: false,
  scrub: { cadenceSeconds: 604800, strategy: "auto" },
  recoveryTest: { mode: "off", cadenceSeconds: 0, validatorId: "", ...recoveryTest }
});
const noLive = { getLiveActivity: vi.fn().mockResolvedValue({ observedAt: T, epoch: "e", pollAfterMs: 1000, deployment: null, sets: [] }) };

function renderSection(over: Partial<RetndApi>) {
  const root = { ...createMockApi("assurance-unavailable"), ...over } as RetndApi;
  render(
    <MemoryRouter>
      <ApiProvider api={withHttpAssurance(root)}>
        <AssuranceSection setId={SET} readOnly={false} />
      </ApiProvider>
    </MemoryRouter>
  );
}
const runButton = () => screen.findByRole("button", { name: "Run recovery test" });

describe("#1111 Run recovery test follows effective_check", () => {
  beforeEach(() => resetGraphForTests());
  afterEach(() => cleanup());

  it("builtin: enabled, no refusal, even though no validator is declared", async () => {
    renderSection({ ...noLive, getBackupSetAssurance: vi.fn().mockResolvedValue(policy({ validatorDeclared: false, effectiveCheck: "builtin" })) });
    const run = await runButton();
    expect((run as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByText(/no recovery-test validator is declared/i)).toBeNull();
  });

  it("builtin wins over a copy recorded as not_configured", async () => {
    renderSection({
      ...noLive, getBackupSetAssurance: vi.fn().mockResolvedValue(policy({ effectiveCheck: "builtin" })),
      listArtifacts: vi.fn().mockResolvedValue([artifact([placement({ recoveryState: "not_configured" })])])
    });
    expect(((await runButton()) as HTMLButtonElement).disabled).toBe(false);
  });

  it("none: disabled, and the engine's reason is the reason", async () => {
    renderSection({ ...noLive, getBackupSetAssurance: vi.fn().mockResolvedValue(policy({
      validatorDeclared: false, effectiveCheck: "none",
      effectiveCheckReason: "the built-in check is off for this backup set and no validator is declared"
    })) });
    const run = await runButton();
    expect((run as HTMLButtonElement).disabled).toBe(true);
    const why = screen.getByText(/the built-in check is off for this backup set/i);
    const ids = (run.getAttribute("aria-describedby") ?? "").split(" ");
    expect(ids.some((id) => id && document.getElementById(id)?.contains(why))).toBe(true);
  });

  it("none without a reason keeps the #1108 wording", async () => {
    renderSection({ ...noLive, getBackupSetAssurance: vi.fn().mockResolvedValue(policy({ validatorDeclared: false, effectiveCheck: "none" })) });
    expect(((await runButton()) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/no recovery-test validator is declared/i)).toBeTruthy();
  });

  it("validator: enabled", async () => {
    renderSection({ ...noLive, getBackupSetAssurance: vi.fn().mockResolvedValue(policy({ validatorDeclared: true, effectiveCheck: "validator" })) });
    expect(((await runButton()) as HTMLButtonElement).disabled).toBe(false);
  });
});
