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
 */
import type { RetndApi, ApiError } from "@shared/api/contracts";
import { asApiError } from "@shared/api/failure";
import { reportSessionLost } from "@shared/api/sessionLoss";
import type { LiveActivity, SetActivity, SetActivityEvent } from "@shared/types/activity";
import { graph, registerInput } from "./graph";

/** How many lines a surface holds per set.
 *
 * The service keeps a bounded tail of its own and this keeps a bounded
 * window onto it, because an unbounded buffer in a tab left open for a
 * week is a leak with a nice name. It is deliberately larger than a strip
 * shows, so scrolling back reaches something, and deliberately far
 * smaller than the durable record: the full history is the Activity page,
 * one click away. */
const PAGE_BUFFER = 300;

/** The cadence used when the service has not answered yet, or answered
 *  with nothing usable. Slow on purpose: the fast cadence is a claim that
 *  something is moving, and before the first answer nothing has claimed
 *  that. */
const FALLBACK_POLL_MS = 10_000;

/** How many events one poll asks for per bucket.
 *
 * The contract's own ceiling, deliberately. Every limit is safe now that
 * the service hands back the OLDEST slice above the cursor, says when one
 * cut a reading short, and the cursor below holds back at whichever
 * bucket said so, so this is about how many polls catching up takes
 * rather than about whether anything is lost. */
const POLL_LIMIT = 200;

/** How many lines the dock holds.
 *
 * More than a strip's 300, because this aggregates every bucket rather
 * than windowing one, and because it is the surface an operator scrolls
 * back through after something went wrong. Still bounded, for the reason
 * that constant already gives: an unbounded buffer in a tab left open for
 * a week is a leak with a nice name. What is gone is said in the
 * scrollback, and the durable record is the Activity page. */
export const DOCK_BUFFER = 1000;

/**
 * Folds a new reading into what a surface already holds.
 *
 * The merge is by sequence and it is what makes the cursor safe. A poll
 * that asks for "everything after 412" comes back with a slice, not a
 * tail, so appending blindly would drop the lines above it and replacing
 * blindly would throw away everything before. Deduplicating by sequence
 * also makes a repeated or overlapping response harmless, which matters
 * because a retried request is not a rare event on a NAS, and because
 * asking again from where a truncated bucket stopped re-sends whatever a
 * quieter bucket had already handed over.
 */
export function mergeActivity(
  previous: SetActivity | undefined,
  next: SetActivity,
  buffer = PAGE_BUFFER
): SetActivity {
  const bySequence = new Map<number, SetActivityEvent>();
  for (const e of previous?.events ?? []) bySequence.set(e.sequence, e);
  for (const e of next.events) bySequence.set(e.sequence, e);
  const ordered = [...bySequence.values()].sort((a, b) => a.sequence - b.sequence);
  const events = ordered.slice(-buffer);
  // The surface's own window is the third way to lose a line and it
  // counts the same: trimming to `buffer` throws lines away exactly as
  // the service's ring does.
  const trimmed = ordered.length > events.length;
  // Whether this window now reaches back at least as far as the service's
  // own buffer does. When it does, every line anybody still holds is on
  // screen, so there is nothing between what the page shows and what the
  // service has.
  const reachesTheServicesOldest = events.length > 0 && next.oldestSequence > 0 && events[0].sequence <= next.oldestSequence;
  return {
    ...next,
    events,
    // A gap is a fact about the buffer, not about the reading that
    // noticed it, so it is carried forward: a later reading answering a
    // caught-up cursor cleanly does not fill in a hole this window
    // already has. What it is NOT is permanent. Left unconditionally
    // sticky, one dropped=true put "earlier lines are not held here any
    // more" on the panel for the life of the tab, including on a page
    // that had since refilled from the service's whole buffer, and a gap
    // warning that fires when there is no gap teaches an operator to
    // ignore the real one.
    dropped: next.dropped || trimmed || (!reachesTheServicesOldest && (previous?.dropped ?? false)),
    // The surface's own window can start later than the service's buffer
    // does. Reporting the service's bound while showing fewer lines would
    // claim continuity the surface does not have.
    oldestSequence: Math.max(next.oldestSequence, events.length > 0 ? events[0].sequence : 0)
  };
}

/**
 * Whether this reading came from a different process than the one the
 * surface has been following.
 *
 * The cursor is a sequence number and the sequence counter is per
 * process: it starts again at zero on every start. So a tab that reached
 * 412 asks a freshly started service for everything after 412, is told
 * correctly that there is nothing, and goes on showing a dead cycle's log
 * for as long as it stays open. Restarts are routine here, which makes
 * this the difference between a panel that says what is happening and one
 * that says what was happening the last time the engine was up.
 *
 * The epoch is the answer that cannot be fooled, because it is not a
 * counter at all: a counter climbs, so a busy new process eventually
 * passes the number the dead one left a cursor at and starts looking
 * live again while its early lines have already been skipped. A name
 * minted per process cannot do that. The sequence comparison beside it
 * is the fallback for a service too old to send one, and within its own
 * window it is sound: the highest sequence only grows inside one
 * process, so a reading below a cursor that service itself handed out is
 * a rewind no live feed can produce.
 */
export function feedRestarted(previous: { epoch: string | null; cursor: number }, next: LiveActivity): boolean {
  if (next.epoch && previous.epoch) return next.epoch !== previous.epoch;
  // A surface holding nothing has no cursor to have been rewound, and a
  // reading with no buckets at all is a deployment with nothing
  // configured rather than a feed that went backwards. The deployment's
  // own bucket counts as a bucket: on a fresh install it is the only one
  // there is, and a fallback that ignored it would call every reading a
  // restart.
  if (previous.cursor === 0 || (next.sets.length === 0 && !next.deployment)) return false;
  const highest = next.sets.reduce((high, s) => Math.max(high, s.latestSequence), next.deployment?.latestSequence ?? 0);
  return highest < previous.cursor;
}

/**
 * The cursor the next poll sends, from a reading and the cursor that
 * fetched it.
 *
 * Two rules, and both of them were a defect in a surface that reimplemented
 * this.
 *
 * It comes from what the service says it HOLDS, never from what happened
 * to arrive. Those differ on every idle poll, which is most polls: a
 * cursor rebuilt from the events in hand falls to zero the moment a
 * reading is empty, and zero means "send me whatever you still hold", so
 * every quiet tick asks a twenty-set deployment for its entire held tail
 * again, per tab. Nothing looks wrong while it happens, because the merge
 * deduplicates by sequence. It only shows up as the window overflowing
 * and the panel claiming, permanently, that earlier lines are gone.
 *
 * And a bucket that was cut short holds it back. The service answers a
 * cursor with the OLDEST slice above it and sets `truncated` when the
 * limit stopped it there, which is an invitation to ask again from where
 * it stopped. A cursor that jumped to the highest sequence anywhere in
 * the reading steps clean over the rest of that bucket instead: those
 * lines are still held, still inside the service's buffer, and are never
 * requested again by anybody, with nothing in any later response saying
 * they were skipped. One busy bucket beside one quiet one is all it
 * takes.
 */
export function nextCursor(previous: number, reading: LiveActivity): number {
  let highest = previous;
  // The lowest page boundary among the buckets that said they have more
  // to hand over, or 0 when none did.
  let page = 0;
  const consider = (events: SetActivityEvent[], latestSequence: number, truncated: boolean) => {
    if (latestSequence > highest) highest = latestSequence;
    for (const e of events) if (e.sequence > highest) highest = e.sequence;
    // A bucket that says it was cut short and hands over nothing names no
    // page boundary, so there is nothing to hold back to. The service
    // cannot produce that reading (it only truncates a slice it has
    // filled), and treating it as a boundary would stall the cursor on
    // the same window for ever.
    if (!truncated || events.length === 0) return;
    // Oldest first on the wire, so the last one handed over is the page
    // boundary.
    const boundary = events[events.length - 1].sequence;
    if (page === 0 || boundary < page) page = boundary;
  };
  for (const s of reading.sets) consider(s.events, s.latestSequence, s.truncated);
  // The deployment's own bucket counts for the same reason it is
  // rendered: on a deployment with nothing configured it is the only
  // bucket there is, and a cursor built from `sets` alone would sit at
  // zero for ever while every poll refetched the same window (#593).
  if (reading.deployment) {
    consider(reading.deployment.events, reading.deployment.latestSequence, reading.deployment.truncated);
  }
  // Never backwards, whatever a reading says: a cursor that rewinds
  // re-requests everything above the point it rewound to on every tick.
  if (page > 0 && page < highest) return Math.max(previous, page);
  return highest;
}

/**
 * Folds one reading into the single window the dock holds.
 *
 * Every bucket in the reading is flattened into one synthetic set, and
 * mergeActivity does the rest: dedup by sequence, order by sequence, trim
 * to the window, and carry `dropped` forward once lines have been lost.
 * That is the same function the strips use and it is used for the same
 * three properties, at the dock's own buffer size.
 *
 * Deduplicating by sequence is correct and cheap because the engine's
 * counter is one counter for the whole process, which is the property
 * the cursor already relies on. What this does NOT do is decide that
 * cursor: it is a window, and a window is what happened to arrive.
 * useActivityWindow takes the cursor from what the service says it holds
 * (see nextCursor), which is a different number on every idle poll and is
 * the whole of the defect this panel shipped with. The set a line belongs to rides along as
 * its own `backup_set` field, filled in from the bucket it arrived in for
 * the events that do not already carry one: an event the service put in
 * a set's bucket IS about that set, so writing it down is recording what
 * the service already decided rather than guessing.
 */
export function foldReading(previous: SetActivity | undefined, next: LiveActivity): SetActivity {
  const events: SetActivityEvent[] = [];
  for (const set of next.sets) {
    for (const e of set.events) {
      events.push(e.fields.backup_set ? e : { ...e, fields: { ...e.fields, backup_set: set.setId } });
    }
  }
  for (const e of next.deployment?.events ?? []) events.push(e);
  events.sort((a, b) => a.sequence - b.sequence);

  // Every bucket's open actions, in the order they started (issue #625).
  // The deployment's are the ones this dock exists for: a cycle belongs
  // to no single backup set, so a cycle that announced itself and went
  // quiet has no strip to be reported on and this is the only surface
  // that can say so.
  const unfinishedActions = [
    ...next.sets.flatMap((s) => s.unfinishedActions ?? []),
    ...(next.deployment?.unfinishedActions ?? [])
  ].sort((a, b) => a.sequence - b.sequence);

  const dropped = next.sets.some((s) => s.dropped) || (next.deployment?.dropped ?? false);
  // The earliest sequence any bucket is still holding. Zero when nothing
  // is held anywhere, which is the honest answer for a process that has
  // said nothing yet: a bound taken over an empty set of buckets would
  // otherwise come out as infinity and read as a window starting after
  // everything that has ever happened.
  const bounds = [...next.sets.map((s) => s.oldestSequence), next.deployment?.oldestSequence ?? 0].filter((n) => n > 0);
  if (events.length > 0) bounds.push(events[0].sequence);
  const oldest = bounds.length > 0 ? Math.min(...bounds) : 0;

  const reading: SetActivity = {
    setId: "",
    active: next.sets.some((s) => s.active),
    stage: null,
    artifact: null,
    artifactsCompleted: 0,
    artifactsTotal: null,
    progressBasis: "unknown",
    bytesTransferred: null,
    bytesTotal: null,
    bytesPerSecond: null,
    failures: 0,
    outcome: null,
    startedAt: null,
    finishedAt: null,
    events,
    unfinishedActions,
    truncated: next.sets.some((s) => s.truncated) || (next.deployment?.truncated ?? false),
    dropped,
    oldestSequence: oldest,
    // The newest sequence any bucket says it HOLDS, which is not the
    // newest that arrived: an idle reading hands over nothing while the
    // service is still holding four thousand lines. Reporting the arrival
    // here would be a window claiming to be a bound.
    latestSequence: next.sets.reduce(
      (high, s) => Math.max(high, s.latestSequence),
      Math.max(next.deployment?.latestSequence ?? 0, events.length > 0 ? events[events.length - 1].sequence : 0)
    )
  };
  return mergeActivity(previous, reading, DOCK_BUFFER);
}


/** The window the docked terminal draws, with the process it belonged to. */
export interface TerminalGeneration {
  /** When the reading that revealed the restart was observed. */
  at: string;
  /** What the dead process left, frozen. */
  window: SetActivity;
}

/** Everything any terminal draws. */
export interface TerminalState {
  /** The process the cursor belongs to, or null before the first reading. */
  epoch: string | null;
  /** The sequence the next reading asks for lines above. */
  cursor: number;
  /** One merged window per backup set, keyed by id. A restart drops these:
   *  a bar and a step sentence for a process that has gone are lies about
   *  work in flight. */
  sets: Record<string, SetActivity>;
  /** The one window over every bucket the docked terminal draws. */
  combined: SetActivity | undefined;
  /** Windows of processes that have restarted, oldest first. A terminal is
   *  a log, and the lines before a crash are the evidence of why. */
  generations: TerminalGeneration[];
  /** The last reading, for the facts about it rather than about the window
   *  (which sets exist right now). */
  reading: LiveActivity | null;
  /** The last poll's failure, or null. The last good window stays under it. */
  error: ApiError | null;
  /** The interval the service asked to be polled at. */
  pollAfterMs: number;
}

/** How many restarted processes' lines are kept above the live ones. */
export const MAX_GENERATIONS = 5;

const INITIAL: TerminalState = {
  epoch: null, cursor: 0, sets: {}, combined: undefined, generations: [], reading: null, error: null, pollAfterMs: FALLBACK_POLL_MS
};

/** The shared terminal state. Registered, so the test reset clears it. */
export const terminalNode = registerInput<TerminalState>("app.terminal", INITIAL);

/**
 * The state after one reading, as a pure function of the state before it.
 *
 * A restart is detected from the epoch (or, for a service too old to send
 * one, from the sequence going backwards) and handled in one place for every
 * terminal: the per-set windows are dropped, the combined window is frozen
 * into `generations`, and the cursor goes back to 0. A restart reading was
 * fetched with the DEAD process's cursor, which the live one cannot honour,
 * so its bounds may not be believed: taking its latestSequence would skip
 * every line the new process holds below it. The next poll refills.
 */
export function ingestReading(state: TerminalState, reading: LiveActivity): TerminalState {
  const restarted = feedRestarted({ epoch: state.epoch, cursor: state.cursor }, reading);
  const generations =
    restarted && state.combined
      ? [...state.generations, { at: reading.observedAt, window: state.combined }].slice(-MAX_GENERATIONS)
      : state.generations;
  const sets: Record<string, SetActivity> = restarted ? {} : { ...state.sets };
  for (const set of reading.sets) sets[set.setId] = mergeActivity(sets[set.setId], set, PAGE_BUFFER);
  return {
    epoch: reading.epoch || null,
    cursor: restarted ? 0 : nextCursor(state.cursor, reading),
    sets,
    combined: foldReading(restarted ? undefined : state.combined, reading),
    generations,
    reading,
    error: null,
    pollAfterMs: reading.pollAfterMs && reading.pollAfterMs > 0 ? reading.pollAfterMs : FALLBACK_POLL_MS
  };
}

/** Commits one reading into the shared node. */
export function writeReading(reading: LiveActivity): void {
  graph.commit("terminal/reading", (tx) => tx.set(terminalNode, ingestReading(graph.read(terminalNode), reading)));
}

/** Commits a failed poll. The window stays: blanking a panel somebody is
 *  reading is a worse answer than an old one that says it is old. */
export function writeFailure(error: ApiError): void {
  graph.commit("terminal/failure", (tx) => tx.set(terminalNode, { ...graph.read(terminalNode), error }));
}

// ---------------------------------------------------------------------------
// The one reader.

let holders = 0;
let feedApi: Pick<RetndApi, "getLiveActivity"> | null = null;
let timer: ReturnType<typeof setTimeout> | undefined;
let inflight = false;
/** A refresh asked for while a reading was in flight: run one more after. */
let again = false;
/** Bumped whenever the feed is restarted, so an answer to a question asked
 *  by the previous feed is not written into the new one. */
let generation = 0;

function schedule(): void {
  if (timer !== undefined) clearTimeout(timer);
  timer = setTimeout(() => void poll(), graph.read(terminalNode).pollAfterMs);
}

async function poll(): Promise<void> {
  if (!feedApi) return;
  if (inflight) { again = true; return; }
  inflight = true;
  const mine = generation;
  const api = feedApi;
  try {
    const reading = await api.getLiveActivity({ since: graph.read(terminalNode).cursor, limit: POLL_LIMIT });
    if (mine === generation) writeReading(reading);
  } catch (e) {
    if (mine === generation) {
      const error = asApiError(e);
      // A session that is gone is not a failure of this read (#795).
      if (error.code === "UNAUTHENTICATED") reportSessionLost();
      writeFailure(error);
    }
  } finally {
    // Only the feed that asked clears the flag: a stale answer arriving after
    // a restart of the feed must not release a newer reading's claim.
    if (mine === generation) inflight = false;
  }
  if (mine !== generation || holders === 0) return;
  if (again) { again = false; void poll(); return; }
  schedule();
}

/**
 * Starts (or joins) the one reader. Returns the release for the caller's
 * cleanup; the reader stops when the last holder lets go.
 *
 * The first holder starts from an empty node and reads at once. Later holders
 * read nothing: they find the node already filled, which is the point. A holder arriving with a
 * different api (a test, a re-login) starts the state over, because a
 * cursor taken from one service means nothing to another.
 */
export function acquireTerminalFeed(api: Pick<RetndApi, "getLiveActivity">): () => void {
  if (holders > 0 && feedApi !== api) stopFeed(true);
  holders += 1;
  if (holders === 1) {
    // Nobody was holding the reader, so whatever the node still has is a
    // window nothing has been updating: lines and a progress bar describing
    // a moment that has passed. Start from nothing.
    graph.commit("terminal/reset", (tx) => tx.set(terminalNode, INITIAL));
    feedApi = api;
    void poll();
  }
  // A release belongs to the feed it joined. If the feed was restarted or
  // reset since (another api took over, a test cleared it), this holder was
  // already dropped with it and letting go again would count it twice.
  const joined = generation;
  let released = false;
  return () => {
    if (released || joined !== generation) return;
    released = true;
    holders -= 1;
    if (holders === 0) stopFeed(false);
  };
}

function stopFeed(reset: boolean): void {
  generation += 1;
  if (timer !== undefined) clearTimeout(timer);
  timer = undefined;
  inflight = false;
  again = false;
  feedApi = null;
  if (reset) {
    holders = 0;
    graph.commit("terminal/reset", (tx) => tx.set(terminalNode, INITIAL));
  }
}

/** Asks for a reading now, out of band with the timer. The engine records
 *  what it did before it answers a request, so a reading issued the moment
 *  that answer lands carries the lines it emitted, and every terminal gets
 *  them in the same commit. */
export function refreshTerminalFeed(): void {
  void poll();
}

/** Test-only: stops the reader and empties the state. */
export function resetTerminalForTests(): void {
  holders = 0;
  stopFeed(false);
  graph.commit("terminal/reset", (tx) => tx.set(terminalNode, INITIAL));
}
