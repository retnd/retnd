/**
 * Issue #1599, found in review of the first batch: a recovery onto the source
 * host asks about an unencrypted connection before it sends the password, and
 * that prompt is the longest wait in either dialog, because a person answers
 * it. The dialog can go while it is open (a route change, a test ending), and
 * what follows the answer used to set state on the gone component, start the
 * recovery with the password for a run nobody can see, and clear the
 * submitting flag on the dead hook.
 *
 * A closed dialog starts nothing. That's the choice MintDialog makes too: the
 * operator never saw a run begin, and the password was only ever meant for
 * the one they were looking at. A recovery already sent still finishes.
 *
 * Each case holds the prompt, unmounts, tears down and only then answers; see
 * test/teardownProbe.ts.
 */
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import type { RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { setInsecureTransportConfirmer } from "@shared/api/client";
import { RecoverArtifactDialog } from "@shared/assurance/RecoverArtifact";
import { RecoverSetDialog } from "@shared/assurance/RecoverSet";
import { resetGraphForTests } from "@shared/state/graph";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";

const SET = "production/postgres-primary";
const ARTIFACT = SET + "/postgres-prod-20260828.dump.zst";

let probe: TeardownProbe;
beforeEach(() => { resetGraphForTests(); probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  setInsecureTransportConfirmer(null);
  cleanup();
});

/** Presses Recover with the source host chosen, ticked and its password
 *  typed, and returns once the insecure-connection prompt is open. */
async function pressRecoverWithPromptOpen(answer: ReturnType<typeof held<boolean>>) {
  const base = createMockApi("insecure-transport");
  const recoverMany = vi.fn(base.recoverMany);
  const api = { ...base, recoverMany } as unknown as RetndApi;
  const asked = vi.fn(() => answer.promise);
  setInsecureTransportConfirmer(asked);
  const view = render(
    <ApiProvider api={api}><RecoverSetDialog setId={SET} engine="artifact" onClose={() => undefined} /></ApiProvider>
  );
  await userEvent.click(await screen.findByRole("checkbox", { name: /I understand this writes/ }));
  await userEvent.type(await screen.findByLabelText("Your password"), "hunter2");
  const go = screen.getByRole("button", { name: "Recover this backup set" }) as HTMLButtonElement;
  await waitFor(() => expect(go.disabled).toBe(false));
  await userEvent.click(go);
  await waitFor(() => expect(asked).toHaveBeenCalled());
  return { ...view, recoverMany };
}

describe("a source-host recovery whose prompt outlives the dialog (#1599)", () => {
  it("starts nothing and touches no state when the prompt is answered yes after the dialog has gone", async () => {
    const answer = held<boolean>();
    const { unmount, recoverMany } = await pressRecoverWithPromptOpen(answer);
    unmount();
    probe.tearDown();
    answer.resolve(true);
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
    expect(recoverMany).not.toHaveBeenCalled();
  });

  it("touches no state when the prompt is answered no after the dialog has gone", async () => {
    const answer = held<boolean>();
    const { unmount, recoverMany } = await pressRecoverWithPromptOpen(answer);
    unmount();
    probe.tearDown();
    answer.resolve(false);
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
    expect(recoverMany).not.toHaveBeenCalled();
  });

  it("still starts the run when the prompt is answered yes and the dialog is there", async () => {
    const answer = held<boolean>();
    const { recoverMany } = await pressRecoverWithPromptOpen(answer);
    await act(async () => { answer.resolve(true); });
    await waitFor(() => expect(recoverMany).toHaveBeenCalled());
  });
});

async function pressRecoverArtifactWithPromptOpen(answer: ReturnType<typeof held<boolean>>) {
  const base = createMockApi("insecure-transport");
  const mintRecovery = vi.fn(base.mintRecovery);
  const api = { ...base, mintRecovery } as unknown as RetndApi;
  const asked = vi.fn(() => answer.promise);
  setInsecureTransportConfirmer(asked);
  const view = render(
    <ApiProvider api={api}><RecoverArtifactDialog artifactId={ARTIFACT} onClose={() => undefined} /></ApiProvider>
  );
  await userEvent.click(await screen.findByRole("checkbox", { name: /I understand this writes/ }));
  await userEvent.type(await screen.findByLabelText("Your password"), "hunter2");
  const go = screen.getByRole("button", { name: "Recover artifact" }) as HTMLButtonElement;
  await waitFor(() => expect(go.disabled).toBe(false));
  await userEvent.click(go);
  await waitFor(() => expect(asked).toHaveBeenCalled());
  return { ...view, mintRecovery };
}

describe("an artifact recovery whose prompt outlives the dialog (#1599)", () => {
  it("starts nothing and touches no state when the prompt is answered yes after the dialog has gone", async () => {
    const answer = held<boolean>();
    const { unmount, mintRecovery } = await pressRecoverArtifactWithPromptOpen(answer);
    unmount();
    probe.tearDown();
    answer.resolve(true);
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
    expect(mintRecovery).not.toHaveBeenCalled();
  });

  it("still mints when the prompt is answered yes and the dialog is there", async () => {
    const answer = held<boolean>();
    const { mintRecovery } = await pressRecoverArtifactWithPromptOpen(answer);
    await act(async () => { answer.resolve(true); });
    await waitFor(() => expect(mintRecovery).toHaveBeenCalled());
  });
});
