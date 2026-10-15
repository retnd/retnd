/**
 * Issues #1174 and #1178: the Run assurance and Run validation buttons share
 * one hook (useRunAssurance). The browser specs hold the happy path; these pin
 * what a browser cannot reach through the mock: a build whose engine refuses
 * the start with the destructive gate's code, one that refuses it for another
 * reason, and a server that offers no start route at all.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { RetndError } from "@shared/api/contracts";
import type { RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { ValidationBlock } from "@shared/assurance/ValidationBlock";
import { recoveryTestWouldBeRefused } from "@shared/assurance/verificationModel";
import { resetGraphForTests } from "@shared/state/graph";
import type { BackupSet } from "@shared/types/backup";

const SET = "production/postgres-primary";
const set = { id: SET, source: "production", set: "postgres-primary", validations: [] } as unknown as BackupSet;

function render1178(start: () => Promise<unknown>, offers?: Partial<NonNullable<RetndApi["assurance"]>["offers"]>) {
  const base = createMockApi("default");
  const assurance = base.assurance!;
  const api = {
    ...base,
    assurance: { ...assurance, offers: { ...assurance.offers, ...offers }, startRecoveryTest: start }
  } as unknown as RetndApi;
  render(
    <MemoryRouter>
      <ApiProvider api={api}><ValidationBlock set={set} /></ApiProvider>
    </MemoryRouter>
  );
}

describe("#1178 Run validation on the Validation pane", () => {
  beforeEach(() => resetGraphForTests());
  afterEach(() => cleanup());

  it("a gate refusal is said as one, with the host command, and the button stays usable", async () => {
    const start = vi.fn().mockRejectedValue(new RetndError({ code: "DESTRUCTIVE_OPERATIONS_DISABLED" as never, message: "refused", status: 403, correlationId: "cid_x" }));
    render1178(start);
    const button = await screen.findByRole("button", { name: "Run validation" });
    await userEvent.click(button);
    const refusal = await waitFor(() => {
      const el = document.querySelector("[data-gate-refusal]");
      expect(el).toBeTruthy();
      return el!;
    });
    expect(refusal.textContent).toContain("The recovery test was not started");
    expect(refusal.textContent).toContain("retnd recovery-test " + SET);
    expect(refusal.textContent).toContain("cid_x");
    expect((button as HTMLButtonElement).disabled).toBe(false);
    expect(start).toHaveBeenCalledTimes(1);
  });

  it("any other refusal is the service's own words, not a gate notice", async () => {
    const start = vi.fn().mockRejectedValue(new RetndError({ code: "BACKUP_SET_NOT_FOUND" as never, message: "no such set", status: 404, correlationId: "cid_y" }));
    render1178(start);
    await userEvent.click(await screen.findByRole("button", { name: "Run validation" }));
    expect((await screen.findByRole("alert")).textContent).toContain("no such set");
    expect(document.querySelector("[data-gate-refusal]")).toBeNull();
  });

  it("a server with no start route gets no button, only the command", async () => {
    render1178(vi.fn(), { recoveryTest: false });
    await screen.findByTestId("validation-block");
    expect(screen.queryByRole("button", { name: "Run validation" })).toBeNull();
  });
});

describe("#1174 recoveryTestWouldBeRefused", () => {
  const p = (over: object) => ({ setId: SET, source: "default" as const, mode: "off", ...over });
  it("follows the engine's effective_check, then validator_declared, and treats unknown as not refused", () => {
    expect(recoveryTestWouldBeRefused(p({ effectiveCheck: "none" }))).toBe(true);
    expect(recoveryTestWouldBeRefused(p({ effectiveCheck: "builtin", validatorDeclared: false }))).toBe(false);
    expect(recoveryTestWouldBeRefused(p({ effectiveCheck: "validator" }))).toBe(false);
    expect(recoveryTestWouldBeRefused(p({ validatorDeclared: false }))).toBe(true);
    expect(recoveryTestWouldBeRefused(p({ validatorDeclared: true }))).toBe(false);
    expect(recoveryTestWouldBeRefused(p({}))).toBe(false);
    expect(recoveryTestWouldBeRefused(null)).toBe(false);
    expect(recoveryTestWouldBeRefused(undefined)).toBe(false);
  });
});

// Review follow-ups on #1184: a row's button tests that row's backup, and a
// list of rows reads the capabilities once and follows each operation once.
import { AssuranceDashboardCard } from "@shared/assurance/AssuranceDashboardCard";
import { AssuranceDueList } from "@shared/assurance/AssuranceDueList";
import { resetAssuranceModelForTests } from "@shared/assurance/verificationModel";
import type { BackupFacts, Tally } from "@shared/assurance/verificationModel";
import { act, fireEvent, within } from "@testing-library/react";

function renderTile(over: (api: RetndApi) => RetndApi = (a) => a) {
  const api = over(createMockApi("default"));
  render(
    <MemoryRouter>
      <ApiProvider api={api}><AssuranceDashboardCard /></ApiProvider>
    </MemoryRouter>
  );
  return api;
}

const rowsList = () => screen.findByRole("list", { name: "Backups needing assurance" });

describe("#1174 review: each row runs its own backup, and the list fans out nothing", () => {
  beforeEach(() => { resetGraphForTests(); resetAssuranceModelForTests(); sessionStorage.clear(); });
  afterEach(() => cleanup());

  it("sends the row's own backup to the engine, not just its set", async () => {
    let seen: unknown[] = [];
    renderTile((api) => {
      const a = api.assurance!;
      return { ...api, assurance: { ...a, startRecoveryTest: (...args: unknown[]) => { seen = args; return (a.startRecoveryTest as (...x: unknown[]) => Promise<never>)(...args); } } } as RetndApi;
    });
    const list = await rowsList();
    await userEvent.click(within(list).getByRole("button", { name: "Run assurance for billing-20260827.sql.gz" }));
    await waitFor(() => expect(seen.length).toBeGreaterThan(0));
    expect(seen[0]).toBe("production/billing-mysql");
    expect(seen[1]).toEqual({ artifactId: "production/billing-mysql/billing-20260827.sql.gz" });
  });

  it("reads the capabilities once for the whole list, not once per row", async () => {
    let reads = 0;
    renderTile((api) => ({ ...api, getCapabilities: () => { reads += 1; return api.getCapabilities(); } }) as RetndApi);
    const list = await rowsList();
    expect(within(list).getAllByRole("listitem").length).toBeGreaterThan(1);
    await act(async () => { await new Promise((r) => setTimeout(r, 50)); });
    expect(reads).toBeLessThanOrEqual(1);
  });

  it("two presses in one tick start one test", async () => {
    let starts = 0;
    renderTile((api) => {
      const a = api.assurance!;
      return { ...api, assurance: { ...a, startRecoveryTest: (...args: unknown[]) => { starts += 1; return (a.startRecoveryTest as (...x: unknown[]) => Promise<never>)(...args); } } } as RetndApi;
    });
    const list = await rowsList();
    const button = within(list).getByRole("button", { name: "Run assurance for billing-20260827.sql.gz" });
    fireEvent.click(button);
    fireEvent.click(button);
    await waitFor(() => expect(starts).toBeGreaterThan(0));
    await act(async () => { await new Promise((r) => setTimeout(r, 50)); });
    expect(starts).toBe(1);
  });
});

describe("#1174 review: a long list is capped per bucket", () => {
  afterEach(() => cleanup());

  it("draws at most twenty rows of a bucket and links to the rest on Backups", () => {
    const ids = Array.from({ length: 23 }, (_, i) => "s/a/b-" + String(i).padStart(2, "0") + ".zst");
    const claim = { state: "never_run", label: "Never run", glyph: { id: "never-run" } };
    const byArtifact = Object.fromEntries(ids.map((id) => [id, {
      artifactId: id, setId: "s/a", content: claim, recovery: claim, policy: null
    } as unknown as BackupFacts]));
    const tally = {
      known: true, total: 23, due: 23, overdue: 0, degraded: 0, untested: 23,
      ids: { due: ids, overdue: [], degraded: [], untested: ids }
    } as Tally;
    render(
      <MemoryRouter>
        <ApiProvider api={createMockApi("default")}>
          <AssuranceDueList tally={tally} byArtifact={byArtifact} readOnly={false} />
        </ApiProvider>
      </MemoryRouter>
    );
    expect(within(screen.getByRole("list", { name: "Backups needing assurance" })).getAllByRole("listitem")).toHaveLength(20);
    const more = screen.getByRole("link", { name: /3 more due backups on Backups/ });
    expect(more.getAttribute("href")).toBe("/backups?assurance=due");
  });
});

describe("#1174 review: the production seam sends the backup as artifact_id", () => {
  it("passes the row's backup through to POST .../recovery-test", async () => {
    const { createHttpAssurance } = await import("@shared/assurance/realAssurance");
    const calls: unknown[][] = [];
    const base = createMockApi("default");
    const api = {
      ...base,
      startRecoveryTest: async (...args: unknown[]) => { calls.push(args); return { backupSetId: "production/billing-mysql", operationId: "op_1", coalesced: false }; }
    } as unknown as RetndApi;
    const seam = createHttpAssurance(api);
    await seam.startRecoveryTest("production/billing-mysql", { artifactId: "production/billing-mysql/billing-20260827.sql.gz" });
    await seam.startRecoveryTest("production/billing-mysql");
    expect(calls[0]).toEqual(["production", "billing-mysql", { artifactId: "production/billing-mysql/billing-20260827.sql.gz" }]);
    expect(calls[1]).toEqual(["production", "billing-mysql"]);
  });
});

describe("#1174 review: one test per set at a time", () => {
  afterEach(() => cleanup());

  it("while one row's test runs, the other rows of that set wait and say why", async () => {
    const ids = ["s/a/one.zst", "s/a/two.zst", "s/b/three.zst"];
    const claim = { state: "never_run", label: "Never run", glyph: { id: "never-run" } };
    const byArtifact = Object.fromEntries(ids.map((id) => [id, {
      artifactId: id, setId: id.split("/").slice(0, 2).join("/"), content: claim, recovery: claim, policy: null
    } as unknown as BackupFacts]));
    const tally = { known: true, total: 3, due: 3, overdue: 0, degraded: 0, untested: 3, ids: { due: ids, overdue: [], degraded: [], untested: ids } } as Tally;
    const running = { id: "op-a", kind: "recovery_test", setId: "s/a", state: "running", stage: "resolving_copy", bytes: 0, totalBytes: 0, startedAt: "" };
    const base = createMockApi("default");
    const api = {
      ...base,
      assurance: {
        ...base.assurance!,
        startRecoveryTest: vi.fn().mockResolvedValue(running),
        readOperation: vi.fn().mockResolvedValue({ operation: running, events: [], nextCursor: 0 })
      }
    } as unknown as RetndApi;
    render(
      <MemoryRouter>
        <ApiProvider api={api}><AssuranceDueList tally={tally} byArtifact={byArtifact} readOnly={false} /></ApiProvider>
      </MemoryRouter>
    );
    await userEvent.click(screen.getByRole("button", { name: "Run assurance for one.zst" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Run assurance for two.zst" })).toBeDisabled());
    const two = screen.getByRole("button", { name: "Run assurance for two.zst" }).closest("li")!;
    expect(two.textContent).toContain("A recovery test of one.zst is running for this set");
    // Another set is not held up.
    expect(screen.getByRole("button", { name: "Run assurance for three.zst" })).toBeEnabled();
  });
});
