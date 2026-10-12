/**
 * The sentences for the activity-feed events a scrub, a recovery and a
 * recovery test produce (issue #985).
 *
 * One home for them, because three surfaces say the same thing: the activity
 * strip and the docked terminal (through `activityLine`), and the operation
 * panel's own line list (through realAssurance). A sentence written twice is
 * a sentence that drifts, and an operator reading a scrub failure in the
 * terminal and again on the set page should read the same words.
 *
 * Every function here is total over the fields it reads: the engine's
 * fields arrive as strings, any of them may be absent on an older engine,
 * and none of them is trusted to be a number. A count that is not one is
 * left out of the sentence rather than printed as NaN.
 *
 * The result (success, warn, error) is stated by the engine on the line and
 * is what the TONE comes from (activityLine's toneOf); the words here follow
 * the same fact, and where a line states no result the words say only
 * "finished", never "passed".
 */
import type { SetActivityEvent } from "@shared/types/activity";
import { bytes } from "@shared/utilities/format";
import { stageLabel } from "./stages";

/** The last segment of an artifact id ("source/set/name"): the strip is
 *  already inside one set. */
export function artifactTail(id: string | undefined): string {
  if (!id) return "";
  const parts = id.split("/");
  return parts[parts.length - 1] || id;
}

/** A count field as a whole number, or null when it is absent or not one. */
function count(v: string | undefined): number | null {
  if (v === undefined || v === "") return null;
  const n = Number(v);
  return Number.isFinite(n) && n >= 0 ? Math.floor(n) : null;
}

const n = (v: string | undefined): number => count(v) ?? 0;

/** The stage's label: the engine's own wins over this build's, and a stage
 *  neither names is shown as received. */
export function stageWords(f: Record<string, string>): string {
  return f.stage_label || stageLabel(f.stage ?? "") || f.stage || "(unnamed stage)";
}

/** `scrub_pass`: one line per pass, with its counts in words. A pass that
 *  found a copy wrong or could not check one says "found problems"; one that
 *  only skipped copies says so; only the rest is "finished". */
export function scrubPassText(e: SetActivityEvent): string {
  const f = e.fields;
  const failed = n(f.failed);
  const incomplete = n(f.could_not_complete);
  const skipped = n(f.skipped_held) + n(f.skipped_leased);
  const head =
    failed > 0 || incomplete > 0 ? "scrub pass found problems"
    : skipped > 0 ? "scrub pass finished with skipped copies"
    : "scrub pass finished";
  const how = f.trigger === "manual" ? "manual" : f.trigger === "schedule" ? "scheduled" : f.trigger;
  const where = [how, f.scope].filter(Boolean).join(", ");
  const parts: string[] = [];
  const checked = count(f.checked);
  if (checked !== null) parts.push(checked + " checked");
  const passed = count(f.passed);
  if (passed !== null) parts.push(passed + " passed");
  if (failed > 0) parts.push(failed + " failed");
  if (incomplete > 0) parts.push(incomplete + " could not complete");
  if (n(f.requires_restore) > 0) parts.push(n(f.requires_restore) + " need a provider restore");
  if (skipped > 0) parts.push(skipped + " skipped (held or already being checked)");
  if (n(f.bytes_read) > 0) parts.push(bytes(n(f.bytes_read)) + " read");
  return head + (where ? " (" + where + ")" : "") + (parts.length ? ": " + parts.join(", ") : "");
}

/** `scrub_failure`: the one per-copy line a pass writes. */
export function scrubFailureText(e: SetActivityEvent): string {
  const f = e.fields;
  return (
    "scrub did not pass: " + (artifactTail(f.artifact) || "a copy") +
    (f.medium ? " on " + f.medium : "") +
    (f.class ? " (" + f.class + " check)" : "") +
    (f.reason ? ": " + f.reason : "")
  );
}

export function recoveryStartText(e: SetActivityEvent): string {
  return "recovery started: " + (artifactTail(e.fields.artifact) || "an artifact");
}

/** `recovery_stage`, for a recovery and for a recovery test (the test's stage
 *  lines carry the recovery_test action). */
export function recoveryStageText(e: SetActivityEvent): string {
  return (e.action === "recovery_test" ? "recovery test stage: " : "recovery stage: ") + stageWords(e.fields);
}

export function recoveryEndText(e: SetActivityEvent): string {
  const f = e.fields;
  const name = artifactTail(f.artifact) || "an artifact";
  if (e.result === "error") return "recovery failed: " + name + (f.reason ? ": " + f.reason : "");
  if (e.result === "warn") return "recovery stopped: " + name + (f.reason ? ": " + f.reason : "");
  const size = n(f.bytes) > 0 ? ", " + bytes(n(f.bytes)) : "";
  const took = f.duration ? ", took " + f.duration : "";
  return "recovery finished: " + name + size + took;
}

export function recoveryTestStartText(e: SetActivityEvent): string {
  const f = e.fields;
  const validator = f.validator && f.validator !== "(withheld)" ? " (validator " + f.validator + ")" : "";
  return "recovery test started: " + (artifactTail(f.artifact) || "an artifact") + validator;
}

export function recoveryTestEndText(e: SetActivityEvent): string {
  const f = e.fields;
  const name = artifactTail(f.artifact) || "an artifact";
  const why = f.reason ? ": " + f.reason : "";
  if (e.result === "success") return "recovery test passed: " + name;
  if (e.result === "error") return "recovery test failed: " + name + why;
  // A warning is a run that proved nothing either way: cancelled, refused for
  // cost, or not completed. It is never worded as a pass.
  if (e.result === "warn") return "recovery test did not complete: " + name + why;
  return "recovery test finished: " + name;
}
