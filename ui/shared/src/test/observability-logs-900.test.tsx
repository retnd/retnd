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
import * as observabilityCommands from "@shared/observability/observabilityCommands";

// Read through the namespace so a missing export is a red assertion rather
// than a module that fails to load.
const commands = observabilityCommands as unknown as {
  LOG_LEVELS?: readonly string[];
  logsLevelCommand?: (level: string) => string;
};

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
  });

  // The echo is the terminal line, from the twin module, beside the control.
  // A line written by hand into the tooltip is one no parser reads, so the
  // tooltip points at the echo instead of carrying its own copy.
  it("leaves the terminal line to the echo, not the tooltip", () => {
    expect((tooltips as Record<string, { html: string }>)["observability.logs.level"].html).not.toContain("retnd settings");
  });

  it("echoes the terminal line for the saved level, and follows a change", async () => {
    show(createMockApi());
    const control = await screen.findByTestId("logs-level-control");
    await waitFor(() =>
      expect(control.textContent).toContain("retnd settings observability patch --logs-level info")
    );
    fireEvent.change(screen.getByLabelText("Log level"), { target: { value: "error" } });
    await waitFor(() =>
      expect(screen.getByTestId("logs-level-control").textContent).toContain("retnd settings observability patch --logs-level error")
    );
  });
});

describe("the log level's twin (#900)", () => {
  it("is built by logsLevelCommand, the same line core/cliecho prints for PATCH {logs_level}", () => {
    expect(typeof commands.logsLevelCommand).toBe("function");
    for (const level of ["debug", "info", "warn", "error"]) {
      expect(commands.logsLevelCommand?.(level)).toBe("retnd settings observability patch --logs-level " + level);
    }
  });

  it("quotes a value a shell would split, so a widened type cannot print a second command", () => {
    expect(commands.logsLevelCommand?.("warn; id")).toBe("retnd settings observability patch --logs-level 'warn; id'");
  });

  it("offers exactly the levels the twin module lists, so the select and the pinned twins cannot part", async () => {
    expect(commands.LOG_LEVELS).toEqual(["debug", "info", "warn", "error"]);
    show(createMockApi());
    const select = (await screen.findByLabelText("Log level")) as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual(commands.LOG_LEVELS);
  });
});
