/**
 * Issue #1589 on the verification page: it set state on the verification's
 * answer even when the page had already unmounted. test/teardownProbe.ts
 * says why that fails a run, and why holding the answer is the way to
 * catch it.
 *
 * The re-read the page publishes into the shared graph is a different
 * thing, and is meant to outlive the page (the banner above the router is
 * drawn from it), so the success case also checks that it still goes out.
 */
import { cleanup, render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { RecoverySettings, RetndApi } from "@shared/api/contracts";
import { resetGraphForTests } from "@shared/state/graph";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import { VerifyEmailPage } from "./VerifyEmailPage";

let probe: TeardownProbe;
beforeEach(() => {
  resetGraphForTests();
  probe = teardownProbe();
});
afterEach(() => {
  probe.dispose();
  cleanup();
  resetGraphForTests();
});

function renderPage(api: RetndApi) {
  return render(
    <MemoryRouter initialEntries={["/verify-email?token=verify-tok-1589"]}>
      <ApiProvider api={api}>
        <Routes>
          <Route path="/verify-email" element={<VerifyEmailPage />} />
        </Routes>
      </ApiProvider>
    </MemoryRouter>
  );
}

describe("VerifyEmailPage after unmount (#1589)", () => {
  it("ignores a verification that succeeds after the page has gone, and still re-reads the account", async () => {
    const answer = held<void>();
    // Refused, as it is for the usual unauthenticated browser: the claim is
    // only that the re-read is still asked for.
    const getRecoverySettings = vi.fn(() => Promise.reject<RecoverySettings>(new Error("not signed in")));
    const api = { verifyRecoveryEmail: () => answer.promise, getRecoverySettings } as unknown as RetndApi;

    const { unmount } = renderPage(api);
    unmount();
    probe.tearDown();
    answer.resolve();
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
    expect(getRecoverySettings).toHaveBeenCalledTimes(1);
  });

  it("ignores a verification that fails after the page has gone", async () => {
    const answer = held<void>();
    const api = { verifyRecoveryEmail: () => answer.promise, getRecoverySettings: vi.fn() } as unknown as RetndApi;

    const { unmount } = renderPage(api);
    unmount();
    probe.tearDown();
    answer.reject(new Error("the link has expired"));
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });
});
