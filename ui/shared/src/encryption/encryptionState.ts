/**
 * What a copy's encryption chips say, as pure functions of the records
 * (EPIC N, issue #995; design docs/design/994-retained-copy-encryption.html,
 * section 1).
 *
 * # Encrypted and verified recoverable are separate facts
 *
 * `Encrypted` is what the backend recorded: ciphertext under a key id. It says
 * nothing about whether anybody can read the copy back. `Verified recoverable`
 * is reached on exactly one path: the copy is encrypted, its recorded key
 * resolves right now, and its own placement row says a verification of class
 * `content` was achieved with a time. The backend writes `content` over an
 * encrypted copy only when a decrypt produced a plaintext digest equal to
 * artifacts.local_hash, so the browser renders that and nothing stronger. A
 * ciphertext existing, an existence check, a key resolving, or the absence of
 * any answer never reaches it.
 *
 * Both inputs come from the copy's own rows. The policy is never consulted:
 * a set that says `required` today can still hold a plaintext copy written
 * yesterday, and the chip must say so.
 */
import type { BackupPlacement } from "@shared/types/backup";
import type { CopyEncryption, EncryptionKey, EncryptionPolicy, EncryptionSuite } from "@shared/types/encryption";

export type RecoveryChip =
  /** A plaintext copy has no recovery question. */
  | "none"
  | "verified"
  | "not-verified"
  | "key-missing"
  /** Only ever drawn for a decrypt-verify this page saw fail closed; the
   *  copies read carries no field for it, so it is passed in, never derived. */
  | "decrypt-failed";

export interface CopyEncryptionView {
  /** The first chip: Encrypted, or Plaintext. */
  sealed: boolean;
  /** The second chip. */
  recovery: RecoveryChip;
  /** When the matching decrypt-verify was recorded, only for "verified". */
  verifiedAt: string | null;
  keyId: string | null;
  /** The suite the copy's record names, drawn on the Encrypted chip
   *  (EPIC T, #1318). Null or absent when the record names none, and then
   *  the chip draws no id rather than a guessed one. */
  suite?: string | null;
}

export function copyEncryptionView(
  copy: CopyEncryption,
  placement: (Pick<BackupPlacement, "verificationClass" | "verifiedAt"> & { encryptionSuite?: string | null }) | undefined,
  failedVerify = false
): CopyEncryptionView {
  if (!copy.encrypted) return { sealed: false, recovery: "none", verifiedAt: null, keyId: null, suite: null };
  // The copies read's format IS the suite id (ADR 0033); the placement's own
  // field says the same thing where the page has only the placement.
  const base = { sealed: true, keyId: copy.keyId, suite: copy.format ?? placement?.encryptionSuite ?? null };
  // The key does not resolve: that is its own state, never a weaker class
  // wearing a stronger name. An unreported answer (null) is not "resolves".
  if (copy.keyResolvable === false) return { ...base, recovery: "key-missing", verifiedAt: null };
  if (failedVerify) return { ...base, recovery: "decrypt-failed", verifiedAt: null };
  if (
    copy.keyResolvable === true &&
    placement?.verificationClass === "content" &&
    placement.verifiedAt !== null &&
    placement.verifiedAt !== undefined
  ) {
    return { ...base, recovery: "verified", verifiedAt: placement.verifiedAt };
  }
  return { ...base, recovery: "not-verified", verifiedAt: null };
}

/** The copy of one placement, joined on the medium id. */
export function copyFor(copies: CopyEncryption[] | null, medium: string): CopyEncryption | undefined {
  return copies?.find((c) => c.medium === medium);
}

/** Whether any copy of the backup is encrypted. A deployment with encryption
 *  off has none, and then the Encryption column is not drawn at all. */
export function anyEncrypted(copies: CopyEncryption[] | null): boolean {
  return copies?.some((c) => c.encrypted) === true;
}

/**
 * Whether Verify recoverable may be offered on a copy. A copy whose key does
 * not resolve offers no action that would imply recoverability: the check
 * would be refused before any read, so the button is not drawn.
 */
export function mayVerify(view: CopyEncryptionView): boolean {
  return view.sealed && view.recovery !== "key-missing";
}

/** The key-health conditions the Dashboard and Settings draw, from the keys
 *  read. Empty when the deployment encrypts nothing (no keys at all). */
export type EncryptionCondition =
  | { kind: "bundle-unconfirmed"; keyId: string }
  | { kind: "key-unresolvable"; keyId: string; reason: string | null };

export function encryptionConditions(keys: { id: string; status: string; resolvable: boolean; reason: string | null; bundleConfirmed: boolean }[]): EncryptionCondition[] {
  const out: EncryptionCondition[] = [];
  for (const k of keys) {
    if (k.status === "retired") continue;
    if (!k.bundleConfirmed) out.push({ kind: "bundle-unconfirmed", keyId: k.id });
    if (!k.resolvable) out.push({ kind: "key-unresolvable", keyId: k.id, reason: k.reason });
  }
  return out;
}

/**
 * A placement's own encryption fields as a CopyEncryption, for the surfaces
 * that have the placement and not the copies read (the Backups list). Only the
 * four fields the placement carries are filled (its suite is the format id); the rest are null, and a
 * resolvability the server did not state stays null, never "resolves".
 */
export function placementEncryption(p: Pick<BackupPlacement, "medium" | "encrypted" | "encryptionKeyId" | "encryptionKeyResolvable" | "encryptionSuite">): CopyEncryption {
  const encrypted = p.encrypted === true;
  return {
    medium: p.medium,
    encrypted,
    format: encrypted ? p.encryptionSuite ?? null : null,
    formatVersion: null,
    keyId: encrypted ? p.encryptionKeyId ?? null : null,
    keyResolvable: encrypted ? p.encryptionKeyResolvable ?? null : null,
    storedDigestAlgorithm: null,
    storedSize: null,
    storedDigest: null,
    plaintextDigest: null
  };
}

/** Whether any of these placements is encrypted. */
export const anyPlacementEncrypted = (placements: readonly Pick<BackupPlacement, "encrypted">[]): boolean =>
  placements.some((p) => p.encrypted === true);

/** The backups with a copy recording `keyId`; an empty id keeps them all. */
export function artifactsUnderKey<T extends { placements: readonly Pick<BackupPlacement, "encrypted" | "encryptionKeyId">[] }>(
  artifacts: readonly T[],
  keyId: string
): T[] {
  if (keyId === "") return [...artifacts];
  return artifacts.filter((a) => a.placements.some((p) => p.encrypted === true && p.encryptionKeyId === keyId));
}

/**
 * T2.1's health sentence for one suite (EPIC T, #1318), word for word what
 * core/internal/keycustody/suitehealth.go's suiteVerdict writes into
 * health.Report.KeyCustody.Suites[].Reason, or null where it writes none: a
 * supported suite, or a deprecated or retired one nothing still needs. The
 * health section is not on the wire, so the tile composes the same sentence
 * from the registry's counts; a change on either side changes both.
 * `activeKeyUnderIt` is SuiteHealth.ActiveKey: the active key is this suite's.
 */
export function suiteHealthSentence(s: EncryptionSuite, activeKeyUnderIt: boolean): string | null {
  const v = s.retirementVersion;
  if (s.status === "deprecated") {
    if (s.activeCopies === 0 && !activeKeyUnderIt) return null;
    const under = activeKeyUnderIt ? s.activeCopies + " active copies and the active key are" : s.activeCopies + " active copies are";
    return under + " under " + s.id + ", a deprecated suite; " + (v ? "retirement announced for version " + v : "no retirement version is announced yet");
  }
  if (s.status === "retired") {
    const retired = v ? "retired in " + v : "retired";
    if (s.activeCopies > 0 && activeKeyUnderIt) {
      return s.activeCopies + " active copies and the active key are under " + s.id + ", " + retired + "; the copies still read, new copies are refused until the key is rotated to another suite";
    }
    if (s.activeCopies > 0) {
      return s.activeCopies + " active copies record " + s.id + ", " + retired + "; they still read, and re-encrypting them under the active suite clears this";
    }
    if (activeKeyUnderIt) return "the active key is under " + s.id + ", " + retired + "; new copies are refused until the key is rotated to another suite";
    return null;
  }
  return null;
}

/**
 * The suite the policy names when it has no active key under it (EPIC T,
 * T1.4's refusal): copies under a required managed policy are refused, before
 * anything is deleted, until a rotation. Null when there is nothing owed, and
 * null when the server is older than the suite fields (a key with no suite is
 * not evidence of a mismatch).
 */
export function suiteWithoutKey(policy: Pick<EncryptionPolicy, "mode" | "keyProvider" | "suite"> | null, keys: Pick<EncryptionKey, "status" | "suite">[]): string | null {
  if (!policy || policy.mode !== "required" || policy.keyProvider.kind !== "managed" || !policy.suite) return null;
  const active = keys.find((k) => k.status === "active");
  if (!active) return policy.suite;
  if (active.suite === undefined || active.suite === null) return null;
  return active.suite === policy.suite ? null : policy.suite;
}
