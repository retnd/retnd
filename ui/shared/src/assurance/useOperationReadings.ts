/**
 * Follows several assurance operations with one poller (issue #1174).
 *
 * The Dashboard's list of due backups can start a run on any row, and a list
 * of forty rows each holding its own `useOperation` would be forty one-second
 * pollers, two of them following the same operation whenever two rows of one
 * set did. This reads each operation the list started once a second from one
 * loop, stops asking about one once it has ended, and stops altogether when
 * none is left running. It keeps the reading only, not the lines: a row says
 * the stage and the outcome, and the lines are the set terminal's.
 */
import { useEffect, useRef, useState } from "react";
import type { AssuranceApi, Operation } from "./types";
import { POLL_MS } from "./useOperation";

export interface OperationReading {
  operation: Operation | null;
  error: string | null;
}

export function useOperationReadings(
  api: AssuranceApi | undefined,
  ids: readonly string[],
  onTerminal?: (op: Operation) => void
): Record<string, OperationReading> {
  const [readings, setReadings] = useState<Record<string, OperationReading>>({});
  const cursors = useRef(new Map<string, number>());
  const ended = useRef(new Set<string>());
  const notify = useRef(onTerminal);
  useEffect(() => { notify.current = onTerminal; });

  // A stable key, so a re-render with the same ids keeps the loop it has.
  const key = [...new Set(ids)].sort().join("\n");

  useEffect(() => {
    if (!api || key === "") return;
    const wanted = key.split("\n");
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      const open = wanted.filter((id) => !ended.current.has(id));
      await Promise.all(open.map(async (id) => {
        try {
          const feed = await api.readOperation(id, cursors.current.get(id) ?? 0);
          if (stopped) return;
          cursors.current.set(id, Math.max(cursors.current.get(id) ?? 0, feed.nextCursor));
          setReadings((r) => ({ ...r, [id]: { operation: feed.operation, error: null } }));
          if (feed.operation.state !== "running") {
            ended.current.add(id);
            notify.current?.(feed.operation);
          }
        } catch (e) {
          if (stopped) return;
          // Keep the last good reading and keep asking: a dropped connection
          // is the ordinary cause. An id that is not on the feed will not be
          // there a second later either, so that one is asked about once.
          const message = e instanceof Error ? e.message : "could not read progress";
          setReadings((r) => ({ ...r, [id]: { operation: r[id]?.operation ?? null, error: message } }));
          if (e instanceof Error && e.name === "NotFollowable") ended.current.add(id);
        }
      }));
      if (stopped || wanted.every((id) => ended.current.has(id))) return;
      timer = setTimeout(tick, POLL_MS);
    };
    void tick();
    return () => { stopped = true; if (timer) clearTimeout(timer); };
  }, [api, key]);

  return readings;
}
