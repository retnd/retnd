/**
 * Issue #1202, the review of #1214: two changes to the address in one tick
 * must both land. react-router's functional updater is handed the parameters
 * of the render it was made in, so the second of two calls used to overwrite
 * the first.
 */
import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { useBatchedSearchParams } from "./useBatchedSearchParams";

function Probe() {
  const [params, patch] = useBatchedSearchParams();
  const loc = useLocation();
  return (
    <>
      <button onClick={() => { patch((p) => p.set("set", "a/b")); patch((p) => p.delete("assurance")); patch((p) => p.set("x", "1")); }}>go</button>
      <button onClick={() => patch((p) => p.set("set", "c/d"))}>again</button>
      <output data-testid="search">{loc.search}</output>
      <output data-testid="set">{params.get("set") ?? ""}</output>
    </>
  );
}

describe("#1202 batched search parameters", () => {
  it("#1202 three updates in one handler all land and nothing else is touched", () => {
    render(<MemoryRouter initialEntries={["/backups?assurance=degraded&keep=me"]}><Probe /></MemoryRouter>);
    fireEvent.click(screen.getByText("go"));
    const q = new URLSearchParams(screen.getByTestId("search").textContent ?? "");
    expect(q.get("set")).toBe("a/b");
    expect(q.has("assurance")).toBe(false);
    expect(q.get("x")).toBe("1");
    expect(q.get("keep")).toBe("me");
  });
  it("#1202 a later tick starts from what the address now holds", () => {
    render(<MemoryRouter initialEntries={["/backups?keep=me"]}><Probe /></MemoryRouter>);
    fireEvent.click(screen.getByText("go"));
    fireEvent.click(screen.getByText("again"));
    const q = new URLSearchParams(screen.getByTestId("search").textContent ?? "");
    expect(q.get("set")).toBe("c/d");
    expect(q.get("x")).toBe("1");
    expect(q.get("keep")).toBe("me");
  });
});
