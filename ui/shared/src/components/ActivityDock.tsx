/**
 * The terminal docked to the bottom of every page (issue #599).
 *
 * The dashboard's strips (#573) answer "what is THIS set doing", with a
 * bar and a step sentence read at a glance while looking at that set's
 * row. This answers a different question: "what has this deployment done,
 * most recently, including the thing I just clicked". Merging the two
 * would mean either losing the per-set bar or drawing one bar for a
 * machine running several sets, and neither is a reading, so the strips
 * stay exactly as they are and this reads the same endpoint.
 *
 * # Why it lives in the shell
 *
 * It mounts in AppShell, as a sibling of the nav/main row rather than a
 * child of <main>. React Router swaps what is inside <main>, so a panel
 * in there unmounts on every navigation and starts again from cursor 0.
 * A sibling never unmounts, so the buffer, the cursor, the epoch, the
 * scroll position and the collapsed state all survive every navigation in
 * the session.
 *
 * It is fixed to the browser window rather than sitting in flow (#617).
 * In flow it rode the end of the document, so on any page longer than a
 * screen the always-on panel scrolled out of view and was only there once
 * you had already reached the bottom, which is the opposite of what it is
 * for. Fixed positioning costs the property that arrangement had for
 * free: nothing below shrinks any more, so the shell reserves
 * dockReservedHeight() under the content column and the last row of a
 * long table can still be scrolled clear of the terminal.
 *
 * A full browser reload is allowed to lose the buffer. The engine holds
 * the authoritative tail and this refills from it on mount with since=0,
 * so a reload costs one poll rather than the history.
 *
 * # What it does with a restart, and why it differs from the strips
 *
 * The polling loop under this panel is the shared terminal state's
 * (state/terminalStore, #596 and #1177), which is where the cursor, the
 * epoch check, the timer and the out-of-band refresh live, once for every
 * terminal. The strips and this panel read the same node, so they change in
 * the same commit. What is this panel's is how it draws a restart, because
 * what the two surfaces do about one is deliberately opposite. A strip
 * drops everything it holds, because a progress bar and a step sentence
 * describing a process that no longer exists are active lies about work in
 * flight. A terminal is a log, and throwing away the lines leading up to a
 * crash throws away the only evidence of why it crashed. So this keeps
 * them (the store freezes them per dead process), draws a rule, and starts
 * a fresh buffer under it.
 *
 * It used to be a copy of that loop rather than a caller of it, and the
 * copy is where its cursor bug lived: rebuilt from the events that
 * happened to arrive, it fell to zero on every idle poll and asked the
 * service for its whole held tail again, invisibly, because the merge
 * deduplicates by sequence.
 *
 * The fresh buffer is structural, not cosmetic: the sequence counter is
 * per process and starts again at 1, so the new process's line 1 would
 * otherwise overwrite the dead process's line 1 in a buffer keyed by
 * sequence.
 *
 * # Two feeds, one log
 *
 * The lines come from the engine, with one exception that is the whole
 * reason state/browserNotices exists: a refusal produced in FRONT of the
 * engine never reaches its ring at all. The destructive gate, the CSRF
 * check and a dead connection all refuse before any handler runs, so
 * there is nothing on the server that could have logged them, and a
 * terminal drawing only the live feed shows nothing whatsoever for
 * exactly the presses that need explaining (issue #597). Those come off
 * that seam and are folded in by time (foldBrowserNotices), marked as
 * this browser's rather than the engine's, and they are what puts a
 * deployment-wide "Run all enabled sets" into the terminal at all. It is
 * EPIC H's standing rule that every action taken in the web UI echoes its
 * equivalent command here, and until this panel read the seam that rule
 * held for everything the engine served and for nothing it refused.
 *
 * # Shared log, private chrome
 *
 * The lines come from the engine's own buffers, so two people watching
 * one NAS see the same lines in the same order with the same sequence
 * numbers, and can quote a timestamp at each other. What is per viewer is
 * only what the viewer did to the panel: collapsed or expanded, the
 * filter, and how tall they dragged it. That is in localStorage and
 * nowhere else.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { usePlatform } from "@shared/platform/PlatformContext";
import { useTerminalFeed } from "@shared/pages/useActivityFeed";
import { DOCK_BUFFER } from "@shared/state/terminalStore";
import { activityLine, logText, UnfinishedActionsNotice } from "@shared/pages/ActivityStrip";
import type { LineTone } from "@shared/pages/ActivityStrip";
import { BROWSER_NOTICE_EVENT, noticeAsEvent } from "@shared/pages/SetActivityPanel";
import { useCausl } from "@shared/state/graph";
import { browserNoticesNode } from "@shared/state/browserNotices";
import type { BrowserNotice } from "@shared/state/browserNotices";
import type { SetActivity, SetActivityEvent, UnfinishedAction } from "@shared/types/activity";
import { clock } from "@shared/utilities/format";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { TooltipId } from "@shared/tooltips/tooltips";
import { copyText } from "@shared/utilities/copyText";

const STORAGE = {
  open: "retnd.dock.open",
  height: "retnd.dock.height",
  filter: "retnd.dock.filter"
} as const;

/** The chips when no reading has named any sets yet. One array, so the
 *  memos keyed on it do not see a new one every render. */
const NO_SETS: string[] = [];

const MIN_HEIGHT = 120;
const MAX_HEIGHT = 720;
const DEFAULT_HEIGHT = 240;

/** The collapsed bar: one line, and the height the panel reserves when it
 *  is not open. Exported because the shell reserves room for it and a
 *  second copy of the number would be a second thing to keep in step. */
export const DOCK_BAR_HEIGHT = 32;

/** The room the panel needs under it. Fixed positioning takes the dock out
 *  of flow, so nothing below it shrinks on its own any more and the shell
 *  has to leave this much or the last row of a long table sits behind the
 *  terminal with no way to scroll it clear. The 8 is the resize handle and
 *  the border. */
export function dockReservedHeight(open: boolean, height: number): number {
  return open ? height + DOCK_BAR_HEIGHT + 8 : DOCK_BAR_HEIGHT;
}

/** One thing the dock draws: a line the engine sent, a line this browser
 *  wrote, or the rule marking where a process ended. The rule is an entry
 *  rather than a flag on the next line because it has a time of its own
 *  and belongs between two lines.
 *
 *  A notice is held as the NOTICE rather than as the event it renders as,
 *  and that is the one place this panel consumes G1.4's seam differently
 *  from the per-set terminal. A BrowserNotice names a LIST of backup sets
 *  (a deployment-wide run names every enabled one), and an event has a
 *  single `backup_set` field with nowhere to put the rest. The per-set
 *  panel never meets that problem: it has already narrowed to one set
 *  with noticesForBackupSet before it converts. This panel has not, and
 *  its per-set chips are exactly the reader that needs the whole list, so
 *  the list survives into the entry and the conversion happens at the
 *  moment of drawing (see entryEvent, which calls the panel's own
 *  noticeAsEvent so both terminals and every export say the same words). */

export type DockEntry =
  | { kind: "event"; event: SetActivityEvent }
  | { kind: "restart"; at: string }
  | { kind: "notice"; notice: BrowserNotice };

/**
 * The event an entry draws as.
 *
 * One conversion, called by everything that renders: the scrollback, the
 * collapsed bar's newest line, the problem counters and both exports. A
 * second copy of it is how the screen and the clipboard come to disagree,
 * which is the argument logText already makes about itself.
 *
 * The restart rule is excluded in the TYPE rather than answered with a
 * null, because it is not an event and has no message, no level and no
 * fields to be asked for. Every caller already knows which one it is
 * holding, so making them say so costs a line and buys a function with no
 * empty answer in it.
 */
export function entryEvent(entry: Exclude<DockEntry, { kind: "restart" }>, index: number): SetActivityEvent {
  return entry.kind === "event" ? entry.event : noticeAsEvent(entry.notice, index);
}

/** What is written in front of a line, and the whole reason `scope` was
 *  plumbed through in #573 and read by nothing until now. */
export interface DockPrefix {
  label: string;
  /** The set this line belongs to, or "" for a deployment-wide one. The
   *  filter reads this; the label does not always equal it, because a
   *  request line is labelled by who made it. */
  setId: string;
  /** Whether this line is an action somebody took rather than work the
   *  engine did. */
  action: boolean;
}

/**
 * Who or what a line is about.
 *
 * Three kinds, and the third is the one that needed a deliberate
 * addition. `[engine]` and `[source/set]` come straight from the event's
 * own scope. A line for an API action names the actor the session
 * identified, and renders as `[you]` when it is this session and as the
 * account name when it is not. That distinction is the point: two people
 * administering one NAS should each be able to see that the OTHER just
 * restarted a cycle, and a panel that labelled every request `[you]`
 * would tell them the opposite.
 */
export function dockPrefix(e: SetActivityEvent, viewer: string | null): DockPrefix {
  const setId = e.scope === "set" ? e.fields.backup_set ?? "" : "";
  if (e.event === BROWSER_NOTICE_EVENT) {
    // A line this browser wrote (state/browserNotices). It is an action
    // somebody took here by definition, and the somebody is whoever is
    // reading, so it is `[you]` without consulting an actor: there is no
    // actor field on it, because the request never reached a service that
    // could have identified one. That is also why it cannot be labelled
    // by the account name the way an api_action is.
    //
    // The line's own text still says `[browser]` (ActivityStrip's
    // activityLine), and the two are not the same claim said twice.
    // `[you]` is WHO, and it is what tells the other administrator on the
    // same NAS that this was not them. `[browser]` is that the engine
    // never saw it, which is the only fact that decides whether the line
    // is evidence about the NAS at all.
    return { label: "you", setId, action: true };
  }
  if (e.event === "api_action") {
    const actor = e.fields.actor ?? "";
    const label = actor === "" ? "request" : viewer !== null && actor === viewer ? "you" : actor;
    return { label, setId, action: true };
  }
  if (e.scope === "deployment") return { label: "engine", setId: "", action: false };
  return { label: setId || "set", setId, action: false };
}

/** What makes an event the same event: its number, when it happened and what
 *  it was. The number alone is not enough, which is the point of the other
 *  two (see dockEntries). */
function eventKey(e: SetActivityEvent): string {
  return e.sequence + "|" + e.at + "|" + e.event;
}

/**
 * Every line the dock is holding, oldest first, bounded.
 *
 * The engine's events are durable (migration 0017): a restart refills the
 * feed from the journal and the sequence continues, so the first reading
 * from the new process carries lines this panel already froze above the
 * restart rule. They are the same lines, not new ones, and showing them
 * twice would tell an operator the crash happened twice. So a live line that
 * is already in the frozen history is dropped here.
 *
 * "Already in" means the same number, the same time and the same event, not
 * the number alone. An engine that does not keep its events restarts its
 * counter at 1, and its line 1 is a different line from the dead process's
 * line 1: they differ in time, so both stay.
 */
export function dockEntries(history: DockEntry[], current: SetActivity | undefined): DockEntry[] {
  const frozen = new Set<string>();
  for (const entry of history) if (entry.kind === "event") frozen.add(eventKey(entry.event));
  const live: DockEntry[] = (current?.events ?? [])
    .filter((event) => !frozen.has(eventKey(event)))
    .map((event) => ({ kind: "event", event }));
  return [...history, ...live].slice(-DOCK_BUFFER);
}

/** The dock's own filters. Never drops a line from the buffer, only from
 *  what is drawn, so clearing one brings everything back. */
export type DockFilter = "all" | "engine" | "mine" | "commands" | string;

/** The two levels the dock counts, and so the two it can filter to (#1115).
 *  The engine's third level, "info", is not a problem and has no count, so
 *  it has no button. One at a time: the source chips are one choice too,
 *  and two levels at once is just "problems", which the pair of counts
 *  already reads as. */
export type DockLevel = "warn" | "error";

/** How many held lines are warnings and how many are errors, over the
 *  WHOLE buffer. Through entryEvent, so a refusal this browser wrote is
 *  counted as the warning it is. It never looks at a filter in force: a
 *  count that shrank when it was pressed could not be pressed again to
 *  see the rest. */
export function countLevels(all: DockEntry[]): { errors: number; warnings: number } {
  let errors = 0;
  let warnings = 0;
  for (const [i, entry] of all.entries()) {
    if (entry.kind === "restart") continue;
    const level = entryEvent(entry, i).level;
    if (level === "error") errors++;
    else if (level === "warn") warnings++;
  }
  return { errors, warnings };
}

/** What a level keeps. A restart rule is not a line of either level, so it
 *  goes while a level is on and comes back with the filter off. (The index
 *  only feeds a notice's sequence in entryEvent; a notice's level does not
 *  depend on it.) */
export function filterByLevel(entries: DockEntry[], level: DockLevel | null): DockEntry[] {
  if (level === null) return entries;
  return entries.filter((entry, i) => entry.kind !== "restart" && entryEvent(entry, i).level === level);
}

/** The sentence read out when a level is switched on or off. */
export function levelStatus(level: DockLevel | null, shown: number, of: number): string {
  if (level === null) return "Showing all " + of + " lines";
  return "Showing " + shown + " of " + of + " lines, " + (level === "warn" ? "warnings" : "errors") + " only";
}

function passesFilter(entry: DockEntry, filter: DockFilter, viewer: string | null): boolean {
  if (entry.kind === "restart") return true;
  if (entry.kind === "notice") {
    // Asked here rather than through dockPrefix, because the question a
    // chip asks a notice is about the LIST of sets it names and a prefix
    // only carries one (see DockEntry).
    switch (filter) {
      case "all":
      case "mine":
        // Always: a notice is this browser's line and there is no other
        // browser whose lines could be in this buffer.
        return true;
      case "engine":
        // Never: it is the one thing in here the engine never saw.
        return false;
      case "commands":
        // It is not an api_action, which is a line the ENGINE emitted
        // about a request it served, and a notice is the opposite: a
        // request that never got that far. It still carries the command
        // the press was equivalent to, and that is what this chip is for.
        return entry.notice.command !== undefined;
      default:
        // A line belongs to a set by NAMING it, which is
        // noticesForBackupSet's rule and the reason a deployment-wide
        // refusal lists every enabled set: somebody looking at one set
        // after pressing "Run all enabled sets" is looking for the answer
        // about that set.
        return entry.notice.backupSetIds.includes(filter);
    }
  }
  const prefix = dockPrefix(entry.event, viewer);
  switch (filter) {
    case "all":
      return true;
    case "engine":
      return prefix.setId === "";
    case "mine":
      return prefix.action && prefix.label === "you";
    case "commands":
      return entry.event.event === "api_action";
    default:
      return prefix.setId === filter;
  }
}

/**
 * The engine's lines with this browser's own folded in, in the order the
 * two happened.
 *
 * This is the seam state/browserNotices was built for and its own doc
 * names two readers: the per-set terminal (G1.3) and this one (G1.2).
 * Only the first was ever wired to it, so until now the "This browser"
 * chip filtered a buffer that could not contain a browser line, and a
 * deployment-wide run announced itself in a banner and nowhere else,
 * which is EPIC H's standing rule half implemented.
 *
 * Placed by TIME, because time is the only thing the two sides share: a
 * notice is written in a browser with no access to the engine's sequence
 * counter, and cannot have one (foldNotices makes the same argument for
 * the per-set panel). What this deliberately does NOT do is re-sort the
 * engine's own lines by their timestamps. They are ordered by sequence,
 * which is one counter for the whole process and therefore a total order
 * on lines that a second-resolution timestamp cannot reproduce. So each
 * notice is INSERTED after everything already stamped at or before it and
 * the engine's order is carried through untouched.
 *
 * Both rings are bounded on their own (DOCK_BUFFER here,
 * MAX_BROWSER_NOTICES there), so this does not re-trim: trimming the
 * merged list would let a burst of refusals evict the engine lines that
 * explain them.
 */
export function foldBrowserNotices(entries: DockEntry[], notices: BrowserNotice[]): DockEntry[] {
  if (notices.length === 0) return entries;
  const out: DockEntry[] = [];
  let i = 0;
  for (const notice of notices) {
    // `!(t > notice.at)` rather than `t <= notice.at` so an unparseable
    // timestamp on an engine line keeps that line where it is instead of
    // being jumped by every notice: NaN fails both comparisons, and only
    // one of the two answers preserves what the engine sent.
    while (i < entries.length && !(entryTime(entries[i]) > notice.at)) out.push(entries[i++]);
    out.push({ kind: "notice", notice });
  }
  while (i < entries.length) out.push(entries[i++]);
  return out;
}

/** When an entry happened, as epoch milliseconds. */
function entryTime(entry: DockEntry): number {
  if (entry.kind === "notice") return entry.notice.at;
  return Date.parse(entry.kind === "restart" ? entry.at : entry.event.at);
}

/** The rule a restart draws, in the one wording both the screen and the
 *  export use. */
export function restartRule(at: string): string {
  return "──── engine restarted at " + clock(at) + " · everything above belongs to a process that has gone ────";
}

/** One word as a shell would need it typed: bare when it is made only of
 *  characters a shell leaves alone, single-quoted otherwise. Mirrors
 *  core/cliecho's shellQuote for the two values this file quotes. */
function shellWord(s: string): string {
  if (s === "") return "''";
  if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(s)) return s;
  return "'" + s.replace(/'/g, "'\\''") + "'";
}

/**
 * What every command below needs beyond its own argv, said once at the
 * top of the panel rather than repeated on every line.
 *
 * It is composed here and not in the engine, because the engine does not
 * have the information. It knows what address it is listening on, and
 * behind a reverse proxy or inside a Synology package that is not the
 * address the reader would type at their own shell. The browser does
 * know it: it is the origin this page was loaded from. And it knows the
 * session's own name, which is the account the commands would run as.
 *
 * The password is a placeholder for the reason usage() already gives for
 * these being environment variables at all: a password on a command line
 * is in every process listing on the host. This panel is exportable and
 * copy-to-clipboard by design, which is the same argument one step
 * further along, so the line never carries one and never could.
 */
export function environmentPreamble(origin: string, viewer: string | null): string {
  const url = shellWord(origin === "" ? "http://127.0.0.1:8080" : origin);
  const user = viewer === null || viewer === "" ? "<the administrator you sign in as>" : shellWord(viewer);
  return "export RETND_API_URL=" + url + " RETND_API_USERNAME=" + user + " RETND_API_PASSWORD=<your password>";
}

/**
 * The window with any command gap that the window itself contradicts
 * resolved into the one sentence about it that is true (issue #662).
 *
 * # The window an operator reported
 *
 *   [you] [browser] This deployment will not start a backup run.
 *     ... Run retnd fetch --backup-set cicd-pipeline/var-backups from a
 *     shell on this host instead.
 *   $ retnd fetch --backup-set cicd-pipeline/var-backups
 *   [you] post /operations refused: DESTRUCTIVE_OPERATIONS_DISABLED
 *   # no retnd equivalent yet · POST /api/v1/operations
 *
 * Two statements about one command, four lines apart. The advice is
 * sound — the operator ran that command and it repaired the set — and the
 * gap line then tells them the operation they were just given a command
 * for has no `retnd` equivalent. A remedy a message offers has to be
 * runnable where it is offered, or the surface must not contradict it.
 *
 * # Why the short form is the half that goes
 *
 * Both halves come from the engine and only one of them is wrong here.
 * "no retnd equivalent yet" is a claim about the ROUTE, and on a screen
 * that has just named a command it reads as a claim about the command.
 * The detail beside it is already the precise truth ("`retnd fetch` starts
 * a cycle in your own shell, not in this engine"), so the detail is
 * promoted to the line and the short claim is dropped. Nothing is
 * invented here and nothing is hidden: the route is still named, and a
 * window with no remedy in it still gets the gap line exactly as before,
 * which is what the control case beside this pins.
 *
 * # Why this is the window's decision and not activityLine's
 *
 * activityLine sees one event. The contradiction does not exist in either
 * line; it exists in their being on one screen, so it can only be
 * answered where the screen is assembled. It is applied to the entries
 * rather than to the rendering so that the panel, the clipboard and the
 * saved file all say the same words, which is the argument logText makes
 * about itself. It is idempotent, so applying it on both the panel's own
 * window and inside dockText costs nothing and cannot drift.
 */
export function withoutContradictedGaps(entries: DockEntry[]): DockEntry[] {
  const remedyOnScreen = entries.some((e) => e.kind === "notice" && (e.notice.command ?? "") !== "");
  if (!remedyOnScreen) return entries;
  return entries.map((entry) => {
    if (entry.kind !== "event") return entry;
    const f = entry.event.fields;
    if (!f.command_gap || f.command) return entry;
    return {
      kind: "event",
      event: { ...entry.event, fields: { ...f, command_gap: f.command_gap_detail ?? "", command_gap_detail: "" } }
    };
  });
}

/** The dock's text, for the clipboard and for the saved file, honouring
 *  whatever filter is on.
 *
 * logText is the strips' own builder and it is reused unchanged for the
 * argument its own doc already makes: one builder, so what an operator
 * pastes into an issue is what they were looking at. The prefixes and the
 * restart rules are laid in around it here, because they are this panel's
 * and no strip has them. The preamble, when given, is the first line for
 * the same reason it is the first line on screen: the commands under it
 * are only runnable with it said once above them. */
export function dockText(entries: DockEntry[], viewer: string | null, preamble: string | null = null): string {
  const lines = withoutContradictedGaps(entries).map((entry, i) => {
    if (entry.kind === "restart") return restartRule(entry.at);
    const event = entryEvent(entry, i);
    const prefix = dockPrefix(event, viewer);
    const [first, ...rest] = logText([event]).split("\n");
    const head = first.replace(/^(\S+)\s/, "$1 [" + prefix.label + "] ");
    return [head, ...rest].join("\n");
  });
  return (preamble === null ? lines : [preamble, ...lines]).join("\n");
}

function readStored(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    // A browser with site data blocked is a browser where the panel opens
    // at its default, not one where the panel throws into a page an
    // operator is reading.
    return null;
  }
}

function writeStored(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    /* see readStored */
  }
}

export function ActivityDock() {
  const { auth } = usePlatform();
  const viewer = auth?.username ?? null;

  // The lines this browser wrote. Read straight off G1.4's seam, the same
  // node SetActivityPanel reads, so a refusal produced in FRONT of the
  // engine reaches this panel at all: the destructive gate, the CSRF
  // check and a dead connection all refuse before any handler runs, so
  // there is nothing on the server that could have logged them and no
  // reading of the live feed can ever carry one.
  const notices = useCausl(browserNoticesNode);

  const [open, setOpen] = useState(() => readStored(STORAGE.open) !== "0");
  const [height, setHeight] = useState(() => Number(readStored(STORAGE.height)) || DEFAULT_HEIGHT);
  const [filter, setFilter] = useState<DockFilter>(() => readStored(STORAGE.filter) ?? "all");
  const [copied, setCopied] = useState(false);
  // Not stored, unlike the source chip: a filter to the problems is a
  // question about right now, and a terminal that comes back showing three
  // lines of forty with no reason on screen reads as a broken one.
  const [level, setLevel] = useState<DockLevel | null>(null);
  const [announcement, setAnnouncement] = useState("");

  // The cursor, the epoch, the timer and the restart check are the shared
  // terminal state's (state/terminalStore, #1177): one reader for every
  // terminal, so this panel and a set's own terminal change in the same
  // commit. What is this panel's is how it draws a restart: it keeps the lines
  // the dead process left, with a rule under each, where a strip drops them.
  const feed = useTerminalFeed();
  const held = feed.state.combined;
  const readingNow = feed.state.reading;
  const feedError = feed.state.error;
  const history = useMemo(
    () =>
      feed.state.generations.reduce<DockEntry[]>(
        (acc, g) => [...dockEntries(acc, g.window), { kind: "restart" as const, at: g.at }].slice(-DOCK_BUFFER),
        []
      ),
    [feed.state.generations]
  );

  useEffect(() => writeStored(STORAGE.open, open ? "1" : "0"), [open]);
  useEffect(() => writeStored(STORAGE.height, String(height)), [height]);
  useEffect(() => writeStored(STORAGE.filter, filter), [filter]);

  // Hand the reserved height to the layout. A custom property rather than
  // a prop or a context because the only reader is a padding rule one
  // level up, and the panel is resizable and collapsible: whatever
  // reserves the room has to move when either changes, and the number is
  // already here.
  useEffect(() => {
    const root = document.documentElement;
    root.style.setProperty("--dock-height", dockReservedHeight(open, height) + "px");
    return () => {
      root.style.removeProperty("--dock-height");
    };
  }, [open, height]);
  useEffect(() => {
    if (!copied) return;
    const id = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(id);
  }, [copied]);

  // The chips are a function of the latest reading rather than state of
  // their own: a set is configured or it is not, and the reading says so
  // on every poll.
  const sets = readingNow?.setIds ?? NO_SETS;
  const all = useMemo(
    () => withoutContradictedGaps(foldBrowserNotices(dockEntries(history, held), notices)),
    [history, held, notices]
  );
  // The source chip first, then the level on what it kept.
  const bySource = useMemo(() => all.filter((e) => passesFilter(e, filter, viewer)), [all, filter, viewer]);
  // The origin this page was loaded from is the address the reader would
  // type, which is the one fact the engine cannot know (see
  // environmentPreamble). Read once per viewer rather than per render.
  const preamble = useMemo(() => environmentPreamble(window.location.origin, viewer), [viewer]);

  const problems = useMemo(() => countLevels(all), [all]);
  // A level whose count has fallen to nothing (the buffer is bounded) has
  // nothing left to filter to and its button is disabled, so it could not
  // be un-pressed: treat it as off rather than leave an empty terminal
  // under a button that cannot be pressed.
  const activeLevel: DockLevel | null =
    level === "warn" && problems.warnings > 0 ? "warn" : level === "error" && problems.errors > 0 ? "error" : null;
  const shown = useMemo(() => filterByLevel(bySource, activeLevel), [bySource, activeLevel]);
  const toggleLevel = useCallback(
    (next: DockLevel) => {
      const target = activeLevel === next ? null : next;
      setLevel(target);
      setAnnouncement(levelStatus(target, filterByLevel(bySource, target).length, bySource.length));
    },
    [activeLevel, bySource]
  );

  const copy = useCallback(() => {
    // "copied" is shown only when the text really went to the clipboard
    // (retnd#1049); on a plain-http origin the fallback chain in
    // copyText gets there, and "Save .txt" stays the route when it cannot.
    void copyText(dockText(shown, viewer, preamble)).then((outcome) => {
      if (outcome === "copied") setCopied(true);
    });
  }, [shown, viewer, preamble]);

  const save = useCallback(() => {
    const blob = new Blob([dockText(shown, viewer, preamble)], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    // The deployment and the time, not a set id: this file is every set.
    anchor.download = "retnd-terminal-" + new Date().toISOString().replace(/[:.]/g, "-") + ".txt";
    anchor.click();
    URL.revokeObjectURL(url);
  }, [shown, viewer, preamble]);

  const dropped = held?.dropped ?? false;

  return (
    <section
      aria-label="Terminal"
      style={{
        position: "fixed",
        left: 0,
        right: 0,
        bottom: 0,
        zIndex: 30,
        display: "flex",
        flexDirection: "column",
        borderTop: "1px solid var(--border)",
        background: "var(--surface)",
        maxHeight: open ? dockReservedHeight(true, height) : DOCK_BAR_HEIGHT
      }}
    >
      {/* Exactly one line tall, always, and DOCK_BAR_HEIGHT rather than a
          second copy of 32: the shell reserves room under the content
          column from that constant and the section's maxHeight above is
          computed from it, so a bar that measured differently from what
          was reserved is content sitting behind the terminal (#617). It
          is the chips inside it that give way (see below), never this. */}
      <div
        style={{
          flex: "none",
          height: DOCK_BAR_HEIGHT,
          display: "flex",
          alignItems: "center",
          gap: "var(--space-3)",
          padding: "0 var(--space-4)",
          fontSize: "var(--text-xs)"
        }}
      >
        <InfoTooltip id="activity.dock.toggle">
          <button
            type="button"
            className="activity-toolbar__toggle"
            onClick={() => setOpen((v) => !v)}
            aria-expanded={open}
          >
            {open ? "Hide terminal" : "Show terminal"}
          </button>
        </InfoTooltip>

        {/* Collapsed it keeps the newest line and a count of what went
            wrong, because a terminal that collapses to nothing teaches an
            operator to stop opening it. */}
        {!open ? (
          <InfoTooltip id="activity.dock.newest" style={{ flex: 1, minWidth: 0 }}>
            <span className="mono" style={{ color: "var(--text-3)", flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
              {newestLine(all, viewer)}
            </span>
          </InfoTooltip>
        ) : (
          /* One line that scrolls, never a second row.
           *
           * These were a wrapping flex row inside a bar pinned at
           * DOCK_BAR_HEIGHT, so as soon as the chips did not fit, the
           * second row was drawn outside the bar and clipped by the
           * section's own maxHeight: the chips past the fold were not
           * merely cramped, they were gone, with nothing on screen saying
           * so. Four backup sets at 1280px is enough, and more sets does
           * it at any width. It was found while recording the
           * documentation GIFs, which were taken at 1440px to dodge it.
           *
           * Letting the bar grow instead is the fix that looks obvious
           * and is not: AppShell reserves dockReservedHeight() under the
           * content column from this exact constant, and the panel's own
           * maxHeight is computed from it, so a taller bar puts the last
           * row of a long table back behind the terminal, which is #617
           * arriving again from the other side.
           *
           * `minWidth: 0` is what actually makes it shrink. A flex
           * child's default min-width is its content, so `flex: 1` on a
           * row of eight chips does not shrink at all and overflows its
           * parent no matter what overflow says. The class draws a 4px
           * scrollbar rather than hiding it. Hiding it read better and
           * measured worse: a plain vertical wheel moves this row 0px,
           * because it only overflows horizontally, so the ways across
           * were shift+wheel, a trackpad swipe or tabbing, and none of
           * those tells you there is anything to reach. 4px of a 32px bar
           * still clears the 24px buttons, and on macOS overlay
           * scrollbars it takes no layout space at all. */
          <span
            role="group"
            aria-label="Filter the terminal"
            className="activity-toolbar__filters"
            style={{
              flex: "1 1 0",
              minWidth: 0,
              display: "flex",
              gap: 6,
              flexWrap: "nowrap",
              overflowX: "auto",
              overflowY: "hidden"
            }}
          >
            {chips(sets).map((chip) => (
              <InfoTooltip
                key={chip.value}
                id={FILTER_TIPS[chip.value] ?? "activity.dock.filter-set"}
                style={{ flex: "0 0 auto" }}
              >
                <button
                  type="button"
                  className="activity-toolbar__button"
                  aria-pressed={filter === chip.value}
                  onClick={() => setFilter(chip.value)}
                  style={{
                    // Never shrink and never wrap the label: a chip narrowed
                    // to fit is a set id an operator cannot read, which is a
                    // quieter version of the same defect.
                    flex: "0 0 auto",
                    whiteSpace: "nowrap",
                    ...(filter === chip.value ? { background: "var(--accent-quiet)", color: "var(--text)" } : {})
                  }}
                >
                  {chip.label}
                </button>
              </InfoTooltip>
            ))}
          </span>
        )}

        {/* Always both, so the bar does not jump when the first warning
            arrives; a count of zero is drawn and cannot be pressed. */}
        <InfoTooltip id="activity.dock.problems" style={{ flex: "none" }}>
          <span role="group" aria-label="Show only problems" className="mono" style={{ flex: "none", display: "flex", gap: 6 }}>
            <LevelButton level="error" count={problems.errors} pressed={activeLevel === "error"} onPress={toggleLevel} />
            <LevelButton level="warn" count={problems.warnings} pressed={activeLevel === "warn"} onPress={toggleLevel} />
          </span>
        </InfoTooltip>

        {/* Never a spinner over a word: while the poll is failing the dock
            says the reading is not refreshing rather than drawing
            something that claims the process is alive. */}
        {/* `flex: none` on everything to the right of the chips: the chip
            strip is the only thing in this bar that may shrink, and a
            line count or a Save button squeezed to nothing would be the
            clip moved rather than fixed. */}
        <InfoTooltip id="activity.dock.status" style={{ flex: "none" }}>
          <span className="mono" style={{ flex: "none", color: feedError ? "var(--warn)" : "var(--text-3)" }}>
            {feedError
              ? "not refreshing"
              : activeLevel
                ? shown.length + " of " + bySource.length + (bySource.length === 1 ? " line" : " lines")
                : shown.length + (shown.length === 1 ? " line" : " lines")}
          </span>
        </InfoTooltip>
        {/* Said once per press, from the handler, and not derived from the
            count: a status that changed with every live line would read the
            tail out a second time on top of the log's own announcements. */}
        <span role="status" className="visually-hidden">
          {announcement}
        </span>

        {/* What a line in the scrollback is made of, said once beside the
            log rather than on each of the thousand lines it can hold. */}
        <InfoTooltip id="activity.dock.log-format" style={{ flex: "none" }} />

        {open ? (
          <>
            {copied ? (
              <InfoTooltip id="activity.dock.copied" alignEnd>
                <span style={{ color: "var(--ok)", fontWeight: 600 }}>copied</span>
              </InfoTooltip>
            ) : null}
            <InfoTooltip id="activity.dock.copy" alignEnd>
              <button type="button" className="activity-toolbar__button" onClick={copy}>
                Copy
              </button>
            </InfoTooltip>
            <InfoTooltip id="activity.dock.save" alignEnd>
              <button type="button" className="activity-toolbar__button" onClick={save}>
                Save .txt
              </button>
            </InfoTooltip>
          </>
        ) : null}
      </div>

      {open ? (
        <DockLog
          entries={shown}
          viewer={viewer}
          height={height}
          onResize={setHeight}
          dropped={dropped}
          preamble={preamble}
          unfinished={held?.unfinishedActions ?? []}
        />
      ) : null}
    </section>
  );
}

/** One of the two problem counts, as a toggle (#1115).
 *
 *  The name states what pressing does ("Show 3 warnings"), not just the
 *  number, and it is the same whether or not the button is pressed:
 *  aria-pressed carries that. Pressed is drawn with weight, an underline
 *  and a ring as well as the fill, so it survives a greyscale screen. */
function LevelButton({
  level,
  count,
  pressed,
  onPress
}: {
  level: DockLevel;
  count: number;
  pressed: boolean;
  onPress(level: DockLevel): void;
}) {
  const noun = level === "error" ? "error" : "warning";
  const label = count + " " + noun + (count === 1 ? "" : "s");
  return (
    <button
      type="button"
      className={"activity-toolbar__button activity-dock__level activity-dock__level--" + level}
      aria-pressed={pressed}
      aria-label={"Show " + label}
      disabled={count === 0}
      onClick={() => onPress(level)}
    >
      {label}
    </button>
  );
}

/** The newest line, for the collapsed bar.
 *
 *  A line this browser wrote counts, and it is the case that matters most
 *  here: a refusal produced in front of the engine arrives at the moment
 *  somebody presses a button, which is the moment they are most likely to
 *  be looking at a collapsed panel. */
function newestLine(entries: DockEntry[], viewer: string | null): string {
  for (let i = entries.length - 1; i >= 0; i--) {
    const entry = entries[i];
    if (entry.kind === "restart") continue;
    const event = entryEvent(entry, i);
    const prefix = dockPrefix(event, viewer);
    const line = activityLine(event);
    return line.time + " [" + prefix.label + "] " + line.text.split("\n")[0];
  }
  return "nothing has happened since this page was opened";
}

function chips(sets: string[]): { value: DockFilter; label: string }[] {
  return [
    { value: "all", label: "Everything" },
    { value: "engine", label: "Engine" },
    ...sets.map((id) => ({ value: id as DockFilter, label: id })),
    { value: "mine", label: "This browser" },
    { value: "commands", label: "Commands only" }
  ];
}

/** Which explanation a chip carries. The four fixed chips each say
 *  something different about what they keep; the per-set chips all say the
 *  same thing, so they share one entry rather than inventing a registry id
 *  per configured set. */
const FILTER_TIPS: Record<string, TooltipId> = {
  all: "activity.dock.filter-all",
  engine: "activity.dock.filter-engine",
  mine: "activity.dock.filter-mine",
  commands: "activity.dock.filter-commands"
};

/**
 * The scrollback.
 *
 * It follows the tail and STOPS following the moment somebody scrolls up,
 * which is the fix for what ActivityLogView does today: pinning scrollTop
 * on every new sequence fights an operator reading back through a
 * failure. Following resumes when they reach the bottom again, so nobody
 * has to find a control for it.
 */
function DockLog({
  entries,
  viewer,
  height,
  onResize,
  dropped,
  preamble,
  unfinished
}: {
  entries: DockEntry[];
  viewer: string | null;
  height: number;
  onResize(next: number): void;
  dropped: boolean;
  preamble: string;
  /** The actions that announced themselves and have not said how they
   *  went (issue #625). Drawn at the foot of the scrollback rather than
   *  among the lines, because it is a statement about NOW rather than
   *  something that happened at a moment: it belongs after the last
   *  thing that did happen, and it goes away by itself when the
   *  completion it is waiting for arrives. */
  unfinished: UnfinishedAction[];
}) {
  const scroller = useRef<HTMLDivElement | null>(null);
  const following = useRef(true);
  const last = entries.length > 0 ? entries.length : 0;

  useEffect(() => {
    const node = scroller.current;
    if (node && following.current) node.scrollTop = node.scrollHeight;
  }, [last]);

  const onScroll = useCallback(() => {
    const node = scroller.current;
    if (!node) return;
    following.current = node.scrollHeight - node.scrollTop - node.clientHeight < 24;
  }, []);

  const startResize = useCallback(
    (event: React.PointerEvent) => {
      const startY = event.clientY;
      const startHeight = height;
      const move = (e: PointerEvent) => {
        const next = Math.min(MAX_HEIGHT, Math.max(MIN_HEIGHT, startHeight + (startY - e.clientY)));
        onResize(next);
      };
      const stop = () => {
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", stop);
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", stop);
    },
    [height, onResize]
  );

  return (
    <>
      <InfoTooltip id="activity.dock.resize" block style={{ flex: "none" }}>
        <div
          onPointerDown={startResize}
          role="separator"
          aria-label="Resize terminal"
          aria-orientation="horizontal"
          style={{ height: 4, cursor: "row-resize", background: "var(--border)", flex: "none" }}
        />
      </InfoTooltip>
      <div
        className="activity-log"
        role="log"
        aria-live="polite"
        ref={scroller}
        onScroll={onScroll}
        style={{ height, maxHeight: height, overflow: "auto" }}
      >
        {/* The environment every command below needs, said once here
            rather than on every line, so each command stays clean and
            runnable under it. Muted, because it is a header and not an
            event: nothing happened at it. */}
        <InfoTooltip id="activity.dock.preamble" block>
          <div className="activity-log__time">
            {preamble}
          </div>
        </InfoTooltip>
        {/* One line at the top of the scrollback rather than a toast that
            disappears: what is gone is gone, and the durable record has a
            name. */}
        {dropped ? (
          <InfoTooltip id="activity.dock.dropped" block>
            <div style={{ color: "var(--term-time)" }}>
              {"──── earlier lines are not held here any more · the full record is on the Activity page ────"}
            </div>
          </InfoTooltip>
        ) : null}
        {entries.map((entry, i) => {
          if (entry.kind === "restart") {
            return (
              <InfoTooltip key={"restart-" + i} id="activity.dock.restart" block>
                <div style={{ color: "var(--term-warn)" }}>
                  {restartRule(entry.at)}
                </div>
              </InfoTooltip>
            );
          }
          const event = entryEvent(entry, i);
          return <DockLine key={event.sequence + "-" + i} event={event} viewer={viewer} />;
        })}
        {/* Inside the scrollback and at the FOOT of it, which is where
            the log already follows to, so a reader watching the tail is
            looking straight at it. Below the scroller it would sit
            outside the height the shell reserves for this panel (see
            dockReservedHeight) and be clipped by the section's own
            maxHeight, which for a notice about something that has gone
            quiet is the worst possible place to put it. */}
        <UnfinishedActionsNotice actions={unfinished} onTerminal />
      </div>
    </>
  );
}

function DockLine({ event, viewer }: { event: SetActivityEvent; viewer: string | null }) {
  const prefix = dockPrefix(event, viewer);
  const line = activityLine(event);
  const tone: LineTone = line.tone;
  return (
    <div>
      <span className="activity-log__time">{line.time}</span>{" "}
      <span
        className="activity-log__time"
        style={{ color: prefix.action ? "var(--term-accent)" : "var(--term-time)" }}
      >
        {"[" + prefix.label + "]"}
      </span>{" "}
      <span className={"activity-log__line--" + tone} style={{ whiteSpace: "pre-wrap" }}>
        {line.text}
      </span>
    </div>
  );
}
