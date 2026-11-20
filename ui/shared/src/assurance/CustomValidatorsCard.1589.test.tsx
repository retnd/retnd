/**
 * Issue #1589: the card must not touch React state once it has unmounted.
 *
 * The hook failed on tier-destination-picker.test.tsx with an unhandled
 * rejection after teardown. The Settings page mounts this card, the card asks
 * for its list, the mock answers 180ms later, and by then the file's last test
 * had unmounted the page and vitest had torn jsdom down. setList ran anyway.
 * React 18 drops an update to an unmounted component without a warning, but
 * before it gets that far it reads `window.event` to pick a lane, and with no
 * `window` left that read throws. The throw lands inside the .then() the card
 * hands the list to, so the promise `void load()` threw away rejects and
 * nothing is listening.
 *
 * Waiting for the timer would make these cases flaky in exactly the way the
 * hook was, so each one holds the request open, unmounts, and only then lets
 * it answer. After the unmount `window.event` is swapped for a getter that
 * throws the error teardown would produce, so any state update the card
 * still makes shows up twice: as a read of `window.event`, and as the
 * unhandled rejection the hook saw.
 */
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { CustomValidator, CustomValidatorList, ValidatorScript } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { CustomValidatorsCard } from "./CustomValidatorsCard";

type Held<T> = { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void };
function held<T>(): Held<T> {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const rejections: unknown[] = [];
const onRejection = (reason: unknown) => { rejections.push(reason); };
let stateReads = 0;
let restoreEvent: (() => void) | null = null;

/** Stands in for teardown: from here on, a state update throws the way it
 *  does once vitest has removed `window`. */
function tearDownWindow() {
  const before = Object.getOwnPropertyDescriptor(window, "event");
  Object.defineProperty(window, "event", {
    configurable: true,
    get() {
      stateReads += 1;
      throw new ReferenceError("window is not defined");
    }
  });
  restoreEvent = () => {
    if (before) Object.defineProperty(window, "event", before);
    else delete (window as { event?: unknown }).event;
  };
}

/** Lets the held answer run its callbacks, then gives Node the turn it
 *  reports unhandled rejections on. */
async function settle() {
  for (let i = 0; i < 5; i++) await Promise.resolve();
  await new Promise((r) => setTimeout(r, 0));
}

beforeEach(() => {
  rejections.length = 0;
  stateReads = 0;
  process.on("unhandledRejection", onRejection);
});

afterEach(() => {
  restoreEvent?.();
  restoreEvent = null;
  process.off("unhandledRejection", onRejection);
  cleanup();
});

describe("CustomValidatorsCard after unmount (#1589)", () => {
  it("ignores a list that answers after the card has gone", async () => {
    const answer = held<CustomValidatorList>();
    const base = createMockApi("default");
    const list = await base.listCustomValidators();
    const api = { ...base, listCustomValidators: () => answer.promise };

    const { unmount } = render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    unmount();
    tearDownWindow();
    answer.resolve(list);
    await settle();

    expect(rejections.map(String)).toEqual([]);
    expect(stateReads).toBe(0);
  });

  it("ignores a list read that fails after the card has gone", async () => {
    const answer = held<CustomValidatorList>();
    const api = { ...createMockApi("default"), listCustomValidators: () => answer.promise };

    const { unmount } = render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    unmount();
    tearDownWindow();
    answer.reject(new Error("the engine went away"));
    await settle();

    expect(rejections.map(String)).toEqual([]);
    expect(stateReads).toBe(0);
  });

  it("ignores a script that arrives after the card has gone", async () => {
    const answer = held<ValidatorScript>();
    const base = createMockApi("default");
    const list = await base.listCustomValidators();
    const script = await base.getCustomValidatorScript("billing-dump");
    const api = {
      ...base,
      listCustomValidators: () => Promise.resolve(list),
      getCustomValidatorScript: () => answer.promise
    };

    const { unmount } = render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "View script billing-dump" }));
    unmount();
    tearDownWindow();
    answer.resolve(script);
    await settle();

    expect(rejections.map(String)).toEqual([]);
    expect(stateReads).toBe(0);
  });

  it("ignores a remove that finishes after the card has gone", async () => {
    const answer = held<CustomValidator>();
    const base = createMockApi("default");
    const list = await base.listCustomValidators();
    const api = {
      ...base,
      listCustomValidators: () => Promise.resolve(list),
      removeCustomValidator: () => answer.promise
    };

    const { unmount } = render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Remove billing-dump" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove validator" }));
    unmount();
    tearDownWindow();
    answer.resolve(list.validators.find((v) => v.name === "billing-dump")!);
    await settle();

    expect(rejections.map(String)).toEqual([]);
    expect(stateReads).toBe(0);
  });

  // The control: the probe is not the thing failing. A card left mounted
  // takes its list as it always did.
  it("still shows a list that answers while the card is there", async () => {
    const answer = held<CustomValidatorList>();
    const base = createMockApi("default");
    const list = await base.listCustomValidators();
    const api = { ...base, listCustomValidators: () => answer.promise };

    render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    answer.resolve(list);
    expect(await screen.findByRole("button", { name: "Remove billing-dump" })).toBeInTheDocument();
  });
});
