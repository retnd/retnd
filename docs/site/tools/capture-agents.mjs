// Re-shoots the four clips of EPIC U's Agents card (#1353, FR-93): the mint-once
// dialog, a confirmation approved with a step-up, a confirmation refused by the
// shut operation gate, and the audit table.
//
// Run it from the repository root, through the Playwright lock (never bare), and
// only at the scratch stage: nothing in this epic's authoring phase runs a browser.
//
//     /Users/rom/workspace/xdg-pw-lock.sh node docs/site/tools/capture-agents.mjs
//
// The whole sequence the release wants, including the one capture script this
// epic did not change run FIRST, is docs/site/tools/capture-agents-all.sh.
//
// It writes GIFs into docs/site/screens/ under the `agents-` prefix and prints
// one line per file. Flags:
//
//   --check          shoot everything twice and fail unless every file is
//                    byte-identical to the run before (the property the release
//                    leans on, and what "deterministic" means here)
//   --only <id>      one manifest id, for a quick look
//   --embeds html    print the <figure> markup for every entry and exit, with no
//   --embeds md      browser; `md` prints Markdown images. This is what goes into
//                    web-ui.html's Agents section after a capture run
//
// # What decides the list
//
// docs/site/tools/agents-captures.json is the one list of what the epic owes:
// ids, file names, alt text, captions and the mock scenario each clip runs in.
// This file holds a `gifs` entry for every id in it and refuses to start when the
// two disagree.
//
// # The arrangement, which is harness.mjs's
//
// The mock API instead of any deployment (a capture pointed at a real instance
// would claim the administrator account and burn the enrolment token), the auth
// stub, a Playwright BORROWED from the retnd-tests checkout at the sha in
// scripts/e2e/tests-repo.pin, and the pinned clock, locale and timezone
// (`page.clock.setFixedTime`, through openApp). Unpin the clock and every frame
// with a timestamp in it moves, which is exactly what --check is there to show.
// Without the checkout this script says so and stops before it starts a server:
// it never guesses and never installs one. Read harness.mjs first.
//
// # No token, no key material, no real host
//
// Before any frame is written, the page's text, its aria labels, titles,
// placeholders and field values are scanned for key material, an agent token
// (rat_ followed by the token alphabet, whole and not just the prefix the list
// shows), the canary values the specs plant and any host name that is not an RFC
// 2606 reserved one. A hit fails the run and nothing is written. The mint clip is
// the one place a token is on screen on purpose, and the mock's token is the
// fixed stand-in `rat_EXAMPLE...` the manifest's scenario returns, which the scan
// allows by name and nothing else.
//
// # What these clips cannot claim
//
// They are the real component tree rendering mock fixtures. The data is not
// real, the gate refusal is the mock's rendering of the real code, and none of it
// shows a deployment.

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Clip, loadPlaywright, mb, openApp, SCREENS, screensTotal, settle, VIEWPORT, withDevServer } from "./harness.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const manifest = JSON.parse(readFileSync(resolve(HERE, "agents-captures.json"), "utf8"));

const args = process.argv.slice(2);
const CHECK = args.includes("--check");
const flagValue = (f) => (args.includes(f) ? args[args.indexOf(f) + 1] : null);
const ONLY = flagValue("--only");
const EMBEDS = flagValue("--embeds");

const CARD = "[data-agents-card]";
// The mock's step-up password (MOCK_ADMIN_PASSWORD in ui/shared/src/api/mock.ts); it opens nothing.
const PASSWORD = "correct-horse-battery";
const DIALOG = '[role="dialog"]';

// ---------------------------------------------------------------------------
// The embedding hook, which needs no browser.

const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/"/g, "&quot;");

function embeds(kind) {
  return manifest.gifs
    .map((g) =>
      kind === "md"
        ? "![" + g.alt + "](docs/site/screens/" + g.name + ".gif)"
        : '<figure class="wide" id="agents-' + g.id + '">\n  <img src="screens/' + g.name + '.gif" alt="' + esc(g.alt) + '" loading="lazy">\n  <figcaption><span class="clip-tag">clip</span>' + esc(g.caption) + "</figcaption>\n</figure>"
    )
    .join("\n");
}

if (EMBEDS) {
  if (EMBEDS !== "html" && EMBEDS !== "md") {
    console.error("--embeds takes html or md");
    process.exit(2);
  }
  console.log(embeds(EMBEDS));
  process.exit(0);
}

// ---------------------------------------------------------------------------
// What may never be in a frame.

const KEY_MATERIAL = /AGE-SECRET-KEY|BEGIN [A-Z ]*PRIVATE KEY|PRIVATE KEY-----|CANARY|\bage1[a-z0-9]{58}\b|\bage1pq1[a-z0-9]{100,}/i;
// A whole token. The list shows eight characters and the mint dialog shows the
// mock's fixed stand-in; anything else this long is a token the mock did not mean to show.
const TOKEN = /\brat_[A-Za-z0-9_-]{20,}/;
const STAND_IN = /^rat_EXAMPLE[A-Za-z0-9_-]*$/;
const HOSTISH = /\b[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.(?:com|net|org|io|dev|app|cloud|de|eu|uk|us|ch|nl|fr|se|info|biz|co)\b/gi;
const SAFE_HOST = /(^|\.)(example\.(com|net|org)|retnd\.[a-z]+|github\.com|ghcr\.io)$/i;

async function visibleCorpus(page) {
  return page.evaluate(() => {
    const parts = [document.body.innerText];
    for (const el of document.querySelectorAll("[aria-label],[title],[placeholder],input,textarea")) {
      for (const a of ["aria-label", "title", "placeholder"]) if (el.getAttribute(a)) parts.push(el.getAttribute(a));
      if ("value" in el && typeof el.value === "string" && el.type !== "checkbox" && el.type !== "radio" && el.type !== "password") parts.push(el.value);
    }
    return parts.join("\n");
  });
}

function corpusProblems(text) {
  const out = [];
  const k = KEY_MATERIAL.exec(text);
  if (k) out.push("key material or a canary value (" + JSON.stringify(k[0].slice(0, 20)) + ")");
  const tok = TOKEN.exec(text);
  if (tok && !STAND_IN.test(tok[0])) out.push("an agent token that is not the mock's stand-in (" + JSON.stringify(tok[0].slice(0, 12)) + "...)");
  for (const h of text.match(HOSTISH) ?? []) if (!SAFE_HOST.test(h)) out.push("a host name that is not reserved: " + h);
  return out;
}

async function assertClean(page, label) {
  const problems = corpusProblems(await visibleCorpus(page));
  if (problems.length) throw new Error(label + " would photograph " + problems.join("; ") + ". Nothing was written for it.");
}

// ---------------------------------------------------------------------------
// The clips. Each waits for the thing itself, never for a number of milliseconds.
// The names below are the Agents card's own, from the design note
// (docs/design/1350-agents.html) and the specification's vocabulary: Agents,
// Token, Scope, Waiting for you, Approve, Deny, Audit.

const card = (page) => page.locator(CARD);
const dialog = (page, name) => page.getByRole("dialog", name ? { name } : {});
// Puts an element at the top of the window, so the clip frames it and not
// whatever the page had above it. scrollIntoViewIfNeeded leaves a tall card
// where it is as soon as any of it shows.
// "instant", because the page scrolls smoothly and a frame taken mid-glide is
// a different picture on every run.
// The other Settings cards are still reading when the Agents card first draws,
// and each that finishes pushes it down, so the page settles first.
const top = async (locator) => {
  await settle(locator.page(), 1500);
  await locator.evaluate((e) => e.scrollIntoView({ block: "start", behavior: "instant" }));
  await settle(locator.page(), 300);
};

const gifs = {
  mint: {
    // No #fragment: openApp appends ?scenario= and a fragment would swallow it.
    path: "/settings",
    async run(page, clip) {
      await card(page).getByRole("heading", { name: "Agents" }).waitFor();
      await card(page).locator('[data-agents-table="tokens"]').waitFor();
      await top(card(page));
      await clip.frame(1.2);
      await card(page).locator('[data-agents-region="tokens"]').getByRole("button", { name: "Create token" }).click();
      const form = dialog(page, "Create token");
      await form.getByLabel("Name", { exact: true }).fill("triage");
      await clip.frame(1.0);
      await form.getByRole("button", { name: "Create token" }).click();
      const shown = dialog(page, /^Your token for agent:/);
      await shown.getByText(/only time this token is shown/).waitFor();
      await clip.frame(2.0);
      await shown.getByRole("button", { name: "I have stored it" }).click();
      await card(page).locator('[data-token="triage"]').waitFor();
      await clip.frame(1.8);
    }
  },
  approve: {
    // No #fragment: openApp appends ?scenario= and a fragment would swallow it.
    path: "/settings",
    async run(page, clip) {
      const row = card(page).locator('[data-confirmation="cf_02"]');
      await row.waitFor();
      await top(card(page).locator('[data-agents-region="confirmations"]'));
      await clip.frame(1.6);
      await row.getByRole("button", { name: "Approve", exact: true }).click();
      await row.getByLabel("Your password").waitFor();
      await clip.frame(1.2);
      await row.getByLabel("Your password").fill(PASSWORD);
      await row.getByRole("button", { name: "Approve and run" }).click();
      await card(page).locator("[data-agents-notice]").getByText("Approved. The request ran.").waitFor();
      // The audit reloads after a decision; the frame waits for the replay's row.
      await card(page).locator('[data-agents-region="audit"] tr[data-tool="recovery_test_start"]').first().waitFor();
      await settle(page, 400);
      await clip.frame(1.8);
    }
  },
  refused: {
    // No #fragment: openApp appends ?scenario= and a fragment would swallow it.
    path: "/settings",
    async run(page, clip) {
      const row = card(page).locator('[data-confirmation="cf_01"]');
      await row.waitFor();
      await top(card(page).locator('[data-agents-region="confirmations"]'));
      await clip.frame(1.2);
      await row.getByRole("button", { name: "Approve", exact: true }).click();
      await row.getByLabel("Your password").fill(PASSWORD);
      await row.getByRole("button", { name: "Approve and run" }).click();
      await card(page).locator("[data-result-strip] [data-strip-code]").getByText("DESTRUCTIVE_OPERATIONS_DISABLED").waitFor();
      await clip.frame(2.4);
    }
  },
  audit: {
    // No #fragment: openApp appends ?scenario= and a fragment would swallow it.
    path: "/settings",
    async run(page, clip) {
      const audit = card(page).locator('[data-agents-region="audit"]');
      await audit.locator('[data-agents-table="audit"]').waitFor();
      await top(audit);
      await clip.frame(1.6);
      await audit.getByLabel("Token", { exact: true }).selectOption({ label: "agent:recovery-drill" });
      await audit.locator('tbody tr[data-tool="run_cycle"]').first().waitFor();
      await clip.frame(1.8);
    }
  }
};

// ---------------------------------------------------------------------------
// The manifest and this file must agree before anything runs.

function agree() {
  const problems = [];
  for (const g of manifest.gifs) if (!gifs[g.id]) problems.push("manifest gif " + g.id + " has no gifs entry");
  for (const id of Object.keys(gifs)) if (!manifest.gifs.some((g) => g.id === id)) problems.push("gifs entry " + id + " is not in the manifest");
  for (const g of manifest.gifs) if (!/^agents-[a-z0-9-]+$/.test(g.name)) problems.push("gif " + g.id + " has the file name " + g.name + ", which is not agents-<name>");
  for (const g of manifest.gifs) if (!g.alt?.trim() || !g.caption?.trim()) problems.push("gif " + g.id + " has no alt text or caption");
  if (ONLY && !manifest.gifs.some((g) => g.id === ONLY)) problems.push("--only " + ONLY + " is not a manifest id");
  return problems;
}

const disagreement = agree();
if (disagreement.length) {
  console.error("capture-agents: the manifest and this script disagree:\n  " + disagreement.join("\n  "));
  process.exit(2);
}

// No pinned retnd-tests checkout to borrow Playwright from: say so and stop,
// before a dev server is started or anything is guessed.
try {
  loadPlaywright();
} catch (e) {
  console.error(
    "capture-agents: refusing to run. There is no Playwright to borrow from the retnd-tests checkout " +
      "pinned in scripts/e2e/tests-repo.pin, and this script does not install one.\n\n" + String(e && e.message ? e.message : e)
  );
  process.exit(2);
}

// ---------------------------------------------------------------------------
// Shooting.

const WINDOW = { x: 0, y: 0, width: VIEWPORT.width, height: VIEWPORT.height };
const GIF_WIDTH = 900;

/** Wraps frame() so every frame is scanned before it is taken. */
function scanned(page, clip, label) {
  const frame = clip.frame.bind(clip);
  clip.frame = async (...a) => {
    // Off every control, so a tooltip the last click left open is not in the
    // picture: the blank margin right of the content column.
    await page.mouse.move(VIEWPORT.width - 10, VIEWPORT.height / 2);
    await assertClean(page, label);
    return frame(...a);
  };
  return clip;
}

async function shootGifs(app) {
  const written = [];
  for (const g of manifest.gifs) {
    if (ONLY && ONLY !== g.id) continue;
    const spec = gifs[g.id];
    const { context, page } = await openApp(app, { path: spec.path, scenario: g.scenario, viewport: VIEWPORT });
    await page.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
    await settle(page, 400);
    // The dock would sit on top of whatever is photographed.
    const hide = page.getByRole("button", { name: "Hide terminal" });
    if (await hide.count()) await hide.click();
    const clip = scanned(page, new Clip(page, g.name, { clip: WINDOW, width: GIF_WIDTH }), g.name);
    await spec.run(page, clip);
    await page.mouse.move(0, 0);
    await clip.write();
    written.push(g.name);
    await context.close();
  }
  return written;
}

const digest = (name) => createHash("sha256").update(readFileSync(join(SCREENS, name + ".gif"))).digest("hex");
const digests = (names) => Object.fromEntries(names.map((n) => [n + ".gif", digest(n)]));

if (!CHECK) {
  const names = await withDevServer(shootGifs);
  const t = screensTotal();
  console.log(names.length + " GIFs written to docs/site/screens/ (" + t.count + " files there, " + mb(t.bytes) + ")");
} else {
  // Two full passes against ONE dev server (a second server in the same process
  // races the first one's port), each hashed straight after it is written, because
  // those bytes are the ones that get committed. Fresh browser contexts per clip.
  let first;
  let second;
  await withDevServer(async (app) => {
    const names = await shootGifs(app);
    first = digests(names);
    await shootGifs(app);
    second = digests(names);
  });
  const moved = Object.keys(first).filter((n) => first[n] !== second[n]);
  if (moved.length) {
    console.error("NOT REPRODUCIBLE: " + moved.length + " of " + Object.keys(first).length + " files differ between two runs:\n  " + moved.join("\n  "));
    process.exit(1);
  }
  console.log("reproducible: " + Object.keys(first).length + " files, byte-identical across two runs");
}
