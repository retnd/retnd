/**
 * The rows of a decrypt-verify, as DATA (EPIC N, issue #995; design section 8).
 *
 * Whether the engine publishes named stages for this operation (Encrypting,
 * Decrypt-verifying with a byte counter) is an owner decision that is not
 * settled: a stage is a published contract, and #993 left it open on purpose.
 * Until it is, the only facts this UI can state are the ones the durable
 * operation record carries today: the run was accepted (the key resolved,
 * because an unresolvable key is refused before any read), it is running or
 * has ended, and how it ended. So the rows are a table and not markup. When
 * stages are published, adding one is one entry in STAGE_ROWS and a line in
 * rowState; no component changes.
 */
import type { AssuranceOperationRecord } from "@shared/api/contracts";

export interface VerifyRow {
  id: string;
  label: string;
  state: "done" | "active" | "pending" | "failed";
}

/** Stages this build knows by name, in order. Empty of any stage the engine
 *  has not published: a stage this build does not name is never invented. */
const STAGE_ROWS: readonly { id: string; label: string }[] = [];

export function verifyRows(record: AssuranceOperationRecord | null): VerifyRow[] {
  const ended = record !== null && record.state !== "running";
  const failed = record !== null && (record.state === "failed" || record.state === "did_not_complete");
  const rows: VerifyRow[] = [{ id: "accepted", label: "Key resolved", state: "done" }];
  for (const s of STAGE_ROWS) {
    rows.push({ id: s.id, label: s.label, state: record && record.stage === s.id && !ended ? "active" : "pending" });
  }
  rows.push({
    id: "decrypt",
    label: "Decrypt-verifying",
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
