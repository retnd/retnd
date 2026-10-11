/**
 * Panel finding C8 (issue #985): the production assurance screens told the
 * operator things the engine did not do, and rendered a failed scrub as
 * "Never run". These pin the corrected behaviour against the PRODUCTION
 * composition (`withHttpAssurance` over a client), not the mock's, because the
 * mock is where every one of those sentences looked fine.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";
import { AssuranceSection } from "@shared/assurance/AssuranceSection";
import { AssuranceSettingsCard } from "@shared/assurance/AssuranceSettingsCard";
import { ClaimChips } from "@shared/assurance/ClaimChips";
import { contentClaim, copyFailures, createHttpAssurance, recoveryClaim, withHttpAssurance } from "@shared/assurance/realAssurance";
import { failureSentence, renderClaim } from "@shared/assurance/claims";
import { resetGraphForTests } from "@shared/state/graph";
import type { SetActivity, SetActivityEvent } from "@shared/types/activity";
import type { BackupArtifact, BackupPlacement } from "@shared/types/backup";

// Raw text through Vite, because tsconfig keeps @types/node out of this program.
import sectionSrc from "../assurance/AssuranceSection.tsx?raw";
import cardSrc from "../assurance/AssuranceSettingsCard.tsx?raw";
import helpSrc from "../components/fieldHelpCopy.ts?raw";
import tooltipsSrc from "../tooltips/tooltips.json?raw";
import contractTsSrc from "./../api/generated/contract.ts?raw";

const SET = "production/postgres-primary";
const T = "2026-10-09T03:00:00Z";

function placement(over: Partial<BackupPlacement> = {}): BackupPlacement {
  return {
    medium: "local", mediumType: "local", location: "/x", sizeBytes: 1, storageClass: "",
    verificationClass: null, verifiedAt: null, access: "immediate", status: "ACTIVE", ...over
  };
}
const artifact = (placements: BackupPlacement[], over: Partial<BackupArtifact> = {}) =>
  ({ id: SET + "/a.dump", setId: SET, placements, quarantine: null, ...over }) as unknown as BackupArtifact;

describe("a copy whose scrub FAILED is shown as failed, not as Never run", () => {
  // A failed scrub clears the copy's class and time, so these are the exact
  // fields the page used to see for it.
  const failedCopy = placement({
    verificationClass: null, verifiedAt: null, assuranceState: "failed", lastCheckedAt: T, lastFailureAt: T,
    lastFailureReason: "sha256 differs from the one recorded at ingest", consecutiveFailures: 3
  });

  it("the Content claim is failed, with the time it failed", () => {
    expect(contentClaim(artifact([failedCopy]), undefined)).toEqual({ status: "failed", achievedClass: "", at: T });
    const r = renderClaim("content", contentClaim(artifact([failedCopy]), undefined), Date.parse("2026-10-10T03:00:00Z"));
    expect(r.state).toBe("failed");
    expect(r.label).toBe("Content check failed");
    expect(r.age).toBe("24h ago");
  });

  it("control: the same copy without the failure record is what used to read Never run", () => {
    const bare = placement({ verificationClass: null, verifiedAt: null });
    expect(contentClaim(artifact([bare]), undefined).status).toBe("never_run");
  });

  it("a failure on one copy is not hidden by a pass on another", () => {
    const good = placement({ medium: "offsite", mediumType: "s3", verificationClass: "content", verifiedAt: "2026-10-01T00:00:00Z", assuranceState: "passed" });
    expect(contentClaim(artifact([failedCopy, good]), undefined).status).toBe("failed");
  });

  it("a check that could not complete is not a pass and not a failure of the copy", () => {
    const c = placement({ assuranceState: "could_not_complete", lastFailureAt: T, lastFailureReason: "the endpoint did not answer", consecutiveFailures: 1 });
    expect(contentClaim(artifact([c]), undefined)).toEqual({ status: "could_not_complete", achievedClass: "", at: T });
  });

  it("exposes the per-copy failure fields: which copy, what ran, how often, when and why", () => {
    const [f] = copyFailures(artifact([failedCopy]));
    expect(f).toEqual({
      medium: "local", kind: "scrub", state: "failed", reason: "sha256 differs from the one recorded at ingest",
      consecutiveFailures: 3, at: T
    });
    expect(failureSentence(f)).toMatch(/^The last scrub of the local copy failed, 3 times in a row \(.*\): sha256 differs from the one recorded at ingest$/);
  });

  it("renders the failure under the chips, as text", () => {
    resetGraphForTests();
    render(
      <ClaimChips assurance={{
        artifactId: "a", copies: 1,
        content: contentClaim(artifact([failedCopy]), undefined),
        recovery: { status: "never_run", achievedClass: "" },
        copyFailures: copyFailures(artifact([failedCopy]))
      }} />
    );
    expect(screen.getByRole("group", { name: /^Content: Content check failed/ })).toBeTruthy();
    const list = screen.getByRole("list", { name: "Copies whose last check did not pass" });
    expect(list.textContent).toContain("sha256 differs from the one recorded at ingest");
    expect(list.textContent).toContain("3 times in a row");
    expect(screen.getByRole("group", { name: /^Content:/ }).textContent).not.toContain("Never run");
    cleanup();
  });
});

describe("the Recovery claim comes from the copies' own recovery records", () => {
  it("is failed when any copy's test failed, and carries when", () => {
    const a = artifact([placement({ recoveryState: "failed", recoveryTestedAt: T, recoveryFailureReason: "the validator rejected the bytes" })]);
    expect(recoveryClaim(a, false, true)).toEqual({ status: "failed", achievedClass: "", at: T });
    expect(copyFailures(a)[0]).toMatchObject({ kind: "recovery_test", state: "failed", reason: "the validator rejected the bytes" });
  });
  it("is a pass only with a recorded pass, and never from an empty record", () => {
    expect(recoveryClaim(artifact([placement({ recoveryState: "passed", recoveryTestedAt: T })]), false, true).status).toBe("pass");
    expect(recoveryClaim(artifact([placement()]), false, true).status).not.toBe("pass");
  });
  it("a policy that turns the test off is not configured, not unknown", () => {
    expect(recoveryClaim(artifact([placement()]), true, true).status).toBe("never_configured");
  });
  it("a test owed and never run is never_run; where nothing reports tests it stays unknown", () => {
    expect(recoveryClaim(artifact([placement()]), false, true).status).toBe("never_run");
    expect(recoveryClaim(artifact([placement()]), false, false).status).toBe("unknown");
  });
});

describe("the production composition offers what exists and no more", () => {
  const fakeRoot = (over: Partial<RetndApi> = {}) => ({ ...createMockApi("assurance-unavailable"), ...over }) as RetndApi;

  it("offers a recovery test, no scrub route and no cancel", () => {
    const a = createHttpAssurance(fakeRoot());
    expect(a.offers).toEqual({ operations: false, recoveryTest: true, recoveryClaim: true });
  });

  it("starts a recovery test through POST .../recovery-test, with no cost acknowledged", async () => {
    const startRecoveryTest = vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "op-1" });
    const a = createHttpAssurance(fakeRoot({ startRecoveryTest }));
    const op = await a.startRecoveryTest(SET);
    expect(startRecoveryTest).toHaveBeenCalledWith("production", "postgres-primary");
    expect(op).toMatchObject({ id: "op-1", kind: "recovery_test", state: "running", setId: SET });
    await expect(a.startScrub(SET)).rejects.toThrow(/no route to start a scrub/);
    await expect(a.cancelOperation("op-1")).rejects.toThrow(/no route to cancel/);
  });

  describe("following a recovery test on the live feed", () => {
    const ev = (sequence: number, event: string, fields: Record<string, string>, result?: SetActivityEvent["result"]): SetActivityEvent => ({
      sequence, at: T, level: "info", event, scope: "set", message: event, fields: { op_id: "op-1", ...fields }, ...(result ? { result } : {})
    });
    const live = (events: SetActivityEvent[], open: boolean) => ({
      observedAt: T, epoch: "e", pollAfterMs: 1000, deployment: null,
      sets: [{
        setId: SET, events, truncated: false, dropped: false, oldestSequence: 1, latestSequence: 9,
        unfinishedActions: open ? [{ action: "recovery_test", actionId: "op-1", startedAt: T, sequence: 1 }] : []
      } as unknown as SetActivity]
    });

    it("reads the stage from recovery_stage lines and finishes on recovery_test_end", async () => {
      const getLiveActivity = vi.fn()
        .mockResolvedValueOnce(live([ev(1, "recovery_test_start", {}), ev(2, "recovery_stage", { stage: "reading", stage_label: "Reading" })], true))
        .mockResolvedValueOnce(live([ev(3, "recovery_test_end", {}, "error")], false));
      const a = createHttpAssurance(fakeRoot({
        startRecoveryTest: vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "op-1" }), getLiveActivity
      }));
      await a.startRecoveryTest(SET);
      const first = await a.readOperation("op-1", 0);
      expect(first.operation).toMatchObject({ state: "running", stage: "reading" });
      expect(first.events.map((e) => e.text)).toEqual(["Recovery test started", "Reading"]);
      expect(first.nextCursor).toBe(2);
      const second = await a.readOperation("op-1", first.nextCursor);
      expect(second.operation.state).toBe("failed");
      expect(second.events[0].text).toBe("Recovery test did not pass");
    });

    it("says untracked, not complete, when the lines fell off the feed", async () => {
      const getLiveActivity = vi.fn()
        .mockResolvedValueOnce(live([ev(1, "recovery_test_start", {})], true))
        .mockResolvedValueOnce(live([], false));
      const a = createHttpAssurance(fakeRoot({
        startRecoveryTest: vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "op-1" }), getLiveActivity
      }));
      await a.startRecoveryTest(SET);
      const first = await a.readOperation("op-1", 0);
      const second = await a.readOperation("op-1", first.nextCursor);
      expect(second.operation.state).toBe("untracked");
    });

    it("reattaches to a test still open on the feed after a reload", async () => {
      const a = createHttpAssurance(fakeRoot({ getLiveActivity: vi.fn().mockResolvedValue(live([], true)) }));
      expect(await a.activeOperation(SET)).toMatchObject({ id: "op-1", state: "running" });
    });
  });
});

describe("the set's Assurance section on a production-shaped server", () => {
  beforeEach(() => resetGraphForTests());
  afterEach(() => cleanup());

  function renderSection(over: Partial<RetndApi> = {}) {
    const root = { ...createMockApi("assurance-unavailable"), ...over } as RetndApi;
    const api = withHttpAssurance(root);
    render(
      <MemoryRouter>
        <ApiProvider api={api}>
          <AssuranceSection setId={SET} readOnly={false} />
        </ApiProvider>
      </MemoryRouter>
    );
    return root;
  }

  it("Run recovery test calls startRecoveryTest; there is no Scrub now button", async () => {
    const startRecoveryTest = vi.fn().mockResolvedValue({ backupSetId: SET, operationId: "op-1" });
    renderSection({
      startRecoveryTest,
      getLiveActivity: vi.fn().mockResolvedValue({ observedAt: T, epoch: "e", pollAfterMs: 1000, deployment: null, sets: [] })
    });
    const run = await screen.findByRole("button", { name: "Run recovery test" });
    expect(screen.queryByRole("button", { name: "Scrub now" })).toBeNull();
    await userEvent.click(run);
    await waitFor(() => expect(startRecoveryTest).toHaveBeenCalledWith("production", "postgres-primary"));
  });

  it("a refused start is shown with the service's own words", async () => {
    const { RetndError } = await import("@shared/api/contracts");
    renderSection({
      startRecoveryTest: vi.fn().mockRejectedValue(new RetndError({
        code: "OPERATION_ALREADY_RUNNING" as never, message: "a recovery test of this backup set is already running", correlationId: "c"
      })),
      getLiveActivity: vi.fn().mockResolvedValue({ observedAt: T, epoch: "e", pollAfterMs: 1000, deployment: null, sets: [] })
    });
    await userEvent.click(await screen.findByRole("button", { name: "Run recovery test" }));
    expect((await screen.findByRole("alert")).textContent).toContain("already running");
  });

  it("says only what the engine does: no route to start a scrub, and the schedule that runs by itself", async () => {
    renderSection();
    const note = await screen.findByTestId("no-run-route");
    expect(note.textContent).toContain("no route to start a scrub");
    expect(note.textContent).toContain("every poll of the running service checks the copies that are due");
    const test = screen.getByTestId("recovery-test-note");
    expect(test.textContent).toContain("only for a set whose mode is required or custom");
    expect(test.textContent).toContain("with the mode off none runs");
    expect(within(screen.getByRole("region", { name: "Assurance" })).queryByText(/runs on the schedule above/)).toBeNull();
  });
});

describe("the global Assurance card", () => {
  beforeEach(() => resetGraphForTests());
  afterEach(() => cleanup());

  it("says what runs by default, names the staging directory once, and has no download-memory knob", async () => {
    render(
      <ApiProvider api={createMockApi("default")}>
        <AssuranceSettingsCard readOnly={false} />
      </ApiProvider>
    );
    expect((await screen.findByTestId("assurance-what-runs")).textContent).toContain("existence check only, with no download");
    const knobs = await screen.findAllByTestId("host-knob");
    expect(knobs.map((k) => k.getAttribute("data-knob"))).toEqual([
      "staging_root", "staging_ttl", "max_concurrent_full_reads", "max_per_destination"
    ]);
    expect(document.body.textContent).toContain("shared by recoveries and recovery tests");
    expect(document.body.textContent).toContain("recovery-staging");
    expect(document.body.textContent).not.toMatch(/download memory/i);
  });

  it("offers no list of registered validators: the validator is the name of a script", async () => {
    render(
      <ApiProvider api={createMockApi("default")}>
        <AssuranceSettingsCard readOnly={false} />
      </ApiProvider>
    );
    const field = await screen.findByRole("textbox", { name: "Validator" });
    expect(field.getAttribute("list")).toBeNull();
    expect(document.querySelector("datalist")).toBeNull();
  });
});

// The old sentences. Every one of these was true of an engine that did not
// exist (a schedule with no consumer, a validator chosen from a registry, a
// memory budget nothing read), and a copy of any of them is a copy of a lie.
describe("the retired sentences are gone from every copy", () => {
  const retired: Array<[string, RegExp]> = [
    ["Scrubs run on the schedule above", /Scrubs run on the schedule above/],
    ["a recovery test runs on its own schedule when its mode is required", /runs on its own schedule when its mode is required/],
    ["the name is checked against the registered validators", /registered validators/i],
    ["the name does not choose the script", /does not (?:choose|select) the script|do(?:es)? not tie the name/i],
    ["the download memory budget", /download memory/i],
    ["web_download_memory_bytes", /web_download_memory_bytes/],
    ["the set is not assured without it", /the set is not assured without it/]
  ];
  const sources: Array<[string, string]> = [
    ["AssuranceSection.tsx", sectionSrc], ["AssuranceSettingsCard.tsx", cardSrc], ["fieldHelpCopy.ts", helpSrc], ["tooltips.json", tooltipsSrc]
  ];
  for (const [what, re] of retired) {
    it("no source says: " + what, () => {
      for (const [name, text] of sources) expect(text, name).not.toMatch(re);
    });
  }
  it("the generated bindings no longer carry the download memory setting", () => {
    expect(contractTsSrc).not.toContain("web_download_memory_bytes");
  });
  it("control: the matcher would have found the sentence it is looking for", () => {
    expect("Scrubs run on the schedule above, and").toMatch(retired[0][1]);
    expect("web_download_memory_bytes: number").toMatch(retired[5][1]);
  });
});
