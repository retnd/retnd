/**
 * Issue #1428: the Backups rows wait for the assurance claims, so a row is
 * drawn once at its settled height. A read that fails settles it at once, and
 * a read that never returns holds the rows for four seconds and no longer.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { BackupsPage } from "@shared/pages/BackupsPage";
import { ApiProvider } from "@shared/api/ApiContext";
import type { RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { resetGraphForTests } from "@shared/state/graph";
import { resetAssuranceModelForTests } from "@shared/assurance/verificationModel";

function renderWith(listClaims: () => Promise<never>) {
  const real = createMockApi();
  const api: RetndApi = { ...real, assurance: { ...real.assurance!, listArtifactAssurance: listClaims as never } };
  return render(
    <MemoryRouter>
      <ApiProvider api={api}>
        <BackupsPage readOnly={false} />
      </ApiProvider>
    </MemoryRouter>
  );
}

describe("#1428 the Backups rows and the claims read", () => {
  beforeEach(() => { vi.useFakeTimers({ shouldAdvanceTime: true }); });
  afterEach(() => {
    vi.useRealTimers();
    resetGraphForTests();
    resetAssuranceModelForTests();
  });

  it("shows the rows at once when the claims read fails", async () => {
    renderWith(() => Promise.reject(new Error("no route")));
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(screen.queryByTestId("backups-reading-claims")).toBeNull();
    expect((await screen.findAllByRole("row")).length).toBeGreaterThan(1);
  });

  it("holds the rows while a read never returns, and shows them after four seconds", async () => {
    renderWith(() => new Promise<never>(() => {}));
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(screen.getByTestId("backups-reading-claims")).toBeTruthy();
    expect(screen.queryAllByRole("row")).toHaveLength(0);
    await act(async () => { await vi.advanceTimersByTimeAsync(3500); });
    expect(screen.queryByTestId("backups-reading-claims")).toBeNull();
    expect(screen.queryAllByRole("row").length).toBeGreaterThan(1);
  });
});
