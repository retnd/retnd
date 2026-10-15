/**
 * Issue #1186: the nav's Backups badge is a count of retained backups, capped
 * at 100 for display, and it is not the number of backup sets.
 */
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { AppShell } from "@shared/layouts/AppShell";
import { PlatformProvider } from "@shared/platform/PlatformContext";
import { genericBridge } from "../../../../apps/generic/frontend/platform";
import { graph, resetGraphForTests } from "@shared/state/graph";
import { artifactsNode, countsNode, healthNode, setsNode } from "@shared/state/appNodes";
import type { BackupArtifact } from "@shared/types/backup";

function shell(backups: number | undefined) {
  render(
    <PlatformProvider bridge={genericBridge}>
      <MemoryRouter>
        <AppShell
          health={null} version={null} theme="light"
          onToggleTheme={() => undefined} onSignOut={() => undefined}
          counts={{ sets: 3, backups, quarantine: 0 }}
        >
          <div />
        </AppShell>
      </MemoryRouter>
    </PlatformProvider>
  );
}

describe("#1186 nav Backups badge", () => {
  it("shows the exact count up to 100", () => {
    shell(40);
    const link = screen.getByRole("link", { name: /^Backups/ });
    expect(link).toHaveTextContent("40");
    expect(link.querySelector("[aria-label='40 backups']")).not.toBeNull();
  });
  it("says 100 at 100 and >100 at 101", () => {
    shell(100);
    expect(screen.getByRole("link", { name: /^Backups/ })).toHaveTextContent("100");
  });
  it("says >100 above the cap, labelled more than 100 backups", () => {
    shell(119);
    const link = screen.getByRole("link", { name: /^Backups/ });
    expect(link).toHaveTextContent(">100");
    expect(link.querySelector("[aria-label='more than 100 backups']")).not.toBeNull();
  });
  it("draws nothing before the count is known", () => {
    shell(undefined);
    expect(screen.getByRole("link", { name: /^Backups/ }).querySelector(".mono")).toBeNull();
  });
});

describe("#1186 countsNode.backups", () => {
  it("counts the retained backups, not the sets in the health report", () => {
    resetGraphForTests();
    const rows = Array.from({ length: 119 }, (_, i) => ({ id: "s/a" + i }) as BackupArtifact);
    graph.commit("test", (tx) => {
      tx.set(setsNode, { data: [{}, {}, {}] } as never);
      tx.set(healthNode, { data: { setsHealthy: 3, setsDegraded: 0, setsStale: 0, setsFailing: 0 } } as never);
      tx.set(artifactsNode, { data: rows } as never);
    });
    expect(graph.read(countsNode).backups).toBe(119);
  });
});
