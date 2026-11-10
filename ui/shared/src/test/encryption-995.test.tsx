/**
 * Issue #995 (EPIC N): the retained-copy encryption screens.
 *
 * Written by reading, against the mock in encryption/mockEncryption.ts and
 * the design in docs/design/994-retained-copy-encryption.html. Not run.
 *
 * What is asserted is mostly what the screens must NOT say:
 *   - a copy that is encrypted is not "Verified recoverable" unless its own
 *     rows say a content check matched, and one whose key does not resolve
 *     offers no verify action;
 *   - a write goes through the route and the card then re-reads, so the
 *     per-set resolution is the engine's and not the page's hope;
 *   - no key material reaches the DOM, the receipt or a fixture, over a
 *     canary planted in the bundle the route returns.
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
import { EncryptionCard } from "@shared/encryption/EncryptionCard";
import { SetEncryptionCard } from "@shared/encryption/SetEncryptionCard";
import { EncryptionHealthRows } from "@shared/encryption/EncryptionHealthRows";
import { PlacementList } from "@shared/components/PlacementList";
import { resetGraphForTests } from "@shared/state/graph";
import { setTooltipsEnabled } from "@shared/state/tooltipNodes";
import type { BackupPlacement } from "@shared/types/backup";
import type { CopyEncryption } from "@shared/types/encryption";

const CANARY = "AGE-SECRET-KEY-CANARY995ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ";

beforeEach(() => {
  window.localStorage.clear();
  resetGraphForTests();
});
afterEach(() => {
  cleanup();
  resetGraphForTests();
  vi.restoreAllMocks();
});

/** Text on the page that is NOT inside a registry pop-up, so a claim about what
 *  the screen says cannot be met by the copy explaining it. */
const onPage = (text: string | RegExp) => screen.queryAllByText(text).filter((n) => n.closest(".tooltip__pop") === null);

function renderWith(api: RetndApi, ui: React.ReactNode) {
  return render(
    <MemoryRouter>
      <ApiProvider api={api}>{ui}</ApiProvider>
    </MemoryRouter>
  );
}

describe("the Settings card, off (default)", () => {
  it("is one switch and a sentence, with no format, cipher, chunk-size or key field", async () => {
    renderWith(createMockApi(), <EncryptionCard readOnly={false} />);
    const sw = await screen.findByRole("switch", { name: "Encrypt copies sent to destinations" });
    expect(sw).not.toBeChecked();
    expect(onPage(/Copies are written exactly as they are today/).length).toBe(1);
    expect(screen.queryByRole("button", { name: /Show key|Copy key|Reveal/i })).toBeNull();
    for (const word of [/cipher/i, /chunk size/i, /algorithm: auto/i]) {
      // The readout says there is none to choose, and nothing offers one.
      expect(screen.queryByRole("textbox", { name: word })).toBeNull();
      expect(screen.queryByRole("combobox", { name: word })).toBeNull();
    }
    expect(onPage(/Deployment-wide/).length).toBeGreaterThan(0);
  });

  it("shows the equivalent commands beside the switch", async () => {
    renderWith(createMockApi(), <EncryptionCard readOnly={false} />);
    await screen.findByRole("switch");
    expect(screen.getByText("retnd encryption policy patch --mode required --managed")).toBeInTheDocument();
    expect(screen.getByText("retnd encryption key create")).toBeInTheDocument();
  });
});

describe("enabling goes through the route and the effective policy re-resolves", () => {
  it("patches once with the managed key, creates the key first, and re-reads the policy", async () => {
    const api = createMockApi();
    const order: string[] = [];
    const create = api.createEncryptionKey.bind(api);
    const patch = api.patchEncryptionPolicy.bind(api);
    const read = api.getEncryptionPolicy.bind(api);
    vi.spyOn(api, "createEncryptionKey").mockImplementation(() => { order.push("create"); return create(); });
    vi.spyOn(api, "patchEncryptionPolicy").mockImplementation((p) => { order.push("patch:" + JSON.stringify(p)); return patch(p); });
    vi.spyOn(api, "getEncryptionPolicy").mockImplementation(() => { order.push("read"); return read(); });
    const user = userEvent.setup();
    renderWith(api, <EncryptionCard readOnly={false} />);

    await user.click(await screen.findByRole("switch", { name: "Encrypt copies sent to destinations" }));
    await user.click(await screen.findByRole("button", { name: "Enable and create key" }));

    await waitFor(() => expect(screen.getByRole("switch", { name: "Encrypt copies sent to destinations" })).toBeChecked());
    // key, then the write, then a read AFTER the write: what the card shows is the route's answer.
    const patchAt = order.findIndex((s) => s.startsWith("patch:"));
    expect(order[patchAt]).toBe('patch:{"mode":"required","keyProvider":{"managed":true}}');
    expect(order.indexOf("create")).toBeLessThan(patchAt);
    expect(order.slice(patchAt + 1)).toContain("read");
  });

  it("is what a set that inherits resolves to afterwards, and an explicit off does not move", async () => {
    const api = createMockApi();
    await api.setBackupSetEncryption("production", "postgres-primary", { mode: "off" });
    await api.patchEncryptionPolicy({ mode: "required", keyProvider: { managed: true } });

    const inheriting = await api.getEncryptionPolicy();
    const byId = Object.fromEntries(inheriting.sets.map((s) => [s.backupSetId, s]));
    expect(byId["production/postgres-primary"]).toMatchObject({ mode: "off", effectiveMode: "off", isOverride: true });
    const other = inheriting.sets.find((s) => s.backupSetId !== "production/postgres-primary");
    expect(other).toMatchObject({ mode: "inherit", effectiveMode: "required", isOverride: false });

    renderWith(api, <SetEncryptionCard source={other!.backupSetId.split("/")[0]} set={other!.backupSetId.split("/")[1]} engine="artifact" readOnly={false} />);
    expect(await screen.findByText(/Inheriting: required \(from the deployment setting\)/)).toBeInTheDocument();
  });

  it("refuses required with no provider in the words a hand-edited file gets, and changes nothing", async () => {
    const api = createMockApi();
    await expect(api.patchEncryptionPolicy({ mode: "required" })).rejects.toThrow(/needs a key_provider/);
    expect((await api.getEncryptionPolicy()).mode).toBe("off");
  });
});

describe("the per-set control", () => {
  it("offers three values and only three, and writes through the route", async () => {
    const api = createMockApi("encryption-confirmed");
    const set = vi.spyOn(api, "setBackupSetEncryption");
    const read = vi.spyOn(api, "getEncryptionPolicy");
    const user = userEvent.setup();
    renderWith(api, <SetEncryptionCard source="production" set="postgres-primary" engine="artifact" readOnly={false} />);

    const group = await screen.findByRole("radiogroup", { name: "Encryption for this set" });
    expect(within(group).getAllByRole("radio").map((r) => (r.parentElement as HTMLElement).textContent)).toEqual(["Inherit", "Off", "Required"]);
    const before = read.mock.calls.length;
    await user.click(within(group).getByLabelText("Off"));
    await waitFor(() => expect(set).toHaveBeenCalledWith("production", "postgres-primary", { mode: "off" }));
    await waitFor(() => expect(read.mock.calls.length).toBeGreaterThan(before));
    expect(await screen.findByText(/written as plaintext whatever the deployment says/)).toBeInTheDocument();
  });

  it("draws a set on the kopia engine disabled with the ADR's statement and no pop-up", async () => {
    renderWith(createMockApi("encryption-confirmed"), <SetEncryptionCard source="production" set="postgres-primary" engine="kopia" readOnly={false} />);
    const group = await screen.findByRole("radiogroup", { name: "Encryption for this set" });
    for (const r of within(group).getAllByRole("radio")) expect(r).toBeDisabled();
    expect(onPage(/already encrypted by the kopia engine/).length).toBe(1);
    // The disabled control is a statement of fact: it is not wrapped in a pop-up host.
    expect(group.closest("[aria-describedby]")).toBeNull();
  });

  it("names a required set whose key cannot be resolved, and says nothing was deleted", async () => {
    renderWith(createMockApi("encryption-key-missing"), <SetEncryptionCard source="production" set="postgres-primary" engine="artifact" readOnly={false} />);
    expect(await screen.findByText("Required, key missing")).toBeInTheDocument();
    expect(onPage(/Copies are being refused/).length).toBe(1);
    expect(onPage(/Nothing was deleted/).length).toBeGreaterThan(0);
  });
});

describe("keys: metadata only, the notice that nags durably, and the refusal that names the copies", () => {
  it("shows the active key by id and the missing-bundle notice until a person confirms", async () => {
    const api = createMockApi("encryption-on");
    renderWith(api, <EncryptionCard readOnly={false} />);
    expect(await screen.findByText("No confirmed recovery bundle")).toBeInTheDocument();
    expect(screen.getAllByText("mk-2026-11-09-a").length).toBeGreaterThan(0);
    // The statement is a button and not an upload: nothing in the card takes a file or key bytes.
    expect(document.querySelector('input[type="file"]')).toBeNull();
    expect(screen.getAllByRole("button", { name: "I have stored the bundle safely" }).length).toBeGreaterThan(0);
  });

  it("clears the notice on confirm, which the route refuses before an export", async () => {
    const api = createMockApi("encryption-on");
    await expect(api.confirmRecoveryBundle("mk-2026-11-09-a")).rejects.toThrow(/no exported bundle/);
    await api.exportRecoveryBundle("mk-2026-11-09-a");
    const confirmed = await api.confirmRecoveryBundle("mk-2026-11-09-a");
    expect(confirmed.bundleConfirmed).toBe(true);
  });

  it("renders the engine's refusal, naming the copies, and offers nothing else", async () => {
    const api = createMockApi("encryption-in-use");
    await api.rotateEncryptionKey();
    const user = userEvent.setup();
    renderWith(api, <EncryptionCard readOnly={false} />);
    await user.click(await screen.findByRole("button", { name: "Retire" }));
    await user.click(await screen.findByRole("button", { name: "Retire key" }));
    // By its own marker: the key banners behind the dialog are alerts too.
    const refusal = await waitFor(() => {
      const el = document.querySelector<HTMLElement>("[data-retire-refusal]");
      expect(el).not.toBeNull();
      return el as HTMLElement;
    });
    expect(refusal).toHaveTextContent(/41 copies still need it/);
    expect(refusal).toHaveTextContent(/postgres-prod-20261108/);
    expect(screen.getByRole("button", { name: "Retire key" })).toBeDisabled();
  });

  it("never expects a 'cannot retire' unavailable error: only the key-in-use and active-key refusals exist", async () => {
    const api = createMockApi("encryption-confirmed");
    await expect(api.retireEncryptionKey("mk-2026-11-09-a")).rejects.toMatchObject({ api: { code: "ENCRYPTION_KEY_STATE" } });
  });
});

describe("the dashboard conditions", () => {
  it("draw nothing for a deployment that encrypts nothing", async () => {
    const { container } = renderWith(createMockApi(), <EncryptionHealthRows />);
    await waitFor(() => expect(container.querySelector("[data-encryption-health]")).toBeNull());
  });

  it("draw the two rows, not dismissible, for an unconfirmed bundle and a key that does not resolve", async () => {
    renderWith(createMockApi("encryption-key-missing"), <EncryptionHealthRows />);
    expect(await screen.findByText("No confirmed recovery bundle")).toBeInTheDocument();
    expect(screen.getByText("A required key cannot be resolved")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /dismiss|close/i })).toBeNull();
  });
});

const placement = (over: Partial<BackupPlacement>): BackupPlacement => ({
  medium: "offsite", mediumType: "s3", location: "k/a", sizeBytes: 10, storageClass: "", verificationClass: null, verifiedAt: null,
  access: "immediate", status: "ACTIVE", ...over
});
const copy = (over: Partial<CopyEncryption>): CopyEncryption => ({
  medium: "offsite", encrypted: true, format: "age", formatVersion: 1, keyId: "mk-1", keyResolvable: true,
  storedDigestAlgorithm: "sha256", storedSize: 10, storedDigest: null, plaintextDigest: null, ...over
});
const local = placement({ medium: "local", mediumType: "local", location: "/data/a" });
const localCopy = copy({ medium: "local", encrypted: false, format: null, formatVersion: null, keyId: null, keyResolvable: null, storedSize: null, storedDigestAlgorithm: null });

describe("per-copy chips on Backup detail", () => {
  it("draws Plaintext beside Encrypted on one backup, as two different things", () => {
    render(<MemoryRouter><PlacementList placements={[local, placement({})]} encryption={[localCopy, copy({})]} /></MemoryRouter>);
    const cells = Array.from(document.querySelectorAll("[data-encryption-cell]"));
    expect(cells.map((c) => c.getAttribute("data-encryption-cell"))).toEqual(["local", "offsite"]);
    expect(within(cells[0] as HTMLElement).getByRole("group", { name: "Plaintext" })).toBeInTheDocument();
    expect(within(cells[1] as HTMLElement).getByRole("group", { name: /^Encrypted/ })).toBeInTheDocument();
  });

  it("renders a copy with ciphertext and no completed decrypt-verify as encrypted and NOT as verified recoverable", () => {
    render(<MemoryRouter><PlacementList placements={[placement({ verificationClass: "existence", verifiedAt: "2026-11-09T02:00:00Z" })]} encryption={[copy({})]} /></MemoryRouter>);
    const cell = document.querySelector('[data-encryption-cell="offsite"]') as HTMLElement;
    expect(within(cell).getByRole("group", { name: /^Encrypted/ })).toBeInTheDocument();
    expect(within(cell).getByRole("group", { name: "Not decrypt-verified" })).toBeInTheDocument();
    expect(within(cell).queryByRole("group", { name: /Verified recoverable/ })).toBeNull();
  });

  it("renders verified recoverable only when the row says a content check matched, with its age", () => {
    render(<MemoryRouter><PlacementList placements={[placement({ verificationClass: "content", verifiedAt: "2026-11-09T02:00:00Z" })]} encryption={[copy({})]} /></MemoryRouter>);
    const cell = document.querySelector('[data-encryption-cell="offsite"]') as HTMLElement;
    expect(within(cell).getByRole("group", { name: /^Verified recoverable/ })).toBeInTheDocument();
  });

  it("renders a copy whose key is unresolvable as key missing and offers no verify action", () => {
    const onVerify = vi.fn();
    render(<MemoryRouter><PlacementList placements={[placement({ verificationClass: "content", verifiedAt: "2026-11-01T02:00:00Z" })]} encryption={[copy({ keyResolvable: false })]} onVerify={onVerify} /></MemoryRouter>);
    const cell = document.querySelector('[data-encryption-cell="offsite"]') as HTMLElement;
    expect(within(cell).getByRole("group", { name: /^Key missing/ })).toBeInTheDocument();
    expect(within(cell).queryByRole("group", { name: /Verified recoverable/ })).toBeNull();
    expect(within(cell).queryByRole("button", { name: "Verify recoverable" })).toBeNull();
  });

  it("draws no Encryption column at all when no copy is encrypted", () => {
    render(<MemoryRouter><PlacementList placements={[local]} encryption={[localCopy]} /></MemoryRouter>);
    expect(screen.queryByRole("columnheader", { name: /Encryption/ })).toBeNull();
    expect(document.querySelector("[data-encryption-cell]")).toBeNull();
  });

  it("states the stored size, the format and the key id beside an encrypted copy, and no digest value it was not given", () => {
    render(<MemoryRouter><PlacementList placements={[placement({})]} encryption={[copy({})]} /></MemoryRouter>);
    const cell = document.querySelector('[data-encryption-cell="offsite"]') as HTMLElement;
    expect(cell).toHaveTextContent("format age v1, key mk-1");
  });
});

describe("the opt-out preference governs every new pop-up, tested with it explicitly ON", () => {
  it("shows a chip's pop-up on hover with tooltips on, and nothing with them off", async () => {
    const user = userEvent.setup();
    setTooltipsEnabled(true);
    render(<MemoryRouter><PlacementList placements={[placement({})]} encryption={[copy({})]} /></MemoryRouter>);
    const chip = screen.getByRole("group", { name: /^Encrypted/ });
    await user.hover(chip);
    const pop = Array.from(document.querySelectorAll<HTMLElement>(".tooltip__pop")).find((p) => p.textContent?.includes("stored as ciphertext"));
    expect(pop).toBeDefined();
    expect(pop).toBeVisible();
    await user.unhover(chip);

    setTooltipsEnabled(false);
    await user.hover(screen.getByRole("group", { name: /^Encrypted/ }));
    expect(Array.from(document.querySelectorAll<HTMLElement>(".tooltip__pop")).find((p) => p.textContent?.includes("stored as ciphertext"))).not.toBeVisible();
  });
});

describe("no key material after the one-time export", () => {
  it("is saved as a file by the client and only a receipt leaves it", async () => {
    const bundle = {
      created: "2026-11-09T09:12:00Z", digest: "ab".repeat(32), exported: "2026-11-09T09:20:00Z", format: "age", instructions: ["decrypt with age"],
      key_id: "mk-1", material: CANARY, recipient: "age1recipient", version: "1"
    };
    const saved: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(bundle), { status: 200, headers: { "content-type": "application/json" } })));
    // jsdom's Blob has no text(); a FileReader reads it.
    const create = vi.fn((blob: Blob) => {
      const reader = new FileReader();
      reader.onload = () => saved.push(String(reader.result));
      reader.readAsText(blob);
      return "blob:x";
    });
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: vi.fn() });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);

    const receipt = await httpApi.exportRecoveryBundle("mk-1");

    expect(receipt).toEqual({ keyId: "mk-1", digest: "ab".repeat(32), fileName: "retnd-recovery-bundle-mk-1.json" });
    expect(JSON.stringify(receipt)).not.toContain(CANARY);
    expect(create).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(saved.join("")).toContain(CANARY));
    expect(document.body.innerHTML).not.toContain(CANARY);
    expect(JSON.stringify({ ...window.localStorage })).not.toContain(CANARY);
    vi.unstubAllGlobals();
  });

  it("never reaches the page: the export dialog, its receipt line and the whole card hold no canary", async () => {
    const api = createMockApi("encryption-on");
    // A route that, against the contract, answers a receipt that happened to carry material.
    vi.spyOn(api, "exportRecoveryBundle").mockImplementation(async (id) => ({ keyId: id, digest: "cd".repeat(32), fileName: "retnd-recovery-bundle-" + id + ".json" }));
    const user = userEvent.setup();
    renderWith(api, <EncryptionCard readOnly={false} />);
    await user.click((await screen.findAllByRole("button", { name: "Export recovery bundle" }))[0]);
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("button", { name: "Download bundle" })).toBeDisabled();
    await user.click(within(dialog).getByRole("checkbox", { name: /I understand this file can decrypt every copy/ }));
    await user.click(within(dialog).getByRole("button", { name: "Download bundle" }));
    // The dialog is gone; the page holds no receipt of its own, it re-reads the key.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.body.innerHTML).not.toContain(CANARY);
  });

  it("has no field anywhere on the card that could hold a key", async () => {
    renderWith(createMockApi("encryption-on"), <EncryptionCard readOnly={false} />);
    await screen.findByText("No confirmed recovery bundle");
    expect(document.querySelector('textarea, input[type="file"]')).toBeNull();
    expect(screen.queryByText(/QR/)).toBeNull();
  });
});

describe("the external reference", () => {
  it("is a single-line box whose label follows the choice, with no paste-the-key variant", async () => {
    const user = userEvent.setup();
    renderWith(createMockApi("encryption-on"), <EncryptionCard readOnly={false} />);
    await user.click(await screen.findByRole("button", { name: "Use an external key reference instead" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Path to the key file")).toHaveAttribute("type", "text");
    await user.click(within(dialog).getByLabelText("Environment variable"));
    expect(within(dialog).getByLabelText("Variable name")).toBeInTheDocument();
    await user.click(within(dialog).getByLabelText("Command"));
    expect(within(dialog).getByLabelText("Executable")).toBeInTheDocument();
    expect(dialog.querySelector("textarea, input[type=file]")).toBeNull();
  });

  it("reports the three probe answers and renders none of what was resolved", async () => {
    const api = createMockApi("encryption-on");
    const user = userEvent.setup();
    renderWith(api, <EncryptionCard readOnly={false} />);
    await user.click(await screen.findByRole("button", { name: "Use an external key reference instead" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByLabelText("Environment variable"));
    await user.type(within(dialog).getByLabelText("Variable name"), "EMPTY");
    await user.click(within(dialog).getByRole("button", { name: "Test reference" }));
    expect(await within(dialog).findByRole("group", { name: "Declared but unreachable" })).toBeInTheDocument();
    expect(dialog).toHaveTextContent("the variable is set to an empty value");
    await user.clear(within(dialog).getByLabelText("Variable name"));
    await user.type(within(dialog).getByLabelText("Variable name"), "FINE");
    await user.click(within(dialog).getByRole("button", { name: "Test reference" }));
    expect(await within(dialog).findByRole("group", { name: "Resolves" })).toBeInTheDocument();
  });

  it("reads back as its description and does not pre-fill the box from it", async () => {
    const api = createMockApi("encryption-on");
    await api.patchEncryptionPolicy({ keyProvider: { external: { file: "/etc/retnd/keys/offsite.key" } } });
    const user = userEvent.setup();
    renderWith(api, <EncryptionCard readOnly={false} />);
    expect(await screen.findByText("file: /etc/retnd/keys/offsite.key")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Use an external key reference instead" }));
    expect(within(await screen.findByRole("dialog")).getByLabelText("Path to the key file")).toHaveValue("");
  });
});

describe("Verify recoverable", () => {
  it("requires the egress acceptance before Verify now is enabled, and echoes the command", async () => {
    const { VerifyRecoverableDialog } = await import("@shared/encryption/VerifyRecoverableDialog");
    const user = userEvent.setup();
    renderWith(createMockApi("encryption-on"), <VerifyRecoverableDialog artifactId="production/postgres-primary/a.zst" copy={copy({ storedSize: 14_200_000_000 })} onClose={() => undefined} onEnded={() => undefined} />);
    const now = screen.getByRole("button", { name: "Verify now" });
    expect(now).toBeDisabled();
    await user.click(screen.getByRole("checkbox", { name: "I accept this read" }));
    expect(now).toBeEnabled();
    expect(screen.getByText("retnd encryption verify production/postgres-primary/a.zst --medium offsite --allow-egress")).toBeInTheDocument();
    expect(screen.getByText(/costs egress/)).toBeInTheDocument();
  });

  it("draws a refused key as key missing, says nothing was read, and starts no operation", async () => {
    const { VerifyRecoverableDialog } = await import("@shared/encryption/VerifyRecoverableDialog");
    const api = createMockApi("encryption-key-missing");
    const user = userEvent.setup();
    renderWith(api, <VerifyRecoverableDialog artifactId="production/postgres-primary/a.zst" copy={copy({ keyId: "mk-old", keyResolvable: true })} onClose={() => undefined} onEnded={() => undefined} />);
    await user.click(screen.getByRole("checkbox", { name: "I accept this read" }));
    await user.click(screen.getByRole("button", { name: "Verify now" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/Nothing was read/);
    expect(within(alert).getByRole("group", { name: /Key missing/ })).toBeInTheDocument();
    expect(document.querySelector("[data-verify-running]")).toBeNull();
  });

  it("follows the operation from the durable record, once, and ends in the engine's words on a failed decrypt", async () => {
    const { VerifyRecoverableDialog } = await import("@shared/encryption/VerifyRecoverableDialog");
    const api = createMockApi("encryption-on");
    const states = ["running", "failed"] as const;
    let n = 0;
    vi.spyOn(api, "getAssuranceOperation").mockImplementation(async () => ({
      operationId: "mock-decrypt-verify", kind: "scrub", backupSetId: "production/postgres-primary",
      state: states[Math.min(n++, 1)], stage: "", reason: "authentication failed at chunk 412 of 9811",
      startedAt: "2026-11-09T02:00:00Z", endedAt: "", events: []
    }));
    const start = vi.spyOn(api, "startDecryptVerify");
    const ended = vi.fn();
    const user = userEvent.setup();
    renderWith(api, <VerifyRecoverableDialog artifactId="production/postgres-primary/a.zst" copy={copy({})} onClose={() => undefined} onEnded={ended} />);
    await user.click(screen.getByRole("checkbox", { name: "I accept this read" }));
    await user.click(screen.getByRole("button", { name: "Verify now" }));
    expect(await screen.findByRole("group", { name: "Decrypt-verifying" })).toBeInTheDocument();
    expect(await screen.findByRole("group", { name: "Decrypt failed" }, { timeout: 8000 })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(/authentication failed at chunk 412 of 9811.*No partial plaintext/);
    expect(start).toHaveBeenCalledTimes(1);
    expect(ended).toHaveBeenCalledWith({ medium: "offsite", ok: false, reason: "authentication failed at chunk 412 of 9811" });
  }, 15000);
});
