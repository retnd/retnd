/**
 * Panel finding C9 (issue #985): the chip being re-checked was dimmed with
 * `opacity: 0.6`, which composites its text with what is behind it. Measured
 * in a browser with the real tokens, the age text fell to 2.50:1 (light) and
 * 3.05:1 (dark) and the label to 4.25:1 (light), under the 4.5:1 minimum.
 *
 * This is a COMPUTED contrast check, not a browser one: it parses the real
 * tokens.css and assurance.css, resolves each chip state's fill, and measures
 * the ink that sits on it in both themes, so it can run in the unit suite and
 * a regression to any dimming or any fill change is caught without launching
 * a browser. The Playwright spec (985-claims) measures the same thing in a
 * browser, on the rendered page.
 *
 * It has its own positive control: the old dimming, modelled the way a
 * browser composites it, is run through the same arithmetic and must come out
 * under the line. A check that cannot fail would say nothing.
 */
import { describe, expect, it } from "vitest";

// Raw text through Vite, because tsconfig keeps @types/node out of this program.
import tokensCss from "../design-system/tokens.css?raw";
import assuranceCss from "../assurance/assurance.css?raw";

type Rgb = [number, number, number];
type Theme = "light" | "dark";

/** `--name: value;` declarations of one rule body. */
function declarations(body: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const m of body.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) out.set(m[1], m[2].trim());
  return out;
}

/** The body of the first rule whose selector is exactly `selector`. */
function ruleBody(css: string, selector: string): string {
  const at = css.indexOf(selector + " {");
  if (at < 0) throw new Error("tokens.css has no rule for " + selector);
  const open = css.indexOf("{", at);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth++;
    if (css[i] === "}" && --depth === 0) return css.slice(open + 1, i);
  }
  throw new Error("unterminated rule " + selector);
}

const light = declarations(ruleBody(tokensCss, ":root"));
const dark = new Map([...light, ...declarations(ruleBody(tokensCss, '[data-theme="dark"]'))]);
const tokens: Record<Theme, Map<string, string>> = { light, dark };

const srgbToLinear = (c: number) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const linearToSrgb = (c: number) => (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055);

/** oklch(L C h) to sRGB 0..255, gamut-clamped, by the CSS Color 4 matrices. */
function oklchToRgb(L: number, C: number, hDeg: number): Rgb {
  const h = (hDeg * Math.PI) / 180;
  const a = C * Math.cos(h);
  const b = C * Math.sin(h);
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.2914855480 * b) ** 3;
  const lin: Rgb = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s
  ];
  return lin.map((c) => Math.round(255 * Math.min(1, Math.max(0, linearToSrgb(c))))) as Rgb;
}

function colour(theme: Theme, value: string): Rgb {
  const v = value.trim();
  const ref = /^var\((--[a-z0-9-]+)\)$/.exec(v);
  if (ref) {
    const next = tokens[theme].get(ref[1]);
    if (next === undefined) throw new Error("no token " + ref[1]);
    return colour(theme, next);
  }
  const hex = /^#([0-9a-f]{6})$/i.exec(v);
  if (hex) return [0, 2, 4].map((i) => parseInt(hex[1].slice(i, i + 2), 16)) as Rgb;
  const ok = /^oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*\)$/.exec(v);
  if (ok) return oklchToRgb(Number(ok[1]), Number(ok[2]), Number(ok[3]));
  throw new Error("cannot read colour " + v);
}

const token = (theme: Theme, name: string): Rgb => colour(theme, "var(" + name + ")");

function luminance([r, g, b]: Rgb): number {
  return 0.2126 * srgbToLinear(r / 255) + 0.7152 * srgbToLinear(g / 255) + 0.0722 * srgbToLinear(b / 255);
}
export function ratio(a: Rgb, b: Rgb): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}
/** `fg` at `alpha` over `bg`, as a browser composites a translucent layer. */
const blend = (fg: Rgb, bg: Rgb, alpha: number): Rgb => fg.map((c, i) => alpha * c + (1 - alpha) * bg[i]) as Rgb;

/** Every chip state's fill token, read from assurance.css: the `background`
 *  of `[data-state="X"] > .achip`, defaulting to the plain chip's --surface. */
function chipFills(): Map<string, string> {
  const fills = new Map<string, string>();
  const base = /\.achip\s*\{[^}]*background:\s*(var\(--[a-z0-9-]+\))/.exec(assuranceCss);
  fills.set("(plain chip)", base ? base[1] : "var(--surface)");
  for (const m of assuranceCss.matchAll(/\[data-state="([a-z_]+)"\]\s*>\s*\.achip\s*\{([^}]*)\}/g)) {
    const bg = /background:\s*(var\(--[a-z0-9-]+\))/.exec(m[2]);
    if (bg) fills.set(m[1], bg[1]);
  }
  // The "existence_only" and "attested_only" rule is a selector list.
  for (const m of assuranceCss.matchAll(/\[data-state="([a-z_]+)"\]\s*>\s*\.achip,\s*\[data-state="([a-z_]+)"\]\s*>\s*\.achip\s*\{([^}]*)\}/g)) {
    const bg = /background:\s*(var\(--[a-z0-9-]+\))/.exec(m[3]);
    if (bg) { fills.set(m[1], bg[1]); fills.set(m[2], bg[1]); }
  }
  // A state whose rule sets a border and no background draws on the plain
  // chip's fill. It still has ink on it, so it still gets measured.
  for (const m of assuranceCss.matchAll(/\[data-state="([a-z_]+)"\]\s*>\s*\.achip/g)) {
    if (!fills.has(m[1])) fills.set(m[1], fills.get("(plain chip)")!);
  }
  return fills;
}

const THEMES: Theme[] = ["light", "dark"];

describe("the chip ink clears WCAG AA on every state's fill, in both themes", () => {
  const fills = chipFills();

  it("reads a fill for each of the states the chips draw", () => {
    // If this shrinks the loop below is checking less than it claims.
    expect(fills.size).toBeGreaterThanOrEqual(15);
    for (const s of ["pass", "failed", "overdue", "unknown", "could_not_complete", "cleanup_failed", "requires_restore", "no_readable_copy"]) {
      expect(fills.has(s), s).toBe(true);
    }
  });

  for (const theme of THEMES) {
    it("label text (--text) is at least 4.5:1 on every fill, " + theme, () => {
      for (const [state, fill] of fills) {
        const r = ratio(token(theme, "--text"), colour(theme, fill));
        expect(r, state + " label on " + fill).toBeGreaterThanOrEqual(4.5);
      }
    });
    it("age and re-checking text (--text-2) is at least 4.5:1 on every fill, " + theme, () => {
      for (const [state, fill] of fills) {
        const r = ratio(token(theme, "--text-2"), colour(theme, fill));
        expect(r, state + " age on " + fill).toBeGreaterThanOrEqual(4.5);
      }
    });
    it("the state glyph colour is at least 3:1 on its own fill, " + theme, () => {
      for (const [state, glyph] of [["pass", "--ok"], ["failed", "--danger"], ["could_not_complete", "--danger"], ["unknown", "--accent"], ["cleanup_failed", "--warn"], ["requires_restore", "--accent"]] as const) {
        const r = ratio(token(theme, glyph), colour(theme, fills.get(state)!));
        expect(r, state + " glyph " + glyph).toBeGreaterThanOrEqual(3);
      }
    });
    it("the re-checking outline (--text-2) is at least 3:1 on the card surface, " + theme, () => {
      expect(ratio(token(theme, "--text-2"), token(theme, "--surface"))).toBeGreaterThanOrEqual(3);
    });
  }
});

describe("nothing in the assurance styles dims text", () => {
  it("declares no opacity at all, so no chip can be composited toward its background", () => {
    const stripped = assuranceCss.replace(/\/\*[\s\S]*?\*\//g, "");
    expect(stripped).not.toMatch(/(^|[;{\s])opacity\s*:/);
  });

  it("marks a chip being re-checked with shape and words, which touch no colour", () => {
    const rule = /\[data-checking="true"\]\s*>\s*\.achip\s*\{([^}]*)\}/.exec(assuranceCss);
    expect(rule, "a [data-checking] rule").not.toBeNull();
    expect(rule![1]).toMatch(/outline:\s*2px dashed/);
    expect(rule![1]).toMatch(/font-style:\s*italic/);
    expect(rule![1]).not.toMatch(/\bcolor\s*:/);
  });

  // The positive control. The old rule, run through the same arithmetic the
  // way a browser composites opacity (the chip, fill and text together, over
  // the card), must fail, or this file could not have caught it.
  it("control: the old opacity 0.6 would have failed the age text and the label", () => {
    const fills = chipFills();
    let failures = 0;
    for (const theme of THEMES) {
      const page = token(theme, "--surface");
      for (const ink of ["--text", "--text-2"]) {
        const fill = blend(colour(theme, fills.get("pass")!), page, 0.6);
        const text = blend(token(theme, ink), page, 0.6);
        if (ratio(text, fill) < 4.5) failures++;
      }
    }
    expect(failures).toBeGreaterThanOrEqual(2);
  });
});
