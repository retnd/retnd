// Re-shoots the pictures and the short GIFs of EPIC N: retained-copy
// encryption, recoverable key custody and the copy states beside them (#998).
//
// Run it from the repository root, through the Playwright lock (never bare):
//
//     /Users/rom/workspace/xdg-pw-lock.sh node docs/site/tools/capture-encryption.mjs
//
// The whole sequence the release wants, including the one capture script this
// epic did not change run FIRST, is docs/site/tools/capture-encryption-all.sh.
//
// It writes PNGs and GIFs into docs/site/screens/ under the `encryption-`
// prefix and prints one line per file. Flags:
//
//   --check          shoot everything twice and fail unless every file is
//                    byte-identical to the run before (the property the release
//                    leans on, and what "deterministic" means here)
//   --only <id>      one manifest id (still or gif), for a quick look
//   --embeds html    print the <figure> markup for every entry and exit, with no
//   --embeds md      browser; `md` prints Markdown images for README.md. This is
//                    the hook #997 references: an id, a file name, alt text
//
// # What decides the list
//
// docs/site/tools/encryption-captures.json is the one list of what the epic
// owes: ids, file names, alt text, captions and the scenario each still is
// taken in. This file holds a `steps` entry for every id the manifest marks
// `capturable` and refuses to start when the two disagree, and
// core/tests/encryptiongate/matrix_test.go checks the same thing from the other
// side. An entry marked `needs-ui-state` is a picture the epic owes and the
// stack cannot yet produce (the mock reaches no such state); it is reported at
// the end of every run, never faked, and the release gate counts its files as
// missing until it becomes capturable.
//
// # The arrangement, which is harness.mjs's
//
// The mock API instead of any deployment (a capture pointed at a real instance
// would claim the administrator account and burn the enrolment token), the auth
// stub, a Playwright BORROWED from the retnd-tests checkout at the sha in
// scripts/e2e/tests-repo.pin, and the pinned clock, locale and timezone. Without
// that checkout this script says so and stops before it starts a server: it
// never guesses and never installs one. Read harness.mjs first.
//
// # Variants
//
// Every still is taken four times: light and dark at the standard 1280px
// window, and light and dark at 360px (the narrowest phone width the epic's own
// Playwright specs hold the card to). Windows are tall on purpose so the pinned
// terminal dock never sits on the region being photographed. GIFs are one
// variant, light at the standard window.
//
// # No key material, no canary, no real host
//
// Before any still or GIF frame is written, the page's text, its aria labels,
// titles, placeholders and field values are scanned for key material, the
// canary values the specs plant and any host name that is not an RFC 2606
// reserved one. A hit fails the run and nothing is written for that picture.
// This reads the DOM that is about to be photographed, which is the corpus the
// pixels are drawn from; it is not OCR of the finished image, and the mock
// holds no key material to begin with (mockEncryption.ts says so).
//
// # What these pictures cannot claim
//
// They are the real component tree rendering mock fixtures. None of them is a
// store-submission screenshot, which this script never makes, and none shows
// named progress stages for Encrypting or Decrypt-verifying: whether the engine
// publishes them is an undecided contract (verifyRows.ts), so the "verify" clip
// shows the one running state the UI has.

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Clip, loadPlaywright, mb, openApp, optimise, SCREENS, screensTotal, settle, shot, VIEWPORT, withDevServer } from "./harness.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const manifest = JSON.parse(readFileSync(resolve(HERE, "encryption-captures.json"), "utf8"));

const args = process.argv.slice(2);
const CHECK = args.includes("--check");
const flagValue = (f) => (args.includes(f) ? args[args.indexOf(f) + 1] : null);
const ONLY = flagValue("--only");
const EMBEDS = flagValue("--embeds");

const SET = "/sets/production/postgres-primary";
const DETAIL = "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst";
const CARD = "[data-encryption-card]";
const DIALOG = '[role="dialog"]';

// ---------------------------------------------------------------------------
// The embedding hook (#997), which needs no browser.

const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/"/g, "&quot;");

function embeds(kind) {
  const out = [];
  const v = (suffix) => manifest.variants.find((x) => x.suffix === suffix);
  for (const s of manifest.stills) {
    const base = "screens/" + s.name;
    if (kind === "md") {
      out.push("![" + s.alt + "](docs/site/screens/" + s.name + ".png)");
      continue;
    }
    out.push(
      '<figure id="enc-' + s.id + '">\n  <picture>\n' +
        (v("-narrow-dark") ? '    <source media="(max-width: 480px) and (prefers-color-scheme: dark)" srcset="' + base + '-narrow-dark.png">\n' : "") +
        (v("-narrow") ? '    <source media="(max-width: 480px)" srcset="' + base + '-narrow.png">\n' : "") +
        (v("-dark") ? '    <source media="(prefers-color-scheme: dark)" srcset="' + base + '-dark.png">\n' : "") +
        '    <img src="' + base + '.png" alt="' + esc(s.alt) + '" loading="lazy">\n  </picture>\n  <figcaption>' + esc(s.caption) + "</figcaption>\n</figure>"
    );
  }
  for (const g of manifest.gifs) {
    out.push(
      kind === "md"
        ? "![" + g.alt + "](docs/site/screens/" + g.name + ".gif)"
        : '<figure id="enc-' + g.id + '">\n  <img src="screens/' + g.name + '.gif" alt="' + esc(g.alt) + '" loading="lazy">\n  <figcaption>' + esc(g.caption) + "</figcaption>\n</figure>"
    );
  }
  return out.join("\n");
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
// What may never be in a picture.

const KEY_MATERIAL = /AGE-SECRET-KEY|BEGIN [A-Z ]*PRIVATE KEY|PRIVATE KEY-----|CANARY|\bage1[a-z0-9]{58}\b/i;
const HOSTISH = /\b[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.(?:com|net|org|io|dev|app|cloud|de|eu|uk|us|ch|nl|fr|se|info|biz|co)\b/gi;
const SAFE_HOST = /(^|\.)(example\.(com|net|org)|retnd\.[a-z]+|github\.com|ghcr\.io)$/i;

/** The words about to be photographed, for the scan below. */
async function visibleCorpus(page) {
  return page.evaluate(() => {
    const parts = [document.body.innerText];
    for (const el of document.querySelectorAll("[aria-label],[title],[placeholder],input,textarea")) {
      for (const a of ["aria-label", "title", "placeholder"]) if (el.getAttribute(a)) parts.push(el.getAttribute(a));
      if ("value" in el && typeof el.value === "string" && el.type !== "checkbox" && el.type !== "radio") parts.push(el.value);
    }
    return parts.join("\n");
  });
}

function corpusProblems(text) {
  const out = [];
  const k = KEY_MATERIAL.exec(text);
  if (k) out.push("key material or a canary value (" + JSON.stringify(k[0].slice(0, 20)) + ")");
  for (const h of text.match(HOSTISH) ?? []) if (!SAFE_HOST.test(h)) out.push("a host name that is not reserved: " + h);
  return out;
}

async function assertClean(page, label) {
  const problems = corpusProblems(await visibleCorpus(page));
  if (problems.length) {
    throw new Error(label + " would photograph " + problems.join("; ") + ". Nothing was written for it.");
  }
}

// ---------------------------------------------------------------------------
// The stills. `path` and `prep` per manifest id. `prep` leaves the page in the
// state to photograph and returns the selector(s) to clip to. It waits for the
// thing itself, never for a number of milliseconds.

const card = (page) => page.locator(CARD);
const dialog = (page, name) => page.getByRole("dialog", name ? { name } : {});

async function rotate(page) {
  await card(page).getByRole("button", { name: "Rotate key" }).click();
  await dialog(page, "Rotate the managed key?").getByRole("button", { name: "Rotate key" }).click();
}

async function openRetire(page) {
  await rotate(page);
  await card(page).getByRole("button", { name: "Retire", exact: true }).click();
  await dialog(page).getByRole("button", { name: "Retire key" }).waitFor();
}

async function selectedMode(page, label) {
  await page.getByRole("radiogroup", { name: "Encryption for this set" }).getByLabel(label, { exact: true }).check();
  await page.waitForFunction(
    (want) => document.querySelector("[data-set-encryption] input[type=radio]:checked")?.closest("label")?.textContent?.trim() === want,
    label
  );
}

async function openVerify(page) {
  const copies = page.getByRole("region", { name: "Copies" });
  await copies.locator('[data-encryption-cell="offsite_s3"]').waitFor();
  await copies.getByRole("button", { name: "Verify recoverable" }).click();
  await dialog(page, /Verify recoverable/).waitFor();
}

const steps = {
  "settings-off": {
    path: "/settings",
    async prep(page) {
      await card(page).getByRole("switch", { name: "Encrypt copies sent to destinations" }).waitFor();
      return [CARD];
    }
  },
  "enable-dialog": {
    path: "/settings",
    async prep(page) {
      await card(page).getByRole("switch").click();
      await dialog(page, "Enable retained-copy encryption").getByText("Who holds the key?").waitFor();
      return [DIALOG];
    }
  },
  "settings-on-unconfirmed": {
    path: "/settings",
    async prep(page) {
      await card(page).getByText("No confirmed recovery bundle").waitFor();
      return [CARD];
    }
  },
  "export-bundle": {
    path: "/settings",
    async prep(page) {
      await card(page).getByRole("button", { name: "Export recovery bundle" }).first().click();
      const dlg = dialog(page, "Export recovery bundle");
      await dlg.getByRole("checkbox", { name: /I understand this file can decrypt every copy/ }).check();
      await dlg.getByRole("button", { name: "Download bundle" }).waitFor();
      return [DIALOG];
    }
  },
  "bundle-confirmed": {
    path: "/settings",
    async prep(page) {
      await card(page).getByText(/confirmed /).first().waitFor();
      return [CARD];
    }
  },
  "external-reference": {
    path: "/settings",
    async prep(page) {
      await card(page).getByRole("button", { name: "Use an external key reference instead" }).click();
      const dlg = dialog(page);
      await dlg.getByLabel("Environment variable").check();
      await dlg.getByLabel("Variable name").fill("EMPTY");
      await dlg.getByRole("button", { name: "Test reference" }).click();
      await dlg.getByRole("group", { name: "Declared but unreachable" }).waitFor();
      return [DIALOG];
    }
  },
  "rotate-confirm": {
    path: "/settings",
    async prep(page) {
      await card(page).getByRole("button", { name: "Rotate key" }).click();
      await dialog(page, "Rotate the managed key?").waitFor();
      return [DIALOG];
    }
  },
  "rotated": {
    path: "/settings",
    async prep(page) {
      await rotate(page);
      await page.waitForFunction(() => document.querySelectorAll("[data-encryption-card] [data-key-id]").length === 2);
      return [CARD];
    }
  },
  "retire-refused": {
    path: "/settings",
    async prep(page) {
      await openRetire(page);
      await dialog(page).getByRole("button", { name: "Retire key" }).click();
      await dialog(page).locator("[data-retire-refusal]").waitFor();
      return [DIALOG];
    }
  },
  "key-missing-settings": {
    path: "/settings",
    async prep(page) {
      await card(page).getByText(/A required key cannot be resolved/).first().waitFor();
      return [CARD];
    }
  },
  "key-missing-dashboard": {
    path: "/",
    async prep(page) {
      await page.locator("[data-encryption-health]").getByText("A required key cannot be resolved").waitFor();
      return ["[data-encryption-health]"];
    }
  },
  "set-inherit": {
    path: SET,
    async prep(page) {
      await page.locator("[data-set-encryption]").getByText(/Inheriting: required/).waitFor();
      return ["[data-set-encryption]"];
    }
  },
  "set-required": {
    path: SET,
    async prep(page) {
      await selectedMode(page, "Required");
      return ["[data-set-encryption]"];
    }
  },
  "set-required-key-missing": {
    path: SET,
    async prep(page) {
      await page.locator("[data-set-encryption]").getByText("Required, key missing").waitFor();
      return ["[data-set-encryption]"];
    }
  },
  "copies-chips": {
    path: DETAIL,
    async prep(page) {
      const copies = page.getByRole("region", { name: "Copies" });
      await copies.locator('[data-encryption-cell="local"]').getByRole("group", { name: "Plaintext" }).waitFor();
      await copies.locator('[data-encryption-cell="offsite_s3"]').getByRole("group", { name: "Not decrypt-verified" }).waitFor();
      return ['[aria-label="Copies"]'];
    }
  },
  "copies-key-missing": {
    path: DETAIL,
    async prep(page) {
      const copies = page.getByRole("region", { name: "Copies" });
      await copies.locator('[data-encryption-cell="offsite_s3"]').getByRole("group", { name: /^Key missing/ }).waitFor();
      return ['[aria-label="Copies"]'];
    }
  },
  "verify-dialog": {
    path: DETAIL,
    async prep(page) {
      await openVerify(page);
      await dialog(page, /Verify recoverable/).getByRole("checkbox", { name: "I accept this read" }).check();
      await dialog(page, /Verify recoverable/).getByText(/retnd encryption verify .* --allow-egress/).waitFor();
      return [DIALOG];
    }
  },
  "verify-running": {
    path: DETAIL,
    async prep(page) {
      await openVerify(page);
      const dlg = dialog(page, /Verify recoverable/);
      await dlg.getByRole("checkbox", { name: "I accept this read" }).check();
      await dlg.getByRole("button", { name: "Verify now" }).click();
      await page.locator('[data-verify-running="true"]').waitFor();
      return [DIALOG];
    }
  }
};

// ---------------------------------------------------------------------------
// The GIFs. One variant: light, the standard window. The crop is the window
// itself (an explicit rectangle) because the dialogs are drawn over the page.

const WINDOW = { x: 0, y: 0, width: VIEWPORT.width, height: VIEWPORT.height };
const GIF_WIDTH = 900;

async function toTop(page, selector) {
  await page.evaluate((s) => document.querySelector(s)?.scrollIntoView({ block: "start" }), selector);
}

const gifs = {
  "enable-flow": {
    path: "/settings",
    async run(page, clip) {
      await card(page).getByRole("switch").waitFor();
      await toTop(page, CARD);
      await clip.frame(0.9);
      await card(page).getByRole("switch").click();
      await dialog(page, "Enable retained-copy encryption").getByText("Who holds the key?").waitFor();
      await clip.frame(1.3);
      await dialog(page, "Enable retained-copy encryption").getByRole("button", { name: "Enable and create key" }).click();
      await dialog(page, "Enable retained-copy encryption").waitFor({ state: "hidden" });
      await card(page).getByText("No confirmed recovery bundle").waitFor();
      await toTop(page, CARD);
      await clip.frame(1.6);
    }
  },
  "bundle-flow": {
    path: "/settings",
    scenario: "encryption-on",
    async run(page, clip) {
      await card(page).getByRole("button", { name: "Export recovery bundle" }).first().click();
      const dlg = dialog(page, "Export recovery bundle");
      await dlg.getByRole("button", { name: "Download bundle" }).waitFor();
      await clip.frame(1.2);
      await dlg.getByRole("checkbox", { name: /I understand this file can decrypt every copy/ }).check();
      await clip.frame(1.0);
      await dlg.getByRole("button", { name: "Download bundle" }).click();
      await card(page).locator("[data-bundle-receipt]").waitFor();
      await toTop(page, CARD);
      await clip.frame(1.8);
    }
  },
  "verify-flow": {
    path: DETAIL,
    scenario: "encryption-on",
    async run(page, clip) {
      await openVerify(page);
      const dlg = dialog(page, /Verify recoverable/);
      await clip.frame(1.3);
      await dlg.getByRole("checkbox", { name: "I accept this read" }).check();
      await clip.frame(1.0);
      await dlg.getByRole("button", { name: "Verify now" }).click();
      await page.locator('[data-verify-running="true"]').waitFor();
      await clip.frame(1.8);
    }
  }
};

// ---------------------------------------------------------------------------
// The manifest and this file must agree before anything runs.

const capturable = manifest.stills.filter((s) => s.state === "capturable");
const notCapturable = manifest.stills.filter((s) => s.state !== "capturable");

function agree() {
  const problems = [];
  for (const s of capturable) if (!steps[s.id]) problems.push("manifest still " + s.id + " has no steps entry");
  for (const id of Object.keys(steps)) if (!capturable.some((s) => s.id === id)) problems.push("steps entry " + id + " is not a capturable manifest still");
  for (const g of manifest.gifs) if (!gifs[g.id]) problems.push("manifest gif " + g.id + " has no gifs entry");
  for (const id of Object.keys(gifs)) if (!manifest.gifs.some((g) => g.id === id)) problems.push("gifs entry " + id + " is not in the manifest");
  if (ONLY && ![...manifest.stills, ...manifest.gifs].some((x) => x.id === ONLY)) problems.push("--only " + ONLY + " is not a manifest id");
  return problems;
}

const disagreement = agree();
if (disagreement.length) {
  console.error("capture-encryption: the manifest and this script disagree:\n  " + disagreement.join("\n  "));
  process.exit(2);
}

// No pinned retnd-tests checkout to borrow Playwright from: say so and stop,
// before a dev server is started or anything is guessed.
try {
  loadPlaywright();
} catch (e) {
  console.error(
    "capture-encryption: refusing to run. There is no Playwright to borrow from the retnd-tests checkout " +
      "pinned in scripts/e2e/tests-repo.pin, and this script does not install one.\n\n" + String(e && e.message ? e.message : e)
  );
  process.exit(2);
}

// ---------------------------------------------------------------------------
// Shooting.

const TALL = 2400;

async function openStill(app, id, scenario, v) {
  const { context, page } = await openApp(app, {
    path: steps[id].path,
    scenario,
    viewport: { width: v.width, height: TALL }
  });
  await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), v.theme);
  await settle(page, 400);
  // The dock would sit on top of whatever is photographed.
  const hide = page.getByRole("button", { name: "Hide terminal" });
  if (await hide.count()) await hide.click();
  return { context, page };
}

async function shootStills(app) {
  const written = [];
  for (const s of capturable) {
    if (ONLY && ONLY !== s.id) continue;
    for (const v of manifest.variants) {
      const { context, page } = await openStill(app, s.id, s.scenario, v);
      const clip = await steps[s.id].prep(page);
      // A hover tooltip from the last click would sit over the picture.
      await page.mouse.move(0, 0);
      await settle(page, 400);
      await assertClean(page, s.name + v.suffix);
      await shot(page, s.name + v.suffix, clip, { pad: 16 });
      written.push(s.name + v.suffix);
      await context.close();
    }
  }
  return written;
}

/** Wraps frame() so every frame is scanned before it is taken. */
function scanned(page, clip, label) {
  const frame = clip.frame.bind(clip);
  clip.frame = async (...a) => {
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
    const { context, page } = await openApp(app, {
      path: spec.path,
      scenario: spec.scenario ?? "default",
      viewport: VIEWPORT
    });
    await page.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
    await settle(page, 400);
    const clip = scanned(page, new Clip(page, g.name, { clip: WINDOW, width: GIF_WIDTH }), g.name);
    await spec.run(page, clip);
    await page.mouse.move(0, 0);
    await clip.write();
    written.push(g.name);
    await context.close();
  }
  return written;
}

async function shootAll(app) {
  const stills = await shootStills(app);
  const gifNames = await shootGifs(app);
  return { stills, gifNames };
}

const digest = (name, ext) => createHash("sha256").update(readFileSync(join(SCREENS, name + ext))).digest("hex");
const digests = ({ stills, gifNames }) => ({
  ...Object.fromEntries(stills.map((n) => [n + ".png", digest(n, ".png")])),
  ...Object.fromEntries(gifNames.map((n) => [n + ".gif", digest(n, ".gif")]))
});

function reportNotCaptured() {
  if (!notCapturable.length) return;
  console.log(
    "\nNOT CAPTURED: " + notCapturable.length + " manifest entries need a UI state the stack does not have yet.\n" +
      "The release gate counts their files as missing until they exist.\n" +
      notCapturable.map((s) => "  " + s.name + ": " + s.reason).join("\n")
  );
}

if (!CHECK) {
  const { stills, gifNames } = await withDevServer(shootAll);
  optimise(stills);
  const t = screensTotal();
  console.log(stills.length + " pictures and " + gifNames.length + " GIFs written to docs/site/screens/ (" + t.count + " files there, " + mb(t.bytes) + ")");
  reportNotCaptured();
} else {
  // Two full passes against ONE dev server (a second server in the same
  // process races the first one's port), each hashed straight after it is
  // written and quantised, because the quantised bytes are the ones that get
  // committed. Fresh browser contexts per picture, so nothing carries over.
  let first;
  let second;
  let names;
  await withDevServer(async (app) => {
    names = await shootAll(app);
    optimise(names.stills);
    first = digests(names);
    await shootAll(app);
    optimise(names.stills);
    second = digests(names);
  });
  const moved = Object.keys(first).filter((n) => first[n] !== second[n]);
  if (moved.length) {
    console.error("NOT REPRODUCIBLE: " + moved.length + " of " + Object.keys(first).length + " files differ between two runs:\n  " + moved.join("\n  "));
    process.exit(1);
  }
  console.log("reproducible: " + Object.keys(first).length + " files, byte-identical across two runs");
  reportNotCaptured();
}
