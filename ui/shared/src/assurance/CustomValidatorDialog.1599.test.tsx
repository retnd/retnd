/**
 * Issue #1599: the validator dialog's save awaits the upload and, when it's
 * refused, sets the error, the refusal and submitting. A refusal that lands
 * after the dialog has gone (the page left, a test ended) is the #1589 shape.
 *
 * Each case holds the upload, unmounts, tears down and only then answers; see
 * test/teardownProbe.ts.
 */
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { CustomValidator, RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { resetGraphForTests } from "@shared/state/graph";
import { held, teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";
import { CustomValidatorDialog } from "./CustomValidatorDialog";

let probe: TeardownProbe;
beforeEach(() => { resetGraphForTests(); probe = teardownProbe(); });
afterEach(() => {
  probe.dispose();
  cleanup();
});

async function pressUpload(create: () => Promise<CustomValidator>) {
  const base = createMockApi("validators-none");
  const call = vi.fn(create);
  const api = { ...base, createCustomValidator: call } as unknown as RetndApi;
  const list = await base.listCustomValidators();
  const view = render(
    <ApiProvider api={api}>
      <CustomValidatorDialog mode={{ kind: "add" }} list={list} onClose={() => undefined} onSaved={() => undefined} />
    </ApiProvider>
  );
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "fresh" } });
  fireEvent.change(screen.getByLabelText("Artifact-name pattern"), { target: { value: "^f" } });
  fireEvent.change(screen.getByLabelText("Script"), { target: { value: "#!/bin/sh\nexit 0\n" } });
  const button = screen.getByRole("button", { name: "Upload validator" });
  await waitFor(() => expect(button).toBeEnabled());
  fireEvent.click(button);
  await waitFor(() => expect(call).toHaveBeenCalled());
  return view;
}

describe("CustomValidatorDialog after unmount (#1599)", () => {
  it("drops a refusal that lands after the dialog has gone", async () => {
    const answer = held<CustomValidator>();
    const { unmount } = await pressUpload(() => answer.promise);
    unmount();
    probe.tearDown();
    answer.reject(new Error("the engine went away"));
    await probe.settle();
    expect(probe.rejections).toEqual([]);
    expect(probe.stateUpdates).toBe(0);
  });

  it("still shows a refusal while it's mounted", async () => {
    const answer = held<CustomValidator>();
    await pressUpload(() => answer.promise);
    answer.reject(new Error("the engine went away"));
    await waitFor(() => expect(screen.getByRole("button", { name: "Upload validator" })).toBeEnabled());
    expect(document.querySelector("[data-validator-error]")).not.toBeNull();
  });
});
