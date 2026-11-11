// Re-shoots the pictures and the short GIFs of EPIC N: retained-copy
// encryption, recoverable key custody and the copy states beside them (#998),
// and of EPIC T's encryption suites (#1320): the Suite select, the
// rotate-then-confirm flow, the per-copy suite chip, the deprecated health
// state and the re-encrypt dry run, ids encryption-25 onwards in the manifest.
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
// terminal dock never sits on the region being photographed. Every still is
// driven at 1280px and only narrowed for the shutter, and the 360px pictures are
// of the photographed element in a 360px content column, because the shell
// itself has no 360px layout (#1337); contentColumn says how. GIFs are one
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
import { copyFileSync, mkdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Clip, loadPlaywright, mb, openApp, optimise, quiet, SCREENS, screensTotal, settle, shot, VIEWPORT, withDevServer } from "./harness.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const manifest = JSON.parse(readFileSync(resolve(HERE, "encryption-captures.json"), "utf8"));

const args = process.argv.slice(2);
const CHECK = args.includes("--check");
const flagValue = (f) => (args.includes(f) ? args[args.indexOf(f) + 1] : null);
const ONLY = flagValue("--only");
const EMBEDS = flagValue("--embeds");

// An artifact-engine set. postgres-primary is a kopia set in the mock (#788),
// and since #1296 a kopia set's card is the "already encrypted by the kopia
// engine" statement with its radios disabled, which is not what 12 to 14 show.
const SET = "/sets/production/auth-config";
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

// age1pq1 is the age-pq/v1 recipient (#1320): public like the X25519 one, and
// kept out of a picture for the same reason, with about 2000 characters where
// the X25519 recipient has 58 after its prefix. Matched on its prefix and a
// short run, not on its full length: ClippedText (#1318) draws any long value
// as its first 80 characters and an ellipsis, so a recipient that reached a
// page would be in the DOM as 80 characters, which a {100,} rule never sees
// (and the canary tail the mock plants would be clipped off with it).
const KEY_MATERIAL = /AGE-SECRET-KEY|AGE-PLUGIN-|BEGIN [A-Z ]*PRIVATE KEY|PRIVATE KEY-----|CANARY|\bage1pq1[a-z0-9]{8,}|\bage1[a-z0-9]{20,}/i;
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

/** The action row's Rotate key, once the card has its keys. While the keys are
 *  still loading the card can briefly draw the "No active key for suite"
 *  banner, which has its own Rotate key, beside a disabled action-row one; a
 *  click then either hit the wrong button or failed on two matches. */
async function rotateButton(page) {
  await card(page).locator("[data-key-id]").first().waitFor();
  const button = card(page).locator('button:not([role="alert"] button)', { hasText: /^Rotate key$/ });
  await button.waitFor();
  return button;
}

async function rotate(page) {
  await (await rotateButton(page)).click();
  await dialog(page, "Rotate the managed key?").getByRole("button", { name: "Rotate key" }).click();
}

async function openRetire(page) {
  await rotate(page);
  await card(page).getByRole("button", { name: "Retire", exact: true }).click();
  await dialog(page).getByRole("button", { name: "Retire key" }).waitFor();
}

async function selectedMode(page, label) {
  // click, not check: the radio is controlled and only reads checked once the
  // write has landed, which check() treats as a click that did nothing.
  await page.getByRole("radiogroup", { name: "Encryption for this set" }).getByLabel(label, { exact: true }).click();
  await page.waitForFunction(
    (want) => document.querySelector("[data-set-encryption] input[type=radio]:checked")?.closest("label")?.textContent?.trim() === want,
    label
  );
}

/** Opens the export dialog from the action row (the last "Export recovery
 *  bundle" button; the first one sits inside the missing-bundle notice, whose
 *  layout at 360px let a click through without opening anything in the first
 *  batch run), scrolls it into view and waits for the dialog itself. */
async function openExportDialog(page) {
  const button = card(page).getByRole("button", { name: "Export recovery bundle" }).last();
  await button.scrollIntoViewIfNeeded();
  await button.click();
  const dlg = dialog(page, "Export recovery bundle");
  await dlg.waitFor({ timeout: 10_000 });
  return dlg;
}

async function openVerify(page) {
  const copies = page.getByRole("region", { name: "Copies" });
  await copies.locator('[data-encryption-cell="offsite_s3"]').waitFor();
  await copies.getByRole("button", { name: "Verify recoverable" }).click();
  await dialog(page, /Verify recoverable/).waitFor();
}

/** Runs the verify to its end (the mock's operation record ends it) and waits
 *  for the chip the ending draws. */
async function endedVerify(page, chip) {
  await openVerify(page);
  const dlg = dialog(page, /Verify recoverable/);
  await dlg.getByRole("checkbox", { name: "I accept this read" }).check();
  await dlg.getByRole("button", { name: "Verify now" }).click();
  await dlg.getByRole("group", { name: chip }).waitFor({ timeout: 20_000 });
  return [DIALOG];
}

/** Picks a suite on the Settings card (#1318), which opens the change dialog. */
async function openSuiteChange(page, id) {
  // click, not check: the radio is controlled and stays unchecked until the
  // patch lands, by design (SuiteControls.tsx), so check() would throw.
  await card(page).locator(`[data-suite-option="${id}"] input[type=radio]`).click();
  await dialog(page, "Change the suite").waitFor();
}

/** The Backups table is wider than its card (1503px in a 1016px scroller at
 *  1280px) and the Encryption column is near its right end, and the Copies
 *  table does the same inside a 360px column. A clip of the table shows a strip
 *  of it, and a clip of the scroller left where it starts shows no Encryption
 *  column at all. So the scroller is scrolled the way a reader would, until the
 *  right edge of the encryption cells' contents (the chips are wider than the
 *  header) is in view, and the scroller is photographed. It is marked with the
 *  cell selector so contentColumn can scroll its copy the same way, since a
 *  clone does not keep a scroll position. Where the cells already fit, this
 *  scrolls nothing. */
async function revealEncryption(page, region, cells) {
  await page.evaluate(({ region, cells }) => {
    window.__captureReveal = (scroller) => {
      // An original under the hidden root in a 360px picture has no layout.
      if (scroller.offsetParent === null) return;
      const inside = scroller.querySelectorAll(scroller.getAttribute("data-capture-reveal"));
      if (!inside.length) throw new Error("no encryption cell inside the table being photographed");
      const edge = Math.max(...[...inside].map((e) => e.getBoundingClientRect().right));
      // Absolute, so running it again once the page has settled lands on the
      // same position whatever the first run saw.
      scroller.scrollLeft = Math.max(0, Math.ceil(scroller.scrollLeft + edge - scroller.getBoundingClientRect().right + 16));
    };
    const scroller = document.querySelector(region + " .table-scroll");
    if (!scroller) throw new Error("no .table-scroll in " + region);
    scroller.setAttribute("data-capture-reveal", cells + " *");
    window.__captureReveal(scroller);
  }, { region, cells });
}

async function revealEncryptionColumn(page) {
  await revealEncryption(page, "main", "[data-encryption-list-cell]");
  return ["main .table-scroll"];
}

async function revealCopiesEncryption(page) {
  await revealEncryption(page, '[aria-label="Copies"]', "[data-encryption-cell]");
  return ['[aria-label="Copies"]'];
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
      const dlg = await openExportDialog(page);
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
      await (await rotateButton(page)).click();
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
      return revealCopiesEncryption(page);
    }
  },
  "copies-key-missing": {
    path: DETAIL,
    async prep(page) {
      const copies = page.getByRole("region", { name: "Copies" });
      await copies.locator('[data-encryption-cell="offsite_s3"]').getByRole("group", { name: /^Key missing/ }).waitFor();
      return revealCopiesEncryption(page);
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
  "backups-list": {
    path: "/backups",
    async prep(page) {
      await page.locator('[data-list-copy="offsite_s3"]').first().waitFor();
      await page.getByRole("columnheader", { name: /Encryption/ }).waitFor();
      return revealEncryptionColumn(page);
    }
  },
  "settings-exported": {
    path: "/settings",
    async prep(page) {
      await card(page).getByText(/exported .*, not yet confirmed/).first().waitFor();
      return [CARD];
    }
  },
  "key-copies-count": {
    path: "/settings",
    async prep(page) {
      await card(page).getByRole("link", { name: /41 in Backups/ }).waitFor();
      return ['table[aria-label="Managed keys"]'];
    }
  },
  "verified-recoverable": {
    path: DETAIL,
    async prep(page) {
      const off = page.getByRole("region", { name: "Copies" }).locator('[data-encryption-cell="offsite_s3"]');
      await off.getByRole("group", { name: /^Encrypted/ }).waitFor();
      await off.getByRole("group", { name: /^Verified recoverable/ }).waitFor();
      return revealCopiesEncryption(page);
    }
  },
  "verify-matched": {
    path: DETAIL,
    async prep(page) {
      return endedVerify(page, /^Verified recoverable/);
    }
  },
  "verify-failed": {
    path: DETAIL,
    async prep(page) {
      return endedVerify(page, /^Decrypt failed/);
    }
  },
  "suite-select": {
    path: "/settings",
    async prep(page) {
      await card(page).locator('[data-suite-group] [data-suite-option="age-pq/v1"]').waitFor();
      return [CARD];
    }
  },
  "suite-rotate-confirm": {
    path: "/settings",
    async prep(page) {
      await openSuiteChange(page, "age-pq/v1");
      await dialog(page, "Change the suite").getByRole("button", { name: "Change suite" }).click();
      await page.locator('[data-suite-step="rotate"][data-state="now"]').waitFor();
      return [DIALOG];
    }
  },
  "suite-chips": {
    path: "/backups",
    async prep(page) {
      await page.locator("[data-echip-suite]", { hasText: "age/v1" }).first().waitFor();
      await page.locator("[data-echip-suite]", { hasText: "age-pq/v1" }).first().waitFor();
      return revealEncryptionColumn(page);
    }
  },
  "suite-deprecated-health": {
    path: "/",
    async prep(page) {
      await page.locator('[data-suite-health="age/v1"]').waitFor();
      return ["[data-encryption-health]"];
    }
  },
  "reencrypt-dry-run": {
    path: "/",
    async prep(page) {
      await page.locator('[data-suite-health="age/v1"]').getByRole("button", { name: "Re-encrypt under current suite" }).click();
      await dialog(page, "Re-encrypt under current suite").locator("[data-reencrypt-plan]").waitFor();
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

/** Scrolls the thing into view at once. Without behavior "instant" the app's
 *  smooth scrolling animates it, and a frame taken 120ms later photographed the
 *  page halfway there (the first frame of enable-flow showed the top of
 *  Settings and a strip of empty background instead of the card). */
async function intoView(page, selector, block) {
  await page.evaluate(({ s, block }) => {
    document.querySelector(s)?.scrollIntoView({ block, behavior: "instant" });
  }, { s: selector, block });
}

async function toTop(page, selector) {
  await intoView(page, selector, "start");
}

const gifs = {
  "enable-flow": {
    path: "/settings",
    async run(page, clip) {
      await card(page).getByRole("switch").waitFor();
      // The cards above this one finish loading after the switch is there and
      // push it down, so scroll once the page has settled, not before.
      await settle(page, 800);
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
      const dlg = await openExportDialog(page);
      await dlg.getByRole("button", { name: "Download bundle" }).waitFor();
      await clip.frame(1.2);
      await dlg.getByRole("checkbox", { name: /I understand this file can decrypt every copy/ }).check();
      await clip.frame(1.0);
      await dlg.getByRole("button", { name: "Download bundle" }).click();
      await dlg.waitFor({ state: "hidden" });
      await card(page).getByText(/exported .*, not yet confirmed/).first().waitFor();
      await toTop(page, CARD);
      await clip.frame(1.8);
    }
  },
  "suite-flow": {
    path: "/settings",
    scenario: "encryption-suite-change",
    async run(page, clip) {
      await card(page).locator("[data-suite-group]").waitFor();
      await settle(page, 800);
      await intoView(page, "[data-suite-group]", "center");
      await clip.frame(1.0);
      await openSuiteChange(page, "age-pq/v1");
      await clip.frame(1.3);
      await dialog(page, "Change the suite").getByRole("button", { name: "Change suite" }).click();
      await page.locator('[data-suite-step="rotate"][data-state="now"]').waitFor();
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

/** Settings scrolls smoothly (components.css), so where the page behind a
 *  dialog sits when the shutter opens depended on how far an animation had got,
 *  and the strip of page around a dialog moved between two runs. Captures
 *  scroll at once. */
async function noSmoothScroll(page) {
  await page.addStyleTag({ content: "html, main { scroll-behavior: auto !important; }" });
}

/** Re-run the column reveal once the page has settled, because a column that
 *  finished loading after the first reveal moved the chips under it. */
async function revealAgain(page) {
  await page.evaluate(() => {
    for (const e of document.querySelectorAll("[data-capture-reveal]")) window.__captureReveal?.(e);
  });
}

async function openStill(app, id, scenario, v) {
  const { context, page } = await openApp(app, {
    path: steps[id].path,
    scenario,
    // Every still is driven in the standard window, and a narrow variant is
    // narrowed only for the shutter (contentColumn). Driving the controls in
    // the squeezed 360px shell let a click land on the wrong thing.
    viewport: { width: VIEWPORT.width, height: TALL }
  });
  await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), v.theme);
  await noSmoothScroll(page);
  await settle(page, 400);
  await hideDock(page);
  await quiet(page);
  return { context, page };
}

/** The dock would sit on top of whatever is photographed. Waited for rather
 *  than counted: the dock renders a moment after the page, and a count() that
 *  ran first left it open in one run of two (encryption-08's first pass had the
 *  dock across the bottom of the card). */
async function hideDock(page) {
  const hide = page.getByRole("button", { name: "Hide terminal" });
  await hide.waitFor({ timeout: 5000 }).catch(() => {});
  if (await hide.count()) await hide.click();
  await page.getByRole("button", { name: "Show terminal" }).waitFor({ timeout: 5000 }).catch(() => {});
}

/** A picture of the wrong thing is worse than no picture. The first batch run
 *  left it to a person to wonder whether the 360px card pictures framed the
 *  card, so: the first clip selector has to match, be visible, be as wide as
 *  the window allows (at least 280px) and no wider than the window, and not be
 *  a sliver. */
async function assertFramed(page, clip, v, label) {
  const box = await page.locator(clip[0]).first().boundingBox();
  if (!box) throw new Error(label + ": " + clip[0] + " has no box, so there is nothing to frame");
  if (box.width > v.width + 1 || box.width < Math.min(280, v.width - 80) || box.height < 40) {
    throw new Error(label + ": " + clip[0] + " is " + Math.round(box.width) + "x" + Math.round(box.height) + " in a " + v.width + "px window, which is not the thing this picture is of");
  }
}

/**
 * The 360px variants.
 *
 * The shell has no narrow layout (no breakpoint in ui/shared, see
 * capture-recovery-assurance.mjs): at 360px it leaves the Settings column about
 * 150px wide and every card in it is squeezed under 50px, in the default
 * scenario too. That is older than this epic and tracked in #1337. What the
 * epic holds the card to is the 995 spec's rule: given the width a 360px
 * screen's content column has (360 less two 16px gutters), the card fits its
 * own content. So that is what these variants show. The real rendered nodes are
 * cloned, live form state included, into a 360px column with 16px gutters on
 * the page background, and the application root is taken out of layout for
 * the shutter. Dialogs get the same treatment: the dialog box asks for a
 * minimum width wider than a 360px window (449px measured), so it is cloned
 * into the column and laid out in flow at the column's width.
 */
async function contentColumn(page, clip, v) {
  if (v.width !== 360) return clip;
  await page.setViewportSize({ width: v.width, height: TALL });
  await page.evaluate(({ sels, width }) => {
    const states = [];
    const column = document.createElement("div");
    column.setAttribute("data-capture-column", "");
    column.style.cssText =
      "width:" + width + "px;padding:16px;box-sizing:border-box;display:flex;flex-direction:column;gap:16px;background:var(--bg);color:var(--text)";
    for (const sel of sels) {
      for (const el of document.querySelectorAll(sel)) {
        const copy = el.cloneNode(true);
        // cloneNode copies attributes, and React keeps checked and value as
        // properties, so carry those across or every radio reads unset.
        // The copy's radios are renamed first, or they would share a group
        // with the originals and checking one would uncheck the other.
        const from = el.querySelectorAll("input,textarea,select");
        copy.querySelectorAll("input,textarea,select").forEach((c, i) => {
          if (c.name) c.name = c.name + "-capture";
          states.push([c, from[i].checked, from[i].value]);
        });
        copy.style.width = "100%";
        copy.style.maxWidth = "100%";
        copy.style.flex = "none";
        copy.style.margin = "0";
        // A dialog is positioned over the page and given a minimum width
        // wider than a 360px window, so in the column it sits in flow.
        copy.style.position = "static";
        copy.style.transform = "none";
        copy.style.inset = "auto";
        copy.style.minWidth = "0";
        copy.style.maxHeight = "none";
        column.append(copy);
      }
    }
    document.getElementById("root").style.display = "none";
    document.body.prepend(column);
    for (const [c, checked, value] of states) {
      if ("checked" in c) c.checked = checked;
      if ("value" in c && c.type !== "radio" && c.type !== "checkbox") c.value = value;
    }
    for (const e of column.querySelectorAll("[data-capture-reveal]")) window.__captureReveal(e);
    window.scrollTo(0, 0);
  }, { sels: clip, width: v.width });
  return ["[data-capture-column]"];
}

async function shootStills(app) {
  const written = [];
  for (const s of capturable) {
    if (ONLY && ONLY !== s.id) continue;
    for (const v of manifest.variants) {
      const { context, page } = await openStill(app, s.id, s.scenario, v);
      // A hover tooltip from the last click would sit over the picture.
      await page.mouse.move(0, 0);
      const prepared = await steps[s.id].prep(page);
      await quiet(page);
      const clip = await contentColumn(page, prepared, v);
      await settle(page, 400);
      await revealAgain(page);
      await assertClean(page, s.name + v.suffix);
      await assertFramed(page, clip, v, s.name + v.suffix);
      await shot(page, s.name + v.suffix, clip, { pad: clip[0] === "[data-capture-column]" ? 0 : 16 });
      written.push(s.name + v.suffix);
      await context.close();
    }
  }
  return written;
}

/** Wraps frame() so every frame waits for a quiet page and is scanned before it is taken. */
function scanned(page, clip, label) {
  const frame = clip.frame.bind(clip);
  clip.frame = async (...a) => {
    // Settled first, for the reason quiet() gives: a frame is a still too.
    await quiet(page);
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
    await noSmoothScroll(page);
    await settle(page, 400);
    // The same as the stills: the dock would cover the lower third of every frame.
    await hideDock(page);
    await quiet(page);
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
    // CAPTURE_FIRST_PASS_DIR keeps the first pass's files, so a file that moves
    // can be compared with what it moved from rather than only reported.
    if (process.env.CAPTURE_FIRST_PASS_DIR) {
      mkdirSync(process.env.CAPTURE_FIRST_PASS_DIR, { recursive: true });
      for (const n of [...names.stills.map((x) => x + ".png"), ...names.gifNames.map((x) => x + ".gif")]) {
        copyFileSync(join(SCREENS, n), join(process.env.CAPTURE_FIRST_PASS_DIR, n));
      }
    }
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
