#!/usr/bin/env python3
"""The contrast rule, applied to the web UI's token pairs that a reviewed
table names (#1318, EPIC T T2.3).

check-tokens.py holds the docs site to WCAG AA from its tokens. This does
the same for the pairs the web UI's encryption suite surfaces introduce, read
from ui/shared/src/design-system/tokens.css in BOTH themes, so a token change
that would sink one of them fails here in a second, naming the pair, instead
of waiting for a browser run.

Each row of PAIRS names a stylesheet, a selector, the foreground token and
the background token the selector paints, and the ratio it needs: 4.5 for
text, 3 for an edge that identifies a control (WCAG 1.4.11). The check runs
in two directions, as check-tokens.py does:

  * the stylesheet must actually declare that selector with those tokens, so
    a row cannot outlive the rule it describes (or exist before it), and
  * the ratio must hold in light and in dark.

A row whose rule is missing is a failure, which is what makes this red until
the stylesheet that draws the surface exists.

Exit status: 0 clean, 1 an offender or a stale row, 2 could not run.
`--selftest` runs the controls: a known-good pair passes, a known-bad pair
fails, and a missing selector is reported.
"""

from __future__ import annotations

import math
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
UI = ROOT / "ui" / "shared" / "src"
TOKENS = UI / "design-system" / "tokens.css"

NORMAL = 4.5
EDGE = 3.0

# An edge drawn with these tokens has to make 3:1 against every background a
# control sits on, not only the one its row names: the card (--surface), the
# quiet fill (--surface-2) and the page itself (--bg), which is what a chip or
# a button shows through when it is not on a card. --edge-3 passed at 3.35:1
# on --surface while it was 2.99:1 on the light --bg, so every edge row is
# also checked against these.
EDGE_TOKENS = ("edge-3", "control-edge")
EDGE_BACKGROUNDS = ("surface", "surface-2", "bg")

# (stylesheet relative to ui/shared/src, selector, foreground property,
#  foreground token, background token, minimum). The background is the
# surface the element sits on when the selector paints none of its own.
PAIRS: list[tuple[str, str, str, str, str, float]] = [
    # The copy chips' edge (#995). The edge rule also checks it against the
    # card and the page, which is where it measured 2.99:1 in the browser.
    ("encryption/encryption.css", ".echip", "border-color", "edge-3", "surface-2", EDGE),
    # The suite id inside the Encrypted chip: secondary ink on the chip fill.
    ("encryption/encryption.css", ".echip__suite", "color", "text-2", "surface-2", NORMAL),
    # Status badges beside each suite option.
    ("encryption/encryption.css", ".esuite__badge", "color", "text", "surface-2", NORMAL),
    ("encryption/encryption.css", ".esuite__badge", "border-color", "edge-3", "surface", EDGE),
    ("encryption/encryption.css", '.esuite__badge[data-suite-badge="default"]', "background", "text", "ok-quiet", NORMAL),
    ("encryption/encryption.css", '.esuite__badge[data-suite-badge="default"]', "border-color", "ok", "surface", EDGE),
    ("encryption/encryption.css", '.esuite__badge[data-suite-badge="deprecated"]', "background", "text", "warn-quiet", NORMAL),
    ("encryption/encryption.css", '.esuite__badge[data-suite-badge="deprecated"]', "border-color", "warn", "surface", EDGE),
    ("encryption/encryption.css", '.esuite__badge[data-suite-badge="retired"]', "color", "text-2", "surface-2", NORMAL),
    # The options: the sentence, the selected row and the disabled (retired) row.
    ("encryption/encryption.css", ".esuite", "border-color", "edge-3", "surface", EDGE),
    ("encryption/encryption.css", ".esuite__desc", "color", "text-2", "surface", NORMAL),
    # The selected row keeps the plain surface (a neutral badge edge on the
    # accent fill measured 2.90:1 in light) and is marked by a 2px accent edge.
    ("encryption/encryption.css", ".esuite[data-selected]", "border-color", "accent", "surface", EDGE),
    ("encryption/encryption.css", ".esuite[data-disabled] .esuite__id", "color", "text-2", "surface", NORMAL),
    ("encryption/encryption.css", ".esuite__reason", "color", "text-2", "surface", NORMAL),
    # The step strip of the change dialog.
    ("encryption/encryption.css", ".esteps__step", "border-color", "edge-3", "surface", EDGE),
    ("encryption/encryption.css", '.esteps__step[data-state="now"]', "background", "text", "accent-quiet", NORMAL),
    ("encryption/encryption.css", '.esteps__step[data-state="done"] .esteps__glyph', "color", "ok", "surface", EDGE),
    # The still-owed list.
    ("encryption/encryption.css", ".eowed", "background", "text", "warn-quiet", NORMAL),
]


def oklch_to_linear(l: float, c: float, h: float) -> tuple[float, float, float]:
    a, b = c * math.cos(math.radians(h)), c * math.sin(math.radians(h))
    l_ = l + 0.3963377774 * a + 0.2158037573 * b
    m_ = l - 0.1055613458 * a - 0.0638541728 * b
    s_ = l - 0.0894841775 * a - 1.2914855480 * b
    lc, mc, sc = l_**3, m_**3, s_**3
    rgb = (
        4.0767416621 * lc - 3.3077115913 * mc + 0.2309699292 * sc,
        -1.2684380046 * lc + 2.6097574011 * mc - 0.3413193965 * sc,
        -0.0041960863 * lc - 0.7034186147 * mc + 1.7076147010 * sc,
    )
    return tuple(min(1.0, max(0.0, x)) for x in rgb)  # type: ignore[return-value]


def hex_to_linear(value: str) -> tuple[float, float, float]:
    h = value.strip().lstrip("#")
    if len(h) == 3:
        h = "".join(ch * 2 for ch in h)
    out = []
    for i in (0, 2, 4):
        v = int(h[i : i + 2], 16) / 255
        out.append(v / 12.92 if v <= 0.04045 else ((v + 0.055) / 1.055) ** 2.4)
    return tuple(out)  # type: ignore[return-value]


def luminance(c: tuple[float, float, float]) -> float:
    return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]


def ratio(a: tuple[float, float, float], b: tuple[float, float, float]) -> float:
    hi, lo = sorted((luminance(a), luminance(b)), reverse=True)
    return (hi + 0.05) / (lo + 0.05)


def block(css: str, selector_re: str) -> str:
    m = re.search(selector_re + r"\s*\{(.*?)\n\}", css, re.S | re.M)
    if not m:
        raise SystemExit(f"check-ui-pairs: no {selector_re} block in {TOKENS}")
    return m.group(1)


def declarations(text: str) -> dict[str, str]:
    return {k: v.strip() for k, v in re.findall(r"--([a-z0-9-]+)\s*:\s*([^;]+);", text)}


def themes() -> dict[str, dict[str, str]]:
    css = TOKENS.read_text(encoding="utf-8")
    light = declarations(block(css, r"^:root"))
    dark = {**light, **declarations(block(css, r'^\[data-theme="dark"\]'))}
    return {"light": light, "dark": dark}


def resolve(tokens: dict[str, str], name: str, depth: int = 0) -> tuple[float, float, float]:
    if depth > 8:
        raise ValueError(f"--{name} does not resolve")
    v = tokens[name]
    ref = re.fullmatch(r"var\(--([a-z0-9-]+)\)", v)
    if ref:
        return resolve(tokens, ref.group(1), depth + 1)
    ok = re.fullmatch(r"oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*\)", v)
    if ok:
        return oklch_to_linear(*(float(x) for x in ok.groups()))
    if v.startswith("#"):
        return hex_to_linear(v)
    raise ValueError(f"--{name}: {v} is not a colour this checker reads")


def rule_declares(sheet: str, selector: str, prop: str, token: str) -> bool:
    """Whether `sheet` has a rule for `selector` (alone or in a list) that
    declares `prop` with var(--token), or for a pair painted on a fill
    (prop "background"), a background of var(--token-of-the-fill)."""
    sheet = re.sub(r"/\*.*?\*/", "", sheet, flags=re.S)
    for sel, body in re.findall(r"([^{}]+)\{([^{}]*)\}", sheet):
        names = [s.strip() for s in sel.split(",")]
        if selector not in names:
            continue
        for p, v in re.findall(r"([a-z-]+)\s*:\s*([^;]+);", body):
            if p in (prop, "border") and f"var(--{token})" in v:
                return True
    return False


def check(pairs, theme_tokens, read_sheet) -> list[str]:
    bad: list[str] = []
    for sheet, selector, prop, fg, bg, need in pairs:
        text = read_sheet(sheet)
        # A row painted on a fill names the fill as the token its rule declares.
        declared = bg if prop == "background" else fg
        if text is None or not rule_declares(text, selector, prop, declared):
            bad.append(f"{sheet} {selector}: no rule declares {prop} var(--{declared}) (stale or not yet drawn)")
            continue
        backgrounds = [bg]
        if fg in EDGE_TOKENS:
            backgrounds += [b for b in EDGE_BACKGROUNDS if b != bg]
        for theme, tokens in theme_tokens.items():
            for b in backgrounds:
                if b not in tokens:
                    bad.append(f"{selector} --{fg}: no --{b} token in {theme} to check the edge against")
                    continue
                r = ratio(resolve(tokens, fg), resolve(tokens, b))
                if r < need:
                    bad.append(f"{selector} --{fg} on --{b} in {theme}: {r:.2f} < {need}")
    return bad


def read_ui_sheet(rel: str) -> str | None:
    p = UI / rel
    return p.read_text(encoding="utf-8") if p.exists() else None


def selftest() -> int:
    tokens = {"t": {"ink": "#191a19", "pale": "#a0a0a0", "paper": "#ffffff"}}
    sheet = ".good { color: var(--ink); }\n.bad { color: var(--pale); }\n"
    read = lambda _rel: sheet  # noqa: E731
    failures = []
    if check([("s", ".good", "color", "ink", "paper", NORMAL)], tokens, read):
        failures.append("a known-good pair was refused")
    if not check([("s", ".bad", "color", "pale", "paper", NORMAL)], tokens, read):
        failures.append("a known-bad pair passed")
    if not check([("s", ".absent", "color", "ink", "paper", NORMAL)], tokens, read):
        failures.append("a row with no rule passed")
    # The edge that passes on the card and fails on the page: 3.35:1 on
    # --surface and 2.99:1 on --bg, the light values --edge-3 shipped with.
    edge = {"t": {"edge-3": "#8b8d8a", "surface": "#ffffff", "surface-2": "#fafaf9", "bg": "#f2f2f0"}}
    edge_sheet = lambda _rel: ".chip { border: 1px solid var(--edge-3); }\n"  # noqa: E731
    if not check([("s", ".chip", "border-color", "edge-3", "surface", EDGE)], edge, edge_sheet):
        failures.append("an edge at 2.99:1 on --bg passed because its row names --surface")
    edge["t"]["edge-3"] = "#888a87"
    if check([("s", ".chip", "border-color", "edge-3", "surface", EDGE)], edge, edge_sheet):
        failures.append("an edge at 3:1 or better on --surface, --surface-2 and --bg was refused")
    for f in failures:
        print("check-ui-pairs selftest: " + f, file=sys.stderr)
    return 1 if failures else 0


def main(argv: list[str]) -> int:
    if "--selftest" in argv:
        return selftest()
    try:
        t = themes()
    except (OSError, SystemExit, ValueError) as e:
        print(f"check-ui-pairs: could not read tokens: {e}", file=sys.stderr)
        return 2
    bad = check(PAIRS, t, read_ui_sheet)
    for b in bad:
        print("check-ui-pairs: " + b, file=sys.stderr)
    if not bad:
        for _sheet, selector, _prop, fg, bg, need in PAIRS:
            backgrounds = [bg] + ([b for b in EDGE_BACKGROUNDS if b != bg] if fg in EDGE_TOKENS else [])
            for b in backgrounds:
                light = ratio(resolve(t["light"], fg), resolve(t["light"], b))
                dark = ratio(resolve(t["dark"], fg), resolve(t["dark"], b))
                print(f"{selector:58s} --{fg} on --{b}: light {light:.2f}, dark {dark:.2f} (needs {need})")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
