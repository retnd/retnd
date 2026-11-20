/**
 * Issue #1528 in the browser, at the component level: the backup page's
 * Retire dialog over the mock's forge fixture (`retire-forge`). The Playwright
 * spec ui/shared/e2e/1528-retire.spec.ts drives the same screens in a real
 * browser; this pins what is asked of the engine and what is shown.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { artifactPath } from "@shared/utilities/routes";
import { BackupDetailPage } from "@shared/pages/BackupDetailPage";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";

const WEEK34_ID = "media/weekly-archive/media-week34.tar";

function renderPage(scenario: "retire-forge" | "retire-source-present" | "backups-ingest-failed") {
  const api = createMockApi(scenario);
  const preview = vi.spyOn(api, "previewRetirement");
  const retire = vi.spyOn(api, "retireArtifact");
  render(
    <MemoryRouter initialEntries={[artifactPath(WEEK34_ID)]}>
      <ApiProvider api={api}>
        <Routes>
          <Route path="/backups/:source/:set/:name" element={<BackupDetailPage />} />
        </Routes>
      </ApiProvider>
    </MemoryRouter>
  );
  return { api, preview, retire };
}

afterEach(cleanup);

describe("#1528 the Retire dialog on a failed backup's page", () => {
  it("#1528 asks the engine first, names the successor, and retires with the note", async () => {
    const user = userEvent.setup();
    const { preview, retire } = renderPage("retire-forge");
    await user.click(await screen.findByRole("button", { name: "Retire…" }));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByText("Gone from the host");
    expect(preview).toHaveBeenCalledWith(WEEK34_ID);
    expect(within(dialog).getByRole("region", { name: "What replaces them" }).textContent).toContain("media-week35.tar");
    await user.type(within(dialog).getByLabelText("Note (optional)"), "host retention");
    await user.click(within(dialog).getByRole("button", { name: "Retire 1 backup" }));
    await within(dialog).findByText("1 backup retired");
    expect(retire).toHaveBeenCalledWith(WEEK34_ID, { note: "host retention" });
    expect(within(dialog).getByRole("status").textContent).toContain("replaced by media-week35.tar");
  });

  it("#1528 refuses a source that is still on the host and never calls retire", async () => {
    const user = userEvent.setup();
    const { retire } = renderPage("retire-source-present");
    await user.click(await screen.findByRole("button", { name: "Retire…" }));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByText("Still on the host");
    expect(within(dialog).getByRole("button", { name: "Retry instead" })).toBeTruthy();
    expect((within(dialog).getByRole("button", { name: "Retire" }) as HTMLButtonElement).disabled).toBe(true);
    expect(retire).not.toHaveBeenCalled();
  });

  it("#1528 with nothing newer, Retire waits for the acknowledgement and sends no_successor", async () => {
    const user = userEvent.setup();
    const { retire } = renderPage("backups-ingest-failed");
    await user.click(await screen.findByRole("button", { name: "Retire…" }));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByText(/This set has no newer good backup/);
    expect((within(dialog).getByRole("button", { name: "Retire" }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(within(dialog).getByRole("checkbox", { name: /I understand there is no replacement/ }));
    await user.click(within(dialog).getByRole("button", { name: "Retire 1 backup" }));
    await waitFor(() => expect(retire).toHaveBeenCalledWith(WEEK34_ID, { no_successor: true }));
  });
});
