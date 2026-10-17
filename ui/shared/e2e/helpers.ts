import { expect } from "@playwright/test";
import type { Page } from "@playwright/test";

/** Opens a path in the development build with the named mock scenario. */
export async function open(page: Page, path: string, scenario = "default", opts: { wire?: boolean } = {}) {
  // The development build has no service behind it, so the one call that is
  // not served by the in-process mock (the session read) is answered here:
  // signed in as the operator the fixtures describe.
  await page.route("**/api/v1/auth/session", (r) => r.fulfill({ json: { username: "admin" } }));
  const sep = path.includes("?") ? "&" : "?";
  // `wire` runs the production assurance seam (realAssurance) over the mock's
  // routes, so a spec can drive the code a built app runs.
  await page.goto(path + sep + "scenario=" + scenario + (opts.wire ? "&wire=1" : ""));
}

/** The Backups page lists a row per artifact; this finds one by file name. */
export function row(page: Page, filename: string) {
  return page.getByRole("row").filter({ hasText: filename });
}

/** WCAG relative-luminance contrast of every text node under `selector`
 *  against its effective background, measured in the browser so the theme's
 *  real computed colours are what is judged. Returns every measurement, with
 *  the ratio it needs: 4.5 for normal text, 3 for large text (24px, or
 *  18.66px at weight 700 or more). */
export async function contrastReport(page: Page, selector: string) {
  return page.evaluate((sel) => {
    const c = document.createElement("canvas").getContext("2d")!;
    const rgb = (s: string): number[] => {
      c.clearRect(0, 0, 1, 1);
      c.fillStyle = "#000";
      c.fillStyle = s;
      c.fillRect(0, 0, 1, 1);
      const d = c.getImageData(0, 0, 1, 1).data;
      return [d[0], d[1], d[2], d[3] / 255];
    };
    const lum = (a: number[]) => {
      const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
      return 0.2126 * f(a[0]) + 0.7152 * f(a[1]) + 0.0722 * f(a[2]);
    };
    const out: Array<{ text: string; ratio: number; need: number; fg: string; bg: string; sel: string; t3: boolean }> = [];
    const t3 = rgb(getComputedStyle(document.documentElement).getPropertyValue("--text-3")).slice(0, 3).join(",");
    // A readable address for the element: its tag and classes, under the nearest
    // ancestor that carries a data-testid (#1235: failures name selector + text).
    const where = (el: Element) => {
      const me = el.tagName.toLowerCase() + [...el.classList].slice(0, 3).map((k) => "." + k).join("") + (el.getAttribute("data-testid") ? "[data-testid=" + el.getAttribute("data-testid") + "]" : "");
      const anc = el.parentElement?.closest("[data-testid]");
      return (anc && anc !== el ? "[data-testid=" + anc.getAttribute("data-testid") + "] " : "") + me;
    };
    document.querySelectorAll(sel + ", " + sel + " *").forEach((e) => {
      const own = [...e.childNodes].some((n) => n.nodeType === 3 && (n.textContent ?? "").trim());
      if (!own) return;
      const cs = getComputedStyle(e);
      if (cs.visibility === "hidden" || cs.display === "none") return;
      // WCAG exempts inactive (disabled) components from the contrast minimum.
      if (e.closest(":disabled")) return;
      let bg: number[] | null = null;
      let bgNode: Element | null = null;
      for (let n: Element | null = e; n; n = n.parentElement) {
        const x = rgb(getComputedStyle(n).backgroundColor);
        if (x[3] > 0.5) { bg = x; bgNode = n; break; }
      }
      if (!bg || !bgNode) return;
      let fg = rgb(cs.color);
      const usesT3 = fg.slice(0, 3).join(",") === t3;
      // Opacity composites. A translucent element dims its own text toward the
      // fill it sits on, and a translucent ancestor of that fill dims the fill
      // and the text together toward whatever is behind it. Ignoring either is
      // how an opacity: 0.6 chip measured as a pass for as long as it did (#985,
      // the panel's C9): getComputedStyle().color does not include it.
      const mix = (top: number[], under: number[], o: number) => [0, 1, 2].map((i) => o * top[i] + (1 - o) * under[i]).concat([1]);
      let inner = 1;
      for (let n: Element | null = e; n && n !== bgNode; n = n.parentElement) inner *= parseFloat(getComputedStyle(n).opacity);
      let outer = 1;
      for (let n: Element | null = bgNode; n; n = n.parentElement) outer *= parseFloat(getComputedStyle(n).opacity);
      if (inner < 1) fg = mix(fg, bg, inner);
      if (outer < 1) {
        let page: number[] = [255, 255, 255, 1];
        for (let n: Element | null = bgNode.parentElement; n; n = n.parentElement) {
          const x = rgb(getComputedStyle(n).backgroundColor);
          if (x[3] > 0.5) { page = x; break; }
        }
        fg = mix(fg, page, outer);
        bg = mix(bg, page, outer);
      }
      const a = lum(fg), b = lum(bg);
      const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      const px = parseFloat(cs.fontSize);
      const need = px >= 24 || (px >= 18.66 && parseInt(cs.fontWeight) >= 700) ? 3 : 4.5;
      out.push({
        text: (e.textContent ?? "").trim().slice(0, 40), ratio: Math.round(ratio * 100) / 100, need,
        fg: fg.slice(0, 3).map(Math.round).join(","), bg: bg.slice(0, 3).map(Math.round).join(","),
        sel: where(e), t3: usesT3
      });
    });
    return out;
  }, selector);
}

/** The measurements that fail their threshold, as "text ratio" strings. */
export async function contrastFailures(page: Page, selector: string) {
  return (await contrastReport(page, selector)).filter((m) => m.ratio < m.need).map((m) => m.text + " " + m.ratio.toFixed(2));
}

/** Measures, prints the worst ratio per distinct text colour and background,
 *  and returns the failures. The printed lines are what the PR quotes. */
export async function measureContrast(page: Page, label: string, selector: string) {
  const all = await contrastReport(page, selector);
  const worst = new Map<string, (typeof all)[number]>();
  for (const m of all) {
    const k = m.fg + " on " + m.bg;
    const w = worst.get(k);
    if (!w || m.ratio < w.ratio) worst.set(k, m);
  }
  for (const [k, m] of worst) {
    console.log("CONTRAST " + label + " | " + m.ratio.toFixed(2) + ":1 (needs " + m.need + ") | rgb(" + k.replace(" on ", ") on rgb(") + ") | " + JSON.stringify(m.text));
  }
  return all.filter((m) => m.ratio < m.need).map((m) => m.text + " " + m.ratio.toFixed(2));
}

/** Sets the theme the way the app's own switch does, then checks it stuck.
 *  The app writes `data-theme` from its own state when it mounts, and since
 *  the async boot (#1216) that can land after a spec's first lines, which
 *  puts the screen back in light. So this lets the page settle for a couple
 *  of frames and asserts the attribute (#1229): a spec that calls it too
 *  early now fails here instead of measuring or recording the wrong theme. */
export async function setTheme(page: Page, theme: "light" | "dark") {
  await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), theme);
  await page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(done, 150)))));
  await expect(page.locator("html"), "setTheme(" + theme + ") did not stick: the app reset it after boot").toHaveAttribute("data-theme", theme, { timeout: 1000 });
}

/** Navigates inside the running app without reloading it. The development
 *  build keeps its settings in the page's memory, so a full load would drop
 *  exactly the state a spec is trying to carry from one screen to the next. */
export async function spaGo(page: Page, path: string) {
  await page.evaluate((p) => {
    window.history.pushState({}, "", p);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, path);
}

/** The boundary of every bordered element under `selector` against the
 *  background it sits on (WCAG 1.4.11 asks for 3:1). Prints the worst ratio
 *  per distinct border colour and returns the ones under 3. */
export async function measureBorders(page: Page, label: string, selector: string) {
  const all = await page.evaluate((sel) => {
    const c = document.createElement("canvas").getContext("2d")!;
    const rgb = (s: string): number[] => {
      c.clearRect(0, 0, 1, 1);
      c.fillStyle = "#000";
      c.fillStyle = s;
      c.fillRect(0, 0, 1, 1);
      const d = c.getImageData(0, 0, 1, 1).data;
      return [d[0], d[1], d[2], d[3] / 255];
    };
    const lum = (a: number[]) => {
      const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
      return 0.2126 * f(a[0]) + 0.7152 * f(a[1]) + 0.0722 * f(a[2]);
    };
    const out: Array<{ what: string; ratio: number; fg: string; bg: string }> = [];
    document.querySelectorAll(sel).forEach((e) => {
      const cs = getComputedStyle(e);
      if (cs.display === "none" || cs.visibility === "hidden" || cs.borderTopStyle === "none" || parseFloat(cs.borderTopWidth) === 0) return;
      if (e.closest(":disabled")) return;
      const edge = rgb(cs.borderTopColor);
      let bg: number[] | null = null;
      for (let n: Element | null = e.parentElement; n; n = n.parentElement) {
        const x = rgb(getComputedStyle(n).backgroundColor);
        if (x[3] > 0.5) { bg = x; break; }
      }
      if (!bg || edge[3] < 0.5) return;
      const a = lum(edge), b = lum(bg);
      out.push({
        what: e.tagName.toLowerCase() + "." + (e.getAttribute("class") ?? "").split(" ")[0],
        ratio: Math.round(((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)) * 100) / 100,
        fg: edge.slice(0, 3).join(","), bg: bg.slice(0, 3).join(",")
      });
    });
    return out;
  }, selector);
  const worst = new Map<string, (typeof all)[number]>();
  for (const m of all) {
    const k = m.fg + " on " + m.bg + " " + m.what;
    const w = worst.get(k);
    if (!w || m.ratio < w.ratio) worst.set(k, m);
  }
  for (const m of worst.values()) {
    console.log("BORDER " + label + " | " + m.ratio.toFixed(2) + ":1 (needs 3) | rgb(" + m.fg + ") on rgb(" + m.bg + ") | " + m.what);
  }
  return all.filter((m) => m.ratio < 3).map((m) => m.what + " " + m.ratio.toFixed(2));
}

/** Every bordered element under `selector`: its edge against its own fill and
 *  against the page it sits on, and its fill against that page. The edge is a
 *  UI boundary, so it needs 3:1 against the page (WCAG 1.4.11); the states
 *  named in `strictFill` need it against their own fill as well. Printed lines
 *  are what the PR quotes. Returns the pairs under 3. */
export async function measureEdges(page: Page, label: string, selector: string, strictFill: string[] = []) {
  const all = await page.evaluate((sel) => {
    const c = document.createElement("canvas").getContext("2d")!;
    const rgb = (s: string): number[] => {
      c.clearRect(0, 0, 1, 1);
      c.fillStyle = "#000";
      c.fillStyle = s;
      c.fillRect(0, 0, 1, 1);
      const d = c.getImageData(0, 0, 1, 1).data;
      return [d[0], d[1], d[2], d[3] / 255];
    };
    const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
    const lum = (a: number[]) => 0.2126 * f(a[0]) + 0.7152 * f(a[1]) + 0.0722 * f(a[2]);
    const ratio = (a: number[], b: number[]) => { const [h, l] = [lum(a), lum(b)].sort((x, y) => y - x); return Math.round(((h + 0.05) / (l + 0.05)) * 100) / 100; };
    const page_ = (e: Element): number[] => {
      for (let n: Element | null = e.parentElement; n; n = n.parentElement) {
        const x = rgb(getComputedStyle(n).backgroundColor);
        if (x[3] > 0.5) return x;
      }
      return [255, 255, 255, 1];
    };
    // The body background is the page; the nearest opaque ancestor is the card.
    const out: Array<{ what: string; edgeFill: number; edgePage: number; edgeBody: number; fillPage: number; edge: string; fill: string; card: string }> = [];
    document.querySelectorAll(sel).forEach((e) => {
      const cs = getComputedStyle(e);
      if (cs.display === "none" || parseFloat(cs.borderTopWidth) === 0 || cs.borderTopStyle === "none") return;
      const edge = rgb(cs.borderTopColor);
      const fillRaw = rgb(cs.backgroundColor);
      const card = page_(e);
      const body = rgb(getComputedStyle(document.body).backgroundColor);
      const fill = fillRaw[3] > 0.5 ? fillRaw : card;
      const own = e.closest("[data-state],[data-outcome]");
      out.push({
        what: (own?.getAttribute("data-state") ?? own?.getAttribute("data-outcome") ?? e.className) + "",
        edgeFill: ratio(edge, fill), edgePage: ratio(edge, card), edgeBody: ratio(edge, body), fillPage: ratio(fill, card),
        edge: edge.slice(0, 3).join(","), fill: fill.slice(0, 3).join(","), card: card.slice(0, 3).join(",")
      });
    });
    return out;
  }, selector);
  const seen = new Set<string>();
  for (const m of all) {
    const k = m.what + m.edge + m.fill + m.card;
    if (seen.has(k)) continue;
    seen.add(k);
    console.log("EDGE " + label + " | " + m.what + " | edge/fill " + m.edgeFill.toFixed(2) + ":1, edge/card " + m.edgePage.toFixed(2) + ":1, edge/page " + m.edgeBody.toFixed(2) + ":1 | fill/card " + m.fillPage.toFixed(2) + ":1 | rgb(" + m.edge + ") rgb(" + m.fill + ") rgb(" + m.card + ")");
  }
  return all
    .filter((m) => m.edgePage < 3 || m.edgeBody < 3 || (strictFill.includes(m.what) && m.edgeFill < 3))
    .map((m) => m.what + " " + Math.min(m.edgePage, m.edgeBody, strictFill.includes(m.what) ? m.edgeFill : 99).toFixed(2));
}
