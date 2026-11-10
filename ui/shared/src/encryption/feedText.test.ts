import { describe, expect, it } from "vitest";
import { activityLine } from "@shared/pages/ActivityStrip";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { TOOLTIPS, TOOLTIP_IDS } from "@shared/tooltips/tooltips";
import type { SetActivityEvent } from "@shared/types/activity";
import { encryptionEventText, isEncryptionEvent } from "./feedText";

/** Issue #995: the durable Activity entries and the registry copy. Written by reading; not run. */

const ev = (event: string, fields: Record<string, string>, result: SetActivityEvent["result"] = "success"): SetActivityEvent => ({
  sequence: 1, at: "2026-11-09T02:00:00Z", level: "info", event, scope: "set", message: event, result, fields
});

describe("the key's life and the stages of an encrypted copy have their own sentences", () => {
  it("words each of the nine events and recognises exactly those", () => {
    const names = [
      "key_created", "key_rotated", "key_bundle_exported", "key_bundle_confirmed", "key_retired",
      "copy_encrypting", "copy_uploading_encrypted", "copy_verifying_stored", "copy_decrypt_verifying"
    ];
    for (const n of names) {
      expect(isEncryptionEvent(n), n).toBe(true);
      expect(encryptionEventText(ev(n, { key_id: "k1", artifact: "s/b/a.zst", medium: "offsite" })), n).not.toBeNull();
    }
    expect(isEncryptionEvent("scrub_pass")).toBe(false);
    expect(encryptionEventText(ev("scrub_pass", {}))).toBeNull();
  });

  it("carries the correlation id so the entry matches the request, and nothing else from the fields", () => {
    const text = encryptionEventText(ev("key_bundle_confirmed", { key_id: "k1", correlation_id: "c-123" })) as string;
    expect(text).toContain("recovery bundle confirmed stored for key k1");
    expect(text).toContain("correlation id c-123");
  });

  it("says a failure is a failure, in the line's own stated result", () => {
    expect(encryptionEventText(ev("copy_decrypt_verifying", { artifact: "s/b/a.zst", medium: "offsite", key_id: "k1" }, "error"))).toMatch(/did not match or could not complete, nothing was written/);
    expect(encryptionEventText(ev("copy_decrypt_verifying", { artifact: "s/b/a.zst", medium: "offsite", key_id: "k1" }, "success"))).toMatch(/plaintext digest matches/);
  });

  it("reaches the one line every surface draws, so the terminal, the set panel and the dashboard agree", () => {
    const line = activityLine(ev("copy_encrypting", { artifact: "s/b/a.zst", medium: "offsite" }));
    expect(line.text).toBe("encrypted a.zst on offsite");
  });

  it("falls to the generic line for a stage this build does not name", () => {
    const line = activityLine(ev("copy_some_future_stage", { medium: "offsite" }));
    expect(line.text).toContain("copy_some_future_stage");
  });

  it("cannot print what the events cannot carry: a canary in an unread field never appears", () => {
    const text = encryptionEventText(ev("key_created", { key_id: "k1", material: "CANARY995" })) as string;
    expect(text).not.toContain("CANARY995");
  });
});

describe("the registry entries for the new controls (#995)", () => {
  const ids = TOOLTIP_IDS.filter((id) => id.startsWith("encryption."));

  it("exist under the encryption namespace, for every control and chip the design names", () => {
    for (const id of [
      "encryption.card", "encryption.enable", "encryption.keyProvider", "encryption.bundle.export", "encryption.bundle.acknowledge",
      "encryption.bundle.confirm", "encryption.key.rotate", "encryption.key.retire", "encryption.set.mode", "encryption.external.source",
      "encryption.external.test", "encryption.verify.run", "encryption.verify.acceptCost", "encryption.state.plaintext",
      "encryption.state.encrypted", "encryption.state.verifiedRecoverable", "encryption.state.notVerified", "encryption.state.keyMissing",
      "encryption.state.decryptFailed", "encryption.health.bundle", "encryption.health.keyMissing"
    ]) {
      expect(ids, id).toContain(id);
    }
  });

  it("has no entry for what the design refused: format, cipher, chunk size, cadence, fingerprint, the kopia control", () => {
    for (const id of ids) {
      expect(id).not.toMatch(/format|cipher|chunk|cadence|fingerprint|strength|kopia|algorithm/i);
    }
  });

  it("uses only the tags the pop-up renders, and never claims an algorithm or strength", () => {
    for (const id of ids) {
      const html = TOOLTIPS[id as keyof typeof TOOLTIPS].html;
      for (const tag of html.match(/<\/?([a-z0-9]+)/gi) ?? []) {
        expect(["strong", "em", "code", "br"], id).toContain(tag.replace(/[</]/g, "").toLowerCase());
      }
      expect(html, id).not.toMatch(/securely|strongly|AES|ChaCha|military|unbreakable/i);
    }
  });

  it("gives the one new input a three-part field help whose third part states what typing would cause", () => {
    const help = FIELD_HELP.encryptionExternalReference;
    expect(help.what.length).toBeGreaterThan(20);
    expect(help.example).toBe("/etc/retnd/keys/offsite.key");
    expect(help.effect).toMatch(/refused rather than written in plaintext/);
    expect(help.effect).toMatch(/refuses it when the configuration loads/);
  });
});
