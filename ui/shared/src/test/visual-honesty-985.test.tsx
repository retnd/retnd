/**
 * Visual honesty on the assurance surfaces (issue #985, #1070): the Backups
 * footer, the Content chip across copies, the Not configured edge and the
 * outcome of a browser download.
 *
 * Each of these was a place the screen said something the data did not: a
 * footer in the tertiary ink that fails AA and called every copy verified, a
 * Content chip that took the strongest class across copies and so read
 * "Content verified" for a backup with two copies nobody had looked at, a
 * dashed edge that sat at 2.93:1 against its own fill, and a download that
 * ended and left no trace on the artifact page.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import "@shared/design-system/components.css";
import { ApiProvider } from "@shared/api/ApiContext";
import { createMockApi } from "@shared/api/mock";
import type { RetndApi } from "@shared/api/contracts";
import { BackupsPage } from "@shared/pages/BackupsPage";
import { BackupDetailPage } from "@shared/pages/BackupDetailPage";
import { ClaimChips } from "@shared/assurance/ClaimChips";
import { renderClaim } from "@shared/assurance/claims";
import { createHttpAssurance } from "@shared/assurance/realAssurance";
import { downloadOutcomeBadge, downloadOutcomeOf, downloadOutcomeText } from "@shared/assurance/feedText";
import { artifactPath } from "@shared/utilities/routes";
import { resetGraphForTests } from "@shared/state/graph";
import type { LiveActivity, SetActivityEvent } from "@shared/types/activity";
import type { ArtifactAssurance } from "@shared/assurance/types";
import type { BackupArtifact, BackupPlacement } from "@shared/types/backup";

import backupsSrc from "../pages/BackupsPage.tsx?raw";
import tokensCss from "../design-system/tokens.css?raw";
import assuranceCss from "../assurance/assurance.css?raw";

afterEach(() => { cleanup(); resetGraphForTests(); });

const NOW = Date.parse("2026-10-12T00:00:00Z");
const T = "2026-10-11T00:00:00Z";
const BOUNDARY =
  "This product recovers and exports the retained artifact; restoring it into an application remains the operator's responsibility.";

describe("#1070 the Backups footer says the boundary, in the secondary ink", () => {
  it("states the boundary sentence, not 'retained, verified copies'", async () => {
    render(
      <MemoryRouter>
        <ApiProvider api={createMockApi()}>
          <BackupsPage readOnly={false} />
        </ApiProvider>
      </MemoryRouter>
    );
    const note = await screen.findByText(BOUNDARY);
    expect(note.textContent).not.toMatch(/verified copies/);
    expect(document.body.textContent).not.toMatch(/does not perform application restore/);
  });

  it("draws the boundary sentence in var(--text-2), never --text-3", () => {
    const at = backupsSrc.indexOf("remains the operator");
    expect(at).toBeGreaterThan(0);
    const span = backupsSrc.slice(backupsSrc.lastIndexOf("<span", at), at);
    expect(span).toContain("var(--text-2)");
    expect(span).not.toContain("var(--text-3)");
  });
});

const copy = (over: Partial<BackupPlacement> = {}): BackupPlacement => ({
  medium: "local", mediumType: "local", location: "/x", sizeBytes: 1, storageClass: "",
  verificationClass: null, verifiedAt: null, access: "immediate", status: "ACTIVE", ...over
});
const verified = (over: Partial<BackupPlacement> = {}) => copy({ verificationClass: "content", verifiedAt: T, assuranceState: "passed", ...over });

describe("D3 a bare 'Content verified' is never drawn while a copy is unknown", () => {
  const base = { artifactId: "s/a/x", content: { status: "pass", achievedClass: "content", at: T }, recovery: { status: "never_run", achievedClass: "" } } as const;

  it("one verified copy out of three says so, and says what the other two are", () => {
    const r = renderClaim("content", base.content, NOW, ["content", "unchecked", "unchecked"]);
    expect(r.state).toBe("pass");
    expect(r.label).toBe("Content verified · 1 of 3 copies, 2 never checked");
  });

  it("names the weaker states, the least a copy says first: could not complete, then attested only", () => {
    expect(renderClaim("content", base.content, NOW, ["content", "existence"]).label)
      .toBe("Content verified · 1 of 2 copies, 1 existence only");
    expect(renderClaim("content", base.content, NOW, ["content", "attested", "could_not_complete"]).label)
      .toBe("Content verified · 1 of 3 copies, 1 could not complete, 1 attested only");
  });

  it("is bare only when every copy is content verified, or when the one copy is", () => {
    expect(renderClaim("content", base.content, NOW, ["content", "content"]).label).toBe("Content verified");
    expect(renderClaim("content", base.content, NOW, ["content"]).label).toBe("Content verified");
  });

  it("is not bare when several copies exist and the server did not itemise them", () => {
    const chips = (copies: number) => (
      <ClaimChips assurance={{ ...base, copies, copyContent: undefined } as ArtifactAssurance} />
    );
    const { unmount } = render(chips(3));
    const label = document.querySelector('[data-claim="content"] [data-chip]')!.textContent ?? "";
    expect(label).toContain("Content verified");
    expect(label).not.toMatch(/Content verified(?!\s*·)/);
    unmount();
  });

  it("the chip carries the qualified words on the page, with the partial cue", () => {
    render(<ClaimChips assurance={{ ...base, copies: 3, copyContent: ["content", "unchecked", "unchecked"] } as ArtifactAssurance} />);
    const chip = document.querySelector('[data-claim="content"]')!;
    expect(chip.textContent).toContain("Content verified · 1 of 3 copies, 2 never checked");
    expect(chip.getAttribute("data-coverage")).toBe("partial");
    cleanup();
    render(<ClaimChips assurance={{ ...base, copies: 2, copyContent: ["content", "content"] } as ArtifactAssurance} />);
    expect(document.querySelector('[data-claim="content"]')!.getAttribute("data-coverage")).toBeNull();
  });

  it("the production composition reports each copy, so the chip is built from copies and not the strongest one", async () => {
    const art = {
      id: "s/a/x", setId: "s/a", placements: [
        verified(),
        copy({ medium: "offsite", mediumType: "s3" }),
        copy({ medium: "cold", mediumType: "s3", verificationClass: "existence", verifiedAt: T }),
        copy({ medium: "gone", status: "DELETE_PENDING" })
      ], quarantine: null
    } as unknown as BackupArtifact;
    const api = { ...createMockApi("assurance-unavailable"), listArtifacts: async () => [art], getArtifact: async () => art } as RetndApi;
    const a = await createHttpAssurance(api).getArtifactAssurance(art.id);
    expect(a.copies).toBe(3);
    expect(a.copyContent).toEqual(["content", "unchecked", "existence"]);
    expect(renderClaim("content", a.content, NOW, a.copyContent).label).toBe("Content verified · 1 of 3 copies, 1 never checked, 1 existence only");
  });
});

/** The edge of a chip state: its rule's border-color, else the plain chip's. */
function edgeOf(state: string): string {
  const rule = new RegExp('\\[data-state="' + state + '"\\]\\s*>\\s*\\.achip\\s*\\{([^}]*)\\}').exec(assuranceCss);
  const own = rule && /border-color:\s*(var\(--[a-z0-9-]+\))/.exec(rule[1]);
  if (own) return own[1];
  return /\.achip\s*\{[^}]*border:\s*[\d.]+px solid (var\(--[a-z0-9-]+\))/.exec(assuranceCss)![1];
}
function fillOf(state: string): string {
  const rule = new RegExp('\\[data-state="' + state + '"\\]\\s*>\\s*\\.achip\\s*\\{([^}]*)\\}').exec(assuranceCss);
  return (rule && /background:\s*(var\(--[a-z0-9-]+\))/.exec(rule[1]))?.[1] ?? "var(--surface)";
}
function tokenHex(theme: "light" | "dark", name: string): [number, number, number] {
  const block = theme === "light" ? /:root\s*\{([^}]*)\}/.exec(tokensCss)![1] : /\[data-theme="dark"\]\s*\{([^}]*)\}/.exec(tokensCss)![1];
  const m = new RegExp(name + "\\s*:\\s*#([0-9a-f]{6})", "i").exec(block);
  if (!m) throw new Error("no hex " + name + " in " + theme);
  return [0, 2, 4].map((i) => parseInt(m[1].slice(i, i + 2), 16)) as [number, number, number];
}
const lum = ([r, g, b]: number[]) => {
  const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
};
const ratio = (a: number[], b: number[]) => { const [h, l] = [lum(a), lum(b)].sort((x, y) => y - x); return (h + 0.05) / (l + 0.05); };

describe("D8 the Not configured chip edge is a UI boundary: 3:1 against its fill and the page", () => {
  for (const theme of ["light", "dark"] as const) {
    for (const state of ["never_configured", "no_copy"]) {
      it(state + " edge, " + theme, () => {
        const edge = edgeOf(state).replace(/var\((--[a-z0-9-]+)\)/, "$1");
        const fill = fillOf(state).replace(/var\((--[a-z0-9-]+)\)/, "$1");
        const e = tokenHex(theme, edge);
        expect(ratio(e, tokenHex(theme, fill)), "against its fill " + fill).toBeGreaterThanOrEqual(3);
        expect(ratio(e, tokenHex(theme, "--surface")), "against the card").toBeGreaterThanOrEqual(3);
        expect(ratio(e, tokenHex(theme, "--bg")), "against the page").toBeGreaterThanOrEqual(3);
      });
    }
  }
  it("control: the old edge (--text-3 on --surface-3) is under 3:1 in the light theme", () => {
    expect(ratio(tokenHex("light", "--text-3"), tokenHex("light", "--surface-3"))).toBeLessThan(3);
  });
});

// How a download ended, from the engine's own rows (core/service/recover_stream.go).
const ART = "production/postgres-primary/postgres-prod-20260828.dump.zst";
let seq = 100;
function row(operation: string, fields: Record<string, string>, level: SetActivityEvent["level"], artifact = ART): SetActivityEvent {
  return {
    sequence: ++seq, at: T, level, event: "api_action", scope: "deployment", message: operation,
    fields: { actor: "admin", route: "GET /api/v1/recoveries/{grant}", artifact, operation, ...fields }
  };
}
const completed = (a = ART) => row("recovery verified", { outcome: "ok", bytes: "5000", total_bytes: "5000" }, "info", a);
const interrupted = (a = ART) => row("recovery interrupted", { outcome: "interrupted", bytes: "1200", total_bytes: "5000", reached: "1200" }, "warn", a);
const failedVerification = (a = ART) =>
  row("recovery failed verification", { outcome: "failed_verification", bytes: "2048", total_bytes: "5000", reached: "2048", error_code: "RECOVERY_LOCAL_COPY_CORRUPT" }, "error", a);

describe("item 14 each way a download ends is its own outcome, and none is a success", () => {
  it("reads three distinct outcomes off the feed", () => {
    const got = [completed(), interrupted(), failedVerification()].map((e) => downloadOutcomeOf([e], ART)!);
    expect(got.map((o) => o.kind)).toEqual(["completed", "interrupted", "failed_verification"]);
    expect(got[1]).toMatchObject({ bytes: 1200, total: 5000, reached: 1200 });
    expect(got[2]).toMatchObject({ bytes: 2048, total: 5000 });
  });

  it("badges and sentences are three different strings; the shorts name N of M", () => {
    const got = [completed(), interrupted(), failedVerification()].map((e) => downloadOutcomeOf([e], ART)!);
    const badges = got.map(downloadOutcomeBadge);
    expect(badges).toEqual(["Completed", "Interrupted at 1200 of 5000 bytes", "Failed verification"]);
    expect(new Set(got.map(downloadOutcomeText)).size).toBe(3);
    expect(downloadOutcomeText(got[1])).toMatch(/1200 of 5000 bytes/);
    expect(downloadOutcomeText(got[2])).toMatch(/does not match|damaged/);
  });

  it("takes the newest outcome for THIS artifact and ignores others and the per-range rows", () => {
    const earlier = interrupted();
    const later = completed();
    const other = failedVerification("s/other/y");
    const range = row("recovery range failed", { outcome: "failed", range: "bytes=0-9" }, "error");
    expect(downloadOutcomeOf([earlier, later, other, range], ART)!.kind).toBe("completed");
    expect(downloadOutcomeOf([later, earlier], ART)!.kind).toBe("completed");
    expect(downloadOutcomeOf([other, range], ART)).toBeNull();
  });

  it("an unreadable count is left out, never printed as NaN", () => {
    const o = downloadOutcomeOf([row("recovery interrupted", { outcome: "interrupted" }, "warn")], ART)!;
    expect(downloadOutcomeBadge(o)).toBe("Interrupted");
    expect(downloadOutcomeText(o)).not.toMatch(/NaN|undefined/);
  });

  function detailWith(events: SetActivityEvent[]) {
    const base = createMockApi();
    const api = {
      ...base,
      getLiveActivity: async (o?: Parameters<RetndApi["getLiveActivity"]>[0]) => {
        const l = await base.getLiveActivity(o);
        return { ...l, deployment: l.deployment ? { ...l.deployment, events: [...l.deployment.events, ...events] } : l.deployment } as LiveActivity;
      }
    } as RetndApi;
    return render(
      <MemoryRouter initialEntries={[artifactPath(ART)]}>
        <ApiProvider api={api}>
          <Routes><Route path="/backups/:source/:set/:name" element={<BackupDetailPage />} /></Routes>
        </ApiProvider>
      </MemoryRouter>
    );
  }

  for (const [name, ev, kind, words] of [
    ["completed", completed, "completed", /^Completed/],
    ["interrupted", interrupted, "interrupted", /Interrupted at 1200 of 5000 bytes/],
    ["failed verification", failedVerification, "failed_verification", /Failed verification/]
  ] as const) {
    it("the artifact page draws '" + name + "' as its own outcome and never as a pass", async () => {
      detailWith([ev()]);
      const box = await screen.findByTestId("download-outcome");
      expect(box.getAttribute("data-outcome")).toBe(kind);
      expect(box.querySelector("[data-badge]")!.textContent).toMatch(words);
      // No success rendering: not the pass state, not the success tokens, not the words of a pass.
      expect(box.closest("[data-state]")?.getAttribute("data-state")).not.toBe("pass");
      expect(box.outerHTML).not.toMatch(/data-state="pass"|--ok\b|success/);
      expect(box.textContent).not.toMatch(/Content verified|Recovery test passed|Recovered/);
    });
  }

  it("draws nothing when the feed holds no outcome for the artifact", async () => {
    detailWith([completed("s/other/y")]);
    await screen.findByText("postgres-prod-20260828.dump.zst");
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByTestId("download-outcome")).toBeNull();
  });

  it("the download CSS gives the three outcomes different shapes and no success token", () => {
    const rules = [...assuranceCss.matchAll(/\[data-outcome="([a-z_]+)"\][^{]*\{([^}]*)\}/g)];
    const names = new Set(rules.map((m) => m[1]));
    expect([...names].sort()).toEqual(["completed", "failed_verification", "interrupted"]);
    for (const m of rules) expect(m[2], m[1]).not.toMatch(/--ok/);
    const shape = (n: string) => rules.filter((m) => m[1] === n).map((m) => m[2].replace(/\s+/g, " ")).join("|");
    expect(new Set([shape("completed"), shape("interrupted"), shape("failed_verification")]).size).toBe(3);
  });
});
