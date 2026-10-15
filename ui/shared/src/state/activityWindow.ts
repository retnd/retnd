/**
 * The pure half of the shared terminal state (issues #1177, #1181): how one
 * reading folds into the windows every terminal draws.
 *
 * Nothing here talks to the network or to the graph. The reader in
 * state/terminalStore owns the polling and the one causl node; these take
 * the state before a reading and give back the state after it, which is what
 * lets the tests drive every edge (a replay, a restart, a bounded window, an
 * idle tick) without a timer.
 *
 * # An idle reading changes nothing, and says so by identity
 *
 * Most readings are idle: every bucket answers with no events and the same
 * bounds. Each fold here hands back the object it was given when the reading
 * changes nothing that fold owns, all the way up to the state itself. A
 * terminal subscribes to the node with `useCausl`, so a new object on every
 * tick woke every terminal, the whole set page and the dock's 1000-line
 * pipeline for nothing. Keeping identity is what makes a quiet tab quiet.
 */
import type { ApiError } from "@shared/api/contracts";
import type { LiveActivity, SetActivity, SetActivityEvent, UnfinishedAction } from "@shared/types/activity";

/** How many lines a surface holds per set.
 *
 * The service keeps a bounded tail of its own and this keeps a bounded
 * window onto it, because an unbounded buffer in a tab left open for a
 * week is a leak with a nice name. It is deliberately larger than a strip
 * shows, so scrolling back reaches something, and deliberately far
 * smaller than the durable record: the full history is the Activity page,
 * one click away. */
export const PAGE_BUFFER = 300;

/** The cadence used when the service has not answered yet, or answered
 *  with nothing usable. Slow on purpose: the fast cadence is a claim that
 *  something is moving, and before the first answer nothing has claimed
 *  that. */
export const FALLBACK_POLL_MS = 10_000;

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
  const ordered = combine(previous?.events ?? [], next.events);
  const events = ordered.length > buffer ? ordered.slice(-buffer) : ordered;
  // The surface's own window is the third way to lose a line and it
  // counts the same: trimming to `buffer` throws lines away exactly as
  // the service's ring does.
  const trimmed = ordered.length > events.length;
  // Whether this window now reaches back at least as far as the service's
  // own buffer does. When it does, every line anybody still holds is on
  // screen, so there is nothing between what the page shows and what the
  // service has.
  const reachesTheServicesOldest = events.length > 0 && next.oldestSequence > 0 && events[0].sequence <= next.oldestSequence;
  const merged: SetActivity = {
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
  // An idle bucket that says what it said last time is the same window.
  // Handing back the old object is what stops every subscriber re-rendering
  // on a tick that changed nothing.
  return previous && sameWindow(previous, merged) ? previous : merged;
}

/**
 * The held lines followed by the new ones, by sequence and without repeats.
 *
 * The wire hands a bucket over oldest first, and a reading that answers a
 * cursor starts above everything already held, so the usual case is a plain
 * append. Only a reading that overlaps (a retry, a truncated bucket asked
 * again) pays for the map and the sort. No new lines at all returns the held
 * array itself, which is what lets an idle reading keep identity.
 */
function combine(held: SetActivityEvent[], incoming: SetActivityEvent[]): SetActivityEvent[] {
  if (incoming.length === 0) return held;
  let ascending = true;
  for (let i = 1; i < incoming.length; i++) {
    if (incoming[i].sequence <= incoming[i - 1].sequence) { ascending = false; break; }
  }
  if (ascending && (held.length === 0 || incoming[0].sequence > held[held.length - 1].sequence)) {
    return held.length === 0 ? incoming : held.concat(incoming);
  }
  const bySequence = new Map<number, SetActivityEvent>();
  for (const e of held) bySequence.set(e.sequence, e);
  for (const e of incoming) bySequence.set(e.sequence, e);
  return [...bySequence.values()].sort((a, b) => a.sequence - b.sequence);
}

/** Whether two windows draw the same thing: the same lines (by identity,
 *  which combine keeps when nothing arrived) and the same facts. */
function sameWindow(a: SetActivity, b: SetActivity): boolean {
  return (
    a.events === b.events &&
    a.setId === b.setId &&
    a.active === b.active &&
    a.stage === b.stage &&
    a.artifact === b.artifact &&
    a.artifactsCompleted === b.artifactsCompleted &&
    a.artifactsTotal === b.artifactsTotal &&
    a.progressBasis === b.progressBasis &&
    a.bytesTransferred === b.bytesTransferred &&
    a.bytesTotal === b.bytesTotal &&
    a.bytesPerSecond === b.bytesPerSecond &&
    a.failures === b.failures &&
    a.outcome === b.outcome &&
    a.startedAt === b.startedAt &&
    a.finishedAt === b.finishedAt &&
    a.truncated === b.truncated &&
    a.dropped === b.dropped &&
    a.oldestSequence === b.oldestSequence &&
    a.latestSequence === b.latestSequence &&
    sameActions(a.unfinishedActions, b.unfinishedActions)
  );
}

function sameActions(a: UnfinishedAction[] | undefined, b: UnfinishedAction[] | undefined): boolean {
  if (a === b) return true;
  const x = a ?? [];
  const y = b ?? [];
  if (x.length !== y.length) return false;
  for (let i = 0; i < x.length; i++) {
    const p = x[i];
    const q = y[i];
    if (p.actionId !== q.actionId || p.action !== q.action || p.startedAt !== q.startedAt || p.sequence !== q.sequence) return false;
  }
  return true;
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

/** What a terminal needs from the last reading itself, as opposed to the
 *  windows folded from it: which sets the deployment has right now (the
 *  dock's chips). Kept apart from the reading so the 200-line payload is not
 *  held twice, and so a reading naming the same sets keeps this identity. */
export interface ReadingFacts {
  setIds: string[];
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
  /** How many readings in a row each held set has been missing from. A set
   *  that was removed stops appearing, and its window goes once this
   *  reaches EVICT_AFTER, rather than living for as long as the tab does. */
  absent: Record<string, number>;
  /** The one window over every bucket the docked terminal draws. */
  combined: SetActivity | undefined;
  /** Windows of processes that have restarted, oldest first. A terminal is
   *  a log, and the lines before a crash are the evidence of why. */
  generations: TerminalGeneration[];
  /** The last reading's own facts, or null before the first. */
  reading: ReadingFacts | null;
  /** The last poll's failure, or null. The last good window stays under it. */
  error: ApiError | null;
  /** The interval the service asked to be polled at. */
  pollAfterMs: number;
}

/** How many restarted processes' lines are kept above the live ones. */
export const MAX_GENERATIONS = 5;

/** How many readings in a row a set can be missing from before its window
 *  is dropped. More than one, so a reading narrowed or cut short for some
 *  reason of its own does not blank a strip somebody is reading. */
export const EVICT_AFTER = 3;

export const INITIAL_TERMINAL: TerminalState = {
  epoch: null, cursor: 0, sets: {}, absent: {}, combined: undefined, generations: [], reading: null, error: null,
  pollAfterMs: FALLBACK_POLL_MS
};

/**
 * The state after one reading, as a pure function of the state before it.
 *
 * A restart is detected from the epoch (or, for a service too old to send
 * one, from the sequence going backwards) and handled in one place for every
 * terminal: the per-set windows are dropped, the combined window is frozen
 * into `generations`, and the cursor goes back to 0. A restart reading was
 * fetched with the DEAD process's cursor, which the live one cannot honour,
 * so neither its bounds nor its lines can be believed: folding them in drew
 * a partial window for a tick. It contributes nothing but the new epoch, and
 * the reader asks again from 0 at once (see `restartedBy`).
 *
 * When the reading changes nothing (the usual idle tick) this returns
 * `state` itself, and each set window and the docked window it leaves alone
 * keep their identity, so only the terminals whose lines moved re-render.
 */
export function ingestReading(state: TerminalState, reading: LiveActivity): TerminalState {
  const pollAfterMs = reading.pollAfterMs && reading.pollAfterMs > 0 ? reading.pollAfterMs : FALLBACK_POLL_MS;
  const facts = factsOf(state.reading, reading);
  if (feedRestarted({ epoch: state.epoch, cursor: state.cursor }, reading)) {
    return {
      ...INITIAL_TERMINAL,
      epoch: reading.epoch || null,
      generations: state.combined
        ? [...state.generations, { at: reading.observedAt, window: state.combined }].slice(-MAX_GENERATIONS)
        : state.generations,
      reading: facts,
      pollAfterMs
    };
  }

  let sets = state.sets;
  const present = new Set<string>();
  for (const set of reading.sets) {
    present.add(set.setId);
    const held = sets[set.setId];
    const merged = mergeActivity(held, set, PAGE_BUFFER);
    if (merged === held) continue;
    if (sets === state.sets) sets = { ...state.sets };
    sets[set.setId] = merged;
  }
  let absent = state.absent;
  for (const id of Object.keys(state.sets)) {
    const missed = present.has(id) ? 0 : (state.absent[id] ?? 0) + 1;
    if (missed === (state.absent[id] ?? 0)) continue;
    if (absent === state.absent) absent = { ...state.absent };
    if (missed === 0) delete absent[id];
    else absent[id] = missed;
    if (missed >= EVICT_AFTER) {
      if (sets === state.sets) sets = { ...state.sets };
      delete sets[id];
      delete absent[id];
    }
  }

  const combined = foldReading(state.combined, reading);
  const epoch = reading.epoch || null;
  const cursor = nextCursor(state.cursor, reading);
  if (
    sets === state.sets && absent === state.absent && combined === state.combined && facts === state.reading &&
    epoch === state.epoch && cursor === state.cursor && pollAfterMs === state.pollAfterMs && state.error === null
  ) {
    return state;
  }
  return { epoch, cursor, sets, absent, combined, generations: state.generations, reading: facts, error: null, pollAfterMs };
}

/** Whether this reading is the one that reveals a restart. The reader asks
 *  again at once when it is, rather than leaving the panels empty for a
 *  whole poll interval. */
export function restartedBy(state: TerminalState, reading: LiveActivity): boolean {
  return feedRestarted({ epoch: state.epoch, cursor: state.cursor }, reading);
}

function factsOf(previous: ReadingFacts | null, reading: LiveActivity): ReadingFacts {
  const setIds = reading.sets.map((s) => s.setId);
  if (previous && previous.setIds.length === setIds.length && previous.setIds.every((id, i) => id === setIds[i])) return previous;
  return { setIds };
}
