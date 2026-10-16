/**
 * Follows one assurance operation from the durable feed (issue #985).
 *
 * # Reload and reconnect
 *
 * Nothing here is the source of truth. The current stage and the bytes so
 * far come from the service's reading of the operation, and the lines come
 * from its cursor-paged event list, so a reload re-reads both and arrives at
 * the same place. A stale spinner is impossible by construction: the
 * indicator is drawn from the last reading, and the loop stops only on a
 * terminal state.
 *
 * Lines are merged BY SEQUENCE. A reconnect that replays from the start, or
 * a retried request that overlaps one already merged, adds nothing the
 * reader already holds, which is the no-duplicate-terminal-lines guarantee.
 * The cursor only moves forward.
 */
import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { describeFailure } from "@shared/api/failure";
import type { AssuranceApi, Operation, OperationEvent } from "./types";

export const POLL_MS = 1000;

const NO_EVENTS: OperationEvent[] = [];

/** Folds a reading into what is held: unique by seq, ordered by seq. */
export function mergeBySeq(held: OperationEvent[], incoming: OperationEvent[]): OperationEvent[] {
  if (incoming.length === 0) return held;
  const have = new Set(held.map((e) => e.seq));
  const fresh = incoming.filter((e) => !have.has(e.seq));
  if (fresh.length === 0) return held;
  return [...held, ...fresh].sort((a, b) => a.seq - b.seq);
}

/** How a caller follows a run. */
export interface UseOperationOptions {
  /** Whether to hold the run's lines. A caller that points at the set
   *  terminal instead of drawing a log (the Assurance card, #1176) passes
   *  false: the cursor still moves, so nothing is asked for twice, but a
   *  long run's lines are not kept in a second place nothing reads. */
  retainEvents?: boolean;
}

/** What every follower of one operation sees. Replaced, never mutated, so
 *  it is a valid external-store snapshot. */
interface Reading {
  operation: Operation | null; events: OperationEvent[]; error: string | null;
  /** Nothing of the run is on record anywhere (#1200): a pruned run, or an
   *  id that was never one. Said in words, never drawn as an empty panel. */
  missing?: boolean;
}
const NOTHING: Reading = { operation: null, events: NO_EVENTS, error: null };

/** One operation's single poller, shared by every hook following it (the
 *  set page's Assurance section and Validation pane both attach to the run
 *  that is going, review of #1184). It runs while anyone holds it. */
interface Follow {
  reading: Reading;
  listeners: Set<() => void>;
  enders: Set<(op: Operation) => void>;
  /** How many holders want the lines kept. None: none are. */
  retaining: number;
  holders: number;
  ended: boolean;
  stop(): void;
}

const follows = new WeakMap<AssuranceApi, Map<string, Follow>>();

function acquire(api: AssuranceApi, id: string, retain: boolean): Follow {
  let byId = follows.get(api);
  if (!byId) { byId = new Map(); follows.set(api, byId); }
  let f = byId.get(id);
  if (!f) {
    const entry: Follow = {
      reading: NOTHING, listeners: new Set(), enders: new Set(), retaining: 0, holders: 0, ended: false, stop: () => {}
    };
    const publish = (r: Reading) => { entry.reading = r; for (const l of entry.listeners) l(); };
    let cursor = 0;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      try {
        const feed = await api.readOperation(id, cursor);
        if (stopped) return;
        cursor = Math.max(cursor, feed.nextCursor);
        publish({
          operation: feed.operation,
          events: entry.retaining > 0 ? mergeBySeq(entry.reading.events, feed.events) : NO_EVENTS,
          error: null
        });
        if (feed.operation.state !== "running") {
          entry.ended = true;
          for (const end of entry.enders) end(feed.operation);
          return;
        }
      } catch (e) {
        if (stopped) return;
        // The reading failed: say so, keep the last good one, and keep
        // trying. A dropped connection is the ordinary cause.
        const message = e instanceof Error ? e.message : "could not read progress";
        // Nothing of the run is on record (#1200): it will not be there a
        // second later, so say so once, in words, and stop asking.
        const missing = e instanceof Error && e.name === "NotOnRecord";
        publish({ ...entry.reading, error: message, ...(missing ? { missing } : {}) });
        if (missing) return;
        // An id that is not on the feed will not be there a second later
        // either: say so once and stop asking.
        if (e instanceof Error && e.name === "NotFollowable") return;
      }
      timer = setTimeout(tick, POLL_MS);
    };
    entry.stop = () => { stopped = true; if (timer) clearTimeout(timer); };
    byId.set(id, entry);
    f = entry;
    void tick();
  }
  f.holders += 1;
  if (retain) f.retaining += 1;
  return f;
}

function release(api: AssuranceApi, id: string, f: Follow, retain: boolean): void {
  f.holders -= 1;
  if (retain) f.retaining -= 1;
  if (f.holders > 0) return;
  f.stop();
  follows.get(api)?.delete(id);
}

export function useOperation(
  api: AssuranceApi | undefined,
  operationId: string | null,
  onTerminal?: (op: Operation) => void,
  { retainEvents = true }: UseOperationOptions = {}
) {
  const notify = useRef(onTerminal);
  useEffect(() => { notify.current = onTerminal; });

  // Subscribing is holding: the shared poller runs while any hook is
  // subscribed to it, and this hook hears the end once. A hook that joins a
  // run another holder already saw end hears it too.
  const subscribe = useCallback((cb: () => void) => {
    if (!api || !operationId) return () => {};
    const follow = acquire(api, operationId, retainEvents);
    let heard = false;
    const end = (op: Operation) => { if (!heard) { heard = true; notify.current?.(op); } };
    follow.listeners.add(cb);
    follow.enders.add(end);
    if (follow.ended && follow.reading.operation) {
      const done = follow.reading.operation;
      queueMicrotask(() => end(done));
    }
    return () => {
      follow.listeners.delete(cb);
      follow.enders.delete(end);
      release(api, operationId, follow, retainEvents);
    };
  }, [api, operationId, retainEvents]);
  const reading = useSyncExternalStore(
    subscribe,
    () => (api && operationId ? follows.get(api)?.get(operationId)?.reading : undefined) ?? NOTHING
  );
  // A holder that keeps no lines shows none, even when another holder does.
  const operation = reading.operation;
  const events = retainEvents ? reading.events : NO_EVENTS;
  const error = reading.error;

  // A cancel the service refused (it already ended, or the session lost its
  // right) is shown, in the service's words. Held with the id it belongs to,
  // like the reading, so it never outlives the operation it was about.
  const [refusal, setRefusal] = useState<{ id: string | null; message: string } | null>(null);
  const cancelError = refusal && refusal.id === operationId ? refusal.message : null;
  // The service accepted a cancel, which the next reading will confirm; it
  // is said now rather than a poll later, on this holder's view.
  const [cancelling, setCancelling] = useState<string | null>(null);

  const cancel = useCallback(async () => {
    if (!api || !operationId) return;
    setRefusal(null);
    try {
      await api.cancelOperation(operationId);
      setCancelling(operationId);
    } catch (e) {
      setRefusal({ id: operationId, message: describeFailure(e, "could not cancel").message });
    }
  }, [api, operationId]);

  const shown = operation && cancelling === operationId && operation.state === "running" && !operation.cancelling
    ? { ...operation, cancelling: true }
    : operation;
  return { operation: shown, events, error, cancel, cancelError, missing: !!reading.missing };
}
