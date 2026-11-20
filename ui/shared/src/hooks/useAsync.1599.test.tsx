/**
 * Issue #1599, found while tracing parent callbacks in review: useAsync's
 * `reload` bumps a counter in the component that called the hook, so a
 * handler that reloads after an await (`.then(() => list.reload())`) sets
 * state on a gone component, the #1589 shape, from a function the guard test
 * can't see into. `reload` now checks the component is still there.
 *
 * See test/teardownProbe.ts.
 */
import { cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useAsync } from "@shared/hooks/useAsync";
import type { AsyncState } from "@shared/hooks/useAsync";
import { teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => { probe.dispose(); cleanup(); });

function Reader({ read, grab }: { read: () => Promise<number>; grab: (s: AsyncState<number>) => void }) {
  const state = useAsync(read, [read]);
  grab(state);
  return <span>{state.data}</span>;
}

describe("useAsync's reload after unmount (#1599)", () => {
  it("does nothing, and sets no state, once the component has gone", async () => {
    const read = vi.fn().mockResolvedValue(1);
    let state!: AsyncState<number>;
    const view = render(<Reader read={read} grab={(s) => { state = s; }} />);
    await waitFor(() => expect(read).toHaveBeenCalledTimes(1));
    view.unmount();
    probe.tearDown();
    expect(() => state.reload()).not.toThrow();
    await probe.settle();
    expect(probe.stateUpdates).toBe(0);
    expect(read).toHaveBeenCalledTimes(1);
  });

  it("still reads again while the component is there", async () => {
    const read = vi.fn().mockResolvedValue(1);
    let state!: AsyncState<number>;
    render(<Reader read={read} grab={(s) => { state = s; }} />);
    await waitFor(() => expect(read).toHaveBeenCalledTimes(1));
    state.reload();
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2));
  });
});
