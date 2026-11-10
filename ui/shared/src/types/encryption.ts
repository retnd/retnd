/**
 * Retained-copy encryption as the UI holds it (EPIC N, issue #995).
 *
 * These are the domain shapes behind the /api/v1/encryption routes (#993).
 * Nothing in here can carry key material, and that is a property of the
 * types and not of the pages: a key is an id, a status and two timestamps; a
 * key provider reads back as a description of its source; the one thing that
 * does carry material, the recovery bundle, has no type here at all, because
 * the client turns it into a file download and hands back a receipt.
 */

/** What the deployment says. The API's policy has two values; a backup set has
 *  a third, `inherit`, which follows the deployment. */
export type EncryptionMode = "off" | "required";
export type SetEncryptionMode = "inherit" | EncryptionMode;

/** A key provider as it reads back (never a reference that could be parsed). */
export interface KeyProviderState {
  kind: "managed" | "external" | "none";
  /** The description of an external source ("file: /path"), absent otherwise. */
  source: string | null;
}

/** An external key reference as it goes in: exactly one of the three. Write-only. */
export type ExternalKeyReference = { file: string } | { env: string } | { command: string[] };

/** A key provider as it goes in. */
export type KeyProviderInput = { managed: true } | { external: ExternalKeyReference };

export interface SetEncryption {
  backupSetId: string;
  /** What the set says. */
  mode: SetEncryptionMode;
  /** What applies after inheritance. */
  effectiveMode: EncryptionMode;
  isOverride: boolean;
  keyProvider: KeyProviderState;
  effectiveKeyProvider: KeyProviderState;
  /** "repository" for a kopia-engine set: required is satisfied by kopia's own repository encryption. */
  satisfiedBy?: "repository";
}

export interface EncryptionPolicy {
  mode: EncryptionMode;
  keyProvider: KeyProviderState;
  sets: SetEncryption[];
}

export interface EncryptionPolicyPatch {
  mode?: EncryptionMode;
  keyProvider?: KeyProviderInput;
}

export interface EncryptionOverride {
  mode: SetEncryptionMode;
  keyProvider?: KeyProviderInput;
}

export type KeyStatus = "active" | "superseded" | "retired";
export type Resolvability = "resolves" | "unreachable" | "not_declared";

/** One managed key's identity and condition. Never material, a recipient or a fingerprint. */
export interface EncryptionKey {
  id: string;
  status: KeyStatus;
  createdAt: string;
  retiredAt: string | null;
  resolvable: boolean;
  resolvability: Resolvability;
  /** Why it does not resolve, naming the source and never its content. */
  reason: string | null;
  bundleConfirmed: boolean;
  bundleConfirmedAt: string | null;
  /** When a bundle was last exported for this key, or null if never. */
  bundleExportedAt: string | null;
  /** How many active copies record this key. */
  copiesRecording: number;
}

export interface ProviderProbe {
  state: Resolvability;
  source: string;
  reason: string | null;
}

/** One copy's encryption state, as GET /encryption/copies reports it. */
export interface CopyEncryption {
  /** "local", or a storage medium id. */
  medium: string;
  encrypted: boolean;
  format: string | null;
  formatVersion: number | null;
  keyId: string | null;
  /** Null on a plaintext copy: nothing to resolve. */
  keyResolvable: boolean | null;
  storedDigestAlgorithm: string | null;
  storedSize: number | null;
  /** The digest of the stored bytes (ciphertext), and the artifact's own
   *  plaintext digest. Null when the route omitted them. */
  storedDigest: string | null;
  plaintextDigest: string | null;
}

export interface DecryptVerifyRequest {
  artifactId: string;
  medium?: string;
  acknowledgeEgress?: boolean;
}

/** What exporting a bundle leaves behind in the page: which key, the digest
 *  the file carries, and the name it was saved under. No material. */
export interface BundleReceipt {
  keyId: string;
  digest: string;
  fileName: string;
}
