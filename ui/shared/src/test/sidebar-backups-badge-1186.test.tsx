/**
 * Issue #1186: the nav's Backups badge counts the backups held outside
 * quarantine, capped at 100 for display, and it is not the number of backup
 * sets. Review of #1187: the figure comes off the health report the shell
 * already polls (`retained_count`), so no screen downloads the whole backups
 * list every 30 seconds to read its length.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { App } from "@shared/App";
import { AppShell } from "@shared/layouts/AppShell";
import { BACKUPS_BADGE_CAP } from "@shared/layouts/navCounts";
import { PlatformProvider } from "@shared/platform/PlatformContext";
import { genericBridge } from "../../../../apps/generic/frontend/platform";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import { graph, resetGraphForTests } from "@shared/state/graph";
import { countsNode, healthNode, setsNode } from "@shared/state/appNodes";
import type { RetndApi } from "@shared/api/contracts";
import type { AuthContext, PlatformBridge } from "@shared/types/platform";

afterEach(() => { cleanup(); resetGraphForTests(); });

function shell(backups: number | undefined) {
  render(
    <PlatformProvider bridge={genericBridge}>
      <ApiProvider api={createMockApi()}>
      <MemoryRouter>
        <AppShell
          health={null} version={null} theme="light"
          onToggleTheme={() => undefined} onSignOut={() => undefined}
          counts={{ sets: 3, backups, quarantine: 0 }}
        >
          <div />
        </AppShell>
      </MemoryRouter>
      </ApiProvider>
    </PlatformProvider>
  );
}

describe("#1186 nav Backups badge", () => {
  it("shows the exact count up to 100, labelled with what it counts", () => {
    shell(40);
    const link = screen.getByRole("link", { name: /^Backups/ });
    expect(link).toHaveTextContent("40");
    expect(link.querySelector("[aria-label='40 backups outside quarantine']")).not.toBeNull();
  });
  it("says 100 at 100", () => {
    shell(100);
    expect(screen.getByRole("link", { name: /^Backups/ })).toHaveTextContent("100");
  });
  it("says >100 above the cap, labelled more than 100", () => {
    shell(119);
    const link = screen.getByRole("link", { name: /^Backups/ });
    expect(link).toHaveTextContent(">" + BACKUPS_BADGE_CAP);
    expect(link.querySelector("[aria-label='more than 100 backups outside quarantine']")).not.toBeNull();
  });
  it("draws nothing before the count is known", () => {
    shell(undefined);
    expect(screen.getByRole("link", { name: /^Backups/ }).querySelector(".mono")).toBeNull();
  });
  it("leaves the other badges as plain numbers", () => {
    shell(40);
    const sets = screen.getByRole("link", { name: /^Backup sets/ }).querySelector(".mono");
    expect(sets).toHaveTextContent("3");
    expect(sets?.getAttribute("aria-label")).toBeNull();
  });
});

describe("#1186 countsNode.backups", () => {
  it("is the health report's retained_count, not the sets in it", () => {
    graph.commit("test", (tx) => {
      tx.set(setsNode, { data: [{}, {}, {}] } as never);
      tx.set(healthNode, { data: { setsHealthy: 3, setsDegraded: 0, setsStale: 0, setsFailing: 0, retainedCount: 119 } } as never);
    });
    expect(graph.read(countsNode).backups).toBe(119);
  });

  it("is unknown, not zero, when the engine is too old to report it", () => {
    graph.commit("test", (tx) => {
      tx.set(healthNode, { data: { setsHealthy: 3, setsDegraded: 0, setsStale: 0, setsFailing: 0, retainedCount: null } } as never);
    });
    expect(graph.read(countsNode).backups).toBeUndefined();
  });
});

describe("#1186 the shell does not download the backups list for a badge", () => {
  const AUTHENTICATED: AuthContext = { authenticated: true, username: "bm-admin", mode: "local-account" };
  const bridge: PlatformBridge = { ...genericBridge, getAuthContext: () => Promise.resolve(AUTHENTICATED) };

  it("draws the badge from the health report on the dashboard without calling listArtifacts", async () => {
    const base = createMockApi();
    const listArtifacts = vi.fn(base.listArtifacts);
    const api: RetndApi = { ...base, listArtifacts };
    render(
      <MemoryRouter initialEntries={["/"]}>
        <ApiProvider api={api}>
          <PlatformProvider bridge={bridge}>
            <App />
          </PlatformProvider>
        </ApiProvider>
      </MemoryRouter>
    );
    const nav = await screen.findByRole("navigation", { name: "Sections" }, { timeout: 4000 });
    const health = await base.getHealth();
    await waitFor(() => expect(nav.querySelector("a[href='/backups'] .mono")).toHaveTextContent(String(health.retainedCount)));
    expect(listArtifacts).not.toHaveBeenCalled();
  });
});
