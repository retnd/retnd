/**
 * EPIC Q, owner decision 1: the old Validation column is retired.
 *
 * FR-13's ingest-time validation answered "did the checksum match and did the
 * validator accept it, at the moment this backup was ingested". The Backups
 * table drew that as a green tick and the word "Verified" in a column of its
 * own, beside the Assurance chips that say what is actually known NOW:
 * Stored, Content verified, Recovery tested. An operator saw two different
 * things both called verified, and the green one said nothing about whether
 * the bytes are still good. The epic's rule is that no surface collapses
 * claims into one green tick, so the column goes and the three claims are the
 * only place this page says "verified".
 *
 * What is NOT retired: the wire field. `validation` stays on the artifact
 * (existing API shapes may only grow), and a validation that FAILED is still
 * worth saying, so it is shown as a failure and never as a pass.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { BackupsPage } from "@shared/pages/BackupsPage";
import { BackupDetailPage } from "@shared/pages/BackupDetailPage";
import { ApiProvider } from "@shared/api/ApiContext";
import type { RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { artifactPath } from "@shared/utilities/routes";
import { resetGraphForTests } from "@shared/state/graph";
import type { BackupArtifact } from "@shared/types/backup";
import tooltips from "@shared/tooltips/tooltips.json";

afterEach(() => {
  cleanup();
  resetGraphForTests();
});

async function withValidation(validation: BackupArtifact["validation"]): Promise<{ api: RetndApi; artifact: BackupArtifact }> {
  const base = createMockApi();
  const artifact = { ...(await createMockApi().listArtifacts())[0], validation };
  const api = {
    ...base,
    listArtifacts: () => Promise.resolve([artifact]),
    getArtifact: () => Promise.resolve(artifact)
  } as RetndApi;
  return { api, artifact };
}

function renderList(api: RetndApi) {
  return render(
    <MemoryRouter>
      <ApiProvider api={api}>
        <BackupsPage readOnly={false} />
      </ApiProvider>
    </MemoryRouter>
  );
}

function renderDetail(api: RetndApi, id: string) {
  return render(
    <MemoryRouter initialEntries={[artifactPath(id)]}>
      <ApiProvider api={api}>
        <Routes>
          <Route path="/backups/:source/:set/:name" element={<BackupDetailPage />} />
        </Routes>
      </ApiProvider>
    </MemoryRouter>
  );
}

describe("the Validation column is retired", () => {
  it("the Backups table has no Validation column header", async () => {
    const { api, artifact } = await withValidation("verified");
    renderList(api);
    await screen.findByText(artifact.filename);
    expect(screen.queryByRole("columnheader", { name: /validation/i })).toBeNull();
    expect(screen.getByRole("columnheader", { name: /assurance/i })).toBeTruthy();
  });

  it("a verified artifact is not stamped with a bare green Verified", async () => {
    const { api, artifact } = await withValidation("verified");
    renderList(api);
    await screen.findByText(artifact.filename);
    const row = screen.getByText(artifact.filename).closest("tr") as HTMLElement;
    // "Content verified" is a claim chip with its own age; a standalone
    // "Verified" is the retired column.
    expect(within(row).queryByText(/^Verified$/)).toBeNull();
  });

  it("a validation that failed is still said, as a failure and never as a pass", async () => {
    const { api, artifact } = await withValidation("failed");
    renderList(api);
    await screen.findByText(artifact.filename);
    const row = screen.getByText(artifact.filename).closest("tr") as HTMLElement;
    expect(within(row).getByText(/failed validation/i)).toBeTruthy();
    expect(within(row).queryByText(/^Verified$/)).toBeNull();
  });

  it("the artifact page has no green Verified badge in its title", async () => {
    const { api, artifact } = await withValidation("verified");
    renderDetail(api, artifact.id);
    await screen.findAllByText(artifact.filename);
    expect(screen.queryByText(/^Verified$/)).toBeNull();
  });

  it("the artifact page still says a validation failed", async () => {
    const { api, artifact } = await withValidation("failed");
    renderDetail(api, artifact.id);
    await screen.findAllByText(artifact.filename);
    expect(screen.getAllByText(/failed validation/i).length).toBeGreaterThan(0);
  });

  it("the column's tooltip entry is gone, so nothing can point at it", () => {
    expect(Object.keys(tooltips as Record<string, unknown>)).not.toContain("backups.col.validation");
  });
});
