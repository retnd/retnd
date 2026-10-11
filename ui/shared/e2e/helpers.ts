import type { Page } from "@playwright/test";

/** Opens a path in the development build with the named mock scenario. */
export async function open(page: Page, path: string, scenario = "default") {
  const sep = path.includes("?") ? "&" : "?";
  await page.goto(path + sep + "scenario=" + scenario);
}

/** The Backups page lists a row per artifact; this finds one by file name. */
export function row(page: Page, filename: string) {
  return page.getByRole("row").filter({ hasText: filename });
}

/** WCAG relative-luminance contrast of every text node under `selector`
 *  against its effective background, measured in the browser so the theme's
 *  real computed colours are what is judged. Returns the failures. */
export async function contrastFailures(page: Page, selector: string) {
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
    const out: string[] = [];
    document.querySelectorAll(sel + ", " + sel + " *").forEach((e) => {
      const own = [...e.childNodes].some((n) => n.nodeType === 3 && (n.textContent ?? "").trim());
      if (!own) return;
      const cs = getComputedStyle(e);
      if (cs.visibility === "hidden" || cs.display === "none") return;
      let bg: number[] | null = null;
      for (let n: Element | null = e; n; n = n.parentElement) {
        const x = rgb(getComputedStyle(n).backgroundColor);
        if (x[3] > 0.5) { bg = x; break; }
      }
      if (!bg) return;
      const fg = rgb(cs.color);
      const a = lum(fg), b = lum(bg);
      const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      const px = parseFloat(cs.fontSize);
      const need = px >= 24 || (px >= 18.66 && parseInt(cs.fontWeight) >= 700) ? 3 : 4.5;
      if (ratio < need) out.push((e.textContent ?? "").trim().slice(0, 40) + " " + ratio.toFixed(2));
    });
    return out;
  }, selector);
}
