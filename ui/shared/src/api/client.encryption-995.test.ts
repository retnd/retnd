import { afterEach, describe, expect, it, vi } from "vitest";
import { httpApi } from "./client";

/** Issue #995 follow-up: the wire fields #993 added are mapped, and absent means absent. Not run. */

function answer(body: unknown) {
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, headers: { get: () => null }, json: async () => body })));
}
afterEach(() => vi.unstubAllGlobals());

describe("the encryption fields on the wire", () => {
  it("maps a key's copy count and export time, and a never-exported key stays null", async () => {
    answer({ keys: [
      { id: "k1", status: "active", created_at: "2026-11-09T09:12:00Z", resolvable: true, resolvability: "resolves", bundle_confirmed: false, copies_recording: 41, bundle_exported_at: "2026-11-09T09:20:00Z" },
      { id: "k2", status: "superseded", created_at: "2026-08-14T09:12:00Z", resolvable: true, resolvability: "resolves", bundle_confirmed: false, copies_recording: 0 }
    ] });
    const keys = await httpApi.listEncryptionKeys();
    expect(keys[0]).toMatchObject({ copiesRecording: 41, bundleExportedAt: "2026-11-09T09:20:00Z" });
    expect(keys[1]).toMatchObject({ copiesRecording: 0, bundleExportedAt: null });
  });

  it("maps a copy's two digests and leaves an omitted one null", async () => {
    answer({ copies: [{ medium: "offsite", encrypted: true, stored_digest: "sha256:f17", plaintext_digest: "sha256:a04" }, { medium: "local", encrypted: false }] });
    const copies = await httpApi.listEncryptionCopies("s/b/a");
    expect(copies[0]).toMatchObject({ storedDigest: "sha256:f17", plaintextDigest: "sha256:a04" });
    expect(copies[1]).toMatchObject({ storedDigest: null, plaintextDigest: null });
  });

  it("maps a placement's encryption fields, and a plaintext copy carries none", async () => {
    answer({ id: "s/b/a", placements: [
      { medium: "offsite", medium_type: "s3", location: "k", access: "immediate", status: "ACTIVE", encrypted: true, encryption_key_id: "k1", encryption_key_resolvable: true },
      { medium: "local", medium_type: "local", location: "/p", access: "immediate", status: "ACTIVE" }
    ] });
    const a = await httpApi.getArtifact("s/b/a");
    expect(a.placements[0]).toMatchObject({ encrypted: true, encryptionKeyId: "k1", encryptionKeyResolvable: true });
    expect(a.placements[1]).toMatchObject({ encrypted: false, encryptionKeyId: null, encryptionKeyResolvable: null });
  });
});
