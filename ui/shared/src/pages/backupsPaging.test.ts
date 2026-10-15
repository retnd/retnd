import { afterEach, describe, expect, it, vi } from "vitest";
import { ALL_BELOW, PAGE_SIZE_KEY, allOffered, pageWindow, readPageSize, rememberPageSize, isPageSize } from "./backupsPaging";

afterEach(() => { vi.restoreAllMocks(); window.localStorage.clear(); });

describe("#1180 the remembered page size", () => {
  it("is 20 when nothing was remembered", () => {
    expect(readPageSize()).toBe(20);
  });

  it("returns what was remembered, for 20, 50 and 100", () => {
    for (const n of [20, 50, 100] as const) { rememberPageSize(n); expect(readPageSize()).toBe(n); }
  });

  it("ignores a stored value that is not one of the three", () => {
    for (const v of ["7", "all", "All", "", "1000", "-20", "20.5"]) {
      window.localStorage.setItem(PAGE_SIZE_KEY, v);
      expect(readPageSize()).toBe(20);
    }
  });

  it("survives a storage that throws on read and on write", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new DOMException("blocked", "SecurityError"); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new DOMException("blocked", "SecurityError"); });
    expect(readPageSize()).toBe(20);
    expect(() => rememberPageSize(50)).not.toThrow();
  });
});

describe("#1180 All", () => {
  it("is offered under 1000 backups and not at 1000", () => {
    expect(allOffered(0)).toBe(true);
    expect(allOffered(ALL_BELOW - 1)).toBe(true);
    expect(allOffered(ALL_BELOW)).toBe(false);
    expect(allOffered(5000)).toBe(false);
  });
});

describe("#1180 the window of a page", () => {
  it("cuts a list into pages of the chosen size, the last one short", () => {
    expect(pageWindow(34, 20, 0)).toEqual({ start: 0, end: 20, page: 0, pages: 2 });
    expect(pageWindow(34, 20, 1)).toEqual({ start: 20, end: 34, page: 1, pages: 2 });
  });

  it("clamps a page that no longer exists to the last one", () => {
    expect(pageWindow(34, 20, 9).page).toBe(1);
    expect(pageWindow(34, 20, -3).page).toBe(0);
  });

  it("is one page for All and for an empty list", () => {
    expect(pageWindow(34, "all", 5)).toEqual({ start: 0, end: 34, page: 0, pages: 1 });
    expect(pageWindow(0, 20, 0)).toEqual({ start: 0, end: 0, page: 0, pages: 1 });
  });

  it("holds exactly one full page without inventing a second", () => {
    expect(pageWindow(20, 20, 0).pages).toBe(1);
    expect(pageWindow(21, 20, 0).pages).toBe(2);
  });
});

describe("isPageSize", () => {
  it("accepts the three sizes and nothing else, so the select's value narrows without a cast", () => {
    expect([20, 50, 100].every(isPageSize)).toBe(true);
    expect([0, 25, 1000, NaN].some(isPageSize)).toBe(false);
  });
});
