// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyTheme, resolveTheme } from "./theme";
import html from "../index.html?raw";

const inline = /<script id="theme-boot">([\s\S]*?)<\/script>/.exec(html)![1];

/** Runs index.html's pre-paint script against the current document. */
function runInline() {
  new Function(inline)();
}

beforeEach(() => {
  window.localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
});
afterEach(() => vi.restoreAllMocks());

describe("the pre-paint script in index.html (#1236)", () => {
  it("sets dark from a stored dark preference", () => {
    window.localStorage.setItem("retnd.theme", "dark");
    runInline();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });
  it("sets light from a stored light preference", () => {
    window.localStorage.setItem("retnd.theme", "light");
    runInline();
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });
  it("defaults to light with nothing stored", () => {
    runInline();
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });
  it("leaves a valid attribute that is already set alone", () => {
    window.localStorage.setItem("retnd.theme", "light");
    document.documentElement.setAttribute("data-theme", "dark");
    runInline();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });
  it("replaces an attribute that is not a theme", () => {
    window.localStorage.setItem("retnd.theme", "dark");
    document.documentElement.setAttribute("data-theme", "sepia");
    runInline();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });
  it("falls back to light when storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("blocked"); });
    runInline();
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });
});

describe("the canvas rule in index.html (#1236)", () => {
  it("declares color-scheme for both themes, before the stylesheets", () => {
    const css = /<style id="theme-boot-canvas">([\s\S]*?)<\/style>/.exec(html)![1];
    expect(css).toMatch(/:root\s*\{\s*color-scheme:\s*light;\s*\}/);
    expect(css).toMatch(/:root\[data-theme="dark"\]\s*\{\s*color-scheme:\s*dark;\s*\}/);
    expect(html.indexOf('id="theme-boot"')).toBeLessThan(html.indexOf('id="theme-boot-canvas"'));
    expect(html.indexOf('id="theme-boot-canvas"')).toBeLessThan(html.indexOf('src="/src/main.tsx"'));
  });
  it("holds no colour value of its own", () => {
    const css = /<style id="theme-boot-canvas">([\s\S]*?)<\/style>/.exec(html)![1];
    expect(css).not.toMatch(/#[0-9a-f]{3,8}\b|rgb|hsl|oklch/i);
  });
});

describe("resolveTheme and applyTheme", () => {
  it("prefers the attribute the page already carries over storage", () => {
    window.localStorage.setItem("retnd.theme", "light");
    document.documentElement.setAttribute("data-theme", "dark");
    expect(resolveTheme()).toBe("dark");
  });
  it("reads storage when the attribute is missing", () => {
    window.localStorage.setItem("retnd.theme", "dark");
    expect(resolveTheme()).toBe("dark");
  });
  it("is light with neither, and ignores prefers-color-scheme", () => {
    window.matchMedia = vi.fn().mockReturnValue({ matches: true }) as unknown as typeof window.matchMedia;
    expect(resolveTheme()).toBe("light");
  });
  it("applyTheme writes the attribute and remembers it, and survives blocked storage", () => {
    applyTheme("dark");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(window.localStorage.getItem("retnd.theme")).toBe("dark");
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("blocked"); });
    expect(() => applyTheme("light")).not.toThrow();
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });
});
