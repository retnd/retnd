/**
 * The Actions menu's edges (issue #1202, the review of #1214): the cases the
 * menu's own happy path never reaches. The item list comes from props while
 * the highlighted position is the menu's own state, so anything that changes
 * the list while the menu is open has to leave the position usable.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ActionMenu } from "@shared/components/ActionMenu";
import type { ActionMenuGroup } from "@shared/components/ActionMenu";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const frame = () => act(() => new Promise<void>((r) => requestAnimationFrame(() => r())));

function groupsOf(ids: string[], onSelect: (id: string) => void = () => {}): ActionMenuGroup[] {
  return ids.length === 0 ? [] : [{ id: "g", label: "Group", items: ids.map((id) => ({ id, label: id.toUpperCase(), echo: "retnd " + id, onSelect: () => onSelect(id) })) }];
}

function Harness({ groups, rootProps }: { groups: ActionMenuGroup[] | (() => ActionMenuGroup[]); rootProps?: Record<`data-${string}`, string> }) {
  return <ActionMenu label="Actions" groups={groups} open openAt="first" onOpen={() => {}} onClose={() => {}} rootProps={rootProps} />;
}

describe("#1202 the item list shrinks or empties while the menu is open", () => {
  it("#1202 Enter after the list shrank under the highlighted item picks the last one left", () => {
    const picked: string[] = [];
    const { rerender } = render(<Harness groups={groupsOf(["a", "b", "c"], (id) => picked.push(id))} />);
    const menu = screen.getByRole("menu");
    fireEvent.keyDown(menu, { key: "End" });
    rerender(<Harness groups={groupsOf(["a"], (id) => picked.push(id))} />);
    expect(() => fireEvent.keyDown(screen.getByRole("menu"), { key: "Enter" })).not.toThrow();
    expect(picked).toEqual(["a"]);
  });

  it("#1202 an empty menu takes the arrow keys, Home, End, Enter and a letter without throwing", () => {
    render(<Harness groups={groupsOf([])} />);
    const menu = screen.getByRole("menu");
    for (const key of ["ArrowDown", "ArrowUp", "Home", "End", "Enter", " ", "a"]) {
      expect(() => fireEvent.keyDown(menu, { key }), key).not.toThrow();
    }
  });

  it("#1202 an item that arrives after an empty spell is reachable, not stuck on NaN", () => {
    const { rerender } = render(<Harness groups={groupsOf([])} />);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "ArrowDown" });
    fireEvent.keyDown(screen.getByRole("menu"), { key: "ArrowUp" });
    rerender(<Harness groups={groupsOf(["a", "b"])} />);
    const items = screen.getAllByRole("menuitem");
    expect(items.filter((i) => i.tabIndex === 0)).toHaveLength(1);
  });
});

describe("#1202 groups are built only while the menu is open", () => {
  it("#1202 a closed menu never calls the groups thunk, an open one does", () => {
    const thunk = vi.fn(() => groupsOf(["a"]));
    const { rerender } = render(<ActionMenu label="Actions" groups={thunk} open={false} onOpen={() => {}} onClose={() => {}} />);
    expect(thunk).not.toHaveBeenCalled();
    rerender(<ActionMenu label="Actions" groups={thunk} open onOpen={() => {}} onClose={() => {}} />);
    expect(thunk).toHaveBeenCalled();
    expect(screen.getAllByRole("menuitem")).toHaveLength(1);
  });
});

describe("#1202 root props and group keys", () => {
  it("#1202 data attributes for the root come from rootProps, and are absent without them", () => {
    const { rerender } = render(<Harness groups={groupsOf(["a"])} rootProps={{ "data-assurance": "" }} />);
    expect(screen.getByRole("menu").hasAttribute("data-assurance")).toBe(true);
    rerender(<Harness groups={groupsOf(["a"])} />);
    expect(screen.getByRole("menu").hasAttribute("data-assurance")).toBe(false);
  });

  it("#1202 two groups with the same label are both drawn and warn about nothing", () => {
    const err = vi.spyOn(console, "error").mockImplementation(() => {});
    const same: ActionMenuGroup[] = [
      { id: "one", label: "Same", items: [{ id: "a", label: "A", onSelect: () => {} }] },
      { id: "two", label: "Same", items: [{ id: "b", label: "B", onSelect: () => {} }] }
    ];
    render(<Harness groups={same} />);
    expect(screen.getAllByRole("group")).toHaveLength(2);
    expect(err).not.toHaveBeenCalled();
  });
});

describe("#1202 scrolling the page under an open menu", () => {
  it("#1202 a scroll that moves nothing neither re-renders nor pulls focus back", async () => {
    render(<Harness groups={groupsOf(["a", "b"])} />);
    const first = screen.getAllByRole("menuitem")[0];
    const focus = vi.spyOn(first, "focus");
    for (let i = 0; i < 5; i++) window.dispatchEvent(new Event("scroll"));
    await frame();
    await frame();
    expect(focus).not.toHaveBeenCalled();
  });

  it("#1202 a burst of scroll events is measured once per frame", async () => {
    render(<Harness groups={groupsOf(["a", "b"])} />);
    const rect = vi.spyOn(HTMLButtonElement.prototype, "getBoundingClientRect");
    await frame();
    rect.mockClear();
    for (let i = 0; i < 20; i++) window.dispatchEvent(new Event("scroll"));
    await frame();
    await frame();
    expect(rect.mock.calls.length).toBeLessThanOrEqual(1);
  });

  it("#1202 when the footer changes the pop-up stays put and is only kept inside the viewport", async () => {
    // Sized by its footer: the second item's twin is wider and taller. The
    // trigger sits low, so the first placement is above it.
    const size = (el: HTMLElement) => (el.getAttribute("role") === "menu" && el.textContent?.includes("retnd b") ? { w: 300, h: 150 } : { w: 200, h: 100 });
    vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) { return size(this).w; });
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) { return size(this).h; });
    vi.spyOn(HTMLButtonElement.prototype, "getBoundingClientRect").mockReturnValue({ top: 700, bottom: 720, right: 900, left: 850, width: 50, height: 20, x: 850, y: 700, toJSON: () => ({}) });
    render(<Harness groups={groupsOf(["a", "b"])} />);
    const menu = screen.getByRole("menu");
    await frame();
    expect(menu.style.top).toBe("596px");
    expect(menu.style.left).toBe("700px");
    fireEvent.keyDown(menu, { key: "ArrowDown" });
    await frame();
    // Taller and wider now, and still where the pointer found it.
    expect(menu.style.left).toBe("700px");
    expect(menu.style.top).toBe("596px");
  });
});
