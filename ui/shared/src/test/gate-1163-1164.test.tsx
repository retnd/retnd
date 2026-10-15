/**
 * Issues #1163 and #1164 in the shared UI.
 *
 * What the engine now asks for, and what it no longer does, as the screens
 * have to honour it:
 *
 *   - a restore on a local-account profile sends the administrator's password
 *     again and an explicit acknowledgement, and asks for neither where the
 *     gate is shut (there is nothing to ask for, and nothing will run);
 *   - releasing a hold sends an explicit acknowledgement on every profile;
 *   - verify, hold and release-hold carry no shut-gate notice and no
 *     refusal copy, because the engine does not refuse them;
 *   - the capability field is read, not assumed, for each of those.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { ApiProvider } from "@shared/api/ApiContext";
import type { RetndApi } from "@shared/api/contracts";
import { createMockApi, resetMockFixtures } from "@shared/api/mock";
import type { Scenario } from "@shared/api/mock";
import { SnapshotDetailPage } from "@shared/pages/SnapshotDetailPage";
import { SnapshotRestorePage } from "@shared/pages/SnapshotRestorePage";
import { SnapshotsPage } from "@shared/pages/SnapshotsPage";
import { graph, resetGraphForTests } from "@shared/state/graph";
import { setsNode, versionNode } from "@shared/state/appNodes";
import type { VersionInfo } from "@shared/types/operation";
import { restorePath, snapshotPath, snapshotsPath } from "@shared/utilities/routes";

const VERSION: VersionInfo = {
  api: "v1", service: "1.3.0", buildCommit: "9f4c1ab", goVersion: "go1.27.0",
  engine: "1.68.2", configRevision: "cfg_9f4c1ab", ready: true, compatible: true
};

async function seed(api: RetndApi) {
  const sets = await api.listSets();
  act(() => {
    graph.commit("test/seed", (tx) => {
      tx.set(versionNode, { data: VERSION, error: null, loading: false });
      tx.set(setsNode, { data: sets, error: null, loading: false });
    });
  });
}

function renderAt(api: RetndApi, path: string, pattern: string, element: JSX.Element) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ApiProvider api={api}>
        <Routes>
          <Route path={pattern} element={element} />
        </Routes>
      </ApiProvider>
    </MemoryRouter>
  );
}

const press = (name: string | RegExp) =>
  act(() => {
    screen.getByRole("button", { name }).click();
  });

async function walkRestoreToConfirm() {
  await screen.findByText(/newest known-good/);
  press("Where");
  fireEvent.change(screen.getByLabelText("Restore into"), { target: { value: "/data/restores/x" } });
  press("Confirm");
}

const RUN = "run-2026-09-13-0400";

describe("#1163 a restore on a local-account profile", () => {
  afterEach(() => {
    cleanup();
    resetGraphForTests();
    resetMockFixtures();
    vi.restoreAllMocks();
  });

  const restorePage = (api: RetndApi) =>
    renderAt(api, restorePath("production", "postgres-primary", RUN), "/sets/:source/:set/restore", <SnapshotRestorePage readOnly={false} />);

  it("asks for the password and an acknowledgement, and sends both", async () => {
    const api = createMockApi("local-account-profile" as Scenario);
    const restore = vi.spyOn(api, "restoreSnapshot");
    await seed(api);
    restorePage(api);
    await walkRestoreToConfirm();

    expect((await screen.findByRole("button", { name: "Start restore" })).hasAttribute("disabled")).toBe(true);
    fireEvent.change(await screen.findByLabelText("Your password"), { target: { value: "correct-horse-battery" } });
    expect(screen.getByRole("button", { name: "Start restore" }).hasAttribute("disabled"), "the password alone is not the acknowledgement").toBe(true);
    act(() => {
      screen.getByRole("checkbox", { name: /I understand/ }).click();
    });
    press("Start restore");

    await waitFor(() => expect(restore).toHaveBeenCalledTimes(1));
    const sent = restore.mock.calls[0][0];
    expect(sent.stepUpPassword).toBe("correct-horse-battery");
    expect(sent.acknowledged).toBe(true);
  });

  it("says a wrong password plainly and keeps the form", async () => {
    const api = createMockApi("local-account-profile" as Scenario);
    await seed(api);
    restorePage(api);
    await walkRestoreToConfirm();
    fireEvent.change(await screen.findByLabelText("Your password"), { target: { value: "nope" } });
    act(() => {
      screen.getByRole("checkbox", { name: /I understand/ }).click();
    });
    press("Start restore");
    const alert = await waitFor(() => {
      const el = document.querySelector("[data-step-up-error]");
      if (el === null) throw new Error("no step-up error");
      return el;
    });
    expect(alert.textContent).toMatch(/password/i);
    expect((screen.getByLabelText("Your password") as HTMLInputElement).value).toBe("nope");
  });

  it("does not ask for a password where the gate is shut", async () => {
    const api = createMockApi("gateway-profile" as Scenario);
    await seed(api);
    restorePage(api);
    await walkRestoreToConfirm();
    expect(screen.queryByLabelText("Your password")).toBeNull();
    expect(document.querySelector("[data-gate-notice]")).not.toBeNull();
  });

  it("does not ask for one on a deployment whose engine does not say it needs one", async () => {
    const api = createMockApi();
    await seed(api);
    restorePage(api);
    await walkRestoreToConfirm();
    expect(screen.queryByLabelText("Your password")).toBeNull();
  });
});

describe("#1164 verify, hold and release-hold", () => {
  afterEach(() => {
    cleanup();
    resetGraphForTests();
    resetMockFixtures();
    vi.restoreAllMocks();
  });

  for (const scenario of ["gateway-profile", "local-account-profile"] as const) {
    it(`${scenario}: the snapshot list carries no gate notice and Verify latest is served`, async () => {
      const api = createMockApi(scenario as Scenario);
      const verify = vi.spyOn(api, "verifySnapshot");
      await seed(api);
      renderAt(api, snapshotsPath("production", "postgres-primary"), "/sets/:source/:set/snapshots", <SnapshotsPage readOnly={false} />);
      const verifyButton = await screen.findByRole("button", { name: "Verify latest" });
      // Disabled until the snapshots are read: pressing it before then does nothing.
      await waitFor(() => expect(verifyButton.hasAttribute("disabled")).toBe(false));
      expect(document.querySelector("[data-gate-notice]")).toBeNull();
      press("Verify latest");
      await waitFor(() => expect(verify).toHaveBeenCalledTimes(1));
      expect(document.querySelector("[data-gate-refusal]")).toBeNull();
    });

    it(`${scenario}: releasing a hold needs the acknowledgement and sends it`, async () => {
      const api = createMockApi(scenario as Scenario);
      const release = vi.spyOn(api, "releaseSnapshotHold");
      await seed(api);
      // Z6 and Z5 sit on the same run. The mock's holds are shared state that
      // resetMockFixtures does not rewind, so each scenario releases its own.
      const holdId = scenario === "gateway-profile" ? "hold_01J9Z6" : "hold_01J9Z5";
      renderAt(api, snapshotPath("production", "postgres-primary", "run-2026-09-12-1000"), "/sets/:source/:set/snapshots/:runId", <SnapshotDetailPage readOnly={false} />);
      await screen.findByRole("button", { name: "Release hold " + holdId });
      expect(document.querySelector("[data-gate-notice]")).toBeNull();

      press("Release hold " + holdId);
      expect(screen.getByRole("button", { name: "Release hold" }).hasAttribute("disabled")).toBe(true);
      act(() => {
        screen.getByRole("checkbox", { name: /I understand/ }).click();
      });
      expect(screen.getByRole("button", { name: "Release hold" }).hasAttribute("disabled")).toBe(false);
      press("Release hold");

      await waitFor(() => expect(release).toHaveBeenCalledTimes(1));
      expect(release.mock.calls[0][0].acknowledged).toBe(true);
      expect(release.mock.calls[0][0].holdId).toBe(holdId);
    });
  }

  it("the mock refuses a release that did not carry the acknowledgement, as the engine does", async () => {
    const api = createMockApi("gateway-profile" as Scenario);
    await expect(
      api.releaseSnapshotHold({
        backupSetId: "production/postgres-primary",
        holdId: "hold_01J9Z6",
        configRevision: "cfg_9f4c1ab",
        idempotencyKey: "k",
        acknowledged: false
      })
    ).rejects.toMatchObject({ api: { code: "ACKNOWLEDGEMENT_REQUIRED" } });
  });

  it("restore stays behind the gate on a gateway profile", async () => {
    const api = createMockApi("gateway-profile" as Scenario);
    await expect(
      api.restoreSnapshot({
        backupSetId: "production/postgres-primary",
        targetPath: "/x",
        configRevision: "cfg_9f4c1ab",
        idempotencyKey: "k"
      })
    ).rejects.toMatchObject({ api: { code: "DESTRUCTIVE_OPERATIONS_DISABLED" } });
  });
});
