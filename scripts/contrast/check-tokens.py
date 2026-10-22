#!/usr/bin/env python3
"""The contrast rule, applied to the docs site's colour tokens (#1054).

The rule is WCAG 2.x AA: at least 4.5:1 for normal text and 3:1 for large
text (18pt, or 14pt bold) and for the boundary of a UI component, judged
against the colour of the background the text actually sits on, in BOTH
themes. docs/site/tools/check-contrast.mjs applies it to the rendered
pages; this script applies it to the tokens, with no browser, so the answer
arrives in a second and names the token to change rather than the element
that happened to show it.

How a "pair the CSS can combine" is found, and why it is not guessed:

  * TEXT_ON below is the reviewed list of which surface each text token may
    sit on. It is written out rather than derived, because the CSS cannot
    say which surface a rule ends up on: that depends on the markup.
  * The CSS is then read for completeness in both directions. Every
    `color: var(--x)` and every `background: var(--y)` in the site's
    stylesheets and inline style blocks must be a token this table
    classifies, and any rule that declares both must be a pair the table
    lists. A new token, or a new combination, therefore fails here until
    somebody decides what it sits on, instead of passing by not being
    looked at.
  * Literal colours in a `color:` declaration are refused, because
    docs/site/theme.css forbids them and a literal survives the theme
    switch.

Exit status: 0 clean, 1 an offender (or a gap in the table), 2 could not
run. `--selftest` runs the positive controls: a known-good pair passes, a
known-bad pair fails, and the pair the pronunciation line really shipped
with is rejected.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

SITE = Path(__file__).resolve().parents[2] / "docs" / "site"

NORMAL = 4.5
LARGE = 3.0

# text token -> (minimum ratio, surfaces it can sit on). Every entry is
# evaluated in both themes. The ratio is NORMAL unless the token is only
# ever used at a large size, and none of them is.
SURFACES_PAGE = ("bg", "surface", "code-bg", "code-bg-out", "table-header-bg")
# hl-bg is the highlight inside a code block, which only ever carries
# --text, so it is not offered to the other text tokens.
SURFACES_TINT = ("ok-bg", "warn-bg", "danger-bg", "hl-bg")

TEXT_ON: dict[str, tuple[float, tuple[str, ...]]] = {
    "text": (NORMAL, SURFACES_PAGE + SURFACES_TINT),
    "text-light": (NORMAL, (*SURFACES_PAGE, "ok-bg", "warn-bg", "danger-bg")),
    "text-faint": (NORMAL, SURFACES_PAGE),
    "primary": (NORMAL, (*SURFACES_PAGE, "ok-bg", "warn-bg", "danger-bg")),
    "primary-dark": (NORMAL, ("bg", "surface", "table-header-bg")),
    "accent": (NORMAL, SURFACES_PAGE),
    "ok": (NORMAL, ("bg", "surface", "code-bg", "ok-bg")),
    "warn": (NORMAL, ("bg", "surface", "code-bg", "warn-bg")),
    "danger": (NORMAL, ("bg", "surface", "code-bg", "danger-bg")),
    # Text painted in white on a fill: the hero bar (always dark), and the
    # two hover fills that carry white text.
    "white": (NORMAL, ("hero-bg", "fill-primary", "fill-ok")),
}

# Tokens that are not text colours and so are not looked up in TEXT_ON.
# A border or rule is decoration here: every bordered thing on this site
# carries its own label or text, so the border is not what identifies it
# (WCAG 1.4.11). The day a form control is added, its border token goes in
# CONTROL_BORDERS and is held to 3:1 against the surface it sits on.
DECORATIVE = {"border", "border-strong", "max-width"}
CONTROL_BORDERS: dict[str, tuple[str, ...]] = {}

THEMES = ("light", "dark")


def parse_hex(value: str) -> tuple[int, int, int]:
    h = value.strip().lstrip("#")
    if len(h) == 3:
        h = "".join(c * 2 for c in h)
    if not re.fullmatch(r"[0-9a-fA-F]{6}", h):
        raise ValueError(f"not a hex colour: {value!r}")
    return int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16)


def _lin(v: int) -> float:
    c = v / 255
    return c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4


def luminance(rgb: tuple[int, int, int]) -> float:
    r, g, b = rgb
    return 0.2126 * _lin(r) + 0.7152 * _lin(g) + 0.0722 * _lin(b)


def ratio(a: str, b: str) -> float:
    la, lb = luminance(parse_hex(a)), luminance(parse_hex(b))
    hi, lo = (la, lb) if la >= lb else (lb, la)
    return (hi + 0.05) / (lo + 0.05)


def strip_comments(css: str) -> str:
    return re.sub(r"/\*.*?\*/", "", css, flags=re.S)


def resolve_tokens(theme_css: str) -> dict[str, dict[str, str]]:
    """Light and dark token maps. Dark inherits what it does not redeclare,
    which is how the cascade treats --white and --max-width."""
    css = strip_comments(theme_css)
    blocks = {
        "light": re.search(r':root,\s*\[data-theme="light"\]\s*\{(.*?)\}', css, re.S),
        "dark": re.search(r'\[data-theme="dark"\]\s*\{(.*?)\}', css, re.S),
    }
    for name, m in blocks.items():
        if m is None:
            raise SystemExit(f"check-tokens: no {name} token block in theme.css")
    out: dict[str, dict[str, str]] = {}
    for name in THEMES:
        m = blocks[name]
        assert m is not None
        out[name] = dict(re.findall(r"--([a-z0-9-]+)\s*:\s*([^;]+);", m.group(1)))
    out["dark"] = {**out["light"], **out["dark"]}
    return out


def evaluate(tokens: dict[str, dict[str, str]]) -> list[str]:
    failures: list[str] = []
    for theme in THEMES:
        t = tokens[theme]
        for fg, (need, surfaces) in TEXT_ON.items():
            for bg in surfaces:
                if fg not in t or bg not in t:
                    failures.append(f"[{theme}] --{fg} on --{bg}: a token is not defined in this theme")
                    continue
                got = ratio(t[fg], t[bg])
                if got + 1e-9 < need:
                    failures.append(
                        f"[{theme}] --{fg} {t[fg]} on --{bg} {t[bg]} is {got:.2f}:1, needs {need}:1"
                    )
        for border, surfaces in CONTROL_BORDERS.items():
            for bg in surfaces:
                got = ratio(t[border], t[bg])
                if got + 1e-9 < LARGE:
                    failures.append(
                        f"[{theme}] control border --{border} on --{bg} is {got:.2f}:1, needs {LARGE}:1"
                    )
    return failures


def css_sources() -> list[tuple[str, str]]:
    out = [(p.name, p.read_text(encoding="utf-8")) for p in sorted(SITE.glob("*.css"))]
    for html in sorted(SITE.glob("*.html")):
        text = html.read_text(encoding="utf-8")
        for i, m in enumerate(re.finditer(r"<style[^>]*>(.*?)</style>", text, re.S)):
            out.append((f"{html.name}<style #{i + 1}>", m.group(1)))
    return out


def audit_usage(tokens: dict[str, dict[str, str]]) -> list[str]:
    """Completeness: nothing the CSS paints is outside the table."""
    failures: list[str] = []
    known_text = set(TEXT_ON) | DECORATIVE
    surfaces = {s for _, ss in TEXT_ON.values() for s in ss}
    for name, raw in css_sources():
        css = strip_comments(raw)
        for rule in re.finditer(r"([^{}]+)\{([^{}]*)\}", css):
            sel, body = rule.group(1).strip(), rule.group(2)
            fg = re.findall(r"(?<![-\w])color\s*:\s*([^;]+)", body)
            bg = re.findall(r"(?<![-\w])background(?:-color)?\s*:\s*([^;]+)", body)
            fg_tokens = [m for v in fg for m in re.findall(r"var\(--([a-z0-9-]+)", v)]
            bg_tokens = [m for v in bg for m in re.findall(r"var\(--([a-z0-9-]+)", v)]
            for v in fg:
                v = v.strip()
                if re.match(r"#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(|oklch\(", v):
                    failures.append(f"{name}: `{sel}` paints a literal text colour ({v}); use a token")
            for tkn in fg_tokens:
                if tkn not in known_text:
                    failures.append(f"{name}: `{sel}` uses --{tkn} as a text colour and TEXT_ON does not classify it")
            for tkn in bg_tokens:
                if tkn not in surfaces and tkn not in {"white", "hero-bg", "primary", "ok"} and tkn in tokens["light"]:
                    failures.append(f"{name}: `{sel}` paints --{tkn} as a background and no text pair covers it")
            for f in fg_tokens:
                for b in bg_tokens:
                    if f in TEXT_ON and b in surfaces and b not in TEXT_ON[f][1]:
                        failures.append(
                            f"{name}: `{sel}` combines --{f} with --{b} and TEXT_ON does not list that pair"
                        )
    return failures


def selftest(tokens: dict[str, dict[str, str]]) -> int:
    bad = 0

    def expect(label: str, cond: bool) -> None:
        nonlocal bad
        print(f"  {'ok  ' if cond else 'FAIL'} {label}")
        if not cond:
            bad += 1

    expect("a known-good pair passes (black on white is 21:1)", abs(ratio("#000000", "#ffffff") - 21.0) < 0.01)
    expect("a known-bad pair fails (grey on grey is under 4.5)", ratio("#8b8d8a", "#f4f4f2") < NORMAL)
    expect(
        "the pair the pronunciation line shipped with is rejected (#5b5d5b on #16283a)",
        ratio("#5b5d5b", "#16283a") < NORMAL,
    )
    expect(
        "evaluate() reports a planted offender",
        any("--text" in f for f in evaluate({**tokens, "light": {**tokens["light"], "text": "#f0f0ee"}})),
    )
    expect(
        "the table is read in both themes (a dark-only offender is reported)",
        any("[dark]" in f for f in evaluate({**tokens, "dark": {**tokens["dark"], "text": "#222222"}})),
    )
    return 1 if bad else 0


def main(argv: list[str]) -> int:
    theme_path = SITE / "theme.css"
    if not theme_path.is_file():
        print(f"check-tokens: {theme_path} not found", file=sys.stderr)
        return 2
    tokens = resolve_tokens(theme_path.read_text(encoding="utf-8"))
    if "--selftest" in argv:
        return selftest(tokens)
    failures = evaluate(tokens) + audit_usage(tokens)
    if failures:
        print(f"contrast tokens: {len(failures)} offender(s)")
        for f in failures:
            print(f"  {f}")
        return 1
    pairs = sum(len(s) for _, s in TEXT_ON.values()) * len(THEMES)
    print(f"contrast tokens: ok, {pairs} text/background pairs held to WCAG AA in both themes")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
