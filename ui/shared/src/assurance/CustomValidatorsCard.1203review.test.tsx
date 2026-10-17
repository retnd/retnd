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
