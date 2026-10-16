/**
 * Finished assurance operations that are no longer on the live feed (issue
 * #1200).
 *
 * The live feed is a bounded tail, so an operation that ended a while ago is
 * not on it, and an Operation page opened from a link or a reload has to find
 * the run somewhere else. These are the runs the durable activity record
 * (migration 0018) still holds for the "operation-history" scenario, written
 * as the engine writes them: a start line, the ONE stage line the journal
 * keeps per operation (the last stage the run reached; the writer coalesces
 * the rest), and an end line, each carrying the run's `op_id`. A scrub has no
 * start or stage line, only its `scrub_pass`.
 *
 * One more id is deliberately absent: a run older than the record's 30 days,
 * whose lines were pruned. The page has to say that in words.
 */
import type { SetActivityEvent } from "@shared/types/activity";
import type { ValidatorResult } from "./types";
import { stageLabel } from "./stages";

export const OPERATION_HISTORY_SCENARIO = "operation-history";
/** A recovery test whose lines leave the live tail as they are written, so
 *  the page that started it never sees them there. Only the unfinished-action
 *  list carries it while it runs. */
export const OFF_TAIL_SCENARIO = "operation-off-tail";

export const HISTORY_SET = "production/postgres-primary";
export const HISTORY_IDS = {
  passed: "aop_1200_passed",
  failed: "aop_1200_failed",
  cancelled: "aop_1200_cancelled",
  scrub: "aop_1200_scrub",
  pruned: "aop_1200_pruned"
} as const;

const HOUR = 3_600_000;
const NEWEST = "production/postgres-primary/postgres-prod-20260828.dump.zst";
const OLDER = "production/postgres-primary/postgres-prod-20260827.dump.zst";
export const FAILED_REASON = "sha256 differs from the one recorded at ingest";
export const CANCELLED_REASON = "cancelled before it finished; no result was recorded";

/** One stored line, as the record keeps it: the set it was written for and the event. */
export interface StoredLine { setId: string; event: SetActivityEvent }

function recoveryTest(
  opId: string, setId: string, artifact: string, startedAt: number, seq: number,
  stage: string, end: { result: "success" | "error" | "warn"; level: "info" | "error" | "warn"; reason?: string; secs: number }
): StoredLine[] {
  const at = (s: number) => new Date(startedAt + s * 1000).toISOString();
  const base = { scope: "set" as const, action: "recovery_test", actionId: opId };
  return [
    { setId, event: { ...base, sequence: seq, at: at(0), level: "info", event: "recovery_test_start", message: "recovery test starting",
      fields: { op_id: opId, backup_set: setId, artifact, validator: "pg-restore-check" } } },
    { setId, event: { ...base, sequence: seq + 1, at: at(end.secs - 1), level: "info", event: "recovery_stage", message: "recovery stage",
      fields: { op_id: opId, backup_set: setId, stage, stage_label: stageLabel(stage) } } },
    { setId, event: { ...base, sequence: seq + 2, at: at(end.secs), level: end.level, event: "recovery_test_end", result: end.result,
      message: "recovery test finished",
      fields: { op_id: opId, backup_set: setId, artifact, validator: "pg-restore-check", duration: end.secs + "s", ...(end.reason ? { reason: end.reason } : {}) } } }
  ];
}

/** The durable lines of each fixture run, oldest first, relative to `now`. */
export function historyLines(now: number): Record<string, StoredLine[]> {
  return {
    [HISTORY_IDS.passed]: recoveryTest(HISTORY_IDS.passed, HISTORY_SET, NEWEST, now - 3 * HOUR, 90_310, "complete",
      { result: "success", level: "info", secs: 25 }),
    [HISTORY_IDS.failed]: recoveryTest(HISTORY_IDS.failed, HISTORY_SET, OLDER, now - 30 * HOUR, 90_110, "verifying",
      { result: "error", level: "error", reason: FAILED_REASON, secs: 19 }),
    [HISTORY_IDS.cancelled]: recoveryTest(HISTORY_IDS.cancelled, HISTORY_SET, NEWEST, now - 6 * HOUR, 90_210, "reading",
      { result: "warn", level: "warn", reason: CANCELLED_REASON, secs: 4 }),
    [HISTORY_IDS.scrub]: [{
      setId: HISTORY_SET,
      event: {
        sequence: 90_400, at: new Date(now - 2 * HOUR).toISOString(), scope: "set", level: "info", event: "scrub_pass", result: "success",
        message: "scrub pass complete",
        fields: {
          trigger: "manual", scope: "set " + HISTORY_SET, op_id: HISTORY_IDS.scrub, selected: "2", checked: "2", passed: "2", failed: "0",
          could_not_complete: "0", requires_restore: "0", skipped_held: "0", skipped_leased: "0", bytes_read: "9600000000"
        }
      }
    }]
  };
}

/** The set's newest recovery test output, which belongs to the passed run
 *  and to no other: the failed run is older, so the output is not its. */
export function historyValidatorResult(now: number): ValidatorResult {
  return {
    artifactId: NEWEST, medium: "local", operationId: HISTORY_IDS.passed, state: "passed",
    recordedAt: new Date(now - 3 * HOUR + 25_000).toISOString(), truncated: false,
    records: [
      { kind: "output", text: "pg-restore-check: restored into a scratch cluster" },
      { kind: "output", text: "pg-restore-check: 220 tables, 0 errors" },
      { kind: "output", text: "pg-restore-check: ok" }
    ]
  };
}
