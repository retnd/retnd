/**
 * Issue #1189: the set Workflow card's before and after controls.
 *
 * The browser specs (ui/shared/e2e/1189-*.spec.ts) drive the real page.
 * These pin the two rules that are about props: a read-only deployment
 * gets no live control even in edit mode, and the card never claims a
 * per-set control outside edit mode.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { BackupSetWorkflowCard } from "@shared/pages/BackupSetWorkflowCard";

afterEach(cleanup);

function renderCard(props: { readOnly: boolean; editing: boolean }) {
  return render(
    <MemoryRouter>
      <ApiProvider api={createMockApi("workflow-no-globals")}>
        <BackupSetWorkflowCard source="media" set="weekly-archive" {...props} />
      </ApiProvider>
    </MemoryRouter>
  );
}

describe("#1189 set workflow hooks editor", () => {
  it("offers enabled before and after inputs in edit mode", async () => {
    renderCard({ readOnly: false, editing: true });
    expect(await screen.findByTestId("workflow-set-before-input")).toBeEnabled();
    expect(screen.getByTestId("workflow-set-after-input")).toBeEnabled();
  });

  it("disables both inputs when the deployment is read-only", async () => {
    renderCard({ readOnly: true, editing: true });
    expect(await screen.findByTestId("workflow-set-before-input")).toBeDisabled();
    expect(screen.getByTestId("workflow-set-after-input")).toBeDisabled();
  });

  it("shows no inputs outside edit mode and does not claim a per-set control", async () => {
    renderCard({ readOnly: false, editing: false });
    await waitFor(() => expect(screen.getByText(/No hooks are configured/)).toBeInTheDocument());
    expect(screen.queryByTestId("workflow-set-before-input")).toBeNull();
    expect(document.body.textContent).not.toContain("per set here");
  });
});
