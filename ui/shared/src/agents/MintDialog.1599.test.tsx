/**
 * Issue #1599: Create token awaits the mint and then sets the reveal, the
 * error and busy. The dialog holds Cancel while the request is out, so the
 * only way it unmounts mid-request is the page going with it (or a test
 * ending), and then each of those setters is the #1589 shape. The secret in a
 * late answer is dropped with the dialog, which is what the dialog's own
 * header already says about unmounting.
 *
 * Each case holds the mint, unmounts, tears down and only then answers; see
 * test/teardownProbe.ts.
 */
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { resetGraphForTests } from "@shared/state/graph";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import type { MintedAgentToken } from "@shared/types/agents";
import { MintDialog } from "./MintDialog";

let probe: TeardownProbe;
beforeEach(() => { resetGraphForTests(); probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
});

async function pressCreate(mint: () => Promise<MintedAgentToken>) {
  const base = createMockApi("default");
  const minted = await base.mintAgentToken({ name: "nightly", scope: "read", expiresIn: "90d" });
  const call = vi.fn(mint);
  const api = { ...base, getCapabilities: undefined, mintAgentToken: call } as unknown as RetndApi;
  const view = render(
    <ApiProvider api={api}>
      <MintDialog tokens={[]} onMinted={() => undefined} onUncertain={() => undefined} onClose={() => undefined} />
    </ApiProvider>
  );
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "nightly" } });
  fireEvent.click(screen.getByRole("button", { name: "Create token" }));
  await waitFor(() => expect(call).toHaveBeenCalled());
  return { ...view, minted };
}

describe("MintDialog after unmount (#1599)", () => {
  it("drops a token that arrives after the dialog has gone", async () => {
    const answer = held<MintedAgentToken>();
    const { unmount, minted } = await pressCreate(() => answer.promise);
    unmount();
    probe.tearDown();
    answer.resolve(minted);
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("drops a refusal that arrives after the dialog has gone", async () => {
    const answer = held<MintedAgentToken>();
    const { unmount } = await pressCreate(() => answer.promise);
    unmount();
    probe.tearDown();
    answer.reject(new Error("no reply"));
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("still reveals the token while it's mounted", async () => {
    const answer = held<MintedAgentToken>();
    const { minted } = await pressCreate(() => answer.promise);
    await act(async () => { answer.resolve(minted); });
    expect(await screen.findByRole("button", { name: /I have stored it/i })).toBeInTheDocument();
  });
});
