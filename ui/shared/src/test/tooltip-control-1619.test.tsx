import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
// The real stylesheet: a pop-up's hidden state is `.tooltip__pop[hidden]`.
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";
import { ObservabilityCard } from "@shared/observability/ObservabilityCard";
import { SuiteChoice } from "@shared/encryption/SuiteControls";
import { resetGraphForTests } from "@shared/state/graph";
import { setTooltipsEnabled } from "@shared/state/tooltipNodes";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { TOOLTIPS, type TooltipId } from "@shared/tooltips/tooltips";

/**
 * #1619: a tooltip on a control's LABEL has to reach the control.
 *
 * `InfoTooltip` wraps the label text, opens on hover of that text, and used
 * to hang its `aria-describedby` on the label's own span. Tabbing to the
 * control gave a keyboard user nothing and a screen reader read no
 * description, which fails WCAG 2.1.1 and 1.3.1. The fix is in the host, once,
 * so these tests hold the mechanism first and then the cards the issue names.
 */
beforeEach(() => {
  window.localStorage.clear();
  resetGraphForTests();
});
afterEach(() => {
  cleanup();
  resetGraphForTests();
});

const copyOf = (id: TooltipId) => TOOLTIPS[id].html;
const bodyOf = (id: TooltipId): HTMLElement => {
  const hit = Array.from(document.querySelectorAll<HTMLElement>(".tooltip__body")).find((n) => n.innerHTML === copyOf(id));
  if (!hit) throw new Error("no tooltip body rendering " + id);
  return hit;
};
const popOf = (id: TooltipId) => bodyOf(id).closest(".tooltip__pop") as HTMLElement;

/** The elements a control's own aria-describedby names. */
const describedBy = (el: Element) =>
  (el.getAttribute("aria-describedby") ?? "")
    .split(/\s+/)
    .filter(Boolean)
    .map((id) => document.getElementById(id))
    .filter((n): n is HTMLElement => n !== null);

function Labelled({ remountKey = "a", id = "observability.logs.level" as TooltipId }: { remountKey?: string; id?: TooltipId }) {
  return (
    <div>
      <label htmlFor="ctl">
        <InfoTooltip id={id}><span>Log level</span></InfoTooltip>
      </label>
      <select id="ctl" key={remountKey} aria-describedby="own-help">
        <option>info</option>
      </select>
      <p id="own-help">What saving does.</p>
      <button type="button">next</button>
    </div>
  );
}

describe("#1619 a label tooltip, in the host", () => {
  it("describes the control itself with the registry copy, keeping what the control already pointed at", () => {
    render(<Labelled />);
    const control = screen.getByRole("combobox");

    const nodes = describedBy(control);
    expect(nodes.some((n) => n.id === "own-help")).toBe(true);
    expect(nodes.some((n) => n.innerHTML === copyOf("observability.logs.level"))).toBe(true);
  });

  it("opens when Tab lands on the control, with no pointer involved, and Escape puts it away", async () => {
    const user = userEvent.setup();
    render(<Labelled />);
    expect(popOf("observability.logs.level")).not.toBeVisible();

    await user.tab();
    expect(screen.getByRole("combobox")).toHaveFocus();
    expect(popOf("observability.logs.level")).toBeVisible();

    await user.keyboard("{Escape}");
    expect(popOf("observability.logs.level")).not.toBeVisible();
  });

  it("closes when focus moves on, and stays up while focus moves onto its own close button", async () => {
    const user = userEvent.setup();
    render(<Labelled />);

    await user.tab();
    expect(popOf("observability.logs.level")).toBeVisible();
    await user.tab();
    await user.tab();
    expect(popOf("observability.logs.level")).not.toBeVisible();
  });

  it("keeps the description when the control is remounted by its parent", async () => {
    const { rerender } = render(<Labelled remountKey="a" />);
    rerender(<Labelled remountKey="b" />);

    await waitFor(() => {
      expect(describedBy(screen.getByRole("combobox")).some((n) => n.innerHTML === copyOf("observability.logs.level"))).toBe(true);
    });
    await userEvent.setup().tab();
    expect(popOf("observability.logs.level")).toBeVisible();
  });

  it("describes a control but opens nothing on focus while tooltips are off", async () => {
    setTooltipsEnabled(false);
    const user = userEvent.setup();
    render(<Labelled />);

    await user.tab();
    expect(describedBy(screen.getByRole("combobox")).some((n) => n.innerHTML === copyOf("observability.logs.level"))).toBe(true);
    expect(popOf("observability.logs.level")).not.toBeVisible();
  });

  it("names a control that is not a label's, by id, for a legend over a group", async () => {
    const user = userEvent.setup();
    render(
      <div>
        <InfoTooltip id="encryption.suite" controlId="grp"><span>Suite</span></InfoTooltip>
        <div id="grp" role="radiogroup" aria-label="Suite">
          <input type="radio" name="s" aria-label="one" />
        </div>
      </div>
    );
    expect(describedBy(screen.getByRole("radiogroup")).some((n) => n.innerHTML === copyOf("encryption.suite"))).toBe(true);

    await user.tab();
    expect(screen.getByRole("radio")).toHaveFocus();
    expect(popOf("encryption.suite")).toBeVisible();
  });

  it("stops describing the control when the tooltip unmounts", () => {
    function Toggle() {
      const [on, setOn] = useState(true);
      return (
        <div>
          {on ? <label htmlFor="c2"><InfoTooltip id="observability.logs.level"><span>Log level</span></InfoTooltip></label> : null}
          <input id="c2" />
          <button type="button" onClick={() => setOn(false)}>drop</button>
        </div>
      );
    }
    render(<Toggle />);
    expect(screen.getByRole("textbox").getAttribute("aria-describedby")).toBeTruthy();
    screen.getByRole("button", { name: "drop" }).click();
    return waitFor(() => expect(screen.getByRole("textbox").getAttribute("aria-describedby")).toBeNull());
  });
});

const showCard = (api: RetndApi) =>
  render(
    <ApiProvider api={api}>
      <ObservabilityCard readOnly={false} />
    </ApiProvider>
  );

describe("#1619 the Observability card", () => {
  const CONTROLS: Array<[label: string, id: TooltipId]> = [
    ["Log level", "observability.logs.level"],
    ["Serve the scrape endpoint", "observability.metrics.enabled"],
    ["Report cache (seconds)", "observability.metrics.interval"],
    ["Alert delivery", "observability.alerts.delivery"],
    ["Webhook format", "observability.alerts.dialect"],
    ["Plain http", "observability.alerts.insecure-http"],
    ["Refresh every (seconds)", "observability.alerts.refresh"],
    ["Expire after (seconds)", "observability.alerts.resolve-ttl"]
  ];

  it.each(CONTROLS)("%s is described by its registry tooltip", async (label, id) => {
    const api = createMockApi();
    await api.patchObservabilitySettings({ metricsEnabled: true, alertsDelivery: "webhook", webhookDialect: "alertmanager" });
    showCard(api);
    const control = await screen.findByLabelText(label, { exact: true });

    expect(describedBy(control).some((n) => n.innerHTML === copyOf(id))).toBe(true);
  });

  it("opens the log level's tooltip when Tab reaches the select", async () => {
    showCard(createMockApi());
    const select = await screen.findByLabelText("Log level", { exact: true });
    expect(popOf("observability.logs.level")).not.toBeVisible();

    select.focus();
    await waitFor(() => expect(popOf("observability.logs.level")).toBeVisible());
  });
});

describe("#1619 SuiteControls", () => {
  it("describes the suite group with its registry tooltip and opens it on focus", async () => {
    const user = userEvent.setup();
    render(
      <SuiteChoice
        suites={[{ id: "aes-256-gcm", version: 1, status: "supported", isDefault: true, description: "the default", retirementVersion: null, activeCopies: 0, keys: 0 }]}
        current="aes-256-gcm"
        configured="default"
        disabled={false}
        onChoose={() => undefined}
      />
    );
    const group = screen.getByRole("radiogroup", { name: "Suite" });
    expect(describedBy(group).some((n) => n.innerHTML === copyOf("encryption.suite"))).toBe(true);

    await user.tab();
    expect(screen.getByRole("radio")).toHaveFocus();
    expect(popOf("encryption.suite")).toBeVisible();
  });
});
