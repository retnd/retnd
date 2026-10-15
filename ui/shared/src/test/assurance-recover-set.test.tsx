/**
 * The Recover this backup set dialog's logic (#1106), driven against a stub of
 * the two calls. The layout, theme and the whole flow in a real browser are the
 * Playwright spec's job; this holds what travels in the request and which
 * choices turn the action on.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RecoverManyPlan, RecoverManyResult, RetndApi } from "@shared/api/contracts";
import { RecoverSetDialog } from "@shared/assurance/RecoverSet";
import { resetGraphForTests } from "@shared/state/graph";

const SET = "production/postgres-primary";
const totals = (o: object = {}) => ({ count: 2, ready: 2, recovered: 0, failed: 0, skipped: 0, needsThaw: 0, quarantined: 0, unrecoverable: 0, bytes: 2000, ...o });
const plan = (o: Partial<RecoverManyPlan> = {}): RecoverManyPlan => ({
  set: SET, target: { kind: "staging", path: "x" }, folder: "x/" + SET, totals: totals({ count: 3, quarantined: 1 }),
  items: [
    { artifactId: SET + "/a", name: "a", status: "ready", sizeBytes: 1000 },
    { artifactId: SET + "/b", name: "b", status: "ready", sizeBytes: 1000 },
    { artifactId: SET + "/q", name: "q", status: "quarantined", message: "checksum mismatch" }
  ],
  chargeableCount: 0, chargeableBytes: 0, unknownSizes: 0, reserveBytes: 0, ...o
});
const result: RecoverManyResult = {
  operationId: "op", target: { kind: "staging", path: "x" }, folder: "x", cancelled: false, manifestPath: "/s/m.json",
  items: [{ artifactId: SET + "/a", name: "a", status: "recovered", bytes: 1000 }], totals: totals({ recovered: 1, count: 1, ready: 0, bytes: 1000 })
};

function renderDialog(over: Partial<RetndApi>, engine = "artifact") {
  resetGraphForTests();
  const api = { ...createMockApi("default"), ...over } as RetndApi;
  render(<ApiProvider api={api}><RecoverSetDialog setId={SET} engine={engine} onClose={() => undefined} /></ApiProvider>);
  return api;
}
afterEach(() => cleanup());
// #1191: the dialog starts on the set's source host, so a staging run is chosen.
const toStaging = async () => userEvent.click(await screen.findByRole("radio", { name: "Folder under the staging root on the host" }));

describe("Recover this backup set", () => {
  it("shows the pre-flight before anything is chosen to run, listing what is not included", async () => {
    renderDialog({ planRecoverMany: vi.fn().mockResolvedValue(plan()) });
    const pre = await screen.findByTestId("preflight-summary");
    expect(pre.textContent).toContain("3 backups: 2 to recover");
    expect(pre.textContent).toContain("1 quarantined");
    expect(screen.getByRole("list", { name: "Backups in this recovery" }).textContent).toContain("checksum mismatch");
    expect((screen.getByRole("button", { name: "Recover this backup set" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("sends the choices as the request, once a destination is named", async () => {
    const recover = vi.fn().mockResolvedValue(result);
    const planFn = vi.fn().mockResolvedValue(plan());
    renderDialog({ planRecoverMany: planFn, recoverMany: recover });
    await screen.findByTestId("preflight-summary");
    await userEvent.click(screen.getByRole("radio", { name: "The newest restore point only" }));
    await toStaging();

    await userEvent.type(screen.getByRole("textbox", { name: /staging root/ }), "x");
    await userEvent.click(screen.getByRole("button", { name: "Recover this backup set" }));
    await waitFor(() => expect(recover).toHaveBeenCalled());
    expect(recover.mock.calls[0][0]).toEqual({ set: SET, newestOnly: true, target: { kind: "staging", path: "x" } });
    expect(await screen.findByTestId("result-counts")).toBeTruthy();
    expect(screen.getByTestId("result-manifest").textContent).toContain("/s/m.json");
  });

  it("a billed set stays off until the one acknowledgement is ticked, and the request then carries it", async () => {
    const recover = vi.fn().mockResolvedValue(result);
    const planFn = vi.fn().mockImplementation(async (r) =>
      plan({ chargeableCount: 2, chargeableBytes: 2000, ...(r.acknowledged ? {} : { refusal: { code: "RECOVERY_COST_NOT_ACKNOWLEDGED", message: "billed" } }) }));
    renderDialog({ planRecoverMany: planFn, recoverMany: recover });
    await toStaging();

    await userEvent.type(await screen.findByRole("textbox", { name: /staging root/ }), "x");
    const go = screen.getByRole("button", { name: "Recover this backup set" }) as HTMLButtonElement;
    await screen.findByTestId("set-cost");
    expect(go.disabled).toBe(true);
    expect(screen.getByRole("dialog").textContent).not.toContain("--acknowledge-cost");
    await userEvent.click(screen.getByRole("checkbox", { name: /I accept the cost/ }));
    expect(screen.getByRole("dialog").textContent).toContain("--acknowledge-cost");
    await waitFor(() => expect(go.disabled).toBe(false));
    await userEvent.click(go);
    await waitFor(() => expect(recover).toHaveBeenCalled());
    expect(recover.mock.calls[0][0].acknowledged).toBe(true);
  });

  it("stays off while the host would refuse the run for room, and shows its sentence with the numbers", async () => {
    renderDialog({ planRecoverMany: vi.fn().mockResolvedValue(plan({ refusal: { code: "RECOVERY_NO_SPACE", message: "the files need 2000 bytes and the staging disk has 10 bytes free" } })) });
    await toStaging();

    await userEvent.type(await screen.findByRole("textbox", { name: /staging root/ }), "x");
    const alert = await screen.findByTestId("preflight-refusal");
    expect(alert.textContent).toContain("2000 bytes");
    expect((screen.getByRole("button", { name: "Recover this backup set" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("offers no browser download, and says why", async () => {
    renderDialog({ planRecoverMany: vi.fn().mockResolvedValue(plan()) });
    await screen.findByTestId("preflight-summary");
    expect(screen.queryByRole("radio", { name: /browser/i })).toBeNull();
    expect(screen.getByTestId("no-browser-download").textContent).toMatch(/no browser download/);
  });

  it("a set on the incremental engine is a snapshot restore, with the command, and cannot be run from here", async () => {
    const planFn = vi.fn();
    renderDialog({ planRecoverMany: planFn }, "kopia");
    expect(screen.getByTestId("incremental-engine-set").textContent).toContain("snapshot restore");
    expect(screen.getByRole("dialog").textContent).toContain("retnd snapshot restore " + SET);
    expect(screen.queryByRole("button", { name: "Recover this backup set" })).toBeNull();
    expect(planFn).not.toHaveBeenCalled();
  });

  it("a refusal when the run starts is shown and nothing is called recovered", async () => {
    const recover = vi.fn().mockRejectedValue(Object.assign(new Error("no"), { api: { code: "RECOVERY_NO_SPACE", message: "no room" } }));
    renderDialog({ planRecoverMany: vi.fn().mockResolvedValue(plan()), recoverMany: recover });
    await toStaging();

    await userEvent.type(await screen.findByRole("textbox", { name: /staging root/ }), "x");
    await waitFor(() => expect((screen.getByRole("button", { name: "Recover this backup set" }) as HTMLButtonElement).disabled).toBe(false));
    await userEvent.click(screen.getByRole("button", { name: "Recover this backup set" }));
    expect(await screen.findByTestId("refusal")).toBeTruthy();
    expect(screen.queryByTestId("result")).toBeNull();
  });
});
