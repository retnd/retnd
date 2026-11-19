/**
 * EPIC O (#898, #899): the Observability section of Settings. The metrics
 * sub-section is the first real one: its switch, its report cache and the
 * scrape credential's one-time reveal.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";
import { ObservabilityCard } from "@shared/observability/ObservabilityCard";
import { metricsEnabledCommand, metricsIntervalCommand, mintScrapeTokenCommand } from "@shared/observability/observabilityCommands";

afterEach(cleanup);

const show = (api: RetndApi, readOnly = false) =>
  render(
    <ApiProvider api={api}>
      <ObservabilityCard readOnly={readOnly} />
    </ApiProvider>
  );

describe("the Observability card on Settings", () => {
  it("reports the defaults of a deployment that never wrote the block: everything off, no credential", async () => {
    show(createMockApi());
    const box = await screen.findByLabelText("Serve the scrape endpoint");
    expect((box as HTMLInputElement).checked).toBe(false);
    expect(screen.getByTestId("scrape-token-state").textContent).toMatch(/No scrape token has been minted/);
    const summary = screen.getByTestId("observability-summary");
    expect(within(summary).getByText("info (from config)")).toBeTruthy();
    expect(within(summary).getByText("platform")).toBeTruthy();
  });

  it("turns the endpoint on through the engine and reports what the engine now says", async () => {
    const api = createMockApi();
    show(api);
    const box = await screen.findByLabelText("Serve the scrape endpoint");
    await userEvent.click(box);
    await waitFor(() => expect((screen.getByLabelText("Serve the scrape endpoint") as HTMLInputElement).checked).toBe(true));
    expect((await api.getObservabilitySettings()).metrics.enabled).toBe(true);
  });

  it("refuses a report cache below one second before anything is sent, and saves a good one", async () => {
    const api = createMockApi();
    show(api);
    const field = await screen.findByLabelText("Report cache (seconds)");
    await userEvent.clear(field);
    await userEvent.type(field, "0");
    expect(screen.getByRole("alert").textContent).toMatch(/at least 1/);
    expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
    await userEvent.clear(field);
    await userEvent.type(field, "45");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(async () => expect((await api.getObservabilitySettings()).metrics.minRebuildIntervalSeconds).toBe(45));
  });

  it("shows a minted token once, and nowhere after the operator says it is stored", async () => {
    const api = createMockApi();
    show(api);
    await userEvent.click(await screen.findByRole("button", { name: "Mint scrape token" }));
    const field = (await screen.findByLabelText("Your scrape token")) as HTMLTextAreaElement;
    expect(field.value).toMatch(/^rsc_/);
    const token = field.value;
    await waitFor(() => expect(screen.getByTestId("scrape-token-state").textContent).toContain(token.slice(0, 8)));
    expect(screen.getByTestId("scrape-token-state").textContent).not.toContain(token);

    await userEvent.click(screen.getByRole("button", { name: "I have stored it" }));
    expect(screen.queryByLabelText("Your scrape token")).toBeNull();
    expect(document.body.textContent).not.toContain(token);
    expect(screen.getByRole("button", { name: "Mint a new scrape token" })).toBeTruthy();
  });

  it("disables every control when the page is read only", async () => {
    show(createMockApi(), true);
    expect((await screen.findByLabelText("Serve the scrape endpoint") as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Mint scrape token" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("says so when the engine has no configuration yet, rather than showing defaults as fact", async () => {
    const api: RetndApi = {
      ...createMockApi(),
      getObservabilitySettings: () => Promise.reject(Object.assign(new Error("not configured"), { status: 503, code: "NOT_CONFIGURED" }))
    };
    show(api);
    await waitFor(() => expect(screen.queryByLabelText("Serve the scrape endpoint")).toBeNull());
  });
});

describe("the commands the card echoes are the ones the terminal prints", () => {
  // The strings core/cliecho builds for the same requests (routes.go, held by
  // the golden core/cliecho/testdata/cli_outcomes.golden).
  it("matches cliecho for the switch, the interval and the mint", () => {
    expect(metricsEnabledCommand(true)).toBe("retnd settings observability patch --metrics-enabled=true");
    expect(metricsEnabledCommand(false)).toBe("retnd settings observability patch --metrics-enabled=false");
    expect(metricsIntervalCommand(30)).toBe("retnd settings observability patch --metrics-min-rebuild-interval 30s");
    expect(metricsIntervalCommand(90)).toBe("retnd settings observability patch --metrics-min-rebuild-interval 1m30s");
    expect(mintScrapeTokenCommand()).toBe("retnd settings observability rotate-scrape-token");
  });
});
