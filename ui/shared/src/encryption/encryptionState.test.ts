import { describe, expect, it } from "vitest";
import type { CopyEncryption } from "@shared/types/encryption";
import { anyEncrypted, copyEncryptionView, encryptionConditions, mayVerify } from "./encryptionState";

/**
 * Issue #995: encrypted and verified recoverable are separate states, and the
 * browser may not report recoverable on anything but a completed decrypt-verify
 * of a copy whose key resolves. Written by reading; not run.
 */

const encrypted = (over: Partial<CopyEncryption> = {}): CopyEncryption => ({
  medium: "offsite", encrypted: true, format: "age", formatVersion: 1, keyId: "k1", keyResolvable: true,
  storedDigestAlgorithm: "sha256", storedSize: 10, ...over
});
const plain: CopyEncryption = {
  medium: "local", encrypted: false, format: null, formatVersion: null, keyId: null, keyResolvable: null,
  storedDigestAlgorithm: null, storedSize: null
};

describe("a copy with ciphertext and no completed decrypt-verify is encrypted and not verified recoverable", () => {
  it("is not decrypt-verified when nothing has verified it", () => {
    const v = copyEncryptionView(encrypted(), { verificationClass: null, verifiedAt: null });
    expect(v).toMatchObject({ sealed: true, recovery: "not-verified", verifiedAt: null });
  });

  it("is not verified by an existence check or an attestation", () => {
    for (const verificationClass of ["existence", "attested"] as const) {
      const v = copyEncryptionView(encrypted(), { verificationClass, verifiedAt: "2026-11-09T09:00:00Z" });
      expect(v.recovery, verificationClass).toBe("not-verified");
    }
  });

  it("is not verified by a content class that carries no time", () => {
    expect(copyEncryptionView(encrypted(), { verificationClass: "content", verifiedAt: null }).recovery).toBe("not-verified");
  });

  it("is not verified when the answer about the key is missing", () => {
    expect(copyEncryptionView(encrypted({ keyResolvable: null }), { verificationClass: "content", verifiedAt: "2026-11-09T09:00:00Z" }).recovery).toBe("not-verified");
  });

  it("is not verified with no placement row to say anything", () => {
    expect(copyEncryptionView(encrypted(), undefined).recovery).toBe("not-verified");
  });

  it("is verified recoverable on a content class with a time and a key that resolves", () => {
    const v = copyEncryptionView(encrypted(), { verificationClass: "content", verifiedAt: "2026-11-09T09:00:00Z" });
    expect(v).toMatchObject({ sealed: true, recovery: "verified", verifiedAt: "2026-11-09T09:00:00Z", keyId: "k1" });
  });
});

describe("a copy whose key is unresolvable renders the degraded state (#995)", () => {
  it("is key missing even when an older content check is on record", () => {
    const v = copyEncryptionView(encrypted({ keyResolvable: false }), { verificationClass: "content", verifiedAt: "2026-11-01T09:00:00Z" });
    expect(v.recovery).toBe("key-missing");
    expect(v.sealed).toBe(true);
    expect(v.verifiedAt).toBeNull();
  });

  it("offers no verify action that would imply recoverability", () => {
    expect(mayVerify(copyEncryptionView(encrypted({ keyResolvable: false }), undefined))).toBe(false);
    expect(mayVerify(copyEncryptionView(encrypted(), undefined))).toBe(true);
    expect(mayVerify(copyEncryptionView(plain, undefined))).toBe(false);
  });

  it("shows a decrypt that failed closed, only when this page saw it fail", () => {
    expect(copyEncryptionView(encrypted(), undefined, true).recovery).toBe("decrypt-failed");
    expect(copyEncryptionView(encrypted(), undefined, false).recovery).toBe("not-verified");
  });
});

describe("a plaintext copy is its own chip and never inferred from the policy", () => {
  it("has no recovery question", () => {
    expect(copyEncryptionView(plain, { verificationClass: "content", verifiedAt: "2026-11-09T09:00:00Z" })).toMatchObject({ sealed: false, recovery: "none", keyId: null });
  });

  it("only the encrypted copies make the column appear", () => {
    expect(anyEncrypted([plain])).toBe(false);
    expect(anyEncrypted(null)).toBe(false);
    expect(anyEncrypted([plain, encrypted()])).toBe(true);
  });
});

describe("the conditions the Dashboard draws", () => {
  const key = (over: object = {}) => ({ id: "k1", status: "active", resolvable: true, reason: null, bundleConfirmed: true, ...over });

  it("is empty for a deployment that encrypts nothing", () => {
    expect(encryptionConditions([])).toEqual([]);
  });

  it("is empty for a healthy confirmed key", () => {
    expect(encryptionConditions([key()])).toEqual([]);
  });

  it("names a key with no confirmed bundle and a key that does not resolve", () => {
    expect(encryptionConditions([key({ bundleConfirmed: false })])).toEqual([{ kind: "bundle-unconfirmed", keyId: "k1" }]);
    expect(encryptionConditions([key({ resolvable: false, reason: "custody refused" })])).toEqual([{ kind: "key-unresolvable", keyId: "k1", reason: "custody refused" }]);
  });

  it("ignores a retired key", () => {
    expect(encryptionConditions([key({ status: "retired", bundleConfirmed: false, resolvable: false })])).toEqual([]);
  });
});
