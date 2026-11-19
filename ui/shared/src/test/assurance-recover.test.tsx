/**
 * Issue #985: the Recover artifact dialog's logic, driven against a stub of
 * the two recovery calls. The browser behaviour (layout, theme, focus) is the
 * Playwright specs' job; this covers the branches a fixture scenario cannot
 * reach cheaply, chiefly the refusal that names a cost the placements did not
 * predict, and what travels in the mint request.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { RetndError } from "@shared/api/contracts";
import type { ApiErrorCode, RecoveryMint, RetndApi } from "@shared/api/contracts";
import { RecoverArtifactDialog } from "@shared/assurance/RecoverArtifact";
import { planOf } from "@shared/assurance/copyPlan";
import type { BackupArtifact, BackupPlacement } from "@shared/types/backup";
import { resetGraphForTests } from "@shared/state/graph";

const ID = "production/postgres-primary/postgres-prod-20260828.dump.zst";

const grant = (target: "staging" | "storage" | "download" = "staging"): RecoveryMint => ({
  kind: "grant", artifactId: ID, copy: "local", grant: "g1", downloadUrl: "/api/v1/recoveries/g1",
  expiresAt: "2026-10-11T00:05:00Z", target: { kind: target }
});

function renderDialog(over: Partial<RetndApi>) {
  resetGraphForTests();
  const api = { ...createMockApi("default"), ...over } as RetndApi;
  render(
    <ApiProvider api={api}>
      <RecoverArtifactDialog artifactId={ID} onClose={() => undefined} />
    </ApiProvider>
  );
  return api;
}

afterEach(() => cleanup());

describe("a read that costs money the placements did not predict", () => {
  it("is refused once, shown as a decision with the service's own words, and retried only with the box ticked", async () => {
    const mint = vi.fn()
      .mockRejectedValueOnce(new RetndError({
        code: "RECOVERY_COST_NOT_ACKNOWLEDGED" as ApiErrorCode,
        message: "Downloading from offsite_s3 is billed by the provider. Nothing was started.",
        correlationId: "c"
      }))
      .mockResolvedValueOnce(grant());
    renderDialog({
      mintRecovery: mint,
      redeemRecovery: vi.fn().mockResolvedValue({
        artifactId: ID, copy: "local", target: { kind: "staging", path: "x" }, path: "/staging/x", bytes: 1, sha256: "a".repeat(64)
      })
    });
    await userEvent.click(await screen.findByRole("radio", { name: "Under the staging root on this retnd host" }));
    await userEvent.type(screen.getByRole("textbox", { name: /Name under the staging root/ }), "x");
    await userEvent.click(screen.getByRole("button", { name: "Recover artifact" }));

    const decision = await screen.findByRole("group", { name: "This read costs money" });
    expect(decision.textContent).toContain("Downloading from offsite_s3 is billed by the provider");
    expect(mint.mock.calls[0][1].acknowledged).toBeUndefined();
    const retry = screen.getByRole("button", { name: "Accept the cost and recover" });
    expect((retry as HTMLButtonElement).disabled).toBe(true);

    await userEvent.click(screen.getByRole("checkbox", { name: "I accept this cost" }));
    // the terminal equivalent now carries the flag the CLI really has
    expect(screen.getByRole("dialog").textContent).toContain("--acknowledge-cost");
    expect(screen.getByRole("dialog").textContent).not.toContain("--accept-cost");
    await userEvent.click(retry);
    await waitFor(() => expect(mint).toHaveBeenCalledTimes(2));
    expect(mint.mock.calls[1][1].acknowledged).toBe(true);
  });
});

describe("what travels in the mint request", () => {
  it("never sends an acknowledgement unless the person ticked one", async () => {
    const mint = vi.fn().mockResolvedValue(grant());
    renderDialog({ mintRecovery: mint, redeemRecovery: vi.fn().mockResolvedValue({ artifactId: ID, copy: "local", target: { kind: "staging" }, bytes: 1, sha256: "b".repeat(64) }) });
    await userEvent.click(await screen.findByRole("radio", { name: "Under the staging root on this retnd host" }));
    await userEvent.type(screen.getByRole("textbox", { name: /Name under the staging root/ }), "x");
    await userEvent.click(screen.getByRole("button", { name: "Recover artifact" }));
    await waitFor(() => expect(mint).toHaveBeenCalled());
    expect(mint.mock.calls[0][1]).toEqual({ target: { kind: "staging", path: "x" }, acknowledged: undefined });
  });

  it("a redeem that fails is a refusal and never a recovered state", async () => {
    const redeem = vi.fn().mockRejectedValue(new RetndError({ code: "RECOVERY_NO_SPACE" as ApiErrorCode, message: "no room", correlationId: "c" }));
    renderDialog({ mintRecovery: vi.fn().mockResolvedValue(grant()), redeemRecovery: redeem });
    await userEvent.click(await screen.findByRole("radio", { name: "Under the staging root on this retnd host" }));
    await userEvent.type(screen.getByRole("textbox", { name: /Name under the staging root/ }), "x");
    await userEvent.click(screen.getByRole("button", { name: "Recover artifact" }));
    const refusal = await screen.findByTestId("refusal");
    expect(refusal.getAttribute("data-code")).toBe("RECOVERY_NO_SPACE");
    expect(screen.queryByTestId("placed")).toBeNull();
    expect(screen.getByRole("dialog").textContent).not.toMatch(/Recovered/);
  });
});

function placement(over: Partial<BackupPlacement>): BackupPlacement {
  return {
    medium: "local", mediumType: "local", location: "/x", sizeBytes: 1, storageClass: "",
    verificationClass: null, verifiedAt: null, access: "immediate", status: "ACTIVE", ...over
  };
}
const withCopies = (p: BackupPlacement[]) => ({ placements: p }) as unknown as BackupArtifact;

describe("what the copies allow", () => {
  it("a free copy beside an archived one needs no restore", () => {
    const p = planOf(withCopies([placement({}), placement({ medium: "cold", access: "requires_restore", storageClass: "DEEP_ARCHIVE" })]));
    expect(p.readable).toBe(true);
    expect(p.archived).toBe(false);
  });
  it("an archived-only backup is a restore, and says where", () => {
    const p = planOf(withCopies([placement({ medium: "cold", access: "requires_restore", storageClass: "DEEP_ARCHIVE" })]));
    expect(p).toMatchObject({ archived: true, readable: false, where: { medium: "cold", storageClass: "DEEP_ARCHIVE" } });
  });
  it("a restore in flight is restoring, not a second decision", () => {
    expect(planOf(withCopies([placement({ access: "restoring" })])).restoring).toBe(true);
  });
  it("an unreachable copy is not an archived one", () => {
    const p = planOf(withCopies([placement({ access: "unreachable" })]));
    expect(p).toMatchObject({ unreachable: true, archived: false });
  });
  it("no copy, or one whose removal is recorded, is none", () => {
    expect(planOf(withCopies([])).none).toBe(true);
    expect(planOf(withCopies([placement({ status: "DELETE_PENDING" })])).none).toBe(true);
  });
});
