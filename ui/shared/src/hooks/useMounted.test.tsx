/**
 * Issue #1599: useMounted says whether the component is still there, for code
 * that runs after an await. These pin the four things a guard like this can
 * get wrong: it must be true after StrictMode's rehearsal remount, it must
 * not be a stale copy a handler closed over, it must already be true when
 * there's something to click, and it must never go false on a component that
 * is still mounted (a Suspense boundary hiding it isn't an unmount).
 */
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { StrictMode, Suspense, useLayoutEffect, useRef, useState } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import { useMounted } from "./useMounted";

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
});

/** Presses a button, awaits `answer`, then shows it if still mounted: the
 *  shape every migrated handler has. */
function Saver({ answer, seen }: { answer: Promise<string>; seen?: (m: { readonly current: boolean }) => void }) {
  const [text, setText] = useState("idle");
  const mountedRef = useMounted();
  useLayoutEffect(() => { seen?.(mountedRef); });
  const save = async () => {
    setText("saving");
    const got = await answer;
    if (!mountedRef.current) return;
    setText(got);
  };
  return <button onClick={() => void save()}>{text}</button>;
}

describe("useMounted (#1599)", () => {
  it("lets an answer through while mounted", async () => {
    const answer = held<string>();
    render(<Saver answer={answer.promise} />);
    fireEvent.click(screen.getByRole("button"));
    expect(screen.getByRole("button")).toHaveTextContent("saving");
    await act(async () => { answer.resolve("saved"); });
    expect(screen.getByRole("button")).toHaveTextContent("saved");
  });

  it("drops an answer that lands after unmount, with no state update and no rejection", async () => {
    const answer = held<string>();
    const { unmount } = render(<Saver answer={answer.promise} />);
    fireEvent.click(screen.getByRole("button"));
    unmount();
    probe.tearDown();
    answer.resolve("saved");
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("is true after StrictMode's rehearsal unmount and remount, so an answer still lands", async () => {
    const answer = held<string>();
    const refs: { readonly current: boolean }[] = [];
    render(<StrictMode><Saver answer={answer.promise} seen={(m) => refs.push(m)} /></StrictMode>);
    expect(refs.at(-1)?.current).toBe(true);
    fireEvent.click(screen.getByRole("button"));
    await act(async () => { answer.resolve("saved"); });
    expect(screen.getByRole("button")).toHaveTextContent("saved");
  });

  it("still drops an answer after a StrictMode unmount", async () => {
    const answer = held<string>();
    const { unmount } = render(<StrictMode><Saver answer={answer.promise} /></StrictMode>);
    fireEvent.click(screen.getByRole("button"));
    unmount();
    probe.tearDown();
    answer.resolve("saved");
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("is one ref for the component's life, so a handler from an earlier render reads it now", () => {
    const refs: { readonly current: boolean }[] = [];
    const never = new Promise<string>(() => undefined);
    const { rerender, unmount } = render(<Saver answer={never} seen={(m) => refs.push(m)} />);
    rerender(<Saver answer={never} seen={(m) => refs.push(m)} />);
    rerender(<Saver answer={never} seen={(m) => refs.push(m)} />);
    expect(refs.length).toBeGreaterThanOrEqual(3);
    expect(new Set(refs).size).toBe(1);
    const first = refs[0];
    expect(first.current).toBe(true);
    unmount();
    expect(first.current).toBe(false);
  });

  it("is already true in the commit, before passive effects run", () => {
    let atCommit: boolean | undefined;
    function Early() {
      const mountedRef = useMounted();
      useLayoutEffect(() => { atCommit = mountedRef.current; }, [mountedRef]);
      return null;
    }
    render(<Early />);
    expect(atCommit).toBe(true);
  });

  it("stays true while a Suspense boundary hides the component, and an answer then still lands", async () => {
    let wake!: () => void;
    const pending = new Promise<void>((r) => { wake = r; });
    let suspend = false;
    function Sometimes() {
      if (suspend) throw pending;
      return null;
    }
    // The control: a flag cleared in a layout cleanup, the way a guard built
    // on useLayoutEffect alone would be. It goes false when the boundary
    // hides the content, which shows this case really takes the hide path.
    const layoutOnly: { current: boolean | null } = { current: null };
    function LayoutFlag() {
      const flag = useRef(false);
      useLayoutEffect(() => {
        flag.current = true;
        layoutOnly.current = true;
        return () => { flag.current = false; layoutOnly.current = false; };
      }, []);
      return null;
    }
    const answer = held<string>();
    const refs: { readonly current: boolean }[] = [];
    function Page() {
      return (
        <Suspense fallback={<p>loading</p>}>
          <Saver answer={answer.promise} seen={(m) => refs.push(m)} />
          <LayoutFlag />
          <Sometimes />
        </Suspense>
      );
    }
    const { rerender } = render(<Page />);
    fireEvent.click(screen.getByRole("button"));

    suspend = true;
    rerender(<Page />);
    expect(screen.getByText("loading")).toBeInTheDocument();
    expect(layoutOnly.current).toBe(false);
    expect(refs[0].current).toBe(true);

    await act(async () => { answer.resolve("saved"); });
    suspend = false;
    await act(async () => { wake(); await pending; });
    expect(screen.getByRole("button")).toHaveTextContent("saved");
  });
});
