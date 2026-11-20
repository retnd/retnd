/**
 * Issue #1599, the last batch: components, encryption, auth and
 * observability. Two of its handlers can lose their component on the way
 * through, and both then set state on it:
 *
 *  - The first-run guide's "Skip for now": skipping the last pending step is
 *    what takes the guide away, and its busy flag is cleared after that.
 *  - A clipped value's Copy, which waits on the clipboard and then says
 *    "Copied".
 *
 * Each case holds the answer, unmounts, tears down and only then answers;
 * see test/teardownProbe.ts.
 */
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FirstRunStatus } from "@shared/api/contracts";
import { FirstRunGuide } from "@shared/components/FirstRunGuide";
import { ClippedText } from "@shared/encryption/ClippedText";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
  vi.restoreAllMocks();
});

const status: FirstRunStatus = {
  configured: true,
  steps: { administrator: "done", email: "pending", backup_set: "done" },
  nextStep: "email"
};

describe("the first-run guide's Skip after unmount (#1599)", () => {
  function pressSkip(skip: () => Promise<void>) {
    const view = render(
      <MemoryRouter>
        <FirstRunGuide status={status} configured onSkip={skip} />
      </MemoryRouter>
    );
    fireEvent.click(screen.getByRole("button", { name: /Skip for now/ }));
    return view;
  }

  it("leaves a guide alone once skipping the last step has taken it away", async () => {
    const answer = held<void>();
    const { unmount } = pressSkip(() => answer.promise);
    unmount();
    probe.tearDown();
    answer.resolve();
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("still frees the button while the guide is there", async () => {
    const answer = held<void>();
    pressSkip(() => answer.promise);
    expect(screen.getByRole("button", { name: /Skip for now/ })).toBeDisabled();
    answer.resolve();
    await waitFor(() => expect(screen.getByRole("button", { name: /Skip for now/ })).toBeEnabled());
  });
});

describe("a clipped value's Copy after unmount (#1599)", () => {
  const long = "x".repeat(200);

  function pressCopy(write: () => Promise<void>) {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn(write) } });
    const view = render(<ClippedText value={long} />);
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    return view;
  }

  it("says nothing once the value has gone", async () => {
    const answer = held<void>();
    const { unmount } = pressCopy(() => answer.promise);
    unmount();
    probe.tearDown();
    answer.resolve();
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("still says Copied while the value is there", async () => {
    const answer = held<void>();
    pressCopy(() => answer.promise);
    answer.resolve();
    expect(await screen.findByRole("status")).toHaveTextContent("Copied");
  });
});
