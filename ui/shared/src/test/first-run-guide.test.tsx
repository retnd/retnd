/**
 * retnd#1049: the guided first-run panel. A fresh install leads through
 * its steps, each skippable, and never shows a refusal for something that
 * has not been configured yet.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { App } from "@shared/App";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { PlatformProvider } from "@shared/platform/PlatformContext";
import { resetGraphForTests } from "@shared/state/graph";
import { genericBridge } from "../../../../apps/generic/frontend/platform";
import type { RetndApi } from "@shared/api/contracts";
import type { AuthContext, PlatformBridge } from "@shared/types/platform";

const AUTHENTICATED: AuthContext = { authenticated: true, username: "bm-admin", mode: "local-account" };
const bridge: PlatformBridge = { ...genericBridge, getAuthContext: () => Promise.resolve(AUTHENTICATED) };

function renderApp(api: RetndApi) {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <ApiProvider api={api}>
        <PlatformProvider bridge={bridge}>
          <App />
        </PlatformProvider>
      </ApiProvider>
    </MemoryRouter>
  );
}

afterEach(() => {
  cleanup();
  resetGraphForTests();
  vi.restoreAllMocks();
});

describe("the first-run guide", () => {
  it("names the three steps and puts a control on the next one", async () => {
    renderApp(createMockApi("first-run"));

    const guide = await screen.findByTestId("first-run-next-step");
    expect(guide.getAttribute("data-next-step")).toBe("backup_set");
    for (const step of ["administrator", "email", "backup_set"]) {
      expect(guide.querySelector(`[data-step="${step}"]`)).not.toBeNull();
    }
    expect(within(guide).getByRole("link", { name: /add backup set/i })).toBeInTheDocument();
    // A guide is a notice, not an alert: nothing here is a failure.
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("skips a step durably through the API and keeps it reachable", async () => {
    const api = createMockApi("first-run");
    const skip = vi.spyOn(api, "skipFirstRunStep");
    renderApp(api);

    const guide = await screen.findByTestId("first-run-next-step");
    await userEvent.click(within(guide).getByRole("button", { name: /skip for now/i }));

    await waitFor(() => expect(skip).toHaveBeenCalledWith("backup_set"));
    // Every step is now done or skipped, so the guide has nothing left to lead.
    await waitFor(() => expect(screen.queryByTestId("first-run-next-step")).toBeNull());
  });
});
