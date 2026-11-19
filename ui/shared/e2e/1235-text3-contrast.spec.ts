/**
 * Issue #1235: --text-3 (3.35:1 light, 3.85:1 dark on the page) is drawn as
 * text all over the product, and normal text needs 4.5:1. This walks the real
 * screens the mock scenarios can reach, in both themes, and measures every
 * text node's computed colour against its effective background (disabled
 * controls are exempt; borders and fills are not text and are not measured).
 *
 * Each failure prints one FAIL line: screen | theme | selector | text | ratio
 * (needs N) | fg on bg | "T3" when the text is drawn with --text-3. The
 * printed lines are the reproduction the PR quotes; the test fails while any
 * exist. Run it with:
 *
 *   npx playwright test -c e2e/playwright.config.ts e2e/1235-text3-contrast.spec.ts
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { contrastReport, open, setTheme } from "./helpers";

const SET = "/sets/production/postgres-primary";
const AUTH = "/sets/production/auth-config";
const RUN = "run-2026-09-13-0400";
const NEWEST = "/backups/production/postgres-primary/postgres-prod-20260829.dump.zst";
const OPS = "/assurance/operations/";

type Screen = {
  name: string;
  path: string;
  scenario?: string;
  wire?: boolean;
  /** Lets a screen be driven past its first paint (a dialog, a step). */
  drive?: (page: Page) => Promise<void>;
};

/** Opens every Edit control on the page (a set's cards each have one), so the
 *  fields, hints and environment editor behind them are on screen. */
async function editEverything(page: Page) {
  for (const b of await page.getByRole("button", { name: "Edit", exact: true }).all()) {
    if (await b.isVisible() && await b.isEnabled()) await b.click();
  }
  await page.waitForTimeout(500);
}

const SCREENS: Screen[] = [
  { name: "dashboard", path: "/" },
  { name: "dashboard-first-run", path: "/", scenario: "first-run" },
  { name: "dashboard-version-mismatch", path: "/", scenario: "version-mismatch" },
  { name: "dashboard-storage-critical", path: "/", scenario: "storage-critical" },
  { name: "dashboard-empty", path: "/", scenario: "empty" },
  { name: "dashboard-insecure-transport", path: "/", scenario: "insecure-transport" },
  { name: "dashboard-activity-unreadable", path: "/", scenario: "activity-unreadable" },
  { name: "sets", path: "/sets" },
  { name: "sets-empty", path: "/sets", scenario: "empty" },
  { name: "set-new-wizard", path: "/sets/new" },
  { name: "set-new-wizard-first-run", path: "/sets/new", scenario: "first-run" },
  { name: "set-postgres", path: SET },
  { name: "set-postgres-wired", path: SET, scenario: "assurance-split", wire: true },
  { name: "set-auth-config", path: AUTH },
  { name: "set-billing", path: "/sets/production/billing-mysql" },
  { name: "set-snapshot", path: SET, scenario: "assurance-snapshot-set" },
  { name: "set-gateway-profile", path: SET, scenario: "gateway-profile" },
  { name: "set-read-only-source", path: SET, scenario: "read-only-source" },
  { name: "set-snapshots", path: SET + "/snapshots" },
  { name: "set-snapshot-detail", path: SET + "/snapshots/" + RUN },
  { name: "set-restore", path: SET + "/restore" },
  { name: "set-snapshot-retention", path: SET + "/snapshot-retention" },
  { name: "set-edit-mode", path: SET, drive: editEverything },
  { name: "set-edit-mode-workflow-roots", path: "/sets/media/weekly-archive", scenario: "workflow-no-globals", drive: editEverything },
  { name: "settings-add-destination", path: "/settings", drive: async (page) => {
      await page.getByRole("button", { name: /Add a destination/ }).first().click();
      await page.waitForTimeout(500);
    } },
  { name: "settings-validator-edit-dialog", path: "/settings", scenario: "validators-states", drive: async (page) => {
      await page.getByRole("button", { name: "Edit manifest-check" }).click();
      await expect(page.getByRole("dialog", { name: "Edit validator" })).toBeVisible();
      await page.waitForTimeout(500);
    } },
  { name: "repositories", path: "/repositories" },
  { name: "repositories-new", path: "/repositories/new" },
  { name: "repositories-health", path: "/repositories/health" },
  { name: "repositories-maintenance", path: "/repositories/maintenance" },
  { name: "workflow-run", path: "/workflow-runs/wfr_2f91a4" },
  { name: "workflow-run-2", path: "/workflow-runs/wfr_7b03d9" },
  { name: "backups", path: "/backups" },
  { name: "backups-many", path: "/backups", scenario: "backups-many" },
  { name: "backups-wired", path: "/backups", scenario: "default", wire: true },
  { name: "backups-ingest-failed", path: "/backups", scenario: "backups-ingest-failed" },
  { name: "backup-detail", path: NEWEST },
  { name: "backup-detail-wired", path: NEWEST, scenario: "assurance-split", wire: true },
  { name: "backup-detail-validator-output", path: NEWEST, scenario: "assurance-validator-output", wire: true },
  { name: "operation-passed", path: OPS + "aop_1200_passed", scenario: "operation-history", wire: true },
  { name: "operation-failed", path: OPS + "aop_1200_failed", scenario: "operation-history", wire: true },
  { name: "operation-running", path: OPS + "aop_1200_scrub_running", scenario: "operation-history", wire: true },
  { name: "activity", path: "/activity" },
  { name: "quarantine", path: "/quarantine" },
  { name: "settings", path: "/settings" },
  { name: "settings-validators", path: "/settings", scenario: "validators-states" },
  { name: "settings-no-destinations", path: "/settings", scenario: "no-destinations" },
  { name: "settings-gate-shut", path: "/settings", scenario: "gate-shut" },
  { name: "settings-local-account", path: "/settings", scenario: "local-account-profile" },
  { name: "settings-backup-defaults", path: "/settings/backup-defaults" },
  { name: "catalog-recovery", path: "/catalog-recovery", scenario: "catalog-recovery" },
  { name: "catalog-recovery-default", path: "/catalog-recovery" }
];

/** The screens an operator reaches before signing in: the session read is
 *  answered 401 so the app draws its unauthenticated routes. */
const SIGNED_OUT = ["/login", "/enroll", "/forgot-password", "/reset-password?token=x", "/verify-email?token=x"];

type Row = Awaited<ReturnType<typeof contrastReport>>[number];

function fails(screen: string, theme: string, all: Row[]) {
  const seen = new Set<string>();
  const lines: string[] = [];
  for (const m of all) {
    if (m.ratio >= m.need) continue;
    const line = [
      "FAIL " + screen, theme, m.sel, JSON.stringify(m.text), m.ratio.toFixed(2) + ":1 (needs " + m.need + ")",
      "rgb(" + m.fg + ") on rgb(" + m.bg + ")", m.t3 ? "T3" : "other"
    ].join(" | ");
    if (seen.has(line)) continue;
    seen.add(line);
    lines.push(line);
  }
  for (const l of lines) console.log(l);
  return lines;
}

async function settle(page: Page) {
  await page.waitForLoadState("networkidle");
  await expect(page.locator("main, [data-testid], h1").first()).toBeVisible({ timeout: 20_000 });
  await page.waitForTimeout(400);
}

for (const theme of ["light", "dark"] as const) {
  for (const s of SCREENS) {
    test(`#1235 every text on ${s.name} meets AA (${theme})`, async ({ page }) => {
      await open(page, s.path, s.scenario ?? "default", { wire: s.wire });
      await settle(page);
      if (s.drive) await s.drive(page);
      // The theme is set after the screen has rendered, and asserted, so the
      // app's own boot cannot put it back (#1236 is the real fix for that).
      await setTheme(page, theme);
      const all = await contrastReport(page, "body");
      // A screen that drew nothing would pass vacuously.
      expect(all.length, s.name + " drew no text").toBeGreaterThan(5);
      expect(fails(s.name, theme, all)).toEqual([]);
    });
  }

  for (const path of SIGNED_OUT) {
    test(`#1235 every text on signed-out ${path} meets AA (${theme})`, async ({ page }) => {
      await page.route("**/api/v1/auth/session", (r) => r.fulfill({ status: 401, json: { error: "unauthenticated" } }));
      await page.goto(path + (path.includes("?") ? "&" : "?") + "scenario=default");
      await settle(page);
      await setTheme(page, theme);
      const all = await contrastReport(page, "body");
      expect(all.length, path + " drew no text").toBeGreaterThan(2);
      expect(fails("signed-out " + path, theme, all)).toEqual([]);
    });
  }
}
