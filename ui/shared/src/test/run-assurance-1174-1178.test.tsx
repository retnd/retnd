/**
 * Issues #1174 and #1178: the Run assurance and Run validation buttons share
 * one hook (useRunAssurance). The browser specs hold the happy path; these pin
 * what a browser cannot reach through the mock: a build whose engine refuses
 * the start with the destructive gate's code, one that refuses it for another
 * reason, and a server that offers no start route at all.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { RetndError } from "@shared/api/contracts";
import type { RetndApi } from "@shared/api/contracts";
import { createMockApi } from "@shared/api/mock";
import { ValidationBlock } from "@shared/assurance/ValidationBlock";
import { recoveryTestWouldBeRefused } from "@shared/assurance/verificationModel";
import { resetGraphForTests } from "@shared/state/graph";
import type { BackupSet } from "@shared/types/backup";

const SET = "production/postgres-primary";
const set = { id: SET, source: "production", set: "postgres-primary", validations: [] } as unknown as BackupSet;

function render1178(start: () => Promise<unknown>, offers?: Partial<NonNullable<RetndApi["assurance"]>["offers"]>) {
  const base = createMockApi("default");
  const assurance = base.assurance!;
  const api = {
    ...base,
    assurance: { ...assurance, offers: { ...assurance.offers, ...offers }, startRecoveryTest: start }
  } as unknown as RetndApi;
  render(
    <MemoryRouter>
      <ApiProvider api={api}><ValidationBlock set={set} /></ApiProvider>
    </MemoryRouter>
  );
}

describe("#1178 Run validation on the Validation pane", () => {
  beforeEach(() => resetGraphForTests());
  afterEach(() => cleanup());

  it("a gate refusal is said as one, with the host command, and the button stays usable", async () => {
    const start = vi.fn().mockRejectedValue(new RetndError({ code: "DESTRUCTIVE_OPERATIONS_DISABLED" as never, message: "refused", status: 403, correlationId: "cid_x" }));
    render1178(start);
    const button = await screen.findByRole("button", { name: "Run validation" });
    await userEvent.click(button);
    const refusal = await waitFor(() => {
      const el = document.querySelector("[data-gate-refusal]");
      expect(el).toBeTruthy();
      return el!;
    });
    expect(refusal.textContent).toContain("The recovery test was not started");
    expect(refusal.textContent).toContain("retnd recovery-test " + SET);
    expect(refusal.textContent).toContain("cid_x");
    expect((button as HTMLButtonElement).disabled).toBe(false);
    expect(start).toHaveBeenCalledTimes(1);
  });

  it("any other refusal is the service's own words, not a gate notice", async () => {
    const start = vi.fn().mockRejectedValue(new RetndError({ code: "BACKUP_SET_NOT_FOUND" as never, message: "no such set", status: 404, correlationId: "cid_y" }));
    render1178(start);
    await userEvent.click(await screen.findByRole("button", { name: "Run validation" }));
    expect((await screen.findByRole("alert")).textContent).toContain("no such set");
    expect(document.querySelector("[data-gate-refusal]")).toBeNull();
  });

  it("a server with no start route gets no button, only the command", async () => {
    render1178(vi.fn(), { recoveryTest: false });
    await screen.findByTestId("validation-block");
    expect(screen.queryByRole("button", { name: "Run validation" })).toBeNull();
  });
});

describe("#1174 recoveryTestWouldBeRefused", () => {
  const p = (over: object) => ({ setId: SET, source: "default" as const, mode: "off", ...over });
  it("follows the engine's effective_check, then validator_declared, and treats unknown as not refused", () => {
    expect(recoveryTestWouldBeRefused(p({ effectiveCheck: "none" }))).toBe(true);
    expect(recoveryTestWouldBeRefused(p({ effectiveCheck: "builtin", validatorDeclared: false }))).toBe(false);
    expect(recoveryTestWouldBeRefused(p({ effectiveCheck: "validator" }))).toBe(false);
    expect(recoveryTestWouldBeRefused(p({ validatorDeclared: false }))).toBe(true);
    expect(recoveryTestWouldBeRefused(p({ validatorDeclared: true }))).toBe(false);
    expect(recoveryTestWouldBeRefused(p({}))).toBe(false);
    expect(recoveryTestWouldBeRefused(null)).toBe(false);
    expect(recoveryTestWouldBeRefused(undefined)).toBe(false);
  });
});
