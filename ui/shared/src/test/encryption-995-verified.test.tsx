/**
 * Issue #995 follow-up: the mock reaches the Verified recoverable chip and the
 * two endings of a decrypt-verify, which the capture manifest needs. Written by
 * reading; not run.
 *
 * Names the capture manifest references:
 *   scenario "encryption-verified"      copy encrypted, key resolves, content check on record
 *   scenario "encryption-verify-failed" same copy, not verified; its verify ends failed closed
 *   record   "mock-decrypt-verify-passed" and "mock-decrypt-verify-failed" (state passed / failed)
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import "@shared/design-system/components.css";
import "@shared/encryption/encryption.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { PlacementList } from "@shared/components/PlacementList";
import { VerifyRecoverableDialog } from "@shared/encryption/VerifyRecoverableDialog";
import { EncryptionChip } from "@shared/encryption/EncryptionChips";
import { resetGraphForTests } from "@shared/state/graph";

beforeEach(() => { window.localStorage.clear(); resetGraphForTests(); });
afterEach(() => { cleanup(); resetGraphForTests(); });

describe("encryption-verified reaches the green chip, on the copy's own rows", () => {
  it("serves an encrypted off-site placement with a content check and a resolving key", async () => {
    const api = createMockApi("encryption-verified");
    const a = await api.getArtifact("production/postgres-primary/postgres-prod-20260828.dump.zst");
    const off = a.placements.find((p) => p.medium === "offsite_s3")!;
    expect(off).toMatchObject({ encrypted: true, encryptionKeyResolvable: true, verificationClass: "content" });
    expect(off.verifiedAt).not.toBeNull();
    const local = a.placements.find((p) => p.medium === "local")!;
    expect(local.encrypted).toBeFalsy();
  });

  it("draws Verified recoverable on the copy, and the other scenarios still do not", async () => {
    for (const [scenario, verified] of [["encryption-verified", true], ["encryption-on", false], ["encryption-confirmed", false]] as const) {
      cleanup();
      const api = createMockApi(scenario);
      const a = await api.getArtifact("production/postgres-primary/postgres-prod-20260828.dump.zst");
      const copies = await api.listEncryptionCopies(a.id);
      render(<MemoryRouter><PlacementList placements={a.placements} encryption={copies} /></MemoryRouter>);
      const cell = document.querySelector('[data-encryption-cell="offsite_s3"]') as HTMLElement;
      expect(within(cell).queryAllByRole("group", { name: /^Verified recoverable/ }).length, scenario).toBe(verified ? 1 : 0);
    }
  });
});

describe("the two indicators look different", () => {
  it("are different states with different glyphs and different words", () => {
    render(<><EncryptionChip state="encrypted" /><EncryptionChip state="verified" age="Aug 28 02:00" /></>);
    const enc = screen.getByRole("group", { name: "Encrypted" });
    const ver = screen.getByRole("group", { name: /^Verified recoverable/ });
    expect(enc.getAttribute("data-state")).not.toBe(ver.getAttribute("data-state"));
    expect(enc.querySelector(".echip__glyph")!.textContent).not.toBe(ver.querySelector(".echip__glyph")!.textContent);
    expect(enc.textContent).not.toBe(ver.textContent);
  });

  it("is not told apart by colour alone: the stylesheet gives the green chip a different fill and the plain one a solid neutral edge", async () => {
    const css = (await import("@shared/encryption/encryption.css?raw")).default as string;
    expect(css).toMatch(/\[data-state="verified"\][^}]*var\(--ok\)/);
    expect(css).toMatch(/\[data-state="verified"\][^}]*var\(--ok-quiet\)/);
    expect(css).not.toMatch(/\[data-state="encrypted"\][^}]*var\(--ok/);
  });
});

function dialog(scenario: string) {
  const api = createMockApi(scenario);
  const ended: unknown[] = [];
  render(
    <MemoryRouter><ApiProvider api={api}>
      <VerifyRecoverableDialog
        artifactId="production/postgres-primary/postgres-prod-20260828.dump.zst"
        copy={{ medium: "offsite_s3", encrypted: true, format: "age", formatVersion: 1, keyId: "mk-2026-11-09-a", keyResolvable: true, storedDigestAlgorithm: "sha256", storedSize: 10, storedDigest: null, plaintextDigest: null }}
        onClose={() => undefined}
        onEnded={(r) => ended.push(r)}
      />
    </ApiProvider></MemoryRouter>
  );
  return { api, ended };
}

describe("the mock's operation records end", () => {
  it("has a passing record and a failing record, readable by id in any encryption scenario", async () => {
    const api = createMockApi("encryption-on");
    expect(await api.getAssuranceOperation("mock-decrypt-verify-passed")).toMatchObject({ state: "passed", kind: "scrub" });
    const failed = await api.getAssuranceOperation("mock-decrypt-verify-failed");
    expect(failed.state).toBe("failed");
    expect(failed.reason).toMatch(/authentication failed at chunk/);
  });

  it("ends a verify in a match under encryption-verified", async () => {
    const user = userEvent.setup();
    const { ended } = dialog("encryption-verified");
    await user.click(screen.getByRole("checkbox", { name: "I accept this read" }));
    await user.click(screen.getByRole("button", { name: "Verify now" }));
    expect(await screen.findByRole("group", { name: /^Verified recoverable/ }, { timeout: 8000 })).toBeInTheDocument();
    await waitFor(() => expect(ended).toEqual([{ medium: "offsite_s3", ok: true, reason: null }]));
  }, 15000);

  it("ends a verify failed closed under encryption-verify-failed, naming no partial plaintext", async () => {
    const user = userEvent.setup();
    const { ended } = dialog("encryption-verify-failed");
    await user.click(screen.getByRole("checkbox", { name: "I accept this read" }));
    await user.click(screen.getByRole("button", { name: "Verify now" }));
    expect(await screen.findByRole("group", { name: "Decrypt failed" }, { timeout: 8000 })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(/No partial plaintext/);
    await waitFor(() => expect(ended).toHaveLength(1));
  }, 15000);

  it("leaves encryption-on's verify running, as the running capture needs", async () => {
    const api = createMockApi("encryption-on");
    const s = await api.startDecryptVerify({ artifactId: "production/postgres-primary/a", acknowledgeEgress: true });
    expect(s.operationId).toBe("mock-decrypt-verify");
    await expect(api.getAssuranceOperation(s.operationId)).rejects.toBeDefined();
  });
});
