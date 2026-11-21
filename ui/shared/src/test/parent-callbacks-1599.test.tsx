/**
 * Issue #1599, found in review of the last batch: a dialog calls its
 * parent's `onSaved` or `onDone` after an await, and three of the parents'
 * handlers set their own state with nothing checking they're still there.
 * The throw lands inside the dialog's try block, so the dialog's catch hides
 * it today and the next edit to that block would turn it into the #1589
 * failure.
 *
 * Each case stubs the child dialog to capture the callback the parent gave
 * it, unmounts the parent, tears down, and calls the callback the way the
 * dialog would after its request answers; see test/teardownProbe.ts.
 */
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiProvider } from "@shared/api/ApiContext";
import type { RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { DeclareValidator } from "@shared/assurance/DeclareValidator";
import { SuiteChangeFlow } from "@shared/encryption/SuiteControls";
import { TierDestinationActions } from "@shared/pages/retentionChain";
import { teardownProbe } from "@shared/test/teardownProbe";
import type { TeardownProbe } from "@shared/test/teardownProbe";

const captured: { validator?: (s: unknown) => void; bundle?: (r: unknown) => void; s3?: (c: unknown) => void } = {};

vi.mock("@shared/assurance/CustomValidatorDialog", () => ({
  CustomValidatorDialog: (p: { onSaved(s: unknown): void }) => { captured.validator = p.onSaved; return <div data-testid="stub-validator" />; }
}));
vi.mock("@shared/encryption/BundleDialog", () => ({
  BundleDialog: (p: { onDone(r: unknown): void }) => { captured.bundle = p.onDone; return <div data-testid="stub-bundle" />; }
}));
vi.mock("@shared/pages/S3DestinationWizard", () => ({
  S3DestinationWizard: (p: { onSaved(c: unknown): void }) => { captured.s3 = p.onSaved; return <div data-testid="stub-s3" />; }
}));

let probe: TeardownProbe;
beforeEach(() => { probe = teardownProbe(); });
afterEach(() => { probe.dispose(); cleanup(); vi.restoreAllMocks(); });

const apiOf = (): RetndApi => createMockApi("default");

describe("parent handlers handed to a dialog, called after the parent has gone (#1599)", () => {
  it("DeclareValidator's onSaved sets nothing", async () => {
    const view = render(<ApiProvider api={apiOf()}><DeclareValidator setId="production/postgres-primary" readOnly={false} /></ApiProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Declare validator" }));
    await screen.findByTestId("stub-validator");
    view.unmount();
    probe.tearDown();
    expect(() => captured.validator!({ validator: { name: "v", pattern: "*", backupSetId: "" }, edited: false })).not.toThrow();
    await probe.settle();
    expect(probe.stateUpdates).toBe(0);
  });

  it("SuiteChangeFlow's bundle onDone sets nothing", async () => {
    const onChanged = vi.fn();
    const view = render(
      <ApiProvider api={apiOf()}><SuiteChangeFlow suite="aes-256-gcm" hasActiveKey={false} start="rotate" onChanged={onChanged} onClose={() => undefined} /></ApiProvider>
    );
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Rotate key" })); });
    await screen.findByTestId("stub-bundle");
    view.unmount();
    probe.tearDown();
    expect(() => captured.bundle!({ keyId: "k", fileName: "f", digest: "d" })).not.toThrow();
    await probe.settle();
    expect(probe.stateUpdates).toBe(0);
  });

  it("a tier's add-destination onSaved sets nothing", async () => {
    const tier = { medium: "m1" } as never;
    const view = render(
      <ApiProvider api={apiOf()}><TierDestinationActions tier={tier} readOnly={false} onDestinationsChanged={() => undefined} onPick={() => undefined} /></ApiProvider>
    );
    fireEvent.click(await screen.findByRole("button", { name: /add .*destination/i }));
    await waitFor(() => expect(captured.s3).toBeDefined());
    view.unmount();
    probe.tearDown();
    expect(() => captured.s3!({ id: "d1" })).not.toThrow();
    await probe.settle();
    expect(probe.stateUpdates).toBe(0);
  });
});
