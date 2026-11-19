/**
 * Issue #1203, review round: how the add/edit dialog behaves under typing,
 * files, slow answers and its own shared state. The dialog is mounted over the
 * in-process mock with single methods swapped for spies.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { RetndError } from "@shared/api/contracts";
import type { CustomValidatorList, CustomValidatorPreview, RetndApi } from "@shared/api/contracts";
import { graph, resetGraphForTests } from "@shared/state/graph";
import { setsNode } from "@shared/state/appNodes";
import type { BackupSet } from "@shared/types/backup";
import { CustomValidatorDialog } from "./CustomValidatorDialog";
import type { ValidatorDialogMode } from "./CustomValidatorDialog";

async function mount(over: Partial<RetndApi> = {}, mode: ValidatorDialogMode = { kind: "add" }) {
  const base = createMockApi("validators-none");
  const api = { ...base, ...over } as RetndApi;
  const list: CustomValidatorList = await base.listCustomValidators();
  const utils = render(
    <ApiProvider api={api}>
      <CustomValidatorDialog mode={mode} list={list} onClose={() => undefined} onSaved={() => undefined} />
    </ApiProvider>
  );
  return { api, list, ...utils };
}

const pattern = () => screen.getByLabelText("Artifact-name pattern") as HTMLInputElement;
const type = (el: HTMLElement, value: string) => fireEvent.change(el, { target: { value } });

describe("CustomValidatorDialog (review of #1203)", () => {
  beforeEach(() => { resetGraphForTests(); });
  afterEach(() => { vi.restoreAllMocks(); });

  it("encodes the script once for a keystroke, not once per use of its size", async () => {
    await mount();
    const encode = vi.spyOn(TextEncoder.prototype, "encode");
    type(screen.getByLabelText("Script"), "#!/bin/sh\nexit 0\n");
    expect(encode.mock.calls.length).toBeLessThanOrEqual(1);
    expect(screen.getByText(/17 bytes of 256 KiB/)).toBeInTheDocument();
  });

  it("aborts the preview a newer pattern replaced", async () => {
    const signals: Array<AbortSignal | undefined> = [];
    const previewCustomValidator = vi.fn((_req: unknown, opts?: { signal?: AbortSignal }) => {
      signals.push(opts?.signal);
      return new Promise<CustomValidatorPreview>(() => undefined);
    });
    await mount({ previewCustomValidator } as unknown as Partial<RetndApi>);
    type(pattern(), "^a$");
    await waitFor(() => expect(signals).toHaveLength(1));
    type(pattern(), "^ab$");
    await waitFor(() => expect(signals).toHaveLength(2));
    expect(signals[0]?.aborted).toBe(true);
    expect(signals[1]?.aborted).toBe(false);
  });

  it("aborts the preview when the dialog goes away", async () => {
    const signals: Array<AbortSignal | undefined> = [];
    const previewCustomValidator = vi.fn((_req: unknown, opts?: { signal?: AbortSignal }) => {
      signals.push(opts?.signal);
      return new Promise<CustomValidatorPreview>(() => undefined);
    });
    const { unmount } = await mount({ previewCustomValidator } as unknown as Partial<RetndApi>);
    type(pattern(), "^a$");
    await waitFor(() => expect(signals).toHaveLength(1));
    unmount();
    expect(signals[0]?.aborted).toBe(true);
  });

  it("forgets a failed preview as soon as the pattern changes", async () => {
    let calls = 0;
    const previewCustomValidator = vi.fn(() => (++calls === 1 ? Promise.reject(new Error("engine down")) : new Promise<CustomValidatorPreview>(() => undefined)));
    await mount({ previewCustomValidator } as unknown as Partial<RetndApi>);
    type(pattern(), "^a$");
    const failure = () => document.querySelector("[data-validator-preview] [role=alert]");
    await waitFor(() => expect(failure()).not.toBeNull());
    type(pattern(), "^ab$");
    expect(failure()).toBeNull();
  });

  it("takes its backup sets from the shared sets node and does not list them again", async () => {
    const listSets = vi.fn(() => Promise.reject(new Error("the dialog must read setsNode")));
    const set = { id: "production/billing-mysql", name: "Billing MySQL" } as BackupSet;
    act(() => { graph.commit("test/seed-sets", (tx) => tx.set(setsNode, { data: [set], error: null, loading: false })); });
    await mount({ listSets } as unknown as Partial<RetndApi>);
    expect(listSets).not.toHaveBeenCalled();
    expect(await screen.findByRole("option", { name: /Billing MySQL/ })).toBeInTheDocument();
  });

  it("fetches the default script once however often it is asked for", async () => {
    const base = createMockApi("validators-none");
    const getDefaultValidatorScript = vi.fn(() => base.getDefaultValidatorScript());
    const first = await mount({ getDefaultValidatorScript } as Partial<RetndApi>);
    fireEvent.click(screen.getByRole("button", { name: "Start from the default script" }));
    await waitFor(() => expect((screen.getByLabelText("Script") as HTMLTextAreaElement).value).toContain("#!/usr/bin/env bash"));
    fireEvent.click(screen.getByRole("button", { name: "Start from the default script" }));
    expect(getDefaultValidatorScript).toHaveBeenCalledTimes(1);
    first.unmount();
    await mount({ getDefaultValidatorScript } as Partial<RetndApi>);
    fireEvent.click(screen.getByRole("button", { name: "Start from the default script" }));
    await waitFor(() => expect((screen.getByLabelText("Script") as HTMLTextAreaElement).value).toContain("#!/usr/bin/env bash"));
    expect(getDefaultValidatorScript).toHaveBeenCalledTimes(1);
  });

  it("offers the engine's own suggestion for the backup, until the operator types", async () => {
    const mode: ValidatorDialogMode = { kind: "add", backupSetId: "s/x", artifact: { id: "s/x/db-2026.sql", filename: "db-2026.sql" } };
    const listCustomValidatorPatterns = vi.fn(() =>
      Promise.resolve({ backupSetId: "s/x", patterns: [{ pattern: "^ENGINE$", example: "db-2026.sql", matchCount: 1, matchSets: ["s/x"], validator: "" }] })
    );
    await mount({ listCustomValidatorPatterns } as unknown as Partial<RetndApi>, mode);
    await waitFor(() => expect(pattern().value).toBe("^ENGINE$"));
    type(pattern(), "^mine$");
    expect(pattern().value).toBe("^mine$");
  });

  describe("the file picker", () => {
    const pick = (file: { name: string; size: number; arrayBuffer(): Promise<ArrayBuffer> }) => {
      const input = screen.getByLabelText("Script file") as HTMLInputElement;
      fireEvent.change(input, { target: { files: [file] } });
    };
    const buf = (...n: number[]) => new Uint8Array(n).buffer;

    it("refuses a file over the limit without reading it", async () => {
      await mount();
      const arrayBuffer = vi.fn(() => Promise.resolve(buf(65)));
      pick({ name: "big.sh", size: 300000, arrayBuffer });
      expect(await screen.findByText(/300000 bytes; this deployment takes at most 256 KiB/)).toBeInTheDocument();
      expect(arrayBuffer).not.toHaveBeenCalled();
      expect((screen.getByLabelText("Script") as HTMLTextAreaElement).value).toBe("");
    });

    it("refuses a file that is not UTF-8", async () => {
      await mount();
      pick({ name: "bin.sh", size: 2, arrayBuffer: () => Promise.resolve(buf(0xff, 0xfe)) });
      expect(await screen.findByText(/not valid UTF-8/)).toBeInTheDocument();
      expect((screen.getByLabelText("Script") as HTMLTextAreaElement).value).toBe("");
    });

    it("says so when the file cannot be read", async () => {
      await mount();
      pick({ name: "x.sh", size: 2, arrayBuffer: () => Promise.reject(new Error("denied")) });
      expect(await screen.findByText(/could not be read/)).toBeInTheDocument();
    });

    it("says when line endings were normalised", async () => {
      await mount();
      pick({ name: "w.sh", size: 4, arrayBuffer: () => Promise.resolve(buf(97, 13, 10, 98)) });
      expect(await screen.findByText(/line endings were converted/i)).toBeInTheDocument();
      expect((screen.getByLabelText("Script") as HTMLTextAreaElement).value).toBe("a\nb");
    });
  });
});

// The contract #1213 settled on: an edit is a whole replacement.
describe("CustomValidatorDialog editing (#1213 contract)", () => {
  beforeEach(() => { resetGraphForTests(); });

  const current = {
    name: "billing-dump", pattern: "^b$", backupSetId: "production/billing-mysql", scope: "backup_set" as const, anchored: true, env: "minimal" as const,
    scriptFile: "billing-dump.local.sh", scriptPresent: true, scriptSha256: "feedbeef", pinnedSha256: "feedbeef", scriptState: "pinned" as const, scriptSizeBytes: 3, uploadedAt: "",
    matchCount: 0, matchSets: [], exampleName: ""
  };

  async function save(update: ReturnType<typeof vi.fn>, typedScript?: string) {
    const onSaved = vi.fn();
    const base = createMockApi("validators-none");
    const api = { ...base, updateCustomValidator: update } as unknown as RetndApi;
    const list = await base.listCustomValidators();
    render(
      <ApiProvider api={api}>
        <CustomValidatorDialog mode={{ kind: "edit", validator: current }} list={list} onClose={() => undefined} onSaved={onSaved} />
      </ApiProvider>
    );
    if (typedScript !== undefined) type(screen.getByLabelText("Script"), typedScript);
    const button = screen.getByRole("button", { name: "Save changes" });
    await waitFor(() => expect(button).toBeEnabled());
    fireEvent.click(button);
    return onSaved;
  }

  it("keeps the script by the hash it read when the script box is empty, and says its scope", async () => {
    const update = vi.fn(() => Promise.resolve(current));
    await save(update);
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(
      "billing-dump", { pattern: "^b$", backupSetId: "production/billing-mysql", keepScriptSha256: "feedbeef", env: "minimal" }, {}
    );
  });

  it("sends the script and not the hash when a new one was typed", async () => {
    const update = vi.fn(() => Promise.resolve(current));
    await save(update, "#!/bin/sh\nexit 0\n");
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(
      "billing-dump", { pattern: "^b$", backupSetId: "production/billing-mysql", script: "#!/bin/sh\nexit 0\n", env: "minimal" }, {}
    );
  });

  it("explains a script that changed under it, and does not report a save", async () => {
    const update = vi.fn(() => Promise.reject(new RetndError({ code: "VALIDATOR_SCRIPT_CHANGED" as never, message: "the stored script is not the one you read", correlationId: "c" })));
    const onSaved = await save(update);
    const alert = await screen.findByText(/script on the host changed/i);
    expect(alert).toBeInTheDocument();
    expect(alert.textContent).toMatch(/nothing was saved/i);
    expect(onSaved).not.toHaveBeenCalled();
  });
});
