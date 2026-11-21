import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { useState } from "react";
import { createPortal } from "react-dom";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
// The real stylesheet: a pop-up's closed state is `.tooltip__pop[hidden]`.
import "@shared/design-system/components.css";
import { resetGraphForTests } from "@shared/state/graph";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { TooltipAutoAttach } from "@shared/tooltips/TooltipAutoAttach";

/**
 * #1646: a tooltip open over a trigger goes away when that trigger opens a modal
 * dialog. The mechanism sits in the tooltip layer (tooltips/closeOnModal.ts), so
 * it is held here for both layers, the typed host and the delegated data-tip one,
 * with the dialog mounted in place and portalled to the body, by pointer and by
 * keyboard, and it must leave a pop-up alone when what arrives is not a modal
 * dialog or is the dialog the pop-up lives in.
 */
const shownPops = () => Array.from(document.querySelectorAll<HTMLElement>(".tooltip__pop")).filter((p) => !p.hasAttribute("hidden"));

function Dialog() {
  return (
    <div role="dialog" aria-modal="true" aria-label="Sure?">
      <button type="button">Cancel</button>
    </div>
  );
}

function Typed({ portal = false }: { portal?: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <InfoTooltip id="agents.token.create">
        <button type="button" onClick={() => setOpen(true)}>Open</button>
      </InfoTooltip>
      {open ? (portal ? createPortal(<Dialog />, document.body) : <Dialog />) : null}
    </div>
  );
}

function Delegated({ portal = false }: { portal?: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <TooltipAutoAttach>
      <button type="button" data-tip="shell.sign-out" onClick={() => setOpen(true)}>Open</button>
      {open ? (portal ? createPortal(<Dialog />, document.body) : <Dialog />) : null}
    </TooltipAutoAttach>
  );
}

beforeEach(() => {
  window.localStorage.clear();
  resetGraphForTests();
});
afterEach(() => {
  cleanup();
  resetGraphForTests();
});

describe("#1646 a modal dialog opening puts every tooltip away", () => {
  for (const [layer, Layer] of [["typed host", Typed], ["data-tip layer", Delegated]] as const) {
    for (const portal of [false, true]) {
      const where = portal ? "portalled to the body" : "in place";

      it(layer + ": hover the trigger, click it, and no pop-up is left over the dialog (" + where + ")", async () => {
        const user = userEvent.setup();
        render(<Layer portal={portal} />);
        await user.hover(screen.getByRole("button", { name: "Open" }));
        expect(shownPops()).toHaveLength(1);

        await user.click(screen.getByRole("button", { name: "Open" }));
        expect(screen.getByRole("dialog")).toBeInTheDocument();
        await waitFor(() => expect(shownPops()).toHaveLength(0));
      });

      it(layer + ": Tab to the trigger, press Enter, and no pop-up is left over the dialog (" + where + ")", async () => {
        const user = userEvent.setup();
        render(<Layer portal={portal} />);
        await user.tab();
        expect(screen.getByRole("button", { name: "Open" })).toHaveFocus();
        expect(shownPops()).toHaveLength(1);

        await user.keyboard("{Enter}");
        expect(screen.getByRole("dialog")).toBeInTheDocument();
        await waitFor(() => expect(shownPops()).toHaveLength(0));
      });
    }
  }

  it("typed host: a pinned pop-up (touch tap) goes too", async () => {
    render(<Typed />);
    const button = screen.getByRole("button", { name: "Open" });
    // jsdom has no PointerEvent, so the touch pointerdown is built by hand.
    const down = new Event("pointerdown", { bubbles: true, cancelable: true });
    Object.defineProperty(down, "pointerType", { value: "touch" });
    act(() => { button.dispatchEvent(down); });
    await waitFor(() => expect(shownPops()).toHaveLength(1));
    fireEvent.click(button);
    await waitFor(() => expect(shownPops()).toHaveLength(0));
  });

  it("something that is not a modal dialog arriving leaves the pop-up up", async () => {
    const user = userEvent.setup();
    function Plain() {
      const [on, setOn] = useState(false);
      return (
        <div>
          <InfoTooltip id="agents.token.create">
            <button type="button">Open</button>
          </InfoTooltip>
          <button type="button" onClick={() => setOn(true)}>Elsewhere</button>
          {on ? <div role="status">Saved</div> : null}
        </div>
      );
    }
    render(<Plain />);
    const button = screen.getByRole("button", { name: "Open" });
    await user.hover(button);
    fireEvent.click(screen.getByRole("button", { name: "Elsewhere" }));
    expect(await screen.findByRole("status")).toBeInTheDocument();
    // Let the observer run, then check it did nothing.
    await Promise.resolve();
    expect(shownPops()).toHaveLength(1);
  });

  it("a pop-up inside a dialog that is already open still opens", async () => {
    const user = userEvent.setup();
    render(
      <div role="dialog" aria-modal="true" aria-label="Sure?">
        <InfoTooltip id="agents.token.create">
          <button type="button">Inside</button>
        </InfoTooltip>
      </div>
    );
    await user.hover(screen.getByRole("button", { name: "Inside" }));
    expect(shownPops()).toHaveLength(1);
  });

  for (const [layer, Layer] of [["typed host", Typed], ["data-tip layer", Delegated]] as const) {
    it(layer + ": focus landing on the covered trigger while a dialog is open opens nothing", async () => {
      const user = userEvent.setup();
      render(<Layer />);
      await user.click(screen.getByRole("button", { name: "Open" }));
      expect(screen.getByRole("dialog")).toBeInTheDocument();
      await user.unhover(screen.getByRole("button", { name: "Open" }));

      act(() => screen.getByRole("button", { name: "Cancel" }).focus());
      // What the Retire dialog's hand-back timer does while focus is on the body.
      act(() => screen.getByRole("button", { name: "Open" }).focus());
      await Promise.resolve();
      expect(shownPops()).toHaveLength(0);
    });
  }
});
