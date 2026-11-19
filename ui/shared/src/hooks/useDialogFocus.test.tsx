/**
 * The keyboard behaviour the validators' dialogs share (issue #1203, review):
 * Escape closes unless the dialog is busy submitting, and a new onClose from a
 * parent's render neither re-registers the listener nor is missed.
 */
import { useRef } from "react";
import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useDialogFocus } from "./useDialogFocus";

function Box({ onClose, busy }: { onClose(): void; busy?: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  useDialogFocus(box, onClose, { busy });
  return <div ref={box} tabIndex={-1}><button>one</button><button>two</button></div>;
}

describe("useDialogFocus", () => {
  it("closes on Escape", () => {
    const onClose = vi.fn();
    render(<Box onClose={onClose} />);
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("ignores Escape while the dialog is submitting", () => {
    const onClose = vi.fn();
    const { rerender } = render(<Box onClose={onClose} busy />);
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    rerender(<Box onClose={onClose} busy={false} />);
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("calls the newest onClose without re-adding its listener", () => {
    const add = vi.spyOn(window, "addEventListener");
    const first = vi.fn();
    const second = vi.fn();
    const { rerender } = render(<Box onClose={first} />);
    const adds = () => add.mock.calls.filter((c) => c[0] === "keydown").length;
    const before = adds();
    rerender(<Box onClose={second} />);
    expect(adds()).toBe(before);
    fireEvent.keyDown(window, { key: "Escape" });
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
    add.mockRestore();
  });
});
