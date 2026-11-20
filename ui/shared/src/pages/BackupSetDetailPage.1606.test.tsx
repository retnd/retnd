/**
 * Issue #1606: the edit hold's heartbeat against a hold that has a holder.
 *
 * Taking and renewing used to be the same call, so a renewal still in
 * flight when edit mode was left could land after the release and put the
 * hold back for another 90 seconds, stopping any backup run that had
 * started in between. The engine now gives each take a token, a renewal
 * names it, and a renewal whose hold was released or lapsed is refused
 * with EDIT_HOLD_GONE instead of creating one.
 *
 * What the page has to do with that is what these cases pin: renew under
 * the token its own take answered, release with it, treat EDIT_HOLD_GONE
 * as "the hold is gone" rather than as an error, and take a fresh hold
 * only while it is still in edit mode. A fresh hold that answers after the
 * page has left edit mode is given straight back, or it would be the same
 * bug by another road.
 *
 * The heartbeat is caught and run by hand, as in the #1593 file beside
 * this one, so nothing else on the page that keeps a timer fires with it.
 */
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import { RetndError } from "@shared/api/contracts";
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

const FIRST: EditHoldTaken = { token: "hold-first", expiresAt: "2026-11-20T06:01:30Z", stopped: null };
const SECOND: EditHoldTaken = { token: "hold-second", expiresAt: "2026-11-20T06:03:00Z", stopped: null };

const gone = () =>
  new RetndError({
    code: "EDIT_HOLD_GONE",
    message: "this edit hold was released or has lapsed",
    correlationId: "cid_1606"
  });

interface Editing {
  source: string;
  set: string;
  beat: () => void;
  unmount: () => void;
}

/** Opens edit mode on the mock's first set with `take` answering FIRST,
 *  and hands back the heartbeat the page armed. */
async function editing(api: RetndApi): Promise<Editing> {
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
  return { source: target.source, set: target.set, beat: call![0] as () => void, unmount };
}

describe("the edit hold's holder token (#1606)", () => {
  it("renews under the token its own take answered, rather than taking again", async () => {
    const api = createMockApi();
    const take = vi.spyOn(api, "takeEditHold").mockResolvedValue(FIRST);
    const renew = vi.spyOn(api, "renewEditHold").mockResolvedValue(FIRST);
    const { source, set, beat } = await editing(api);

    await act(async () => {
      beat();
      await probe.settle();
    });

    expect(renew).toHaveBeenCalledWith(source, set, FIRST.token);
    expect(take).toHaveBeenCalledTimes(1);
  });

  it("releases with its own token when edit mode is left", async () => {
    const api = createMockApi();
    vi.spyOn(api, "takeEditHold").mockResolvedValue(FIRST);
    const release = vi.spyOn(api, "releaseEditHold");
    const { source, set } = await editing(api);

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "SAVE ALL & EXIT EDIT" }));
    });

    await waitFor(() => expect(release).toHaveBeenCalledWith(source, set, FIRST.token));
  });

  // The issue's race, from the page's side: the renewal was out when edit
  // mode was left, the release went first, and the engine refused the
  // renewal. The page must take nothing back and say nothing.
  it("takes nothing back when a renewal refused after edit mode was left answers", async () => {
    const api = createMockApi();
    const take = vi.spyOn(api, "takeEditHold").mockResolvedValue(FIRST);
    const renewal = held<EditHoldTaken>();
    vi.spyOn(api, "renewEditHold").mockImplementation(() => renewal.promise);
    const release = vi.spyOn(api, "releaseEditHold");
    const { source, set, beat, unmount } = await editing(api);

    beat();
    unmount();
    probe.tearDown();
    renewal.reject(gone());
    await probe.settle();

    expect(release).toHaveBeenCalledWith(source, set, FIRST.token);
    expect(take).toHaveBeenCalledTimes(1);
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  // Still editing, and the hold is gone (it lapsed while the laptop slept,
  // somebody released it from a terminal, or the engine restarted). The
  // form is open, so the page takes a fresh hold, renews under the new
  // token from then on, and shows the operator no error: there is nothing
  // for them to do.
  it("takes a fresh hold, and renews under its token, when the hold is gone while still editing", async () => {
    const api = createMockApi();
    const take = vi.spyOn(api, "takeEditHold").mockResolvedValueOnce(FIRST).mockResolvedValueOnce(SECOND);
    const renew = vi
      .spyOn(api, "renewEditHold")
      .mockImplementationOnce(() => Promise.reject(gone()))
      .mockResolvedValue(SECOND);
    const { source, set, beat } = await editing(api);

    await act(async () => {
      beat();
      await probe.settle();
    });
    expect(take).toHaveBeenCalledTimes(2);

    await act(async () => {
      beat();
      await probe.settle();
    });
    expect(renew).toHaveBeenLastCalledWith(source, set, SECOND.token);
    expect(screen.queryByText(/released or has lapsed/i)).toBeNull();
    expect(screen.getByRole("button", { name: "SAVE ALL & EXIT EDIT" })).toBeInTheDocument();
  });

  // The fresh hold is itself a request that can be out when edit mode is
  // left. If it answers afterwards it is a hold nobody will renew or
  // release, so the page gives it straight back.
  it("gives back a fresh hold that answers after edit mode was left", async () => {
    const api = createMockApi();
    const retake = held<EditHoldTaken>();
    vi.spyOn(api, "takeEditHold").mockResolvedValueOnce(FIRST).mockImplementationOnce(() => retake.promise);
    vi.spyOn(api, "renewEditHold").mockImplementationOnce(() => Promise.reject(gone()));
    const release = vi.spyOn(api, "releaseEditHold");
    const { source, set, beat, unmount } = await editing(api);

    await act(async () => {
      beat();
      await probe.settle();
    });
    unmount();
    probe.tearDown();
    retake.resolve(SECOND);
    await probe.settle();

    expect(release).toHaveBeenCalledWith(source, set, SECOND.token);
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  // A renewal that fails for any other reason is still swallowed, as it
  // always was: the lease simply lapses, which is the safe direction.
  it("does not take a fresh hold when a renewal fails for some other reason", async () => {
    const api = createMockApi();
    const take = vi.spyOn(api, "takeEditHold").mockResolvedValue(FIRST);
    vi.spyOn(api, "renewEditHold").mockRejectedValue(new TypeError("Failed to fetch"));
    const { beat } = await editing(api);

    await act(async () => {
      beat();
      await probe.settle();
    });

    expect(take).toHaveBeenCalledTimes(1);
    expect(probe.rejections).toEqual([]);
  });
});
