/**
 * Issue #1203, review round: removing a validator takes coverage away from the
 * backups it matched, and the confirmation says what they fall back to.
 */
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { CustomValidatorsCard } from "./CustomValidatorsCard";

describe("the remove confirmation", () => {
  it("names the matching backups and what they fall back to, and does not claim the script is deleted", async () => {
    render(<ApiProvider api={createMockApi("default")}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Remove billing-dump" }));
    const dlg = await screen.findByRole("dialog");
    const text = dlg.textContent ?? "";
    expect(text).toMatch(/2 backups/);
    expect(text).toMatch(/validator_id/);
    expect(text).toMatch(/recovery-test folder/);
    expect(text).toMatch(/built-in check/);
    expect(text).not.toMatch(/deletes its script|is gone/);
    expect(within(dlg).getByRole("button", { name: "Remove validator" })).toBeInTheDocument();
  });
});

describe("what the list now says about the directory and about collisions (#1213)", () => {
  const withList = async (over: Partial<Awaited<ReturnType<ReturnType<typeof createMockApi>["listCustomValidators"]>>>) => {
    const base = createMockApi("default");
    const list = await base.listCustomValidators();
    return { ...base, listCustomValidators: () => Promise.resolve({ ...list, ...over }) };
  };

  it("warns about backups two patterns both match, naming the validators and the backup", async () => {
    const api = await withList({ ambiguities: [{ backupSetId: "production/billing-mysql", example: "production/billing-mysql/billing-1.sql.gz", validators: ["billing-dump", "any-sql"] }] });
    render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    const warning = await screen.findByTestId("validators-ambiguities");
    expect(warning).toHaveTextContent("billing-dump and any-sql");
    expect(warning).toHaveTextContent("production/billing-mysql/billing-1.sql.gz");
    expect(warning).toHaveTextContent(/could not complete/);
  });

  it("says nothing about collisions when there are none", async () => {
    const api = await withList({ ambiguities: [] });
    render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    await screen.findByRole("button", { name: "Remove billing-dump" });
    expect(screen.queryByTestId("validators-ambiguities")).toBeNull();
  });

  it("says why a refused directory runs nothing, and offers no add or edit", async () => {
    const api = await withList({ directoryRefused: "the directory is writable by another account" });
    render(<ApiProvider api={api}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    expect(await screen.findByText(/writable by another account/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add validator" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Edit billing-dump" })).toBeDisabled();
  });

  it("says the removed script is moved aside, not deleted", async () => {
    render(<ApiProvider api={createMockApi("default")}><CustomValidatorsCard readOnly={false} /></ApiProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Remove billing-dump" }));
    const dlg = await screen.findByRole("dialog");
    expect(dlg.textContent).toMatch(/moves its script aside/);
    expect(dlg.textContent).toMatch(/recovery-test-validators\.superseded/);
  });
});
