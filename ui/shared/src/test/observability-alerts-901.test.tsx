/**
 * EPIC O (#901, FR-101): the alert delivery controls in Settings. Selection,
 * not fan-out; the webhook URL is never a field and is never shown; a
 * refusal from the engine is shown where the change was made.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { RetndError, type RetndApi } from "@shared/api/contracts";
import { ObservabilityCard } from "@shared/observability/ObservabilityCard";

afterEach(cleanup);

const show = (api: RetndApi, readOnly = false) =>
  render(
    <ApiProvider api={api}>
      <ObservabilityCard readOnly={readOnly} />
    </ApiProvider>
  );

describe("the alert delivery controls", () => {
  it("offers one delivery select, set to platform, and shows no webhook fields until webhook is chosen", async () => {
    show(createMockApi());
    const select = (await screen.findByLabelText("Alert delivery")) as HTMLSelectElement;
    expect(select.value).toBe("platform");
    expect(screen.queryByLabelText("Webhook format")).toBeNull();
    expect(screen.queryByLabelText("Plain http")).toBeNull();
  });

  it("switches to webhook, saves it, and then shows the format and the http choice", async () => {
    const api = createMockApi();
    show(api);
    await userEvent.selectOptions(await screen.findByLabelText("Alert delivery"), "webhook");
    await screen.findByLabelText("Webhook format");
    expect(screen.getByLabelText("Plain http")).toBeTruthy();
    expect((await api.getObservabilitySettings()).alerts.delivery).toBe("webhook");
  });

  it("shows the refresh and expiry fields for the alertmanager format only, and saves a number on blur", async () => {
    const api = createMockApi();
    await api.patchObservabilitySettings({ alertsDelivery: "webhook" });
    show(api);
    expect(screen.queryByLabelText("Refresh every (seconds)")).toBeNull();
    await userEvent.selectOptions(await screen.findByLabelText("Webhook format"), "alertmanager");
    const refresh = (await screen.findByLabelText("Refresh every (seconds)")) as HTMLInputElement;
    await userEvent.clear(refresh);
    await userEvent.type(refresh, "120");
    await userEvent.tab();
    await waitFor(async () => expect((await api.getObservabilitySettings()).alerts.webhook.refreshIntervalSeconds).toBe(120));
  });

  it("says whether a webhook URL is set and never shows one", async () => {
    const api = createMockApi();
    await api.patchObservabilitySettings({ alertsDelivery: "webhook" });
    show(api);
    const state = await screen.findByTestId("alert-webhook-url-state");
    expect(state.textContent).toContain("No webhook URL is set");
    expect(within(screen.getByTestId("alert-delivery")).queryAllByRole("textbox")).toHaveLength(0);
  });

  it("shows the engine's refusal next to the control that caused it", async () => {
    const api: RetndApi = {
      ...createMockApi(),
      patchObservabilitySettings: () =>
        Promise.reject(new RetndError({ code: "INVALID_REQUEST", message: "observability.alerts.webhook.url_ref: say where the URL is kept" }))
    };
    show(api);
    await userEvent.selectOptions(await screen.findByLabelText("Alert delivery"), "webhook");
    const alert = await screen.findByTestId("alert-delivery-error");
    expect(alert.textContent).toContain("url_ref");
  });

  it("is read-only when the session is", async () => {
    show(createMockApi(), true);
    expect(((await screen.findByLabelText("Alert delivery")) as HTMLSelectElement).disabled).toBe(true);
  });

  it("says loudly when the webhook URL cannot be read, and not when it can", async () => {
    const base = createMockApi();
    const withStatus = (urlStatus: "ok" | "unresolvable" | "invalid"): RetndApi => ({
      ...base,
      getObservabilitySettings: async () => {
        const s = await base.getObservabilitySettings();
        return { ...s, alerts: { ...s.alerts, delivery: "webhook", webhook: { ...s.alerts.webhook, urlSet: true, urlStatus } } };
      }
    });
    show(withStatus("unresolvable"));
    const alert = await screen.findByTestId("alert-webhook-unresolvable");
    expect(alert.getAttribute("role")).toBe("alert");
    expect(alert.textContent).toContain("cannot be read");
    cleanup();
    show(withStatus("ok"));
    await screen.findByTestId("alert-webhook-url-state");
    expect(screen.queryByTestId("alert-webhook-unresolvable")).toBeNull();
    expect(screen.queryByTestId("alert-webhook-invalid")).toBeNull();
    cleanup();
    show(withStatus("invalid"));
    const invalid = await screen.findByTestId("alert-webhook-invalid");
    expect(invalid.getAttribute("role")).toBe("alert");
    expect(invalid.textContent).toContain("not an acceptable URL");
    expect(screen.queryByTestId("alert-webhook-unresolvable")).toBeNull();
  });

  it("reads url_status off the wire, and leaves it unset when the engine sent none", async () => {
    const { fromWireObservabilitySettings } = await import("@shared/api/client");
    const wire = {
      metrics: { enabled: false, min_rebuild_interval_seconds: 15, scrape_token: { exists: false } },
      logs: { level: "info", level_in_force: "info", level_source: "config" },
      alerts: { delivery: "webhook", webhook: { dialect: "generic", url_set: true, allow_insecure_http: false, refresh_interval_seconds: 240, resolve_ttl_seconds: 600 } }
    };
    expect(fromWireObservabilitySettings(wire as never).alerts.webhook.urlStatus).toBeUndefined();
    const withStatus = { ...wire, alerts: { ...wire.alerts, webhook: { ...wire.alerts.webhook, url_status: "unresolvable" } } };
    expect(fromWireObservabilitySettings(withStatus as never).alerts.webhook.urlStatus).toBe("unresolvable");
  });
});
