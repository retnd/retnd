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
import type { AchievedClass, ClaimRecord, ClaimStatus } from "./types";

export type RenderState = ClaimStatus | "existence_only" | "attested_only";
export type ClaimKind = "content" | "recovery";

export interface RenderedClaim {
  state: RenderState;
  label: string;
  /** "3d ago", for the states that carry a time. */
  age?: string;
  glyph: Glyph;
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
    failed: "Content check failed",
    unknown: "Unknown",
    never_configured: "Not configured",
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
    failed: "Recovery test failed",
    unknown: "Unknown",
    never_configured: "Not configured",
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

/** What one claim may render, from the record or from nothing at all. */
export function renderClaim(kind: ClaimKind, record: ClaimRecord | undefined, now: number = Date.now()): RenderedClaim {
  const make = (state: RenderState, withAge: boolean): RenderedClaim => ({
    state,
    label: LABELS[kind][state],
    age: withAge ? ageOf(record?.at, now) : undefined,
    glyph: GLYPH[state]
  });
  if (!record) return make("unknown", false);
  let status = knownStatus(record.status);
  // Cleanup is a fact about a recovery test's staged copy. A content record
  // that claims it is a record this build cannot read.
  if (kind === "content" && status === "cleanup_failed") status = "unknown";
  if (status === "pass") {
    if (!validTime(record.at)) return make("unknown", false);
    if (kind === "content") {
      const cls: AchievedClass = record.achievedClass;
      if (cls === "content") return make("pass", true);
      if (cls === "existence") return make("existence_only", true);
      if (cls === "attested") return make("attested_only", true);
      return make("unknown", false);
    }
    return make("pass", true);
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

/** Counts for the dashboard, in the same vocabulary. Unknown is due.
 *
 *  Due looks at BOTH claims: a recovery test that is overdue or never ran is
 *  as due as a scrub that is, and counting only the content claim would show a
 *  set as fine on the dashboard that the Backups page shows as overdue.
 *  `never_configured` and unknown recovery claims are not counted as due,
 *  because a test nobody asked for or a server that cannot say is not a
 *  deadline. Degraded looks at both as well. */
export function tally(items: Array<{ content: ClaimRecord; recovery: ClaimRecord }>, now: number = Date.now()) {
  let due = 0, overdue = 0, degraded = 0, unknown = 0;
  const bad = (s: RenderState) => s === "failed" || s === "could_not_complete" || s === "no_readable_copy" || s === "cleanup_failed";
  for (const it of items) {
    const c = renderClaim("content", it.content, now).state;
    const r = renderClaim("recovery", it.recovery, now).state;
    if (c === "unknown") unknown++;
    const contentDue = c === "unknown" || c === "never_run" || c === "overdue";
    const recoveryDue = r === "never_run" || r === "overdue";
    if (contentDue || recoveryDue) due++;
    if (c === "overdue" || r === "overdue") overdue++;
    if (bad(c) || bad(r)) degraded++;
  }
  return { due, overdue, degraded, unknown };
}
