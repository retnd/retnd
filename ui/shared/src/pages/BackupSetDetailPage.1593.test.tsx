/**
 * Issue #1593: the edit hold's heartbeat once the page has gone.
 *
 * Since #1606 the heartbeat is a renewal under the page's own holder
 * token (renewEditHold) rather than a second take, so the cases below hold
 * that call open; BackupSetDetailPage.1606.test.tsx covers the token.
 *
 * While edit mode is open the page renews its hold on a 30 second interval.
 * Leaving the page clears that interval and releases the hold, and does
 * nothing about a renewal already out: its answer still called setStopped
 * on a page that had unmounted. The renewal's .catch() swallows the throw
 * React makes after teardown, so this one never surfaced as a rejection,
 * but it is a state update on an unmounted page all the same, and the probe
 * counts those. test/teardownProbe.ts says why each case holds the request
 * open, unmounts, and only then lets it answer.
 *
 * The case catches the heartbeat's own callback and runs it, rather than
 * faking the clock, so nothing else on the page that keeps a timer fires
 * with it.
 */
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { EditHoldTaken, RetndApi } from "@shared/api/contracts";
import { createMockApi, resetMockFixtures } from "@shared/api/mock";
import { resetGraphForTests } from "@shared/state/graph";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import { backupSetPath } from "@shared/utilities/routes";
import { BackupSetDetailPage } from "./BackupSetDetailPage";

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
  resetGraphForTests();
  resetMockFixtures();
  vi.restoreAllMocks();
});

/** Opens edit mode on the mock's first set and hands back the heartbeat:
 *  the callback the page gave setInterval with the hold's 30 second period. */
async function editing(api: RetndApi): Promise<{ beat: () => void; unmount: () => void }> {
  const target = (await createMockApi().listSets())[0];
  vi.spyOn(api, "workflowRecovery").mockResolvedValue([]);
  const every = vi.spyOn(globalThis, "setInterval");
  const { unmount } = render(
    <MemoryRouter initialEntries={[backupSetPath(target.source, target.set)]}>
      <ApiProvider api={api}>
        <Routes>
          <Route path="/sets/:source/:set" element={<BackupSetDetailPage readOnly={false} />} />
        </Routes>
      </ApiProvider>
    </MemoryRouter>
  );
  await screen.findByText(target.name);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  });
  await screen.findByRole("button", { name: "SAVE ALL & EXIT EDIT" });
  const call = every.mock.calls.find((c) => c[1] === 30_000);
  expect(call, "the page armed its heartbeat").toBeDefined();
  return { beat: call![0] as () => void, unmount };
}

const STOPPED: EditHoldTaken = {
  token: "hold-1593",
  expiresAt: "2026-11-20T06:00:00Z",
  stopped: { artifact: "2026-11-20T05-00.dump", stage: "transferring" }
};

describe("the edit hold's heartbeat after unmount (#1593)", () => {
  it("ignores a renewal that answers after the page has gone", async () => {
    const api = createMockApi();
    const renewal = held<EditHoldTaken>();
    const renew = vi.spyOn(api, "renewEditHold");
    const { beat, unmount } = await editing(api);
    renew.mockImplementation(() => renewal.promise);

    beat();
    unmount();
    probe.tearDown();
    renewal.resolve(STOPPED);
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  // The control: on a page that is still there, a renewal that stopped a
  // cycle says so through the banner, as it always did.
  it("still reports a cycle the renewal stopped while the page is there", async () => {
    const api = createMockApi();
    const renewal = held<EditHoldTaken>();
    const renew = vi.spyOn(api, "renewEditHold");
    const { beat } = await editing(api);
    renew.mockImplementation(() => renewal.promise);

    beat();
    await act(async () => {
      renewal.resolve(STOPPED);
      await probe.settle();
    });
    expect(screen.getByText(/2026-11-20T05-00\.dump/)).toBeInTheDocument();
  });
});
