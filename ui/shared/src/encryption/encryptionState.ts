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
import type { CopyEncryption } from "@shared/types/encryption";

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
}

export function copyEncryptionView(
  copy: CopyEncryption,
  placement: Pick<BackupPlacement, "verificationClass" | "verifiedAt"> | undefined,
  failedVerify = false
): CopyEncryptionView {
  if (!copy.encrypted) return { sealed: false, recovery: "none", verifiedAt: null, keyId: null };
  const base = { sealed: true, keyId: copy.keyId };
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
 * three fields the placement carries are filled; the rest are null, and a
 * resolvability the server did not state stays null, never "resolves".
 */
export function placementEncryption(p: Pick<BackupPlacement, "medium" | "encrypted" | "encryptionKeyId" | "encryptionKeyResolvable">): CopyEncryption {
  const encrypted = p.encrypted === true;
  return {
    medium: p.medium,
    encrypted,
    format: null,
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
