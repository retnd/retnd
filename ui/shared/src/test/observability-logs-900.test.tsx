/**
 * EPIC O, FR-100 (#900): the log level control in the Observability card.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";
import { ObservabilityCard } from "@shared/observability/ObservabilityCard";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import tooltips from "@shared/tooltips/tooltips.json";

afterEach(cleanup);

const show = (api: RetndApi, readOnly = false) =>
  render(
    <ApiProvider api={api}>
      <ObservabilityCard readOnly={readOnly} />
    </ApiProvider>
  );

describe("the log level control (#900)", () => {
  it("offers the four levels and shows the saved one", async () => {
    show(createMockApi());
    const select = (await screen.findByLabelText("Log level")) as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual(["debug", "info", "warn", "error"]);
    expect(select.value).toBe("info");
  });

  it("saves through the settings route", async () => {
    const api = createMockApi();
    show(api);
    const select = await screen.findByLabelText("Log level");
    fireEvent.change(select, { target: { value: "warn" } });
    await waitFor(async () => expect((await api.getObservabilitySettings()).logs.level).toBe("warn"));
  });

  it("says when the environment is overriding the saved level", async () => {
    const api = createMockApi();
    const base = await api.getObservabilitySettings();
    show({
      ...api,
      getObservabilitySettings: () =>
        Promise.resolve({ ...base, logs: { level: "warn", levelInForce: "debug", levelSource: "environment" } })
    });
    const note = await screen.findByTestId("logs-level-in-force");
    expect(note.textContent).toContain("In force: debug (from environment)");
    expect(note.textContent).toContain("overriding");
  });

  it("is a read-only statement when the viewer cannot write", async () => {
    show(createMockApi(), true);
    expect(((await screen.findByLabelText("Log level")) as HTMLSelectElement).disabled).toBe(true);
  });

  it("explains itself in three parts and names the engine-only scope", () => {
    const h = FIELD_HELP.observabilityLogsLevel;
    expect(h.what).toBeTruthy();
    expect(h.example).toBe("warn");
    expect(h.effect).toContain("web-ui");
    expect(h.effect).toContain("RETND_DEBUG");
    expect(Object.keys(tooltips)).toContain("observability.logs.level");
    expect((tooltips as Record<string, { html: string }>)["observability.logs.level"].html).toContain("--logs-level");
  });
});
