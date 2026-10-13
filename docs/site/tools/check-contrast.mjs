// The contrast rule, enforced against the rendered site (#1054).
//
// # The rule
//
// WCAG 2.x AA, evaluated per element against the colour of that element's
// actual background, in BOTH themes:
//
//   * 4.5:1 for normal text;
//   * 3:1 for large text (18pt = 24px, or 14pt = 18.66px when bold) and
//     for the boundary of a form control.
//
// scripts/contrast/check-tokens.py asks the same question of the colour
// tokens before any browser is involved. This script asks it of what the
// browser actually painted, which is the only place a literal colour, an
// opacity, a translucent overlay or an inherited colour shows up. It exists
// because the token check passed on a hero line that nobody could read:
// the failure was an inherited colour on a surface no token pair named.
//
// # What it does
//
// For every page in the site directory, in light and then dark, it loads the
// page, walks every visible text node, and computes
//
//   * the text colour, blended by the opacity of the element and every
//     ancestor, over
//   * the background, composited layer by layer from the root down through
//     every ancestor's background-color (translucent layers included).
//
// A background-image or gradient behind text cannot be reduced to one
// colour, so it is reported as an offender of its own kind rather than
// passed: a check that cannot tell says so.
//
// What it does not see, said plainly: hover, focus and other states (the
// token check covers the fills those use), text scrolled out of view inside
// an overflow container, and the narrow-viewport drawer. A pass is a pass
// for the default state at 1280px.
//
// Nothing is fixed here. It prints every offender and exits 1 when there is
// one, 2 when it could not run (no browser, no page, zero text nodes read).
// A run that read nothing is a failure on purpose: that is what a green
// check over an empty page would look like.
//
// # Running it
//
//   node docs/site/tools/check-contrast.mjs [--site DIR] [--only FILE]...
//
// playwright-core is resolved from PLAYWRIGHT_CORE_DIR (a node_modules
// parent) when set, else from the normal module path. scripts/contrast/
// run-browser-check.sh does the container, the install and the browsers.

import { createRequire } from "node:module";
import { readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

const args = process.argv.slice(2);
let siteDir = path.resolve(here, "..");
const only = [];
let selftest = false;
for (let i = 0; i < args.length; i++) {
  if (args[i] === "--site") siteDir = path.resolve(args[++i]);
  else if (args[i] === "--only") only.push(args[++i]);
  else if (args[i] === "--selftest") selftest = true;
  else {
    console.error(`unknown argument ${args[i]}`);
    process.exit(2);
  }
}

const require = createRequire(
  process.env.PLAYWRIGHT_CORE_DIR
    ? path.join(process.env.PLAYWRIGHT_CORE_DIR, "noop.js")
    : import.meta.url,
);
let chromium;
try {
  ({ chromium } = require("playwright-core"));
} catch (e) {
  console.error(`cannot load playwright-core: ${e.message}`);
  process.exit(2);
}

const pages = readdirSync(siteDir)
  .filter((f) => f.endsWith(".html"))
  .filter((f) => only.length === 0 || only.includes(f))
  .sort();
if (pages.length === 0) {
  console.error(`no .html pages in ${siteDir}`);
  process.exit(2);
}

// Runs in the page. Returns { read, offenders }.
function auditPage() {
  const probe = document.createElement("canvas").getContext("2d", { willReadFrequently: true });
  probe.canvas.width = probe.canvas.height = 1;
  // Any CSS colour string to [r,g,b,a] (a in 0..1). The canvas does the
  // parsing so rgb(), hex, oklch() and color() all arrive the same way.
  function parse(css) {
    probe.clearRect(0, 0, 1, 1);
    probe.fillStyle = "#000";
    probe.fillStyle = css;
    probe.fillRect(0, 0, 1, 1);
    const d = probe.getImageData(0, 0, 1, 1).data;
    if (d[3] === 0) return [0, 0, 0, 0];
    // getImageData premultiplies away precision at low alpha; for the
    // translucent layers this site uses that is within one level.
    return [d[0], d[1], d[2], d[3] / 255];
  }
  const over = (top, under) => {
    const a = top[3] + under[3] * (1 - top[3]);
    if (a === 0) return [0, 0, 0, 0];
    const mix = (i) => (top[i] * top[3] + under[i] * under[3] * (1 - top[3])) / a;
    return [mix(0), mix(1), mix(2), a];
  };
  const lin = (v) => {
    v /= 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  const lum = (c) => 0.2126 * lin(c[0]) + 0.7152 * lin(c[1]) + 0.0722 * lin(c[2]);
  const ratio = (a, b) => {
    const [hi, lo] = lum(a) >= lum(b) ? [a, b] : [b, a];
    return (lum(hi) + 0.05) / (lum(lo) + 0.05);
  };
  const hex = (c) => "#" + [0, 1, 2].map((i) => Math.round(c[i]).toString(16).padStart(2, "0")).join("");

  const canvasDefault = [255, 255, 255, 1];

  function chain(el) {
    const out = [];
    for (let e = el; e; e = e.parentElement) out.push(e);
    return out; // el first, root last
  }

  // Effective background behind `el`'s own content. Returns
  // { color, unresolved }.
  function backgroundBehind(el, includeSelf) {
    const layers = chain(el);
    if (!includeSelf) layers.shift();
    layers.reverse(); // root first
    let acc = canvasDefault;
    let unresolved = null;
    for (const e of layers) {
      const cs = getComputedStyle(e);
      if (cs.backgroundImage !== "none") unresolved = `${e.tagName.toLowerCase()}${e.className ? "." + String(e.className).split(/\s+/)[0] : ""} has background-image`;
      const bg = parse(cs.backgroundColor);
      if (bg[3] > 0) acc = over(bg, acc);
    }
    return { color: acc, unresolved };
  }

  function visible(el) {
    for (let e = el; e; e = e.parentElement) {
      const cs = getComputedStyle(e);
      if (cs.display === "none" || cs.visibility === "hidden" || cs.visibility === "collapse") return false;
      if (parseFloat(cs.opacity) === 0) return false;
      if (e.hasAttribute("hidden")) return false;
    }
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) return false;
    // Clipped away by an overflow:hidden ancestor (max-height: 0 collapses
    // are how the hero fades its pronunciation line out).
    for (let e = el.parentElement; e; e = e.parentElement) {
      const cs = getComputedStyle(e);
      if (cs.overflow !== "visible" || cs.overflowX !== "visible" || cs.overflowY !== "visible") {
        const pr = e.getBoundingClientRect();
        if (pr.height === 0 || pr.width === 0) return false;
        if (r.bottom <= pr.top || r.top >= pr.bottom || r.right <= pr.left || r.left >= pr.right) return false;
      }
    }
    return true;
  }

  function describe(el) {
    const bits = [];
    for (let e = el, n = 0; e && n < 3; e = e.parentElement, n++) {
      let s = e.tagName.toLowerCase();
      if (e.id) s += "#" + e.id;
      else if (e.classList.length) s += "." + e.classList[0];
      bits.unshift(s);
    }
    return bits.join(" > ");
  }

  const offenders = [];
  let read = 0;
  const skip = new Set(["SCRIPT", "STYLE", "NOSCRIPT", "TEMPLATE", "SVG", "svg", "TITLE", "HEAD"]);
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  const seen = new Set();
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const text = n.nodeValue.replace(/\s+/g, " ").trim();
    if (!text) continue;
    const el = n.parentElement;
    if (!el || skip.has(el.tagName) || el.closest("svg")) continue;
    if (el.closest(":disabled")) continue; // WCAG 1.4.3 exempts inactive controls
    if (!visible(el)) continue;
    // One verdict per element and text colour, so a paragraph of ten links
    // is not ten identical lines.
    const cs = getComputedStyle(el);
    const key = el.tagName + "|" + cs.color + "|" + cs.fontSize + "|" + (n.nodeValue.length > 0 ? describe(el) : "");
    if (seen.has(key + text)) continue;
    seen.add(key + text);
    read++;

    let opacity = 1;
    for (const e of chain(el)) opacity *= parseFloat(getComputedStyle(e).opacity);
    const bg = backgroundBehind(el, true);
    const fgRaw = parse(cs.color);
    // Text alpha and the element's opacity both let the background through.
    const fgAlpha = fgRaw[3] * opacity;
    const fg = over([fgRaw[0], fgRaw[1], fgRaw[2], fgAlpha], bg.color);

    const px = parseFloat(cs.fontSize);
    const bold = parseInt(cs.fontWeight, 10) >= 700;
    const large = px >= 24 || (bold && px >= 18.66);
    const need = large ? 3 : 4.5;
    const got = ratio(fg, bg.color);
    if (bg.unresolved) {
      offenders.push({ kind: "unresolved-background", where: describe(el), text: text.slice(0, 60), detail: bg.unresolved });
    } else if (got + 1e-9 < need) {
      offenders.push({
        kind: "text",
        where: describe(el),
        text: text.slice(0, 60),
        ratio: Math.round(got * 100) / 100,
        need,
        fg: hex(fg),
        bg: hex(bg.color),
        cssColor: cs.color,
        opacity: Math.round(opacity * 100) / 100,
      });
    }
  }

  // Form-control boundaries: 3:1 against what surrounds them.
  for (const el of document.querySelectorAll("input, select, textarea")) {
    if (el.type === "hidden" || el.disabled || !visible(el)) continue;
    read++;
    const cs = getComputedStyle(el);
    const around = backgroundBehind(el, false).color;
    const bw = parseFloat(cs.borderTopWidth);
    const edge = bw > 0 && cs.borderTopStyle !== "none" ? parse(cs.borderTopColor) : parse(cs.backgroundColor);
    if (edge[3] === 0) {
      offenders.push({ kind: "control-boundary", where: describe(el), detail: "no border and no fill, so the control has no visible boundary" });
      continue;
    }
    const got = ratio(over(edge, around), around);
    if (got + 1e-9 < 3) {
      offenders.push({ kind: "control-boundary", where: describe(el), ratio: Math.round(got * 100) / 100, need: 3, fg: hex(over(edge, around)), bg: hex(around) });
    }
  }
  return { read, offenders };
}

const browser = await chromium.launch();

// The positive controls, run before the real pages every time (and alone
// under --selftest): a known-good pair must pass and each known-bad shape
// must be reported, or a green run below means nothing.
{
  const ctl = await browser.newPage();
  const cases = [
    ["good: dark text on a light ground", `<body style="background:#fff"><p style="color:#111">hello</p></body>`, 0],
    ["bad: grey text on a light ground", `<body style="background:#f4f4f2"><p style="color:#8b8d8a">hello</p></body>`, 1],
    ["bad: inherited dark grey on a dark bar, the pronunciation line's shape", `<body style="background:#f4f4f2;color:#5b5d5b"><header style="background:#16283a"><p style="opacity:.78">pronounced</p></header></body>`, 1],
    ["good: white on that same dark bar", `<body style="background:#f4f4f2"><header style="background:#16283a;color:#fff"><p>pronounced</p></header></body>`, 0],
    ["bad: opacity pulls good text under the line", `<body style="background:#fff"><p style="color:#111;opacity:.4">hello</p></body>`, 1],
    ["bad: a translucent layer is composited, not ignored", `<body style="background:#fff"><div style="background:rgba(0,0,0,.6)"><p style="color:#333">hello</p></div></body>`, 1],
    ["good: large text gets 3:1 (bold 24px at about 3.5:1)", `<body style="background:#fff"><p style="color:#858585;font:700 24px sans-serif">hello</p></body>`, 0],
    ["bad: the same grey at normal size", `<body style="background:#fff"><p style="color:#858585;font:16px sans-serif">hello</p></body>`, 1],
    ["bad: a gradient behind text is reported, not passed", `<body style="background:#fff"><p style="color:#111;background-image:linear-gradient(#fff,#000)">hello</p></body>`, 1],
    ["good: hidden text is not read", `<body style="background:#fff"><p style="color:#eee;display:none">hello</p><p style="color:#111">x</p></body>`, 0],
    ["bad: a form control with no boundary", `<body style="background:#fff"><input style="border:0;background:transparent" value="x"></body>`, 1],
  ];
  let bad = 0;
  for (const [label, html, want] of cases) {
    await ctl.setContent(html);
    const { read, offenders } = await ctl.evaluate(auditPage);
    const ok = read > 0 && (offenders.length > 0) === (want > 0);
    console.log(`  ${ok ? "ok  " : "FAIL"} ${label}`);
    if (!ok) bad++;
  }
  await ctl.close();
  if (bad > 0) {
    console.error(`contrast: ${bad} positive control(s) failed, so the check cannot be trusted`);
    await browser.close();
    process.exit(2);
  }
  if (selftest) {
    await browser.close();
    console.log("contrast: positive controls ok");
    process.exit(0);
  }
}

let failed = 0;
let readTotal = 0;
const report = [];
try {
  for (const theme of ["light", "dark"]) {
    const ctx = await browser.newContext({
      colorScheme: theme,
      viewport: { width: 1280, height: 900 },
      reducedMotion: "reduce",
    });
    // The site remembers a choice in localStorage; pin it so the run is
    // about the theme asked for and not about a stored one.
    await ctx.addInitScript((t) => {
      try { localStorage.setItem("theme", t); } catch (_) { /* ignore */ }
    }, theme);
    // Remote fonts and badges are not reachable from a sandbox and do not
    // change a colour; abort them so the load is quick and deterministic.
    await ctx.route(/^https?:/, (r) => r.abort());
    for (const f of pages) {
      const page = await ctx.newPage();
      await page.goto(pathToFileURL(path.join(siteDir, f)).href, { waitUntil: "load" });
      await page.evaluate(() => document.fonts && document.fonts.ready);
      const got = await page.evaluate(() => document.documentElement.getAttribute("data-theme"));
      if (got !== theme) {
        console.error(`${f}: asked for the ${theme} theme and the page is in ${got}`);
        process.exit(2);
      }
      const { read, offenders } = await page.evaluate(auditPage);
      readTotal += read;
      if (read === 0) {
        console.error(`${f} (${theme}): read zero text nodes, which is a check that looked at nothing`);
        process.exit(2);
      }
      for (const o of offenders) report.push({ page: f, theme, ...o });
      failed += offenders.length;
      await page.close();
    }
    await ctx.close();
  }
} finally {
  await browser.close();
}

if (failed > 0) {
  console.log(`contrast: ${failed} offender(s) across ${pages.length} page(s) x 2 themes (${readTotal} elements read)`);
  for (const o of report) {
    if (o.kind === "text") {
      console.log(`  ${o.page} [${o.theme}] ${o.ratio}:1 < ${o.need}:1  ${o.fg} on ${o.bg}  ${o.where}  "${o.text}"  (color ${o.cssColor}, opacity ${o.opacity})`);
    } else if (o.kind === "control-boundary") {
      console.log(`  ${o.page} [${o.theme}] control boundary ${o.ratio ?? "-"}:1  ${o.where}  ${o.detail ?? ""}`);
    } else {
      console.log(`  ${o.page} [${o.theme}] cannot resolve background  ${o.where}  "${o.text}"  ${o.detail}`);
    }
  }
  process.exit(1);
}
console.log(`contrast: ok, ${readTotal} elements read across ${pages.length} page(s) x 2 themes`);
