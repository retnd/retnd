/**
 * Issue #1108: Run recovery test was offered, enabled, on a set that can only
 * refuse. These pin the page against the PRODUCTION composition
 * (withHttpAssurance over a client), the way assurance-honesty.test.tsx does.
 *
 * The page may know three things before the first press, and each has a case:
 * the engine's own "no validator is declared" (`validatorDeclared: false`), the
 * engine having already recorded `not_configured` on the newest copy, and
 * neither (an older server), which must stay as it is today.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { BackupSetAssurance, RetndApi } from "@shared/api/contracts";
import { AssuranceSection } from "@shared/assurance/AssuranceSection";
import { recoveryClaim, withHttpAssurance } from "@shared/assurance/realAssurance";
import { resetGraphForTests } from "@shared/state/graph";
import type { BackupArtifact, BackupPlacement } from "@shared/types/backup";

const SET = "production/postgres-primary";
const T = "2026-10-09T03:00:00Z";

const placement = (over: Partial<BackupPlacement> = {}): BackupPlacement => ({
  medium: "local", mediumType: "local", location: "/x", sizeBytes: 1, storageClass: "",
  verificationClass: null, verifiedAt: null, access: "immediate", status: "ACTIVE", ...over
});
const artifact = (placements: BackupPlacement[]) =>
  ({ id: SET + "/a.dump", setId: SET, producedAt: T, placements, quarantine: null }) as unknown as BackupArtifact;

const policy = (recoveryTest: Partial<BackupSetAssurance["recoveryTest"]>): BackupSetAssurance => ({
  backupSetId: SET, isOverride: false,
  scrub: { cadenceSeconds: 604800, strategy: "auto" },
  recoveryTest: { mode: "off", cadenceSeconds: 0, validatorId: "", ...recoveryTest }
});

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
const noLive = { getLiveActivity: vi.fn().mockResolvedValue({ observedAt: T, epoch: "e", pollAfterMs: 1000, deployment: null, sets: [] }) };
const runButton = () => screen.findByRole("button", { name: "Run recovery test" });

describe("#1108 Run recovery test on a set with no validator", () => {
  beforeEach(() => resetGraphForTests());
  afterEach(() => cleanup());

  it("is disabled when the engine says no validator is declared, and says why beside the button", async () => {
    renderSection({ ...noLive, getBackupSetAssurance: vi.fn().mockResolvedValue(policy({ validatorDeclared: false })) });
    const run = await runButton();
    expect((run as HTMLButtonElement).disabled, "a button that can only refuse must not be offered").toBe(true);
    const why = screen.getByText(/no recovery-test validator is declared/i);
    expect(why).toBeTruthy();
    // the reason is tied to the button for a screen reader, not only placed near it
    const ids = (run.getAttribute("aria-describedby") ?? "").split(" ");
    expect(ids.some((id) => id && document.getElementById(id)?.contains(why))).toBe(true);
  });

  it("points at how to declare one: docs/recovery.md and the set's settings", async () => {
    renderSection({ ...noLive, getBackupSetAssurance: vi.fn().mockResolvedValue(policy({ validatorDeclared: false })) });
    await runButton();
    const section = screen.getByRole("region", { name: "Assurance" });
    const links = within(section).getAllByRole("link").map((a) => a.getAttribute("href") ?? "");
    expect(links.some((h) => /docs\/recovery(\.md)?/.test(h)), "a link to docs/recovery.md; links were " + links.join(", ")).toBe(true);
    expect(within(section).getByText(/validator_id|own policy|Edit/i)).toBeTruthy();
  });

  it("falls back to the copy's own record: a copy the engine already recorded as not_configured disables it too", async () => {
    renderSection({
      ...noLive,
      getBackupSetAssurance: vi.fn().mockResolvedValue(policy({})),
      listArtifacts: vi.fn().mockResolvedValue([artifact([placement({ recoveryState: "not_configured" })])])
    });
    const run = await runButton();
    expect((run as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/no recovery-test validator is declared/i)).toBeTruthy();
  });

  it("control: a set whose validator is declared keeps the button enabled and prints no refusal", async () => {
    renderSection({ ...noLive, getBackupSetAssurance: vi.fn().mockResolvedValue(policy({ mode: "required", validatorId: "pg-check", validatorDeclared: true })) });
    const run = await runButton();
    expect((run as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByText(/no recovery-test validator is declared/i)).toBeNull();
  });

  it("control: a server that does not say, and a copy never run, behaves as before", async () => {
    renderSection({
      ...noLive,
      getBackupSetAssurance: vi.fn().mockResolvedValue(policy({})),
      listArtifacts: vi.fn().mockResolvedValue([artifact([placement({ recoveryState: "never_run" })])])
    });
    expect(((await runButton()) as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByText(/no recovery-test validator is declared/i)).toBeNull();
  });
});

describe("#1108 the Recovery claim of a copy the engine recorded as not_configured", () => {
  it("reads Not configured, not 'never run: a test is owed'", () => {
    const a = artifact([placement({ recoveryState: "not_configured" })]);
    // reported=true is what a server that records the claim sends; the copy
    // says not_configured, which is more specific than "owed and not run".
    expect(recoveryClaim(a, false, true).status).toBe("never_configured");
  });

  it("control: a copy that really was never run still reads never run", () => {
    expect(recoveryClaim(artifact([placement({ recoveryState: "never_run" })]), false, true).status).toBe("never_run");
  });
});
