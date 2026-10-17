/**
 * #1221: editing a validator that opted in to the workflow environment must
 * not change that environment by accident. The edit is a whole replacement, so
 * the dialog sends back the env the validator already has; an add sends none.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";
import { resetGraphForTests } from "@shared/state/graph";
import { CustomValidatorDialog } from "./CustomValidatorDialog";

const current = {
  name: "billing-dump", pattern: "^b$", backupSetId: "production/billing-mysql", scope: "backup_set" as const, anchored: true,
  env: "workflow" as const,
  scriptFile: "billing-dump.local.sh", scriptPresent: true, scriptSha256: "feedbeef", scriptSizeBytes: 3, uploadedAt: "",
  matchCount: 0, matchSets: [], exampleName: ""
};

describe("CustomValidatorDialog and env (#1221)", () => {
  beforeEach(() => { resetGraphForTests(); });

  async function save(over: Partial<RetndApi>, mode: Parameters<typeof CustomValidatorDialog>[0]["mode"], typed?: [string, string][]) {
    const base = createMockApi("validators-none");
    const api = { ...base, ...over } as unknown as RetndApi;
    const list = await base.listCustomValidators();
    render(
      <ApiProvider api={api}>
        <CustomValidatorDialog mode={mode} list={list} onClose={() => undefined} onSaved={() => undefined} />
      </ApiProvider>
    );
    for (const [label, value] of typed ?? []) fireEvent.change(screen.getByLabelText(label), { target: { value } });
    const button = screen.getByRole("button", { name: mode.kind === "edit" ? "Save changes" : "Upload validator" });
    await waitFor(() => expect(button).toBeEnabled());
    fireEvent.click(button);
  }

  it("an edit that keeps the script sends the env the validator has", async () => {
    const update = vi.fn(() => Promise.resolve(current));
    await save({ updateCustomValidator: update }, { kind: "edit", validator: current });
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(
      "billing-dump", { pattern: "^b$", backupSetId: "production/billing-mysql", keepScriptSha256: "feedbeef", env: "workflow" }, {}
    );
  });

  it("an edit that sends a new script sends the env the validator has", async () => {
    const update = vi.fn(() => Promise.resolve(current));
    await save({ updateCustomValidator: update }, { kind: "edit", validator: current }, [["Script", "#!/bin/sh\nexit 0\n"]]);
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(
      "billing-dump", { pattern: "^b$", backupSetId: "production/billing-mysql", script: "#!/bin/sh\nexit 0\n", env: "workflow" }, {}
    );
  });

  it("an add sends no env, so it is minimal", async () => {
    const create = vi.fn((req: unknown) => Promise.resolve({ ...current, ...(req as object) }));
    await save({ createCustomValidator: create as never }, { kind: "add" }, [
      ["Name", "fresh"], ["Artifact-name pattern", "^f"], ["Script", "#!/bin/sh\nexit 0\n"]
    ]);
    await waitFor(() => expect(create).toHaveBeenCalled());
    const [req] = create.mock.calls[0] as unknown as [Record<string, unknown>];
    expect(req).not.toHaveProperty("env");
  });
});
