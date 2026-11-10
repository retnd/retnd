/**
 * Issue #1318 (EPIC T, T2.3): encryption suites in the browser, below the
 * browser specs (e2e/1318-encryption-suites.spec.ts).
 *
 *   - the field help and the registry entries exist, and the sentence under
 *     each suite is the server's Describe() sentence and nothing typed here;
 *   - the four verbs echo exactly what the terminal prints;
 *   - the client maps the suite routes and drops anything that is not an id,
 *     a status, a count or a sentence (a recipient planted in a response
 *     never reaches the domain shape);
 *   - the suite id rides on the Encrypted chip and the digest line, and the
 *     second chip is untouched;
 *   - the card leads through patch, rotate, bundle and confirm, and Cancel
 *     after the patch shows what is owed before anything closes;
 *   - nothing over 80 characters is drawn whole.
 *
 * WRITTEN, NOT RUN: per the owner's suites rule, vitest runs on the release
 * PR. Nothing here has been seen to fail or pass.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import "@shared/design-system/components.css";

import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { httpApi } from "@shared/api/client";
import type { RetndApi } from "@shared/api/contracts";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { Rows } from "@shared/components/Definitions";
import { EncryptionCard } from "@shared/encryption/EncryptionCard";
import { CopyChips } from "@shared/encryption/EncryptionChips";
import { EncryptionDigests } from "@shared/encryption/EncryptionDigests";
import { EncryptionHealthRows } from "@shared/encryption/EncryptionHealthRows";
import { ClippedText, clipText } from "@shared/encryption/ClippedText";
import { PlacementEncryptionChips } from "@shared/encryption/PlacementEncryptionChips";
import { suiteHealthSentence } from "@shared/encryption/encryptionState";
import {
  keyCreateCommand, keyRotateCommand, policyPatchCommand, reencryptCommand, suitesCommand
} from "@shared/encryption/encryptionCommands";
import { resetGraphForTests } from "@shared/state/graph";
import tooltips from "@shared/tooltips/tooltips.json";
import type { BackupPlacement } from "@shared/types/backup";
import type { CopyEncryption, EncryptionSuite } from "@shared/types/encryption";

/** A post-quantum recipient is about 2000 characters. This one is shaped
 *  like one and carries a tail no page may ever hold. */
const PQ_RECIPIENT = "age1pq1" + "q".repeat(1980) + "CANARY1318TAIL";

beforeEach(() => {
  window.localStorage.clear();
  resetGraphForTests();
});
afterEach(() => {
  cleanup();
  resetGraphForTests();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function renderWith(api: RetndApi, ui: React.ReactNode) {
  return render(
    <MemoryRouter>
      <ApiProvider api={api}>{ui}</ApiProvider>
    </MemoryRouter>
  );
}

const suite = (over: Partial<EncryptionSuite>): EncryptionSuite => ({
  id: "age/v1", version: 1, status: "supported", isDefault: false,
  description: "age v1 with an X25519 recipient; 128-bit security level.",
  retirementVersion: null, activeCopies: 0, keys: 0, ...over
});

describe("field help and the registry (#1318)", () => {
  it("has a three-part entry for the suite choice whose effect is stated, not restated", () => {
    const h = FIELD_HELP.encryptionSuite;
    expect(h.what).toMatch(/suite/i);
    expect(h.example).toBe("age-pq/v1");
    expect(h.effect).toMatch(/rotate/i);
    expect(h.effect).toMatch(/existing cop(y|ies)/i);
    expect(h.effect).not.toEqual(h.what);
  });

  it("names a registry id for the group, the statuses, the health rows and re-encrypt", () => {
    const ids = Object.keys(tooltips);
    for (const id of ["encryption.suite", "encryption.suite.status", "encryption.health.suite", "encryption.reencrypt"]) {
      expect(ids, id).toContain(id);
    }
    // The words no surface may carry (ADR D12).
    expect(JSON.stringify(tooltips)).not.toMatch(/military/i);
  });

  it("draws each suite's sentence from the route, verbatim, and not from the registry", async () => {
    const api = createMockApi("encryption-suite-change");
    const sentence = "A sentence only the route knows, for this test.";
    const real = api.listEncryptionSuites.bind(api);
    vi.spyOn(api, "listEncryptionSuites").mockImplementation(async () =>
      (await real()).map((s) => (s.id === "age-pq/v1" ? { ...s, description: sentence } : s))
    );
    renderWith(api, <EncryptionCard readOnly={false} />);
    const group = await screen.findByRole("radiogroup", { name: "Suite" });
    expect(within(group).getByText(sentence)).toBeInTheDocument();
    expect(JSON.stringify(tooltips)).not.toContain(sentence);
  });
});

describe("the four verbs echo what the terminal prints (#1318)", () => {
  it("spells suites, policy patch --suite, key rotate --suite and reencrypt", () => {
    expect(suitesCommand()).toBe("retnd encryption suites");
    expect(policyPatchCommand({ suite: "age-pq/v1" })).toBe("retnd encryption policy patch --suite age-pq/v1");
    expect(keyRotateCommand("age-pq/v1")).toBe("retnd encryption key rotate --suite age-pq/v1");
    expect(keyCreateCommand("age-pq/v1")).toBe("retnd encryption key create --suite age-pq/v1");
    expect(reencryptCommand({ suite: "age/v1", dryRun: true })).toBe("retnd encryption reencrypt --suite age/v1 --dry-run");
    expect(reencryptCommand({ suite: "age/v1", to: "offsite_cold" })).toBe("retnd encryption reencrypt --suite age/v1 --to offsite_cold");
    expect(reencryptCommand({ deprecated: true, dryRun: true })).toBe("retnd encryption reencrypt --deprecated --dry-run");
  });

  it("keeps the EPIC N lines unchanged when no suite is named", () => {
    expect(keyRotateCommand()).toBe("retnd encryption key rotate");
    expect(keyCreateCommand()).toBe("retnd encryption key create");
    expect(policyPatchCommand({ mode: "off" })).toBe("retnd encryption policy patch --mode off");
  });
});

describe("the client over the suite routes (#1318)", () => {
  const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });

  it("maps GET /encryption/suites and keeps an unknown status as no status rather than the nearest one", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({
      suites: [
        { id: "age-pq/v1", version: 1, status: "supported", default: true, describe: "d1", active_copies: 4, keys: 1 },
        { id: "age/v1", version: 1, status: "deprecated", default: false, describe: "d2", retirement_version: "0.9.0", active_copies: 12, keys: 1 },
        { id: "x/v1", version: 1, status: "sunset", default: false, describe: "d3", active_copies: 0, keys: 0 }
      ]
    })));
    const s = await httpApi.listEncryptionSuites();
    expect(s[0]).toEqual({ id: "age-pq/v1", version: 1, status: "supported", isDefault: true, description: "d1", retirementVersion: null, activeCopies: 4, keys: 1 });
    expect(s[1].retirementVersion).toBe("0.9.0");
    expect(s[2].status).toBe("unknown");
  });

  it("sends the suite on a policy patch and reads rotation_needed back", async () => {
    const fetch = vi.fn(async () => json({
      mode: "required", key_provider: { kind: "managed" }, sets: [], suite: "age-pq/v1", suite_configured: "age-pq/v1",
      rotation_needed: true, rotate_command: "retnd encryption key rotate --suite age-pq/v1"
    }));
    vi.stubGlobal("fetch", fetch);
    const p = await httpApi.patchEncryptionPolicy({ suite: "age-pq/v1" });
    expect(JSON.parse(String((fetch.mock.calls[0] as unknown as [string, RequestInit])[1].body))).toEqual({ suite: "age-pq/v1" });
    expect(p.suite).toBe("age-pq/v1");
    expect(p.suiteConfigured).toBe("age-pq/v1");
    expect(p.rotationNeeded).toBe(true);
    expect(p.rotateCommand).toBe("retnd encryption key rotate --suite age-pq/v1");
  });

  it("asks for the dry run with GET and starts the run with the one destination it names", async () => {
    const fetch = vi.fn(async (url: string) => json(String(url).includes("?")
      ? { groups: [{ suite_id: "age/v1", key_id: "k0", candidates: [
          { artifact_id: "a/b/c", source_medium: "offsite_s3", destination_medium: "offsite_cold", verdict: "would_reencrypt" },
          { artifact_id: "a/b/d", source_medium: "offsite_cold", verdict: "refused", refusal: "same_medium", reason: "same medium only" }
        ] }], would_reencrypt: 1, refused: 1 }
      : { backup_set_id: "a/b", operation_id: "op-1" }));
    vi.stubGlobal("fetch", fetch);
    const plan = await httpApi.planReencrypt({ suite: "age/v1" });
    expect(String((fetch.mock.calls[0] as unknown as [string, RequestInit])[0])).toMatch(/\/encryption\/reencrypt\?suite=age%2Fv1$/);
    expect(plan.copies).toEqual([
      { artifactId: "a/b/c", medium: "offsite_s3", suite: "age/v1", keyId: "k0", to: "offsite_cold", refusal: null, reason: null },
      { artifactId: "a/b/d", medium: "offsite_cold", suite: "age/v1", keyId: "k0", to: null, refusal: "same_medium", reason: "same medium only" }
    ]);
    const started = await httpApi.startReencrypt({ suite: "age/v1", to: "offsite_cold" });
    expect(JSON.parse(String((fetch.mock.calls[1] as unknown as [string, RequestInit])[1].body))).toEqual({ suite_id: "age/v1", destination_medium: "offsite_cold" });
    expect(started).toEqual({ backupSetId: "a/b", operationId: "op-1" });
  });

  it("sends the suite on a rotation and drops a recipient the answer carries", async () => {
    const fetch = vi.fn(async () => json({
      id: "mk-2026-11-10-b", status: "active", created_at: "2026-11-10T09:00:00Z", resolvable: true, resolvability: "resolves",
      bundle_confirmed: false, suite: "age-pq/v1", recipient: PQ_RECIPIENT
    }));
    vi.stubGlobal("fetch", fetch);
    const k = await httpApi.rotateEncryptionKey("age-pq/v1");
    // T1.5 takes the suite as a query parameter on the rotate route.
    expect(String((fetch.mock.calls[0] as unknown as [string, RequestInit])[0])).toMatch(/\/encryption\/keys\/rotate\?suite=age-pq%2Fv1$/);
    expect(k.suite).toBe("age-pq/v1");
    expect(JSON.stringify(k)).not.toContain("CANARY1318TAIL");
  });

  it("carries a placement's suite next to its key id", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({
      artifacts: [{
        id: "a/b/c", backup_set_id: "a/b", set_name: "b", name: "c", state: "COMPLETE", validation: "passed", size_bytes: 1,
        placements: [{
          medium: "offsite_s3", medium_type: "s3", location: "x", access: "immediate", status: "ACTIVE", encrypted: true,
          encryption_key_id: "mk-1", encryption_key_resolvable: true, encryption_suite: "age-pq/v1"
        }]
      }]
    })));
    const list = await httpApi.listArtifacts();
    expect(list[0].placements[0].encryptionSuite).toBe("age-pq/v1");
  });
});

describe("the suite on every encrypted copy (#1318)", () => {
  const pl = (over: Partial<BackupPlacement>): BackupPlacement => ({
    medium: "offsite_s3", mediumType: "s3", location: "x", sizeBytes: 1, storageClass: "", verificationClass: "existence",
    verifiedAt: "2026-11-01T02:00:00Z", access: "immediate", status: "ACTIVE", ...over
  });

  it("tells an age/v1 and an age-pq/v1 copy apart, and leaves the second chip as it was", () => {
    render(
      <MemoryRouter>
        <PlacementEncryptionChips placements={[
          pl({ medium: "offsite_s3", location: "a", encrypted: true, encryptionKeyId: "k0", encryptionKeyResolvable: true, encryptionSuite: "age/v1" }),
          pl({ medium: "offsite_cold", location: "b", encrypted: true, encryptionKeyId: "k1", encryptionKeyResolvable: true, encryptionSuite: "age-pq/v1" })
        ]} />
      </MemoryRouter>
    );
    expect(screen.getByRole("group", { name: /^Encrypted age\/v1, key k0/ })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: /^Encrypted age-pq\/v1, key k1/ })).toBeInTheDocument();
    expect(screen.getAllByRole("group", { name: "Not decrypt-verified" })).toHaveLength(2);
  });

  it("draws no guessed id when the record names no suite", () => {
    render(<MemoryRouter><CopyChips view={{ sealed: true, recovery: "not-verified", verifiedAt: null, keyId: "k1", suite: null }} /></MemoryRouter>);
    const chip = screen.getByRole("group", { name: /^Encrypted/ });
    expect(chip.querySelector("[data-echip-suite]")).toBeNull();
  });

  it("names the suite on the stored-bytes digest line", () => {
    const copy: CopyEncryption = {
      medium: "offsite", encrypted: true, format: "age-pq/v1", formatVersion: 1, keyId: "mk-1", keyResolvable: true,
      storedDigestAlgorithm: "sha256", storedSize: 10, storedDigest: "sha256:f17", plaintextDigest: "sha256:a04"
    };
    render(<MemoryRouter><Rows><EncryptionDigests copies={[copy]} /></Rows></MemoryRouter>);
    expect(screen.getByText(/\(suite age-pq\/v1 v1, key mk-1\)/)).toBeInTheDocument();
  });
});

describe("the health rows (#1318)", () => {
  it("speaks T2.1's sentence, word for word what suiteVerdict writes", () => {
    expect(suiteHealthSentence(suite({ status: "deprecated", retirementVersion: "0.9.0", activeCopies: 12, keys: 1 }), false)).toBe(
      "12 active copies are under age/v1, a deprecated suite; retirement announced for version 0.9.0"
    );
    expect(suiteHealthSentence(suite({ status: "deprecated", retirementVersion: "0.9.0", activeCopies: 1, keys: 1 }), true)).toBe(
      "1 active copies and the active key are under age/v1, a deprecated suite; retirement announced for version 0.9.0"
    );
    expect(suiteHealthSentence(suite({ status: "deprecated", activeCopies: 2, keys: 1 }), false)).toBe(
      "2 active copies are under age/v1, a deprecated suite; no retirement version is announced yet"
    );
    expect(suiteHealthSentence(suite({ id: "mock-retired/v1", status: "retired", retirementVersion: "0.8.0", activeCopies: 3, keys: 1 }), false)).toBe(
      "3 active copies record mock-retired/v1, retired in 0.8.0; they still read, and re-encrypting them under the active suite clears this"
    );
    expect(suiteHealthSentence(suite({ id: "mock-retired/v1", status: "retired", activeCopies: 0, keys: 1 }), true)).toBe(
      "the active key is under mock-retired/v1, retired; new copies are refused until the key is rotated to another suite"
    );
    expect(suiteHealthSentence(suite({ status: "supported", activeCopies: 40 }), true)).toBeNull();
    expect(suiteHealthSentence(suite({ status: "deprecated", retirementVersion: "0.9.0", activeCopies: 0, keys: 0 }), false)).toBeNull();
  });

  it("draws a row per deprecated or retired suite still recorded, and opens the dry run first", async () => {
    const api = createMockApi("encryption-suite-lifecycle");
    const plan = vi.spyOn(api, "planReencrypt");
    const start = vi.spyOn(api, "startReencrypt");
    const user = userEvent.setup();
    renderWith(api, <EncryptionHealthRows />);
    const row = await waitFor(() => {
      const r = document.querySelector('[data-suite-health="age/v1"]');
      if (!r) throw new Error("no row yet");
      return r as HTMLElement;
    });
    expect(document.querySelector('[data-suite-health="mock-retired/v1"]')).not.toBeNull();
    await user.click(within(row).getByRole("button", { name: "Re-encrypt under current suite" }));
    expect(plan).toHaveBeenCalledWith(expect.objectContaining({ suite: "age/v1" }));
    expect(start).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("dialog", { name: "Re-encrypt under current suite" });
    // Nothing is armed, and no medium is assumed, until the operator picks one.
    const destination = await within(dialog).findByRole("radio", { name: "offsite_cold" });
    expect(within(dialog).getByRole("button", { name: "Choose a destination" })).toBeDisabled();
    expect(plan).toHaveBeenCalledTimes(1);
    await user.click(destination);
    await waitFor(() => expect(plan).toHaveBeenLastCalledWith({ suite: "age/v1", to: "offsite_cold" }));
    // 12 copies, 2 already on the chosen medium's backup: the count is 10, not 12.
    const run = await within(dialog).findByRole("button", { name: "Re-encrypt 10 copies" });
    await user.click(run);
    expect(start).toHaveBeenCalledWith({ suite: "age/v1", to: "offsite_cold" });
  });

  it("draws nothing when every recorded suite is supported", async () => {
    const { container } = renderWith(createMockApi("encryption-confirmed"), <EncryptionHealthRows />);
    await new Promise((r) => setTimeout(r, 300));
    expect(container.querySelector("[data-suite-health]")).toBeNull();
  });
});

describe("the suite choice and the rotate-then-confirm flow (#1318)", () => {
  it("lists the registry with status, disables a retired suite with its reason, and marks the default", async () => {
    renderWith(createMockApi("encryption-suite-lifecycle"), <EncryptionCard readOnly={false} />);
    const group = await screen.findByRole("radiogroup", { name: "Suite" });
    const retired = group.querySelector('[data-suite-option="mock-retired/v1"]') as HTMLElement;
    expect(within(retired).getByRole("radio")).toBeDisabled();
    expect(within(retired).getByText("Retired")).toBeInTheDocument();
    expect(retired.querySelector("[data-suite-reason]")).not.toBeNull();
    const pq = group.querySelector('[data-suite-option="age-pq/v1"]') as HTMLElement;
    expect(within(pq).getByText("Default")).toBeInTheDocument();
    expect(within(group.querySelector('[data-suite-option="age/v1"]') as HTMLElement).getByText("Deprecated, retiring in 0.9.0")).toBeInTheDocument();
  });

  it("patches, then rotates under the new suite, then exports and confirms, in that order", async () => {
    const api = createMockApi("encryption-suite-change");
    const patch = vi.spyOn(api, "patchEncryptionPolicy");
    const rotate = vi.spyOn(api, "rotateEncryptionKey");
    const exp = vi.spyOn(api, "exportRecoveryBundle");
    const confirm = vi.spyOn(api, "confirmRecoveryBundle");
    const user = userEvent.setup();
    renderWith(api, <EncryptionCard readOnly={false} />);
    await user.click(await screen.findByRole("radio", { name: /^age-pq\/v1/ }));
    const dialog = await screen.findByRole("dialog", { name: "Change the suite" });

    await user.click(within(dialog).getByRole("button", { name: "Change suite" }));
    await waitFor(() => expect(patch).toHaveBeenCalledWith({ suite: "age-pq/v1" }));
    expect(rotate).not.toHaveBeenCalled();

    await user.click(await within(dialog).findByRole("button", { name: "Rotate key" }));
    await waitFor(() => expect(rotate).toHaveBeenCalledWith("age-pq/v1"));

    await user.click(await within(dialog).findByRole("checkbox", { name: /I understand this file can decrypt every copy/ }));
    await user.click(within(dialog).getByRole("button", { name: "Download bundle" }));
    await waitFor(() => expect(exp).toHaveBeenCalledTimes(1));
    expect(confirm).not.toHaveBeenCalled();

    await user.click(await within(dialog).findByRole("button", { name: "I have stored the bundle safely" }));
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    // The order is the point.
    const order = [patch, rotate, exp, confirm].map((s) => s.mock.invocationCallOrder[0]);
    expect([...order].sort((a, b) => a - b)).toEqual(order);
  });

  it("lists what is owed on Cancel after the patch, and leaving keeps the patch and shows the missing key", async () => {
    const api = createMockApi("encryption-suite-change");
    const user = userEvent.setup();
    renderWith(api, <EncryptionCard readOnly={false} />);
    await user.click(await screen.findByRole("radio", { name: /^age-pq\/v1/ }));
    let dialog = await screen.findByRole("dialog", { name: "Change the suite" });
    await user.click(within(dialog).getByRole("button", { name: "Change suite" }));
    await within(dialog).findByRole("button", { name: "Rotate key" });

    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    const owed = await screen.findByRole("dialog", { name: "Still owed" });
    expect(owed.querySelectorAll("[data-owed]")).toHaveLength(3);
    await user.click(within(owed).getByRole("button", { name: "Back to the steps" }));
    dialog = await screen.findByRole("dialog", { name: "Change the suite" });

    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await user.click(within(await screen.findByRole("dialog", { name: "Still owed" })).getByRole("button", { name: "Leave, I will finish later" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await screen.findByText("No active key for suite age-pq/v1")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /^age-pq\/v1/ })).toBeChecked();
  });

  it("closes straight away on Cancel before the patch, and writes nothing", async () => {
    const api = createMockApi("encryption-suite-change");
    const patch = vi.spyOn(api, "patchEncryptionPolicy");
    const user = userEvent.setup();
    renderWith(api, <EncryptionCard readOnly={false} />);
    await user.click(await screen.findByRole("radio", { name: /^age-pq\/v1/ }));
    const dialog = await screen.findByRole("dialog", { name: "Change the suite" });
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(patch).not.toHaveBeenCalled();
    expect(screen.getByRole("radio", { name: /^age\/v1/ })).toBeChecked();
  });

  it("holds no key material in the page across the whole flow", async () => {
    const api = createMockApi("encryption-suite-change");
    const user = userEvent.setup();
    renderWith(api, <EncryptionCard readOnly={false} />);
    await user.click(await screen.findByRole("radio", { name: /^age-pq\/v1/ }));
    const dialog = await screen.findByRole("dialog", { name: "Change the suite" });
    await user.click(within(dialog).getByRole("button", { name: "Change suite" }));
    await user.click(await within(dialog).findByRole("button", { name: "Rotate key" }));
    await within(dialog).findByRole("button", { name: "Download bundle" });
    expect(document.body.innerHTML).not.toMatch(/AGE-SECRET-KEY|age1pq1/);
    expect(document.querySelector('textarea, input[type="file"]')).toBeNull();
  });
});

describe("nothing over 80 characters is drawn whole (#1318)", () => {
  it("clips at 80 with an ellipsis and leaves a short value alone", () => {
    expect(clipText("mk-2026-11-10-a")).toBe("mk-2026-11-10-a");
    const c = clipText(PQ_RECIPIENT);
    expect(c.length).toBe(81);
    expect(c.endsWith("…")).toBe(true);
    expect(c).not.toContain("CANARY1318TAIL");
  });

  it("offers Copy for a clipped value and puts the whole value on the clipboard only", async () => {
    const write = vi.fn(async () => undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText: write }, configurable: true });
    const user = userEvent.setup();
    render(<ClippedText value={PQ_RECIPIENT} />);
    expect(document.body.innerHTML).not.toContain("CANARY1318TAIL");
    await user.click(screen.getByRole("button", { name: "Copy" }));
    expect(write).toHaveBeenCalledWith(PQ_RECIPIENT);
    expect(document.body.innerHTML).not.toContain("CANARY1318TAIL");
  });

  it("clips a planted recipient on the Settings card and on the copies' chips", async () => {
    const api = createMockApi("encryption-suite-canary");
    renderWith(api, <EncryptionCard readOnly={false} />);
    await screen.findByRole("radiogroup", { name: "Suite" });
    expect(document.body.innerHTML).not.toContain("CANARY1318TAIL");
    cleanup();
    render(
      <MemoryRouter>
        <PlacementEncryptionChips placements={[{
          medium: "offsite_s3", mediumType: "s3", location: "x", sizeBytes: 1, storageClass: "", verificationClass: "existence",
          verifiedAt: null, access: "immediate", status: "ACTIVE", encrypted: true, encryptionKeyId: PQ_RECIPIENT,
          encryptionKeyResolvable: true, encryptionSuite: PQ_RECIPIENT
        }]} />
      </MemoryRouter>
    );
    expect(document.body.innerHTML).not.toContain("CANARY1318TAIL");
  });
});
