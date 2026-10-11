// Prints the WCAG 2.x contrast ratio of the text and control pairs in the
// recovery and assurance surfaces, in both themes, as numbers (#988).
//
// Run it from the repository root, in the same arrangement the captures use:
//
//     node docs/site/tools/measure-recovery-contrast.mjs
//
// The rule is 4.5:1 for normal text and 3:1 for large text and for control
// boundaries. `check-contrast.mjs` is the gate (it fails on a violation and
// says nothing about a pass); this is the other half the project's rule asks
// for, which is to state the ratios that were measured rather than say "passes".
// Each pair is read from the browser's computed styles, against the colour the
// text actually sits on (the first ancestor with an opaque background), so an
// inherited colour, an opacity or an overlay that no design token names is in
// the number. It exits 1 if any measured pair is under its threshold.

import { openApp, settle, withDevServer } from "./harness.mjs";

const DETAIL = "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst";
const SET = "/sets/production/postgres-primary";

/** [label, path, scenario, steps(page) -> selector, large?] */
const PAIRS = [
  ["claim chip, content verified (pass)", DETAIL, "default", async () => '[data-claim="content"][data-state="pass"]', false],
  ["claim chip, recovery test failed", DETAIL, "assurance-split", async () => '[data-claim="recovery"][data-state="failed"]', false],
  ["claim chip, stored", DETAIL, "default", async () => '[data-claim="stored"]', false],
  ["claim chip, overdue", "/backups", "default", async () => '[data-claim="content"][data-state="overdue"]', false],
  ["claim chip, existence only", DETAIL, "assurance-existence", async () => '[data-claim="content"][data-state="existence_only"]', false],
  ["claim chip, requires restore", "/backups", "default", async () => '[data-claim="content"][data-state="requires_restore"]', false],
  [
    "recover dialog, body text",
    DETAIL,
    "default",
    async (page) => {
      await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
      return '[role="dialog"] label';
    },
    false
  ],
  [
    "recover dialog, the archive cost notice",
    "/backups/media/weekly-archive/media-week34.tar",
    "default",
    async (page) => {
      await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
      return '[role="group"][aria-label="This copy is archived"] strong';
    },
    false
  ],
  [
    "recover dialog, primary button",
    DETAIL,
    "default",
    async (page) => {
      await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
      return '[role="dialog"] .btn--primary';
    },
    false
  ],
  ["assurance section, command echo", SET, "default", async () => '[aria-label="Assurance"] code, [aria-label="Assurance"] .mono', false],
  [
    "operation panel, the progress log",
    SET,
    "assurance-slow-test",
    async (page) => {
      await page.getByRole("region", { name: "Assurance", exact: true }).getByRole("button", { name: "Run recovery test" }).click();
      await page.getByTestId("operation-progress").waitFor();
      return '[data-testid="operation-progress"] pre, [data-testid="operation-progress"] [data-testid="operation-line"]';
    },
    false
  ]
];

function ratioScript(sel) {
  return (s) => {
    const c = document.createElement("canvas").getContext("2d");
    const rgb = (v) => {
      c.clearRect(0, 0, 1, 1);
      c.fillStyle = "#000";
      c.fillStyle = v;
      c.fillRect(0, 0, 1, 1);
      const d = c.getImageData(0, 0, 1, 1).data;
      return [d[0], d[1], d[2], d[3] / 255];
    };
    const lum = (a) => {
      const f = (x) => ((x /= 255) <= 0.03928 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4));
      return 0.2126 * f(a[0]) + 0.7152 * f(a[1]) + 0.0722 * f(a[2]);
    };
    // Every element under a match that carries text of its own, because the
    // chip's colour is set on an inner span and not on the container the
    // selector names; measuring the container would measure the page's text.
    const own = (e) => [...e.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim());
    const els = [];
    for (const m of document.querySelectorAll(s)) {
      if (own(m)) els.push(m);
      for (const d of m.querySelectorAll("*")) if (own(d)) els.push(d);
    }
    let worst = null;
    for (const e of els) {
      const cs = getComputedStyle(e);
      if (cs.display === "none" || cs.visibility === "hidden" || e.closest(":disabled")) continue;
      let bg = null;
      for (let n = e; n; n = n.parentElement) {
        const x = rgb(getComputedStyle(n).backgroundColor);
        if (x[3] > 0.5) {
          bg = x;
          break;
        }
      }
      if (!bg) continue;
      const a = lum(rgb(cs.color));
      const b = lum(bg);
      const r = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      if (worst === null || r < worst.r) worst = { r, px: parseFloat(cs.fontSize), weight: cs.fontWeight, text: (e.textContent || "").trim().slice(0, 36) };
    }
    return worst;
  };
}

const rows = [];
let bad = 0;
await withDevServer(async (app) => {
  for (const [label, path, scenario, steps, large] of PAIRS) {
    for (const theme of ["light", "dark"]) {
      const { context, page } = await openApp(app, { path, scenario });
      await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), theme);
      await settle(page, 300);
      const sel = await steps(page);
      await settle(page, 300);
      const worst = await page.evaluate(ratioScript(sel), sel);
      await context.close();
      if (!worst) {
        rows.push([label, theme, "-", "-", "NOT FOUND"]);
        bad++;
        continue;
      }
      const isLarge = large || worst.px >= 24 || (worst.px >= 18.66 && parseInt(worst.weight) >= 700);
      const need = isLarge ? 3 : 4.5;
      const ok = worst.r >= need;
      if (!ok) bad++;
      rows.push([label, theme, worst.r.toFixed(2) + ":1", need + ":1", ok ? "ok" : "BELOW"]);
    }
  }
});

const w = Math.max(...rows.map((r) => r[0].length));
console.log("pair".padEnd(w), "theme".padEnd(6), "ratio".padEnd(9), "needs".padEnd(7), "result");
for (const r of rows) console.log(r[0].padEnd(w), r[1].padEnd(6), r[2].padEnd(9), r[3].padEnd(7), r[4]);
if (bad) {
  console.error(bad + " pair(s) below their threshold or not found");
  process.exit(1);
}
