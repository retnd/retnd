/**
 * retnd#1049: the copy helper's fallback chain, and the one property that
 * matters about it: it only ever says "copied" when something was copied.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { copyText } from "@shared/utilities/copyText";

const savedExec = document.execCommand;
const savedClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");

function setClipboard(value: unknown) {
  Object.defineProperty(navigator, "clipboard", { value, configurable: true });
}

afterEach(() => {
  document.execCommand = savedExec;
  if (savedClipboard) Object.defineProperty(navigator, "clipboard", savedClipboard);
  else setClipboard(undefined);
  vi.restoreAllMocks();
});

describe("copyText", () => {
  it("uses the async clipboard when the browser offers one", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard({ writeText });
    document.execCommand = vi.fn(() => false) as unknown as typeof document.execCommand;
    await expect(copyText("abc")).resolves.toBe("copied");
    expect(writeText).toHaveBeenCalledWith("abc");
    expect(document.execCommand).not.toHaveBeenCalled();
  });

  it("falls over to execCommand when there is no async clipboard", async () => {
    setClipboard(undefined);
    const exec = vi.fn(() => true);
    document.execCommand = exec as unknown as typeof document.execCommand;
    await expect(copyText("abc")).resolves.toBe("copied");
    expect(exec).toHaveBeenCalledWith("copy");
    // The hidden textarea is gone again, whatever happened.
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("falls over to execCommand when the async clipboard throws", async () => {
    setClipboard({ writeText: vi.fn().mockRejectedValue(new Error("denied")) });
    document.execCommand = vi.fn(() => true) as unknown as typeof document.execCommand;
    await expect(copyText("abc")).resolves.toBe("copied");
  });

  it("selects the visible details, and says only that, when copying is refused", async () => {
    setClipboard(undefined);
    document.execCommand = vi.fn(() => false) as unknown as typeof document.execCommand;
    const node = document.createElement("div");
    node.textContent = "cid_abc123 something happened";
    document.body.appendChild(node);
    await expect(copyText("abc", node)).resolves.toBe("selected");
    expect(window.getSelection()?.toString()).toContain("cid_abc123");
    node.remove();
  });

  it("reports failure only when nothing at all could be done", async () => {
    setClipboard(undefined);
    document.execCommand = vi.fn(() => false) as unknown as typeof document.execCommand;
    await expect(copyText("abc")).resolves.toBe("failed");
  });
});
