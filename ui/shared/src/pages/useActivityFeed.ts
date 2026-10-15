/**
 * The hooks every terminal reads the shared state through (issues #596, #599,
 * #1177).
 *
 * The loop that used to live here (the cursor, the epoch check, the timer at
 * the service's own cadence, the out-of-band refresh) is state/terminalStore:
 * one reader writing one causl node. What is left is the React side. A
 * terminal calls `useTerminalFeed`, which makes sure the reader is running
 * while it is mounted and hands back the node's value. Several terminals on
 * one page therefore cost one reading per poll, and every one of them
 * re-renders from the same commit.
 *
 * The functions the strips' tests pin (mergeActivity, feedRestarted,
 * nextCursor) moved with the store and are re-exported so their callers did
 * not have to move with them.
 */
import { useEffect, useMemo } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { ApiError } from "@shared/api/contracts";
import { useCausl } from "@shared/state/graph";
import { acquireTerminalFeed, refreshTerminalFeed, terminalNode } from "@shared/state/terminalStore";
import type { TerminalState } from "@shared/state/terminalStore";
import type { LiveActivity, SetActivity } from "@shared/types/activity";

export { feedRestarted, mergeActivity, nextCursor } from "@shared/state/terminalStore";

/** What a terminal gets back. */
export interface TerminalFeed {
  /** Everything any terminal draws, as the one reader last wrote it. */
  state: TerminalState;
  /** Ask again now, out of band with the timer. */
  refresh: () => void;
}

/**
 * Subscribes to the shared terminal state and keeps its reader running while
 * the caller is mounted.
 *
 * Mounting does not read: the first terminal starts the reader and the rest
 * find the node already filled. That is what lets a terminal opened late show
 * what the others hold in its first render.
 */
export function useTerminalFeed(): TerminalFeed {
  const api = useApi();
  useEffect(() => acquireTerminalFeed(api), [api]);
  const state = useCausl(terminalNode);
  return { state, refresh: refreshTerminalFeed };
}

/** What a surface following the per-set feed gets back. */
export interface ActivityFeed {
  /** Every set in the reading, keyed by id, merged across polls. */
  held: Map<string, SetActivity>;
  /** The last reading itself, or null before the first. */
  reading: LiveActivity | null;
  /** The interval the service asked to be polled at. */
  pollAfterMs: number;
  /** The last poll's failure, or null. The last good reading stays under it. */
  error: ApiError | null;
  refresh: () => void;
}

/** The shape the strips want: they draw one panel per set and look their set
 *  up by id. */
export function useActivityFeed(): ActivityFeed {
  const { state, refresh } = useTerminalFeed();
  const held = useMemo(() => new Map(Object.entries(state.sets)), [state.sets]);
  return { held, reading: state.reading, pollAfterMs: state.pollAfterMs, error: state.error, refresh };
}
