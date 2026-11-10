/**
 * The mock's retained-copy encryption (EPIC N, issue #995).
 *
 * It holds state per mock instance for the reason the other settings do: a
 * write has to be visible to the next read, which is what the real route's
 * hot reload gives, and a write in one test must not reach the next.
 *
 * What it models is the contract and nothing stronger. A PATCH re-resolves
 * every set (a set that inherits follows the deployment, an override does
 * not), a key reads back as ids and timestamps, a managed key is created
 * without a bundle confirmed, and a retire of a key a copy records is refused
 * with the server's own code and a sentence naming the copies. There is no key
 * material anywhere in this file, and the bundle export saves nothing: it
 * answers a receipt, so no fixture can leak what it never held.
 */
import { RetndError } from "@shared/api/contracts";
import type { ApiErrorCode, AssuranceOperationRecord, RetndApi, ScrubStart } from "@shared/api/contracts";
import type { BackupArtifact } from "@shared/types/backup";
import type {
  CopyEncryption,
  EncryptionKey,
  EncryptionPolicy,
  KeyProviderState,
  SetEncryption
} from "@shared/types/encryption";

/** The scenarios that start with encryption configured. Everything else is
 *  the default deployment: off, no keys, plaintext copies. */
export const ENCRYPTION_SCENARIOS = [
  "encryption-on",
  "encryption-confirmed",
  "encryption-exported",
  // A copy that is encrypted, whose key resolves and whose placement holds a
  // content-class verification: the one road to Verified recoverable. Its verify
  // ends matched ("mock-decrypt-verify-passed").
  "encryption-verified",
  // The same deployment with no decrypt-verify on record, whose verify ends
  // failed closed ("mock-decrypt-verify-failed").
  "encryption-verify-failed",
  "encryption-key-missing",
  "encryption-in-use"
] as const;

/** Scenarios whose key has a confirmed bundle. */
const CONFIRMED = new Set<string>(["encryption-confirmed", "encryption-verified", "encryption-verify-failed"]);

export const PASSED_ID = "mock-decrypt-verify-passed";
export const FAILED_ID = "mock-decrypt-verify-failed";

const NONE: KeyProviderState = { kind: "none", source: null };
const MANAGED: KeyProviderState = { kind: "managed", source: null };
const KEY_A = "mk-2026-11-09-a";
const KEY_OLD = "mk-2026-08-14-a";

const refuse = (code: string, message: string) =>
  new RetndError({ code: code as ApiErrorCode, message, status: 409 });

const later = <T,>(v: T): Promise<T> => new Promise((r) => setTimeout(() => r(v), 120));

export function createMockEncryption(scenario: string, setIds: () => string[]) {
  const on = scenario.startsWith("encryption-");
  let mode: EncryptionPolicy["mode"] = on ? "required" : "off";
  let provider: KeyProviderState = on ? MANAGED : NONE;
  const overrides = new Map<string, { mode: SetEncryption["mode"]; provider: KeyProviderState }>();
  const keys: EncryptionKey[] = on
    ? [
        {
          id: KEY_A, status: "active", createdAt: "2026-11-09T09:12:00Z", retiredAt: null,
          resolvable: scenario !== "encryption-key-missing", resolvability: scenario === "encryption-key-missing" ? "unreachable" : "resolves",
          reason: scenario === "encryption-key-missing" ? "the key file is not readable by this service" : null,
          bundleConfirmed: CONFIRMED.has(scenario), bundleConfirmedAt: CONFIRMED.has(scenario) ? "2026-11-09T09:31:00Z" : null,
          bundleExportedAt: CONFIRMED.has(scenario) || scenario === "encryption-exported" ? "2026-11-09T09:20:00Z" : null,
          copiesRecording: scenario === "encryption-in-use" ? 41 : 0
        }
      ]
    : [];
  const exported = new Set<string>();

  const resolveSet = (id: string): SetEncryption => {
    const o = overrides.get(id);
    const effMode = o && o.mode !== "inherit" ? o.mode : mode;
    return {
      backupSetId: id,
      mode: o ? o.mode : "inherit",
      effectiveMode: effMode,
      isOverride: o !== undefined,
      keyProvider: o ? o.provider : NONE,
      effectiveKeyProvider: o && o.provider.kind !== "none" ? o.provider : provider
    };
  };
  const policy = (): EncryptionPolicy => ({ mode, keyProvider: provider, sets: setIds().map(resolveSet) });
  const key = (id: string) => {
    const k = keys.find((x) => x.id === id);
    if (!k) throw new RetndError({ code: "ENCRYPTION_KEY_NOT_FOUND", message: "There is no managed key " + id + ".", status: 404 });
    return k;
  };

  const methods: Pick<
    RetndApi,
    | "getEncryptionPolicy" | "patchEncryptionPolicy" | "resetEncryptionPolicy" | "setBackupSetEncryption"
    | "clearBackupSetEncryption" | "listEncryptionKeys" | "createEncryptionKey" | "rotateEncryptionKey"
    | "retireEncryptionKey" | "exportRecoveryBundle" | "confirmRecoveryBundle" | "testEncryptionProvider"
    | "listEncryptionCopies" | "startDecryptVerify"
  > = {
    getEncryptionPolicy: () => later(policy()),
    patchEncryptionPolicy: (patch) => {
      if (patch.mode === undefined && patch.keyProvider === undefined) {
        return Promise.reject(new RetndError({ code: "INVALID_REQUEST", message: "A policy change names a mode or a key provider.", status: 400 }));
      }
      const nextProvider = patch.keyProvider
        ? "managed" in patch.keyProvider
          ? MANAGED
          : { kind: "external" as const, source: describeReference(patch.keyProvider.external) }
        : provider;
      const nextMode = patch.mode ?? mode;
      // The words a hand-edited config.yaml gets: required with no provider.
      if (nextMode === "required" && nextProvider.kind === "none") {
        return Promise.reject(new RetndError({
          code: "INVALID_REQUEST",
          message: "retained_copy_encryption: mode required needs a key_provider (a managed key or an external reference).",
          status: 400
        }));
      }
      mode = nextMode;
      provider = nextProvider;
      return later(policy());
    },
    resetEncryptionPolicy: () => {
      mode = "off";
      provider = NONE;
      return later(policy());
    },
    setBackupSetEncryption: (source, set, o) => {
      const id = source + "/" + set;
      const p = o.keyProvider
        ? "managed" in o.keyProvider ? MANAGED : { kind: "external" as const, source: describeReference(o.keyProvider.external) }
        : NONE;
      overrides.set(id, { mode: o.mode, provider: p });
      return later(resolveSet(id));
    },
    clearBackupSetEncryption: (source, set) => {
      overrides.delete(source + "/" + set);
      return later(resolveSet(source + "/" + set));
    },
    listEncryptionKeys: () => later(structuredClone(keys)),
    createEncryptionKey: () => {
      if (keys.some((k) => k.status === "active")) {
        return Promise.reject(refuse("ENCRYPTION_KEY_STATE", "A managed key is already active; rotate it to make a new one."));
      }
      const k: EncryptionKey = {
        id: "mk-2026-11-10-a", status: "active", createdAt: new Date().toISOString(), retiredAt: null,
        resolvable: true, resolvability: "resolves", reason: null, bundleConfirmed: false, bundleConfirmedAt: null, bundleExportedAt: null, copiesRecording: 0
      };
      keys.push(k);
      return later(structuredClone(k));
    },
    rotateEncryptionKey: () => {
      for (const k of keys) if (k.status === "active") k.status = "superseded";
      const k: EncryptionKey = {
        id: "mk-2026-11-10-b" + keys.length, status: "active", createdAt: new Date().toISOString(), retiredAt: null,
        resolvable: true, resolvability: "resolves", reason: null, bundleConfirmed: false, bundleConfirmedAt: null, bundleExportedAt: null, copiesRecording: 0
      };
      keys.push(k);
      return later(structuredClone(k));
    },
    retireEncryptionKey: (id) => {
      let k: EncryptionKey;
      try {
        k = key(id);
      } catch (e) {
        return Promise.reject(e);
      }
      if (k.status === "active") {
        return Promise.reject(refuse("ENCRYPTION_KEY_STATE", "Key " + id + " is the active key. Rotate first, then retire it."));
      }
      if (scenario === "encryption-in-use") {
        return Promise.reject(refuse(
          "ENCRYPTION_KEY_IN_USE",
          "Cannot retire " + id + ": 41 copies still need it, among them production/postgres-primary/postgres-prod-20261108.dump.zst on offsite_s3."
        ));
      }
      k.status = "retired";
      k.retiredAt = new Date().toISOString();
      return later(structuredClone(k));
    },
    exportRecoveryBundle: (id) => {
      key(id);
      exported.add(id);
      key(id).bundleExportedAt = new Date().toISOString();
      return later({ keyId: id, digest: "ab".repeat(32), fileName: "retnd-recovery-bundle-" + id + ".json" });
    },
    confirmRecoveryBundle: (id) => {
      let k: EncryptionKey;
      try {
        k = key(id);
      } catch (e) {
        return Promise.reject(e);
      }
      if (!exported.has(id) && key(id).bundleExportedAt === null) {
        return Promise.reject(refuse("ENCRYPTION_KEY_STATE", "Key " + id + " has no exported bundle to confirm. Export one first."));
      }
      k.bundleConfirmed = true;
      k.bundleConfirmedAt = new Date().toISOString();
      return later(structuredClone(k));
    },
    testEncryptionProvider: (req) => {
      if ("managedKeyId" in req) {
        const k = keys.find((x) => x.id === req.managedKeyId);
        return later(k
          ? { state: k.resolvability, source: "managed key " + k.id, reason: k.reason }
          : { state: "not_declared" as const, source: "managed key " + req.managedKeyId, reason: null });
      }
      const source = describeReference(req.external) ?? "";
      return later("env" in req.external && req.external.env === "EMPTY"
        ? { state: "unreachable" as const, source, reason: "the variable is set to an empty value" }
        : { state: "resolves" as const, source, reason: null });
    },
    listEncryptionCopies: () => {
      const copies: CopyEncryption[] = [{
        medium: "local", encrypted: false, format: null, formatVersion: null, keyId: null,
        keyResolvable: null, storedDigestAlgorithm: null, storedSize: null, storedDigest: null, plaintextDigest: null
      }];
      if (on) {
        copies.push({
          medium: "offsite_s3", encrypted: true, format: "age", formatVersion: 1,
          keyId: scenario === "encryption-key-missing" ? KEY_OLD : KEY_A,
          keyResolvable: scenario !== "encryption-key-missing",
          storedDigestAlgorithm: "sha256", storedSize: 14_200_000_000,
          storedDigest: "sha256:f17c0de1", plaintextDigest: "sha256:a04b7e22"
        });
      }
      return later(copies);
    },
    startDecryptVerify: (req): Promise<ScrubStart> => {
      if (scenario === "encryption-key-missing") {
        return Promise.reject(refuse("ENCRYPTION_KEY_UNRESOLVABLE", "Key " + KEY_OLD + " does not resolve (the key file is not readable by this service). Nothing was read."));
      }
      // The id decides how the run reads back (encryptionOperationRecord): the
      // two verified scenarios end, every other leaves the run unknown to the
      // record, which the dialog draws as still running.
      const operationId =
        scenario === "encryption-verified" ? PASSED_ID : scenario === "encryption-verify-failed" ? FAILED_ID : "mock-decrypt-verify";
      return later({ backupSetId: req.artifactId.split("/").slice(0, 2).join("/"), operationId });
    }
  };
  return methods;
}

/** A reference as it reads back: a description, never something parseable. */
function describeReference(r: { file: string } | { env: string } | { command: string[] }): KeyProviderState["source"] {
  if ("file" in r) return "file: " + r.file;
  if ("env" in r) return "env: " + r.env;
  return "command: " + (r.command[0] ?? "");
}

/**
 * The placements of a backup as the list and the detail routes carry them in
 * an encryption scenario: the off-site copy is encrypted under the scenario's
 * key, and the local copy sends no encryption field at all, as a plaintext copy
 * does on the wire. Any other scenario is returned as it came.
 */
export function overlayEncryption(scenario: string, a: BackupArtifact): BackupArtifact {
  if (!scenario.startsWith("encryption-")) return a;
  const missing = scenario === "encryption-key-missing";
  return {
    ...a,
    placements: a.placements.map((p) =>
      p.medium === "local"
        ? p
        : {
            ...p, encrypted: true, encryptionKeyId: missing ? KEY_OLD : KEY_A, encryptionKeyResolvable: !missing,
            // The only copy with a decrypt-verify on record: a content class with a time.
            ...(scenario === "encryption-verified" ? { verificationClass: "content" as const, verifiedAt: "2026-08-28T02:07:31+02:00" } : {})
          }
    )
  };
}

/**
 * The durable record of the two decrypt-verify runs this mock can end, read by
 * id the way the real route reads one back. One passed, one failed closed with
 * the engine's kind of reason. Any other id is not here (undefined), so a run
 * the mock started under another scenario is still being looked for.
 */
export function encryptionOperationRecord(operationId: string): AssuranceOperationRecord | undefined {
  if (operationId !== PASSED_ID && operationId !== FAILED_ID) return undefined;
  const passed = operationId === PASSED_ID;
  return {
    operationId, kind: "scrub", backupSetId: "production/postgres-primary",
    state: passed ? "passed" : "failed", stage: passed ? "complete" : "",
    reason: passed ? "" : "authentication failed at chunk 412 of 9811",
    startedAt: "2026-08-29T06:14:00+02:00", endedAt: "2026-08-29T06:15:00+02:00", events: []
  };
}
