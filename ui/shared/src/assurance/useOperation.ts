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
import { useCallback, useEffect, useRef, useState } from "react";
import type { AssuranceApi, Operation, OperationEvent } from "./types";

export const POLL_MS = 1000;

/** Folds a reading into what is held: unique by seq, ordered by seq. */
export function mergeBySeq(held: OperationEvent[], incoming: OperationEvent[]): OperationEvent[] {
  if (incoming.length === 0) return held;
  const have = new Set(held.map((e) => e.seq));
  const fresh = incoming.filter((e) => !have.has(e.seq));
  if (fresh.length === 0) return held;
  return [...held, ...fresh].sort((a, b) => a.seq - b.seq);
}

export function useOperation(api: AssuranceApi | undefined, operationId: string | null, onTerminal?: (op: Operation) => void) {
  // Held with the id they belong to, so switching operations shows nothing
  // stale without resetting state from inside the effect.
  const [held, setHeld] = useState<{ id: string | null; operation: Operation | null; events: OperationEvent[]; error: string | null }>(
    { id: null, operation: null, events: [], error: null }
  );
  const mine = held.id === operationId ? held : { id: operationId, operation: null, events: [] as OperationEvent[], error: null };
  const { operation, events, error } = mine;
  const cursor = useRef(0);
  const finished = useRef(new Set<string>());
  const notify = useRef(onTerminal);
  useEffect(() => { notify.current = onTerminal; });

  useEffect(() => {
    cursor.current = 0;
    if (!api || !operationId) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      try {
        const feed = await api.readOperation(operationId, cursor.current);
        if (stopped) return;
        cursor.current = Math.max(cursor.current, feed.nextCursor);
        setHeld((h) => ({
          id: operationId,
          operation: feed.operation,
          events: mergeBySeq(h.id === operationId ? h.events : [], feed.events),
          error: null
        }));
        if (feed.operation.state !== "running") {
          if (!finished.current.has(operationId)) {
            finished.current.add(operationId);
            notify.current?.(feed.operation);
          }
          return;
        }
      } catch (e) {
        if (stopped) return;
        // The reading failed: say so, keep the last good one, and keep
        // trying. A dropped connection is the ordinary cause.
        const message = e instanceof Error ? e.message : "could not read progress";
        setHeld((h) => ({ ...(h.id === operationId ? h : { id: operationId, operation: null, events: [] }), id: operationId, error: message }));
      }
      timer = setTimeout(tick, POLL_MS);
    };
    void tick();
    return () => { stopped = true; if (timer) clearTimeout(timer); };
  }, [api, operationId]);

  const cancel = useCallback(async () => {
    if (api && operationId) await api.cancelOperation(operationId);
  }, [api, operationId]);

  return { operation, events, error, cancel };
}
