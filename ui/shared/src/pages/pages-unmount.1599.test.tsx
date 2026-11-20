/**
 * Issue #1599, the pages batch: two of the busiest handlers on the pages,
 * each of which awaits a request and then sets state with nothing checking
 * the page is still there.
 *
 *  - BackupSetDetailPage's Edit asks whether a cycle is running before it
 *    holds anything, then sets the warning or enters edit mode.
 *  - A destination row's Test connection sets the report, the usage and busy.
 *
 * Each case holds the request, unmounts, tears down and only then answers;
 * see test/teardownProbe.ts.
 */
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { EditHoldState, EditHoldTaken, MediumPreflight, RetndApi } from "@shared/api/contracts";
import { createMockApi, resetMockFixtures } from "@shared/api/mock";
import { resetGraphForTests } from "@shared/state/graph";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import { backupSetPath } from "@shared/utilities/routes";
import { BackupSetDetailPage } from "./BackupSetDetailPage";
import { StorageDestinationsCard } from "./StorageDestinationsCard";

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
  resetGraphForTests();
  resetMockFixtures();
  vi.restoreAllMocks();
});

async function detailPage(api: RetndApi) {
  const target = (await createMockApi().listSets())[0];
  vi.spyOn(api, "workflowRecovery").mockResolvedValue([]);
  const view = render(
    <MemoryRouter initialEntries={[backupSetPath(target.source, target.set)]}>
      <ApiProvider api={api}>
        <Routes>
          <Route path="/sets/:source/:set" element={<BackupSetDetailPage readOnly={false} />} />
        </Routes>
      </ApiProvider>
    </MemoryRouter>
  );
  await screen.findByText(target.name);
  return view;
}

describe("BackupSetDetailPage's Edit after unmount (#1599)", () => {
  it("ignores the running-work check when it answers after the page has gone", async () => {
    const answer = held<EditHoldState>();
    const api = createMockApi();
    vi.spyOn(api, "getEditHold").mockReturnValue(answer.promise);
    const { unmount } = await detailPage(api);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Edit" })); });
    expect(api.getEditHold).toHaveBeenCalled();
    unmount();
    probe.tearDown();
    answer.resolve({ running: null } as unknown as EditHoldState);
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a failed running-work check after the page has gone", async () => {
    const answer = held<EditHoldState>();
    const api = createMockApi();
    vi.spyOn(api, "getEditHold").mockReturnValue(answer.promise);
    const { unmount } = await detailPage(api);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Edit" })); });
    unmount();
    probe.tearDown();
    answer.reject(new Error("the engine went away"));
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("releases a hold that was taken after the page had gone, with its own token", async () => {
    const taken = held<EditHoldTaken>();
    const api = createMockApi();
    vi.spyOn(api, "takeEditHold").mockReturnValue(taken.promise);
    const release = vi.spyOn(api, "releaseEditHold").mockResolvedValue(undefined);
    const target = (await createMockApi().listSets())[0];
    const { unmount } = await detailPage(api);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Edit" })); });
    await waitFor(() => expect(api.takeEditHold).toHaveBeenCalled());
    unmount();
    probe.tearDown();
    taken.resolve({ token: "tok-late", expiresAt: "2030-01-01T00:00:00Z", stopped: null });
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
    expect(release).toHaveBeenCalledWith(target.source, target.set, "tok-late");
  });
});

describe("a destination row's Test connection after unmount (#1599)", () => {
  async function pressTest(preflight: () => Promise<MediumPreflight>) {
    const call = vi.fn(preflight);
    const api = { ...createMockApi(), preflightStorageMedium: call } as RetndApi;
    const view = render(
      <MemoryRouter>
        <ApiProvider api={api}>
          <StorageDestinationsCard readOnly={false} onChanged={() => undefined} />
        </ApiProvider>
      </MemoryRouter>
    );
    const buttons = await screen.findAllByRole("button", { name: "Test connection" });
    fireEvent.click(buttons[0]);
    await waitFor(() => expect(call).toHaveBeenCalled());
    return view;
  }

  it("ignores a check that answers after the card has gone", async () => {
    const answer = held<MediumPreflight>();
    const real = await createMockApi().preflightStorageMedium((await createMockApi().listStorageMediums())[0].id);
    const { unmount } = await pressTest(() => answer.promise);
    unmount();
    probe.tearDown();
    answer.resolve(real);
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a check that fails after the card has gone", async () => {
    const answer = held<MediumPreflight>();
    const { unmount } = await pressTest(() => answer.promise);
    unmount();
    probe.tearDown();
    answer.reject(new Error("the engine went away"));
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("still shows the busy state clearing while the card is there", async () => {
    const answer = held<MediumPreflight>();
    await pressTest(() => answer.promise);
    expect(screen.getByRole("button", { name: "Working…" })).toBeDisabled();
    answer.reject(new Error("the engine went away"));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Working…" })).toBeNull());
  });
});
