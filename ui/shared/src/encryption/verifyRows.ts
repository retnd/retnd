/**
 * The rows of a decrypt-verify, as DATA (EPIC N, issue #995; design section 8).
 *
 * The engine now publishes "encrypting" and "decrypt-verifying" as progress
 * stages with a byte counter (#1290), on the run cycle's progress and the
 * activity feed. A standalone decrypt-verify rides the scrub operation and its
 * durable record names no stage, so STAGE_ROWS stays empty for it: a stage this
 * build does not see on the record is never invented. What the decrypt row can
 * do is carry a byte reading when the caller has one for this operation, or
 * the scrub record's own bytesRead (#1303), the figure `assurance show` prints.
 * Adding a stage row is still one entry in STAGE_ROWS and a line in rowState;
 * no component changes.
 */
import type { AssuranceOperationRecord } from "@shared/api/contracts";
import { bytes } from "@shared/utilities/format";

export interface VerifyRow {
  id: string;
  label: string;
  state: "done" | "active" | "pending" | "failed";
}

/** Stages this build knows by name, in order. Empty of any stage the engine
 *  has not published: a stage this build does not name is never invented. */
const STAGE_ROWS: readonly { id: string; label: string }[] = [];

/** A byte reading of the running stage, as the progress feed carries it. */
export interface VerifyReading {
  stage: string;
  bytesDone: number;
  bytesTotal?: number;
}

export function verifyRows(record: AssuranceOperationRecord | null, reading: VerifyReading | null = null): VerifyRow[] {
  const ended = record !== null && record.state !== "running";
  const failed = record !== null && (record.state === "failed" || record.state === "did_not_complete");
  const rows: VerifyRow[] = [{ id: "accepted", label: "Key resolved", state: "done" }];
  for (const s of STAGE_ROWS) {
    rows.push({ id: s.id, label: s.label, state: record && record.stage === s.id && !ended ? "active" : "pending" });
  }
  // A standalone verify's record carries bytesRead (#1303), the figure the CLI
  // prints; a reading from the progress feed, when there is one, wins.
  const figure =
    reading && reading.stage === "decrypt-verifying"
      ? " " + bytes(reading.bytesDone) + (reading.bytesTotal !== undefined ? " / " + bytes(reading.bytesTotal) : "")
      : record?.bytesRead !== undefined && record.bytesRead > 0
        ? " " + bytes(record.bytesRead)
        : "";
  rows.push({
    id: "decrypt",
    label: "Decrypt-verifying" + figure,
    state: record === null || record.state === "running" ? "active" : failed ? "failed" : record.state === "passed" ? "done" : "pending"
  });
  rows.push({ id: "compare", label: "Compare digest and record result", state: record?.state === "passed" ? "done" : failed ? "failed" : "pending" });
  return rows;
}

/** The ending in words. Only a pass says match; a failure names the engine's
 *  own reason and says nothing was written. */
export type VerifyEnding =
  | { kind: "running" }
  | { kind: "match" }
  | { kind: "failed"; reason: string }
  | { kind: "cancelled" }
  | { kind: "unfinished" };

export function verifyEnding(record: AssuranceOperationRecord | null): VerifyEnding {
  if (record === null || record.state === "running") return { kind: "running" };
  if (record.state === "passed") return { kind: "match" };
  if (record.state === "cancelled") return { kind: "cancelled" };
  if (record.state === "unfinished") return { kind: "unfinished" };
  return { kind: "failed", reason: record.reason || "the decrypt did not complete" };
}
