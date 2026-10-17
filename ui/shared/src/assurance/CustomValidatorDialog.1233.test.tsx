/**
 * #1233: the dialog shows and sets a validator's environment. The choice is
 * labelled in words, an add defaults to minimal, an edit starts on what the
 * validator has, and both requests carry the choice.
 */
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";
import { resetGraphForTests } from "@shared/state/graph";
import { CustomValidatorDialog } from "./CustomValidatorDialog";

const current = {
  name: "billing-dump", pattern: "^b$", backupSetId: "production/billing-mysql", scope: "backup_set" as const, anchored: true,
  env: "workflow" as const,
  scriptFile: "billing-dump.local.sh", scriptPresent: true, scriptSha256: "feedbeef", pinnedSha256: "feedbeef", scriptState: "pinned" as const, scriptSizeBytes: 3, uploadedAt: "",
  matchCount: 0, matchSets: [], exampleName: ""
};
const MINIMAL = "Minimal environment";
const WORKFLOW = "Workflow environment, secrets included";

describe("CustomValidatorDialog and the environment choice (#1233)", () => {
  beforeEach(() => { resetGraphForTests(); });

  async function open(over: Partial<RetndApi>, mode: Parameters<typeof CustomValidatorDialog>[0]["mode"]) {
    const base = createMockApi("validators-none");
    const api = { ...base, ...over } as unknown as RetndApi;
    const list = await base.listCustomValidators();
    render(
      <ApiProvider api={api}>
        <CustomValidatorDialog mode={mode} list={list} onClose={() => undefined} onSaved={() => undefined} />
      </ApiProvider>
    );
  }
  const group = () => screen.getByRole("radiogroup", { name: "Environment" });
  const fillAdd = () => {
    for (const [label, value] of [["Name", "fresh"], ["Artifact-name pattern", "^f"], ["Script", "#!/bin/sh\nexit 0\n"]]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
  };

  it("offers the two environments in words, minimal chosen on an add", async () => {
    await open({}, { kind: "add" });
    expect(within(group()).getByRole("radio", { name: MINIMAL })).toBeChecked();
    expect(within(group()).getByRole("radio", { name: WORKFLOW })).not.toBeChecked();
  });

  it("says in one sentence what workflow exposes", async () => {
    await open({}, { kind: "add" });
    const text = group().textContent ?? "";
    expect(text).toMatch(/workflow variables/);
    expect(text).toMatch(/secrets/);
  });

  it("an add that chooses workflow sends env workflow", async () => {
    const create = vi.fn((req: unknown) => Promise.resolve({ ...current, ...(req as object) }));
    await open({ createCustomValidator: create as never }, { kind: "add" });
    fillAdd();
    fireEvent.click(within(group()).getByRole("radio", { name: WORKFLOW }));
    const button = screen.getByRole("button", { name: "Upload validator" });
    await waitFor(() => expect(button).toBeEnabled());
    fireEvent.click(button);
    await waitFor(() => expect(create).toHaveBeenCalled());
    expect((create.mock.calls[0] as unknown as [Record<string, unknown>])[0]).toMatchObject({ env: "workflow" });
  });

  it("an add that leaves the choice alone sends env minimal, said out loud", async () => {
    const create = vi.fn((req: unknown) => Promise.resolve({ ...current, ...(req as object) }));
    await open({ createCustomValidator: create as never }, { kind: "add" });
    fillAdd();
    const button = screen.getByRole("button", { name: "Upload validator" });
    await waitFor(() => expect(button).toBeEnabled());
    fireEvent.click(button);
    await waitFor(() => expect(create).toHaveBeenCalled());
    expect((create.mock.calls[0] as unknown as [Record<string, unknown>])[0]).toMatchObject({ env: "minimal" });
  });

  it("an edit starts on the environment the validator has", async () => {
    await open({}, { kind: "edit", validator: current });
    expect(within(group()).getByRole("radio", { name: WORKFLOW })).toBeChecked();
  });

  it("an edit that chooses minimal sends env minimal", async () => {
    const update = vi.fn(() => Promise.resolve(current));
    await open({ updateCustomValidator: update }, { kind: "edit", validator: current });
    fireEvent.click(within(group()).getByRole("radio", { name: MINIMAL }));
    const button = screen.getByRole("button", { name: "Save changes" });
    await waitFor(() => expect(button).toBeEnabled());
    fireEvent.click(button);
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(
      "billing-dump", { pattern: "^b$", backupSetId: "production/billing-mysql", keepScriptSha256: "feedbeef", env: "minimal" }, {}
    );
  });
});
