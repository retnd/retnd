/**
 * Issue #1593: the wizard's host-key probe once the wizard has gone.
 *
 * Opening the Connection test step probes the host for its key, and so does
 * "Re-fetch fingerprint". probeHost() sets five pieces of state after its
 * await, and the effect that starts it had no cleanup at all, so an operator
 * who left the wizard while the probe was out (or a test that unmounted it)
 * had every one of those land on a page that was gone. After teardown React's
 * read of `window.event` throws inside the async function, its promise
 * rejects, and nothing holds it. test/teardownProbe.ts says why each case
 * holds the request open, unmounts, and only then lets it answer.
 */
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { HostKeyProbeResult, RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { PlatformProvider } from "@shared/platform/PlatformContext";
import { resetGraphForTests } from "@shared/state/graph";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import { genericBridge } from "../../../../apps/generic/frontend/platform";
import { BackupSetWizardPage } from "./BackupSetWizardPage";

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
  resetGraphForTests();
});

const KEY: HostKeyProbeResult = {
  algorithm: "ssh-ed25519",
  fingerprint: "SHA256:fixture1593",
  knownHostsLine: "db.example.internal ssh-ed25519 AAAAfixture1593"
};

/** The wizard on its Connection test step, with every host-key probe
 *  answered from `answers` in order. */
function wizard(answers: Array<Promise<HostKeyProbeResult>>) {
  const calls: number[] = [];
  const api: RetndApi = {
    ...createMockApi(),
    probeHostKey: () => {
      calls.push(calls.length);
      return answers[calls.length - 1] ?? new Promise(() => {});
    }
  };
  const view = render(
    <MemoryRouter>
      <ApiProvider api={api}>
        <PlatformProvider bridge={genericBridge}>
          <BackupSetWizardPage readOnly={false} />
        </PlatformProvider>
      </ApiProvider>
    </MemoryRouter>
  );
  fireEvent.click(screen.getByRole("button", { name: "Connection test" }));
  return { ...view, calls };
}

describe("the host-key probe after unmount (#1593)", () => {
  it("ignores a probe that answers after the wizard has gone", async () => {
    const answer = held<HostKeyProbeResult>();
    const { unmount, calls } = wizard([answer.promise]);
    expect(calls).toHaveLength(1);

    unmount();
    probe.tearDown();
    answer.resolve(KEY);
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a probe that fails after the wizard has gone", async () => {
    const answer = held<HostKeyProbeResult>();
    const { unmount } = wizard([answer.promise]);

    unmount();
    probe.tearDown();
    answer.reject(new Error("no route to host"));
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("ignores a re-fetch that answers after the wizard has gone", async () => {
    const again = held<HostKeyProbeResult>();
    const { unmount, calls } = wizard([Promise.resolve(KEY), again.promise]);
    const refetch = await screen.findByRole("button", { name: "Re-fetch fingerprint" });
    await waitFor(() => expect(refetch).toBeEnabled());
    fireEvent.click(refetch);
    expect(calls).toHaveLength(2);

    unmount();
    probe.tearDown();
    again.resolve(KEY);
    await probe.settle();

    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  // The control: a wizard that is still there takes the key and offers to
  // trust it.
  it("still offers to trust a key that answers while the wizard is there", async () => {
    const answer = held<HostKeyProbeResult>();
    wizard([answer.promise]);
    answer.resolve(KEY);
    await waitFor(() => expect(screen.getByRole("button", { name: "Trust host" })).toBeEnabled());
  });
});
