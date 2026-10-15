/**
 * Turns what the server said into what a chip may say (issue #985).
 *
 * # Pass is derived, never carried
 *
 * `status: "pass"` on the wire is a CLAIM, and this is where it is checked
 * rather than believed. A content pass needs an achieved class of `content`
 * and a timestamp; a recovery pass needs a timestamp. A pass that lacks what
 * makes it one renders as `unknown`, and an `existence` or `attested` result
 * renders as itself and never as content verification (the planted mutation
 * in the epic's matrix relabels exactly that).
 *
 * # There is no path from unknown to pass
 *
 * `renderClaim` is a pure function of one record. It has no memory, takes no
 * "previous state" and has no branch that returns `pass` without reading the
 * record's own class and timestamp, so rendering alone cannot promote an
 * unknown claim.
 */
import type { IconName } from "@shared/design-system/icons";
import { stamp } from "@shared/utilities/format";
import { backupSetPath } from "@shared/utilities/routes";
import { SETTINGS_SECTIONS, settingsLink } from "@shared/utilities/routes";
import type { AchievedClass, ClaimRecord, ClaimStatus, CopyContent, CopyFailure } from "./types";

export type RenderState = ClaimStatus | "existence_only" | "attested_only" | "pass_builtin";
export type ClaimKind = "content" | "recovery";

export interface RenderedClaim {
  state: RenderState;
  label: string;
  /** "partial" when a pass covers fewer copies than the backup has, or when
   *  the server did not say which. Absent when every copy backs the claim. */
  coverage?: "partial";
  /** "3d ago", for the states that carry a time. */
  age?: string;
  glyph: Glyph;
  /** In-app path the label links to (a `never_configured` claim on a set that has a configuration). */
  link?: string;
  /** What else is true when the claim has no result: that nothing has run yet. */
  note?: string;
}

// ------------------------------------------- a claim nobody has configured

export const DEFAULTS_ANCHOR = SETTINGS_SECTIONS.assurance;
export const DEFAULTS_LINK = settingsLink("assurance");

/** What is known about the configuration behind a claim that has no result.
 *  `default` and `own` are a set with a configuration (the deployment's, or
 *  its own); `none` is a set whose policy really resolves to nothing; `loading`
 *  is a claim drawn before the set's policy has arrived; `unavailable` is a
 *  policy that was asked for and could not be read. Only `none` may say "Not
 *  configured": the other four say what is known and no more. */
export type PolicyBasis = "default" | "own" | "none" | "loading" | "unavailable";

/** The ONE wording rule for a `never_configured` claim (issue #1141). The
 *  verification model reads it for the chips' Recovery view and `renderClaim`
 *  reads it for the label, so a chip drawn from either says the same thing.
 *  `setId` names the set whose own policy an `own` basis links to, and
 *  `noRunYet` is true only when it is KNOWN that nothing has run, never when
 *  the answer is merely absent. */
export function neverConfiguredWording(
  kind: ClaimKind, basis: PolicyBasis, opts: { setId?: string; noRunYet?: boolean } = {}
): { label: string; link?: string; note?: string } {
  const note = opts.noRunYet ? (kind === "recovery" ? "No recovery test has run yet" : "No content check has run yet") : undefined;
  switch (basis) {
    case "default": return { label: "Default configuration", link: DEFAULTS_LINK, ...(note ? { note } : {}) };
    case "own": {
      const to = opts.setId ? backupSetPath(...(opts.setId.split("/") as [string, string])) + "#assurance" : undefined;
      return { label: "Custom configuration", ...(to ? { link: to } : {}), ...(note ? { note } : {}) };
    }
    case "loading": return { label: "Loading configuration" };
    case "unavailable": return { label: "Configuration unavailable" };
    default: return { label: "Not configured" };
  }
}

const STATUSES: readonly ClaimStatus[] = [
  "unknown", "never_configured", "never_run", "pass", "overdue", "refused_for_cost",
  "could_not_complete", "failed", "requires_restore", "no_readable_copy", "cleanup_failed"
];

/** What marks a state. The retired geometric characters (the tick, the
 *  cross, the circle, the triangle, the slashed circle) are the product's
 *  icon artwork now, so those states name an icon and the rest are plain
 *  text marks. `id` is published on the element so a test can tell the
 *  states apart without reading pixels. */
export interface Glyph { id: string; icon?: IconName; text?: string }
export const GLYPH: Record<RenderState, Glyph> = {
  pass: { id: "pass", icon: "success" },
  pass_builtin: { id: "pass-builtin", icon: "success" },
  failed: { id: "failed", icon: "failure" },
  unknown: { id: "unknown", text: "?" },
  never_configured: { id: "never-configured", text: "\u2212" },
  never_run: { id: "never-run", icon: "status-idle" },
  overdue: { id: "overdue", text: "\u25F7" },
  refused_for_cost: { id: "refused-for-cost", text: "$" },
  could_not_complete: { id: "could-not-complete", icon: "warning" },
  requires_restore: { id: "requires-restore", icon: "backups" },
  no_readable_copy: { id: "no-readable-copy", icon: "quarantine" },
  cleanup_failed: { id: "cleanup-failed", icon: "warning" },
  existence_only: { id: "existence-only", text: "\u2248" },
  attested_only: { id: "attested-only", text: "\u2248" }
};

/** "3d ago" from an RFC 3339 time, or undefined when it is not a time. */
export function ageOf(at: string | undefined, now: number = Date.now()): string | undefined {
  if (!at) return undefined;
  const t = Date.parse(at);
  if (Number.isNaN(t)) return undefined;
  const s = Math.max(0, Math.floor((now - t) / 1000));
  if (s < 90) return "just now";
  const m = Math.floor(s / 60);
  if (m < 90) return m + "m ago";
  const h = Math.floor(m / 60);
  if (h < 48) return h + "h ago";
  return Math.floor(h / 24) + "d ago";
}

function knownStatus(s: string): ClaimStatus {
  return (STATUSES as readonly string[]).includes(s) ? (s as ClaimStatus) : "unknown";
}

function validTime(at: string | undefined): boolean {
  return at !== undefined && !Number.isNaN(Date.parse(at));
}

const LABELS: Record<ClaimKind, Record<RenderState, string>> = {
  content: {
    pass: "Content verified",
    pass_builtin: "Unknown",
    failed: "Content check failed",
    unknown: "Unknown",
    never_configured: neverConfiguredWording("content", "loading").label,
    never_run: "Never run",
    overdue: "Overdue",
    refused_for_cost: "Skipped: read would cost egress",
    could_not_complete: "Could not complete",
    requires_restore: "Archived: thaw needed",
    no_readable_copy: "No readable copy",
    cleanup_failed: "Unknown",
    existence_only: "Existence checked only",
    attested_only: "Attested only"
  },
  recovery: {
    pass: "Recovery test passed",
    pass_builtin: "Recovered, readable",
    failed: "Recovery test failed",
    unknown: "Unknown",
    never_configured: neverConfiguredWording("recovery", "loading").label,
    never_run: "Never run",
    overdue: "Overdue",
    refused_for_cost: "Skipped: read would cost egress",
    could_not_complete: "Could not complete",
    requires_restore: "Archived: thaw needed",
    no_readable_copy: "No readable copy",
    cleanup_failed: "Cleanup failed: staged copy left behind",
    existence_only: "Unknown",
    attested_only: "Unknown"
  }
};

/** The weaker copies of a content pass, in words: "2 never checked". The
 *  order is the order of how little each says. */
const WEAKER: Array<[CopyContent, string]> = [
  ["failed", "failed"], ["could_not_complete", "could not complete"], ["unchecked", "never checked"],
  ["existence", "existence only"], ["attested", "attested only"]
];

/** "Content verified" qualified by the copies behind it, or bare when every
 *  copy backs it. A bare label is a statement about all of them, so it needs
 *  every copy to be content-verified; one copy that is the whole backup is
 *  the only case that can say it without itemising. `copies` is how many
 *  copies the backup has. */
function contentPassLabel(copyContent: readonly CopyContent[] | undefined, copies: number | undefined): { label: string; partial: boolean } {
  const bare = LABELS.content.pass;
  if (!copyContent) {
    // Not itemised: with more than one copy, the other copies are unknown.
    if (copies === undefined || copies <= 1) return { label: bare, partial: false };
    return { label: bare + " \u00B7 " + copies + " copies, which of them is not reported", partial: true };
  }
  const total = copyContent.length;
  const good = copyContent.filter((c) => c === "content").length;
  if (good === total) return { label: bare, partial: false };
  const parts = WEAKER.map(([c, words]) => [copyContent.filter((x) => x === c).length, words] as const).filter(([k]) => k > 0).map(([k, w]) => k + " " + w);
  return { label: bare + " \u00B7 " + good + " of " + total + (total === 1 ? " copy" : " copies") + ", " + parts.join(", "), partial: true };
}

/** What one claim may render, from the record or from nothing at all.
 *
 *  `copyContent` and `copies` qualify a Content pass (D3): the claim keeps the
 *  strongest class across copies, so without them a backup with one verified
 *  copy and two nobody has read would be drawn exactly like one with three. */
export function renderClaim(
  kind: ClaimKind, record: ClaimRecord | undefined, now: number = Date.now(),
  copyContent?: readonly CopyContent[], copies?: number,
  /** What is known about the configuration behind a `never_configured` claim.
   *  Left out, nothing is known yet, which is drawn as loading, never as "Not
   *  configured" (issue #1141). */
  policy: { basis: PolicyBasis; setId?: string; noRunYet?: boolean } = { basis: "loading" }
): RenderedClaim {
  const make = (state: RenderState, withAge: boolean): RenderedClaim => {
    const w = state === "never_configured" ? neverConfiguredWording(kind, policy.basis, policy) : undefined;
    return {
      state,
      label: w?.label ?? LABELS[kind][state],
      age: withAge ? ageOf(record?.at, now) : undefined,
      glyph: GLYPH[state],
      ...(w?.link ? { link: w.link } : {}),
      ...(w?.note ? { note: w.note } : {})
    };
  };
  if (!record) return make("unknown", false);
  let status = knownStatus(record.status);
  // Cleanup is a fact about a recovery test's staged copy. A content record
  // that claims it is a record this build cannot read.
  if (kind === "content" && status === "cleanup_failed") status = "unknown";
  if (status === "pass") {
    if (!validTime(record.at)) return make("unknown", false);
    if (kind === "content") {
      const cls: AchievedClass = record.achievedClass;
      if (cls === "content") {
        const q = contentPassLabel(copyContent, copies ?? copyContent?.length);
        return { ...make("pass", true), label: q.label, ...(q.partial ? { coverage: "partial" as const } : {}) };
      }
      if (cls === "existence") return make("existence_only", true);
      if (cls === "attested") return make("attested_only", true);
      return make("unknown", false);
    }
    // A built-in structure pass proves the file decodes, not that an
    // application can use it, so it has its own words (#1111) and is never
    // worded as a recovery test passing.
    return make(record.check === "builtin" ? "pass_builtin" : "pass", true);
  }
  // Every other state keeps its own age where it has one: "overdue" says
  // how long ago it last passed, "failed" says when it failed.
  const withAge = status === "overdue" || status === "failed" || status === "could_not_complete" || status === "cleanup_failed";
  return make(status, withAge);
}

/** The Stored claim: a fact about copies, not a verdict. */
export function renderStored(copies: number | undefined): { state: "stored" | "no_copy" | "unknown"; label: string } {
  if (copies === undefined) return { state: "unknown", label: "Unknown" };
  if (copies <= 0) return { state: "no_copy", label: "No copy yet" };
  return { state: "stored", label: "Stored · " + copies + (copies === 1 ? " copy" : " copies") };
}

/** The states a Recovery claim is in when nothing has been tested: a test nobody
 *  asked for, one that was asked for and never ran, and one the server cannot
 *  say. None of them is a pass, and none of them is a failure. */
const UNTESTED: readonly RenderState[] = ["never_configured", "never_run", "unknown"];

export type Bucket = "due" | "overdue" | "degraded" | "untested";
export const BUCKETS: readonly Bucket[] = ["due", "overdue", "degraded", "untested"];

/** Which of the dashboard's counts a backup belongs to. This is the ONE
 *  definition (issue #1134): the tally counts through it and the Backups list
 *  filter that each count links to filters through it, so a count and the list
 *  behind it cannot differ. A backup can be in several (a recovery test that
 *  never ran is both due and untested). */
export function bucketsOf(item: { content: ClaimRecord; recovery: ClaimRecord }, now: number = Date.now()): Bucket[] {
  const bad = (st: RenderState) => st === "failed" || st === "could_not_complete" || st === "no_readable_copy" || st === "cleanup_failed";
  const c = renderClaim("content", item.content, now).state;
  const r = renderClaim("recovery", item.recovery, now).state;
  const out: Bucket[] = [];
  const contentDue = c === "unknown" || c === "never_run" || c === "overdue";
  const recoveryDue = r === "never_run" || r === "overdue";
  if (contentDue || recoveryDue) out.push("due");
  if (c === "overdue" || r === "overdue") out.push("overdue");
  if (bad(c) || bad(r)) out.push("degraded");
  if (UNTESTED.includes(r)) out.push("untested");
  return out;
}

/** Counts for the dashboard, in the same vocabulary. Unknown is due.
 *
 *  Due looks at BOTH claims: a recovery test that is overdue or never ran is
 *  as due as a scrub that is, and counting only the content claim would show a
 *  set as fine on the dashboard that the Backups page shows as overdue.
 *  `never_configured` and unknown recovery claims are not due, because a test
 *  nobody asked for or a server that cannot say is not a deadline. They are
 *  `untested`, which is its own count so that a zero in Due is never the only
 *  thing said about a deployment nobody has tested. Degraded looks at both as
 *  well. */
export function tally(items: Array<{ content: ClaimRecord; recovery: ClaimRecord }>, now: number = Date.now()) {
  let due = 0, overdue = 0, degraded = 0, unknown = 0, untested = 0;
  for (const it of items) {
    if (renderClaim("content", it.content, now).state === "unknown") unknown++;
    const b = bucketsOf(it, now);
    if (b.includes("due")) due++;
    if (b.includes("overdue")) overdue++;
    if (b.includes("degraded")) degraded++;
    if (b.includes("untested")) untested++;
  }
  return { due, overdue, degraded, unknown, untested };
}

/** One failed copy as a sentence, in the words the CLI's `retnd artifacts`
 *  would: which copy, what ran, how often, when and what was recorded. The
 *  reason is the journal's own bounded text and is shown as text, never as
 *  markup. */
export function failureSentence(f: CopyFailure): string {
  const where = f.medium === "local" ? "the local copy" : "the copy on " + f.medium;
  const what = f.kind === "scrub" ? "scrub" : "recovery test";
  const head = f.state === "failed"
    ? (f.kind === "scrub" ? "The last scrub of " + where + " failed" : "The last recovery test of " + where + " failed")
    : "The last " + what + " of " + where + " could not complete";
  const streak = f.consecutiveFailures > 1 ? ", " + f.consecutiveFailures + " times in a row" : "";
  const when = f.at ? " (" + stamp(f.at) + ")" : "";
  return head + streak + when + (f.reason ? ": " + f.reason : ".");
}
