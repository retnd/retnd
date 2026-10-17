/**
 * Issue #930: the backup set page had an "Exclude" row nothing could fill.
 *
 * No backup-set schema on the wire carries an exclusion list (exclude_paths
 * lives in the config file only), so the real client could only hand the page
 * an empty array and the row was a permanent dash. These hold that the page
 * does not claim a capability with no data source behind it: no row, no
 * field on the model for it to read, and no help entry describing it. If the
 * wire ever grows the field, it comes back together with its source and these
 * get rewritten on purpose.
 */
import { afterEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { BackupSetDetailPage } from "@shared/pages/BackupSetDetailPage";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { resetGraphForTests } from "@shared/state/graph";
import { backupSetPath } from "@shared/utilities/routes";
import tooltips from "@shared/tooltips/tooltips.json";

afterEach(() => resetGraphForTests());

describe("#930 the backup set page has no Exclude row", () => {
  // These two mock sets are the ones that used to carry exclusions, so a
  // row reading the old field would show them here.
  for (const [source, set] of [["production", "postgres-primary"], ["production", "billing-mysql"]]) {
    it(`${source}/${set} shows Include but no Exclude row or exclusion values`, async () => {
      render(
        <MemoryRouter initialEntries={[backupSetPath(source, set)]}>
          <ApiProvider api={createMockApi()}>
            <Routes>
              <Route path="/sets/:source/:set" element={<BackupSetDetailPage readOnly={false} />} />
            </Routes>
          </ApiProvider>
        </MemoryRouter>
      );
      await screen.findByText("Include");
      expect(screen.queryByText("Exclude")).toBeNull();
      expect(screen.queryByText("*.tmp")).toBeNull();
      expect(screen.queryByText("*.part")).toBeNull();
    });
  }

  it("the backup set model carries no exclusion list for a row to read", async () => {
    for (const s of await createMockApi().listSets()) {
      expect(Object.keys(s)).not.toContain("excludePatterns");
    }
  });

  it("the help catalog has no entry for the row", () => {
    expect(Object.keys(tooltips)).not.toContain("sets.detail.exclude");
  });
});
