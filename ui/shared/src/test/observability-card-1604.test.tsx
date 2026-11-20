/**
 * #1604 (and #1591): the Observability card explains every control EPIC O
 * gave it, says nothing the code contradicts, and shows the log level once.
 *
 * Matrix row O-19 asks three things of the tooltip suite, and this file holds
 * the card to the card-shaped half of them, with the preference explicitly
 * ON so a pass can't be the preference doing the work:
 *
 *   - every control's copy comes from the registry. Each id's copy is swapped
 *     for a sentinel before the card renders, and the control has to show the
 *     sentinel. A control carrying a literal copy of its own text passes a
 *     plain "the pop-up says what the registry says" check, because the two
 *     strings are equal, and fails this one.
 *   - every input has its three-part field help, wired to that input.
 *   - turning tooltips off silences the card's pop-ups, both kinds.
 *
 * The registry-wide halves (no orphan entry, the tag allowlist) are
 * registry-tooltips.test.tsx's, swept over every entry.
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
// The real stylesheet: a pop-up's hidden state is `.tooltip__pop[hidden]`,
// and against no sheet a pop-up that never closes still reads as hidden.
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";
import { FIELD_HELP, type FieldHelpCopy } from "@shared/components/fieldHelpCopy";
import { ObservabilityCard } from "@shared/observability/ObservabilityCard";
import * as commands from "@shared/observability/observabilityCommands";
import { resetGraphForTests } from "@shared/state/graph";
import { setTooltipsEnabled } from "@shared/state/tooltipNodes";
import { TOOLTIPS, type TooltipId } from "@shared/tooltips/tooltips";

const show = (api: RetndApi) =>
  render(
    <ApiProvider api={api}>
      <ObservabilityCard readOnly={false} />
    </ApiProvider>
  );

/** A deployment delivering through an Alertmanager webhook, so all five
 *  alert inputs are on screen. */
async function alertmanagerApi(): Promise<RetndApi> {
  const api = createMockApi();
  await api.patchObservabilitySettings({ alertsDelivery: "webhook", webhookDialect: "alertmanager" });
  return api;
}

const help = FIELD_HELP as unknown as Record<string, FieldHelpCopy | undefined>;
const tips = TOOLTIPS as unknown as Record<string, { label?: string; html: string } | undefined>;
const call = (fn: string, ...args: unknown[]): string | undefined => {
  const f = (commands as Record<string, unknown>)[fn];
  return typeof f === "function" ? (f as (...a: unknown[]) => string)(...args) : undefined;
};
const text = (html: string) => html.replace(/<[^>]*>/g, "");

/** The five alert inputs, by their visible label, and the field-help entry
 *  each one owes. */
const ALERT_INPUTS: Array<[label: string, key: string]> = [
  ["Alert delivery", "observabilityAlertsDelivery"],
  ["Webhook format", "observabilityAlertsDialect"],
  ["Plain http", "observabilityAlertsInsecureHttp"],
  ["Refresh every (seconds)", "observabilityAlertsRefresh"],
  ["Expire after (seconds)", "observabilityAlertsResolveTtl"]
];

/** The nodes an element's aria-describedby names, it and its descendants
 *  together, since a tooltip host describes the one element it wraps. */
function describedNodes(el: Element): HTMLElement[] {
  const holders = [el, ...Array.from(el.querySelectorAll("[aria-describedby]"))];
  return holders
    .flatMap((h) => (h.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean))
    .map((id) => document.getElementById(id))
    .filter((n): n is HTMLElement => n !== null);
}

/** The registry copy a tooltip host on `el` renders, or "" when none does. */
function tooltipOn(el: Element): string {
  return describedNodes(el).filter((n) => n.classList.contains("tooltip__body")).map((n) => n.innerHTML).join("\n");
}

/** The visible <label> of a control, which is where its tooltip sits. */
function labelOf(control: HTMLElement): HTMLElement {
  const label = control.id ? document.querySelector<HTMLElement>(`label[for="${CSS.escape(control.id)}"]`) : null;
  if (!label) throw new Error("no visible <label for> names " + (control.getAttribute("aria-label") ?? control.id));
  return label;
}

/** The control names its three-part help, all of it. */
function expectHelp(control: HTMLElement, copy: FieldHelpCopy) {
  const described = describedNodes(control).map((n) => n.textContent ?? "").join(" ");
  expect(described).toContain(copy.what);
  expect(described).toContain(copy.example);
  expect(described).toContain(copy.effect);
}

beforeEach(() => {
  window.localStorage.clear();
  resetGraphForTests();
  setTooltipsEnabled(true);
});

afterEach(() => {
  cleanup();
  resetGraphForTests();
});

describe("#1604 field help for the five alert inputs", () => {
  it("has a three-part entry for each, none of them restating the field", () => {
    for (const [label, key] of ALERT_INPUTS) {
      const copy = help[key];
      expect(copy, label + " has no fieldHelpCopy.ts entry (" + key + ")").toBeTruthy();
      expect(copy?.what, key + ".what").toBeTruthy();
      expect(copy?.example, key + ".example").toBeTruthy();
      expect(copy?.effect, key + ".effect").toBeTruthy();
      expect(copy?.effect, key + " restates what as its effect").not.toBe(copy?.what);
    }
  });

  it("wires each entry to its own input", async () => {
    show(await alertmanagerApi());
    await screen.findByLabelText("Expire after (seconds)");
    for (const [label, key] of ALERT_INPUTS) {
      const copy = help[key];
      expect(copy, key).toBeTruthy();
      expectHelp(screen.getByLabelText(label, { exact: true }), copy as FieldHelpCopy);
    }
  });

  it("says what a delivery switch actually does: refused without url_ref, and nothing re-announced", () => {
    const effect = help.observabilityAlertsDelivery?.effect ?? "";
    expect(effect).toContain("url_ref");
    expect(effect).toMatch(/not announced again/);
  });

  it("puts the 30 second margin into both Alertmanager timings, since the engine refuses less", () => {
    expect(help.observabilityAlertsRefresh?.effect ?? "").toContain("30 seconds");
    expect(help.observabilityAlertsResolveTtl?.effect ?? "").toContain("30 seconds");
  });
});

describe("#1604 #1591 the log level shows once, with a label and a tooltip of its own", () => {
  it("drops the read-only summary row above the select", async () => {
    show(createMockApi());
    await screen.findByTestId("logs-level-control");
    expect(screen.queryByTestId("observability-summary")).toBeNull();
    expect(document.querySelector('[data-observability-row="logs"]')).toBeNull();
    expect(screen.queryByText("info (from config)")).toBeNull();
    // The level is still stated, once, by the line under the select.
    expect(screen.getByTestId("logs-level-in-force").textContent).toContain("In force: info (from config)");
  });

  it("gives the select a visible label, and that label the select's own tooltip", async () => {
    show(createMockApi());
    const select = (await screen.findByLabelText("Log level", { exact: true })) as HTMLSelectElement;
    expect(screen.getAllByLabelText("Log level", { exact: true })).toHaveLength(1);
    const label = labelOf(select);
    expect(label.textContent).toBe("Log level");
    const tip = tips["observability.logs.level"]?.html ?? "";
    expect(tip).not.toBe("");
    expect(tooltipOn(label)).toBe(tip);
  });

  it("keeps a separate tooltip on the in-force line, which explains where the level came from", async () => {
    show(createMockApi());
    const line = await screen.findByTestId("logs-level-in-force");
    const tip = tips["observability.logs.in-force"]?.html ?? "";
    expect(tip).toContain("environment");
    expect(tooltipOn(line)).toBe(tip);
    expect(tooltipOn(line)).not.toBe(tips["observability.logs.level"]?.html);
  });

  it("explains the select itself: the levels, the save and the environment that outranks it", () => {
    const tip = text(tips["observability.logs.level"]?.html ?? "");
    for (const level of ["debug", "info", "warn", "error"]) expect(tip).toContain(level);
    expect(tip).toContain("RETND_DEBUG");
    expect(tip).toContain("no restart");
    // The echo under the control is the terminal line (#900's review).
    expect(tip).not.toContain("retnd settings");
  });
});

describe("#1604 the card's copy matches the code", () => {
  it("does not say everything is off until turned on, because the engine always logs", async () => {
    show(createMockApi());
    await screen.findByTestId("logs-level-control");
    const card = screen.getByRole("region", { name: "Observability" });
    const intro = card.querySelector(".card__body > p")?.textContent ?? "";
    const tip = text(tips["observability.card"]?.html ?? "");
    for (const words of [intro, tip]) {
      expect(words).not.toMatch(/(all of it|everything) is off/i);
      expect(words).toMatch(/always/);
      expect(words).toContain("info");
    }
  });

  it("says in the delivery tooltip that a receiver you switch to is not told about alerts already standing", () => {
    const tip = text(tips["observability.alerts.delivery"]?.html ?? "");
    expect(tip).not.toContain("keeps what has already been announced");
    expect(tip).toMatch(/per channel setting, not per receiver/);
    expect(tip).toMatch(/already standing/);
    // The two exceptions the code has: Alertmanager's refresh re-sends every
    // standing alert, and a platform with no notifier never told anyone.
    expect(tip).toMatch(/alertmanager[^.]*re-sends/);
    expect(tip).toMatch(/no notifier/);
  });
});

describe("#1604 the alert controls echo the terminal line", () => {
  // The strings core/cliecho prints for PATCH /settings/observability with
  // each field alone (routes.go). core/cmd/retnd's
  // TestAlertTwinsAreTheEcho_1604 holds the fixture lines to cliecho itself.
  const line = (flags: string) => "retnd settings observability patch " + flags;

  it("has a builder per alert input, printing what cliecho prints", () => {
    expect(call("alertsDeliveryCommand", "platform")).toBe(line("--alerts-delivery platform"));
    expect(call("alertsDeliveryCommand", "webhook")).toBe(line("--alerts-delivery webhook"));
    expect(call("webhookDialectCommand", "generic")).toBe(line("--webhook-dialect generic"));
    expect(call("webhookDialectCommand", "alertmanager")).toBe(line("--webhook-dialect alertmanager"));
    expect(call("webhookAllowInsecureHttpCommand", true)).toBe(line("--webhook-allow-insecure-http=true"));
    expect(call("webhookAllowInsecureHttpCommand", false)).toBe(line("--webhook-allow-insecure-http=false"));
    expect(call("webhookRefreshIntervalCommand", 240)).toBe(line("--webhook-refresh-interval 4m"));
    expect(call("webhookRefreshIntervalCommand", 90)).toBe(line("--webhook-refresh-interval 1m30s"));
    expect(call("webhookResolveTtlCommand", 600)).toBe(line("--webhook-resolve-ttl 10m"));
    expect(call("webhookResolveTtlCommand", 3610)).toBe(line("--webhook-resolve-ttl 1h10s"));
  });

  it("quotes a free value a shell would split", () => {
    expect(call("alertsDeliveryCommand", "webhook; id")).toBe(line("--alerts-delivery 'webhook; id'"));
    expect(call("webhookDialectCommand", "generic; id")).toBe(line("--webhook-dialect 'generic; id'"));
  });

  it("shows the line for each control on screen, and follows a change", async () => {
    const api = createMockApi();
    show(api);
    const box = await screen.findByTestId("alert-delivery");
    await waitFor(() => expect(box.textContent).toContain(line("--alerts-delivery platform")));
    expect(box.textContent).not.toContain("--webhook-dialect");

    await userEvent.selectOptions(screen.getByLabelText("Alert delivery", { exact: true }), "webhook");
    await waitFor(() => expect(screen.getByTestId("alert-delivery").textContent).toContain(line("--alerts-delivery webhook")));
    const webhook = screen.getByTestId("alert-delivery").textContent ?? "";
    expect(webhook).toContain(line("--webhook-dialect generic"));
    expect(webhook).toContain(line("--webhook-allow-insecure-http=false"));
    expect(webhook).not.toContain("--webhook-refresh-interval");

    await userEvent.selectOptions(screen.getByLabelText("Webhook format", { exact: true }), "alertmanager");
    await waitFor(() => expect(screen.getByTestId("alert-delivery").textContent).toContain(line("--webhook-refresh-interval 4m")));
    expect(screen.getByTestId("alert-delivery").textContent).toContain(line("--webhook-resolve-ttl 10m"));
  });
});

describe("#1604 every tooltip on the card comes from the registry (preference ON)", () => {
  /** Each control's tooltip host, found the way an operator finds the
   *  control, and the id it has to render. */
  const HOSTS: Array<[TooltipId, () => HTMLElement]> = [
    ["observability.card" as TooltipId, () => screen.getByRole("heading", { name: "Observability" })],
    ["observability.metrics.enabled" as TooltipId, () => labelOf(screen.getByLabelText("Serve the scrape endpoint", { exact: true }))],
    ["observability.metrics.interval" as TooltipId, () => labelOf(screen.getByLabelText("Report cache (seconds)", { exact: true }))],
    ["observability.metrics.mint" as TooltipId, () => screen.getByRole("button", { name: "Mint scrape token" })],
    ["observability.logs.level" as TooltipId, () => labelOf(screen.getByLabelText("Log level", { exact: true }))],
    ["observability.logs.in-force" as TooltipId, () => screen.getByTestId("logs-level-in-force")],
    ["observability.alerts.delivery" as TooltipId, () => labelOf(screen.getByLabelText("Alert delivery", { exact: true }))],
    ["observability.alerts.dialect" as TooltipId, () => labelOf(screen.getByLabelText("Webhook format", { exact: true }))],
    ["observability.alerts.insecure-http" as TooltipId, () => labelOf(screen.getByLabelText("Plain http", { exact: true }))],
    ["observability.alerts.refresh" as TooltipId, () => labelOf(screen.getByLabelText("Refresh every (seconds)", { exact: true }))],
    ["observability.alerts.resolve-ttl" as TooltipId, () => labelOf(screen.getByLabelText("Expire after (seconds)", { exact: true }))]
  ];

  it("renders whatever the registry says today, not a copy the control carries", async () => {
    const saved = new Map<string, string>();
    for (const [id] of HOSTS) {
      const entry = tips[id];
      expect(entry, id + " is not in tooltips.json").toBeTruthy();
      if (!entry) continue;
      saved.set(id, entry.html);
      entry.html = "<em>sentinel for " + id + "</em>";
    }
    try {
      show(await alertmanagerApi());
      await screen.findByLabelText("Expire after (seconds)");
      for (const [id, host] of HOSTS) {
        expect(tooltipOn(host()), id + " does not render its registry entry").toBe("<em>sentinel for " + id + "</em>");
      }
    } finally {
      for (const [id, html] of saved) (tips[id] as { html: string }).html = html;
    }
  });

  it("opens an alert input's tooltip on hover", async () => {
    const user = userEvent.setup();
    show(await alertmanagerApi());
    const label = labelOf(await screen.findByLabelText("Alert delivery", { exact: true }));
    await user.hover(label.querySelector(".tooltip") ?? label);
    const body = describedNodes(label).find((n) => n.classList.contains("tooltip__body"));
    expect(body, "the delivery label has no tooltip").toBeTruthy();
    expect(body?.closest(".tooltip__pop")).toBeVisible();
  });

  it("opens nothing on hover, tooltip or field help, once tooltips are turned off", async () => {
    const user = userEvent.setup();
    setTooltipsEnabled(false);
    show(await alertmanagerApi());
    const select = await screen.findByLabelText("Alert delivery", { exact: true });
    const label = labelOf(select);
    await user.hover(label.querySelector(".tooltip") ?? label);
    await user.hover(select);
    for (const pop of Array.from(document.querySelectorAll<HTMLElement>(".tooltip__pop, .fieldhelp__pop"))) {
      expect(pop).not.toBeVisible();
    }
    // Still announced: the description is not a pop-up.
    expect(within(document.body).getAllByText(help.observabilityAlertsDelivery?.what ?? "no entry").length).toBeGreaterThan(0);
  });
});
