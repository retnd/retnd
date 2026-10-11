/**
 * What the recovery and assurance surfaces know about a backup (issue #985,
 * EPIC Q).
 *
 * # Three claims, three homes
 *
 * `Stored`, `Content verified` and `Recovery tested` are separately durable,
 * separately aged and separately rendered, so each has its own field here and
 * nothing in this file rolls them up. There is no "overall" status and no
 * helper that would compute one: a roll-up is exactly where a checksum pass
 * and a validator failure get averaged into a tick.
 *
 * # Status is a closed enumeration and the zero value is `unknown`
 *
 * Five different reasons produce a not-a-pass (never configured, never run,
 * overdue, refused for cost, could not complete) and a sixth is a verdict
 * that came back wrong (failed). They are six values, not one, and
 * `unknown` is what anything this build does not recognise becomes (see
 * claims.ts), so an un-scrubbed, rebuilt or newer-than-this-build record can
 * never default to good.
 *
 * # Where these shapes come from
 *
 * The routes behind {@link AssuranceApi} belong to the recovery, scrub and
 * recovery-test work of the same epic and had not merged when this was
 * written. These are therefore the shapes the issues describe, held behind
 * one interface so that landing the real client replaces the implementation
 * and not the screens. Until then the production client does not offer it,
 * and every surface renders `unknown` rather than a tick (api/contracts.ts
 * declares `assurance` optional for exactly that reason).
 */

export type ClaimStatus =
  | "unknown"
  | "never_configured"
  | "never_run"
  | "pass"
  | "overdue"
  | "refused_for_cost"
  | "could_not_complete"
  | "failed"
  | "requires_restore"
  | "no_readable_copy";

/** The strongest verification class a copy has actually ACHIEVED, never the
 *  one configured (placements.verification_class). Only `content` is content
 *  verification. */
export type AchievedClass = "" | "content" | "attested" | "existence";

export interface ClaimRecord {
  status: ClaimStatus;
  achievedClass: AchievedClass;
  /** RFC 3339. A pass without a timestamp is not a pass (claims.ts). */
  at?: string;
}

export interface ArtifactAssurance {
  artifactId: string;
  /** How many copies are recorded. Zero is "no copy yet", not an error. */
  copies: number;
  content: ClaimRecord;
  recovery: ClaimRecord;
}

export type SetAssuranceMode = "inherit" | "off" | "required" | "custom";

export interface SetAssurance {
  setId: string;
  mode: SetAssuranceMode;
  /** What the set resolves to now, spelled the way the global field is. */
  resolvedScrubEvery: string;
  /** Present for `custom`. */
  customScrubEvery?: string;
  customValidator?: string;
  /** The set's latest backup, whose claims the section shows. */
  latest?: ArtifactAssurance;
}

/** The four knobs that belong to the host and the process. They are read
 *  here and never written, and no per-set shape carries any of them: the
 *  per-set answer is "no", in writing (docs/design/984-recovery-assurance). */
export interface HostKnobs {
  stagingRoot: string;
  stagingTtl: string;
  concurrency: string;
  downloadMemoryBudget: string;
}

export interface AssuranceSettings {
  scrubEvery: string;
  host: HostKnobs;
}

export type OperationKind = "scrub" | "recovery_test";
export type OperationState = "running" | "complete" | "failed" | "cancelled";

export interface OperationEvent {
  /** Strictly increasing per operation; the key reconnects deduplicate on. */
  seq: number;
  at: string;
  stage: string;
  text: string;
}

export interface Operation {
  id: string;
  kind: OperationKind;
  setId: string;
  state: OperationState;
  /** The current named stage. */
  stage: string;
  bytes: number;
  totalBytes: number;
  startedAt: string;
}

export interface OperationFeed {
  operation: Operation;
  /** Events strictly after the cursor that was asked for. */
  events: OperationEvent[];
  nextCursor: number;
}

export type RecoveryTarget = "browser" | "staging" | "destination";
export type RecoveryState = "running" | "complete" | "cancelled" | "failed";

export interface RecoveryRequest {
  target: RecoveryTarget;
  /** Staging sub-name or destination id, by target. */
  where?: string;
  /** Explicit acknowledgement of an archival thaw's cost. Absent means the
   *  server refuses a read that costs money (never a silent spend). */
  acknowledgeCost?: boolean;
}

export interface Recovery {
  id: string;
  artifactId: string;
  target: RecoveryTarget;
  state: RecoveryState;
  bytes: number;
  totalBytes: number;
  /** Where a browser target streams from; empty for the other targets. */
  streamUrl: string;
}

export interface ArchivalCost {
  provider: string;
  tier: string;
  estimate: string;
}

export interface ValidatorResult {
  exitStatus: number;
  durationMs: number;
  /** Untrusted terminal input. Rendered inert (ValidatorLog.tsx). */
  output: string;
  /** Bytes dropped before `output`, or 0. Carried beside the content, never
   *  inside it, so the marker cannot be forged by the content. */
  truncatedBytes: number;
}

export interface AssuranceApi {
  listArtifactAssurance(setId?: string): Promise<ArtifactAssurance[]>;
  getArtifactAssurance(artifactId: string): Promise<ArtifactAssurance>;
  /** Present when the artifact's only readable copy is archived. */
  getArchivalCost(artifactId: string): Promise<ArchivalCost | null>;
  getSetAssurance(setId: string): Promise<SetAssurance>;
  putSetAssurance(setId: string, mode: SetAssuranceMode, custom?: { scrubEvery: string; validator: string }): Promise<SetAssurance>;
  getSettings(): Promise<AssuranceSettings>;
  putSettings(scrubEvery: string): Promise<AssuranceSettings>;

  startScrub(setId: string): Promise<Operation>;
  startRecoveryTest(setId: string): Promise<Operation>;
  /** The set's operation that is still running, if one is: what a reload
   *  asks, instead of starting another. */
  activeOperation(setId: string): Promise<Operation | null>;
  readOperation(operationId: string, cursor: number): Promise<OperationFeed>;
  cancelOperation(operationId: string): Promise<void>;
  /** The set's most recent recovery test's validator, or null when none has run. */
  getValidatorResult(setId: string): Promise<ValidatorResult | null>;

  createRecovery(artifactId: string, req: RecoveryRequest): Promise<Recovery>;
  getRecovery(recoveryId: string): Promise<Recovery>;
  cancelRecovery(recoveryId: string): Promise<void>;
}
