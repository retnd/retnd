/**
 * Catches a component that touches React state after it has unmounted
 * (#1589).
 *
 * That used to fail the pre-commit hook at random. A page test ends, the
 * page unmounts, vitest removes jsdom's `window`, and a request the page
 * started answers late (the mock answers every call after 180ms). The
 * component's .then() calls a state setter. React 18 drops an update to an
 * unmounted component without a warning, but first it reads `window.event`
 * to pick a lane, and with no `window` left that read throws. The throw
 * lands inside the .then(), the promise the component voided rejects, and
 * vitest reports an unhandled rejection against whichever file was running.
 *
 * Racing the real teardown would make a test exactly as flaky as the hook
 * was. Instead a case holds the request open, unmounts, calls tearDown(),
 * and only then lets the request answer. tearDown() swaps `window.event`
 * for a getter that throws what teardown would, so every state update the
 * component still makes is counted in `stateUpdates` and surfaces in
 * `rejections` as the error the hook saw.
 *
 * Create one per case (in beforeEach) and dispose() it in afterEach.
 */
export interface TeardownProbe {
  /** Unhandled rejections seen since the probe was made, as strings. */
  readonly rejections: string[];
  /** State updates made after tearDown(). */
  readonly stateUpdates: number;
  /** From here on, a state update throws the way it does after teardown. */
  tearDown(): void;
  /** Lets held answers run their callbacks, waits `ms`, then gives Node the
   *  turn it reports unhandled rejections on. */
  settle(ms?: number): Promise<void>;
  dispose(): void;
}

/** Node's process, which vitest runs these files in. This package carries
 *  no Node types, so only the two calls used here are named. */
type RejectionEvents = {
  on(event: "unhandledRejection", listener: (reason: unknown) => void): void;
  off(event: "unhandledRejection", listener: (reason: unknown) => void): void;
};

export function teardownProbe(): TeardownProbe {
  const node = (globalThis as unknown as { process: RejectionEvents }).process;
  const rejections: unknown[] = [];
  const onRejection = (reason: unknown) => { rejections.push(reason); };
  node.on("unhandledRejection", onRejection);
  let stateUpdates = 0;
  let restore: (() => void) | null = null;

  return {
    get rejections() { return rejections.map(String); },
    get stateUpdates() { return stateUpdates; },
    tearDown() {
      const before = Object.getOwnPropertyDescriptor(window, "event");
      Object.defineProperty(window, "event", {
        configurable: true,
        get() {
          stateUpdates += 1;          throw new ReferenceError("window is not defined");
        }
      });
      restore = () => {
        if (before) Object.defineProperty(window, "event", before);
        else delete (window as { event?: unknown }).event;
      };
    },
    async settle(ms = 0) {
      for (let i = 0; i < 5; i++) await Promise.resolve();
      await new Promise((r) => setTimeout(r, ms));
      await new Promise((r) => setTimeout(r, 0));
    },
    dispose() {
      restore?.();
      restore = null;
      node.off("unhandledRejection", onRejection);
    }
  };
}

/** A request the case answers when it chooses, so it can still be in
 *  flight when the component unmounts. */
export function held<T>(): { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void } {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}
