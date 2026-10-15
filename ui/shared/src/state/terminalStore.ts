/**
 * The one state every terminal reads (issue #1177).
 *
 * # What was wrong
 *
 * The dashboard strips, a set's own terminal and the docked global terminal
 * each ran their own polling loop and each kept their own copy of the lines.
 * Two copies of one stream are two answers to "what does this set say", and
 * they differed: they polled on different clocks, so for a tick one showed a
 * line the other did not, they trimmed to different windows, and a terminal
 * opened late started empty and filled in when its own first reading landed.
 *
 * # What it is now
 *
 * One causl node holds everything a terminal draws: a window per backup set,
 * the one window over every bucket the docked terminal draws, the lines of
 * any process that has since restarted, and the last reading's facts (its
 * cadence, its failure). ONE reader writes into it, `acquireTerminalFeed`
 * below, which polls the live activity feed once however many terminals are
 * mounted. Every terminal subscribes to the node through `useCausl`, so they
 * all change in the same commit and a terminal that mounts later reads what
 * is already held, with no reading of its own.
 *
 * # Bounded, and continuous across a reconnect
 *
 * Every window is bounded (PAGE_BUFFER per set, DOCK_BUFFER for the docked
 * one, MAX_GENERATIONS dead processes) so a tab left open for a week holds a
 * flat amount. The cursor lives in the node too, and it comes from what the
 * service says it holds (nextCursor), so a dropped connection resumes from
 * where it stopped and the merge by sequence adds nothing already held. That
 * is the continuity #985 asked for, moved onto the shared state.
 *
 * # Where things live
 *
 * How a reading folds into the windows is pure and lives in
 * state/activityWindow, where the tests drive it without a timer. This file
 * is the node and the one reader: a `TerminalReader` per api, with the
 * holders kept as tokens outside it so a reader replaced by another (a test,
 * a re-login) does not strand the terminals still mounted on it.
 */
import type { RetndApi, ApiError } from "@shared/api/contracts";
import { asApiError } from "@shared/api/failure";
import { reportSessionLost } from "@shared/api/sessionLoss";
import type { LiveActivity } from "@shared/types/activity";
import { graph, registerInput } from "./graph";
import { INITIAL_TERMINAL, ingestReading, restartedBy } from "./activityWindow";
import type { TerminalState } from "./activityWindow";

export { DOCK_BUFFER, EVICT_AFTER, MAX_GENERATIONS } from "./activityWindow";
export type { ReadingFacts, TerminalGeneration, TerminalState } from "./activityWindow";

/** How many events one poll asks for per bucket.
 *
 * The contract's own ceiling, deliberately. Every limit is safe now that
 * the service hands back the OLDEST slice above the cursor, says when one
 * cut a reading short, and the cursor below holds back at whichever
 * bucket said so, so this is about how many polls catching up takes
 * rather than about whether anything is lost. */
const POLL_LIMIT = 200;


/** The longest a failing reader waits between attempts. Long enough that a
 *  NAS that is down is not asked every second by every open tab, short
 *  enough that a terminal picks up again within a minute of it coming back. */
export const MAX_BACKOFF_MS = 60_000;

/** The shared terminal state. Registered, so the test reset clears it. */
export const terminalNode = registerInput<TerminalState>("app.terminal", INITIAL_TERMINAL);

/** Commits one reading into the shared node. A reading that changes nothing
 *  commits nothing: the fold hands back the same state, and there is no
 *  point waking a subscriber to tell it so. */
export function writeReading(reading: LiveActivity): void {
  const before = graph.read(terminalNode);
  const after = ingestReading(before, reading);
  if (after !== before) graph.commit("terminal/reading", (tx) => tx.set(terminalNode, after));
}

/** Commits a failed poll. The window stays: blanking a panel somebody is
 *  reading is a worse answer than an old one that says it is old. */
export function writeFailure(error: ApiError): void {
  graph.commit("terminal/failure", (tx) => tx.set(terminalNode, { ...graph.read(terminalNode), error }));
}

// ---------------------------------------------------------------------------
// The one reader.

/**
 * How long to wait after a run of failures: the service's own cadence
 * doubled per failure, capped, with a fifth either way of jitter so a dozen
 * tabs that lost the NAS together do not come back in step.
 */
export function backoffDelay(pollAfterMs: number, failures: number, random: () => number = Math.random): number {
  if (failures <= 0) return pollAfterMs;
  const base = Math.min(MAX_BACKOFF_MS, pollAfterMs * 2 ** failures);
  return Math.round(base * (0.8 + 0.4 * random()));
}

/**
 * Polls one api into the node. One exists at a time; a different api gets a
 * new one, and the old one is stopped, so an answer to a question it asked
 * can never land in the new one's state.
 */
class TerminalReader {
  private timer: ReturnType<typeof setTimeout> | undefined;
  private inflight = false;
  /** A refresh asked for while a reading was in flight: run one more after. */
  private again = false;
  private stopped = false;
  /** Failed polls in a row, for the backoff. A success resets it. */
  private failures = 0;
  /** The session has gone. No more scheduled polls until a terminal mounts
   *  again (or a refresh succeeds), because every one of them would be
   *  refused the same way and each would report the loss again. */
  halted = false;

  constructor(readonly api: Pick<RetndApi, "getLiveActivity">) {}

  stop(): void {
    this.stopped = true;
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = undefined;
  }

  private schedule(delay: number): void {
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.poll(), delay);
  }

  async poll(): Promise<void> {
    if (this.stopped) return;
    if (this.inflight) { this.again = true; return; }
    this.inflight = true;
    let restarted = false;
    try {
      const reading = await this.api.getLiveActivity({ since: graph.read(terminalNode).cursor, limit: POLL_LIMIT });
      if (this.stopped) return;
      restarted = restartedBy(graph.read(terminalNode), reading);
      writeReading(reading);
      this.failures = 0;
      this.halted = false;
    } catch (e) {
      if (this.stopped) return;
      const error = asApiError(e);
      this.failures += 1;
      // A session that is gone is not a failure of this read (#795), and
      // asking again on a timer cannot bring it back.
      if (error.code === "UNAUTHENTICATED") {
        reportSessionLost();
        this.halted = true;
      }
      writeFailure(error);
    } finally {
      this.inflight = false;
    }
    if (this.stopped) return;
    // A restart reading was asked with the dead process's cursor, so the
    // node now holds empty windows. Ask the live process from 0 straight
    // away rather than leave the panels blank for a whole interval.
    if (this.again || restarted) { this.again = false; void this.poll(); return; }
    if (this.halted) return;
    this.schedule(backoffDelay(graph.read(terminalNode).pollAfterMs, this.failures));
  }
}

/** Every mounted terminal's claim on the reader. Kept outside the reader, so
 *  a reader replaced by another api's still knows who is holding it. */
const holders = new Set<symbol>();
let reader: TerminalReader | null = null;

/**
 * Starts (or joins) the one reader. Returns the release for the caller's
 * cleanup; the reader stops when the last holder lets go.
 *
 * The first holder starts from an empty node and reads at once. Later holders
 * read nothing: they find the node already filled, which is the point. A
 * holder arriving with a different api (a test, a re-login) starts the state
 * over, because a cursor taken from one service means nothing to another,
 * and every holder already mounted carries over to the new reader. A reader
 * halted by a lost session starts again for the next holder.
 */
export function acquireTerminalFeed(api: Pick<RetndApi, "getLiveActivity">): () => void {
  const token = Symbol("terminal holder");
  if (!reader || reader.api !== api) {
    reader?.stop();
    // Whatever the node still has is a window nothing has been updating, or
    // one read from a different service. Start from nothing.
    graph.commit("terminal/reset", (tx) => tx.set(terminalNode, INITIAL_TERMINAL));
    reader = new TerminalReader(api);
    void reader.poll();
  } else if (reader.halted) {
    reader.halted = false;
    void reader.poll();
  }
  holders.add(token);
  return () => {
    if (!holders.delete(token)) return;
    if (holders.size === 0) {
      reader?.stop();
      reader = null;
    }
  };
}

/** Asks for a reading now, out of band with the timer. The engine records
 *  what it did before it answers a request, so a reading issued the moment
 *  that answer lands carries the lines it emitted, and every terminal gets
 *  them in the same commit. */
export function refreshTerminalFeed(): void {
  void reader?.poll();
}

/** Test-only: stops the reader, forgets every holder and empties the state. */
export function resetTerminalForTests(): void {
  reader?.stop();
  reader = null;
  holders.clear();
  graph.commit("terminal/reset", (tx) => tx.set(terminalNode, INITIAL_TERMINAL));
}
