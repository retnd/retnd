/**
 * EPIC O (#898, #899): the Settings section shell. It states what the engine
 * reports, as facts and not controls, and it says nothing is per backup set.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";
import { ObservabilityCard } from "@shared/observability/ObservabilityCard";

afterEach(cleanup);

const show = (api: RetndApi) =>
  render(
    <ApiProvider api={api}>
      <ObservabilityCard readOnly={false} />
    </ApiProvider>
  );

describe("the Observability card on Settings", () => {
  it("reports the defaults of a deployment that never wrote the block: everything off", async () => {
    show(createMockApi());
    const summary = await screen.findByTestId("observability-summary");
    expect(within(summary).getByText("off, no credential minted")).toBeTruthy();
    expect(within(summary).getByText("info (from config)")).toBeTruthy();
    expect(within(summary).getByText("platform")).toBeTruthy();
  });

  it("follows the engine: an enabled endpoint with a minted credential reads as such", async () => {
    const api = createMockApi();
    await api.patchObservabilitySettings({ metricsEnabled: true, logsLevel: "warn" });
    await api.mintScrapeToken();
    show(api);
    const summary = await screen.findByTestId("observability-summary");
    expect(within(summary).getByText("on, credential minted")).toBeTruthy();
    expect(within(summary).getByText("warn (from config)")).toBeTruthy();
  });

  it("says so when the engine has no configuration yet, rather than showing defaults as fact", async () => {
    const api: RetndApi = {
      ...createMockApi(),
      getObservabilitySettings: () => Promise.reject(Object.assign(new Error("not configured"), { status: 503, code: "NOT_CONFIGURED" }))
    };
    show(api);
    await waitFor(() => expect(screen.queryByTestId("observability-summary")).toBeNull());
  });

  it("offers no control: nothing in the shell is editable, so nothing in it needs a tooltip", async () => {
    show(createMockApi());
    await screen.findByTestId("observability-summary");
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(screen.queryAllByRole("textbox")).toHaveLength(0);
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
  });
});
