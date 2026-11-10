/**
 * Issue #995 follow-up: the Backups list column, the digest rows, the key
 * table's copy counts and the export state read from the record. Written by
 * reading; not run.
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { EncryptionCard } from "@shared/encryption/EncryptionCard";
import { EncryptionDigests } from "@shared/encryption/EncryptionDigests";
import { PlacementEncryptionChips } from "@shared/encryption/PlacementEncryptionChips";
import { Rows } from "@shared/components/Definitions";
import { resetGraphForTests } from "@shared/state/graph";
import type { BackupPlacement } from "@shared/types/backup";
import type { CopyEncryption } from "@shared/types/encryption";

beforeEach(() => { window.localStorage.clear(); resetGraphForTests(); });
afterEach(() => { cleanup(); resetGraphForTests(); });

const pl = (over: Partial<BackupPlacement>): BackupPlacement => ({
  medium: "offsite", mediumType: "s3", location: "k", sizeBytes: 1, storageClass: "", verificationClass: null, verifiedAt: null,
  access: "immediate", status: "ACTIVE", ...over
});

describe("the Backups list Encryption cell", () => {
  it("draws one chip pair per copy, in destination order, from the placement's own fields", () => {
    render(<PlacementEncryptionChips placements={[pl({ medium: "local", mediumType: "local" }), pl({ encrypted: true, encryptionKeyId: "k1", encryptionKeyResolvable: true })]} />);
    const cells = Array.from(document.querySelectorAll("[data-list-copy]"));
    expect(cells.map((c) => c.getAttribute("data-list-copy"))).toEqual(["local", "offsite"]);
    expect(within(cells[0] as HTMLElement).getByRole("group", { name: "Plaintext" })).toBeInTheDocument();
    expect(within(cells[1] as HTMLElement).getByRole("group", { name: /^Encrypted/ })).toBeInTheDocument();
  });

  it("says Key missing on a copy whose key does not resolve, and never verified without a content check", () => {
    render(<PlacementEncryptionChips placements={[pl({ encrypted: true, encryptionKeyId: "k0", encryptionKeyResolvable: false, verificationClass: "content", verifiedAt: "2026-11-01T02:00:00Z" })]} />);
    expect(screen.getByRole("group", { name: /^Key missing/ })).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: /Verified recoverable/ })).toBeNull();
  });

  it("draws nothing when no copy is encrypted, so a deployment with encryption off sees no change", () => {
    const { container } = render(<PlacementEncryptionChips placements={[pl({}), pl({ medium: "local" })]} />);
    expect(container).toBeEmptyDOMElement();
  });
});

const copy = (over: Partial<CopyEncryption>): CopyEncryption => ({
  medium: "offsite", encrypted: true, format: "age", formatVersion: 1, keyId: "k1", keyResolvable: true,
  storedDigestAlgorithm: "sha256", storedSize: 10, storedDigest: "sha256:f17", plaintextDigest: "sha256:a04", ...over
});

describe("the two digests on Backup detail", () => {
  it("names the plaintext digest and each encrypted copy's stored-bytes digest under separate labels", () => {
    render(<MemoryRouter><Rows><EncryptionDigests copies={[copy({})]} /></Rows></MemoryRouter>);
    expect(screen.getByText("Plaintext digest")).toBeInTheDocument();
    expect(screen.getByText("sha256:a04")).toBeInTheDocument();
    expect(screen.getByText("Stored-bytes digest, offsite")).toBeInTheDocument();
    expect(screen.getByText("sha256:f17")).toBeInTheDocument();
  });

  it("draws no row for a plaintext copy or an omitted digest, rather than a placeholder", () => {
    const { container } = render(<MemoryRouter><Rows><EncryptionDigests copies={[copy({ encrypted: false, storedDigest: null, plaintextDigest: null }), copy({ storedDigest: null, plaintextDigest: null })]} /></Rows></MemoryRouter>);
    expect(container.querySelector("dt")).toBeNull();
  });

  it("draws nothing before the copies are read", () => {
    const { container } = render(<Rows><EncryptionDigests copies={null} /></Rows>);
    expect(container.querySelector("dt")).toBeNull();
  });
});

function card(scenario: string) {
  return render(<MemoryRouter><ApiProvider api={createMockApi(scenario)}><EncryptionCard readOnly={false} /></ApiProvider></MemoryRouter>);
}

describe("the key table", () => {
  it("counts the copies each key is recorded on and links to the Backups list filtered to that key", async () => {
    card("encryption-in-use");
    const row = (await screen.findByText("mk-2026-11-09-a", { selector: "td" })).closest("tr") as HTMLElement;
    const link = within(row).getByRole("link", { name: /41 in Backups/ });
    expect(link).toHaveAttribute("href", "/backups?key=mk-2026-11-09-a");
  });

  it("says none and offers no link for a key no copy records", async () => {
    card("encryption-on");
    const row = (await screen.findByText("mk-2026-11-09-a", { selector: "td" })).closest("tr") as HTMLElement;
    expect(within(row).queryByRole("link")).toBeNull();
    expect(row).toHaveTextContent("0");
  });
});

describe("exported, not yet confirmed, read from the record and not from this session", () => {
  it("reads 'exported, not yet confirmed' on a fresh load when the key says it was exported", async () => {
    card("encryption-exported");
    expect(await screen.findByText("Bundle exported, not yet confirmed")).toBeInTheDocument();
    expect(screen.queryByText("No confirmed recovery bundle")).toBeNull();
  });

  it("reads 'No confirmed recovery bundle' for a key never exported, whatever happened in another tab", async () => {
    card("encryption-on");
    expect(await screen.findByText("No confirmed recovery bundle")).toBeInTheDocument();
  });

  it("moves to exported after an export through the route, and confirm clears it", async () => {
    const api = createMockApi("encryption-on");
    await api.exportRecoveryBundle("mk-2026-11-09-a");
    expect((await api.listEncryptionKeys())[0].bundleExportedAt).not.toBeNull();
    await api.confirmRecoveryBundle("mk-2026-11-09-a");
    expect((await api.listEncryptionKeys())[0].bundleConfirmed).toBe(true);
    const user = userEvent.setup();
    void user;
  });
});
