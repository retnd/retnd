# The EPIC N and EPIC T rows whose proof is a browser spec, a vitest test or
# the contrast checker (#1331, round 2). Exec'd by selftest.py with Control and
# PLANT defined.
#
# The Playwright specs and the browser contrast check run in the Chromium lane
# (scripts/e2e/ui-browser.sh) from the planted copy: the lane mounts the copy
# read-only at its own path, makes its own linux install of ui/shared, and
# brings the pinned Playwright image, a cap and a stall check, so nothing here
# names a machine (#1543). vitest and tsc need node and ui/shared's
# dependencies and no browser, so they run with the local node or the pinned
# node image (ui_node, #1538). The pair checker is plain python.

from typing import TYPE_CHECKING

from bdtools import selftest_lanes

if TYPE_CHECKING:
    # Only for the type checker: at run time selftest.py execs this file
    # with both names already in scope.
    from bdtools.encryption.selftest import Control

SUITE_CONTROLS = "ui/shared/src/encryption/SuiteControls.tsx"
TOKENS_CSS = "ui/shared/src/design-system/tokens.css"
CHIPS_TSX = "ui/shared/src/encryption/EncryptionChips.tsx"
ENC_CSS = "ui/shared/src/encryption/encryption.css"
SET_CARD = "ui/shared/src/encryption/SetEncryptionCard.tsx"
TOOLTIPS_JSON = "ui/shared/src/tooltips/tooltips.json"
FIELD_HELP = "ui/shared/src/components/fieldHelpCopy.ts"
INFO_TOOLTIP = "ui/shared/src/tooltips/InfoTooltip.tsx"
OPERATION_TS = "ui/shared/src/types/operation.ts"
VERIFY_DIALOG = "ui/shared/src/encryption/VerifyRecoverableDialog.tsx"
CLIENT_TS = "ui/shared/src/api/client.ts"
SITE_THEME = "docs/site/theme.css"


UI_NODE_IMAGE = "node:22-bookworm-slim"
UI_LINKS = ["ui/shared/node_modules"]


def ui_node(exe: str, env: str) -> list[str]:
    # vitest and tsc need node and ui/shared's dependencies, not a browser. The
    # copy's node_modules is a link into the real tree. With node and that link
    # in place they run right here (the gate container has both); otherwise in
    # the pinned node image with the link's target mounted beside the copy, the
    # way the agents helpers do it. `npm exec --no` never downloads: a missing
    # tool is a failure, not a fetch. The `then ... npm exec --no` is what
    # selftest_lanes reads as the node lane, and it needs node_modules either way.
    flags = " ".join("-e " + e_ for e_ in env.split())
    return ["sh", "-c",
            'w=$(cd ../.. && pwd -P); if command -v node >/dev/null 2>&1 && [ -d node_modules ]; then '
            f'env {env} npm exec --no -- {exe}; else '
            'l=$(readlink node_modules || echo "$w/ui/shared"); '
            f'docker run --rm --cpus 3 --memory 4g --user 501:20 -e HOME=/tmp/h {flags} '
            f'-v "$w:$w" -v "$l:$l" -w "$w/ui/shared" {UI_NODE_IMAGE} npm exec --no -- {exe}; fi']


def ui_tsc() -> list[str]:
    return ui_node("tsc --noEmit --pretty false", "CI=1")


def ui_vitest(files: str, name: str = "") -> list[str]:
    t = f' -t "{name}"' if name else ""
    return ui_node(f"vitest run {files}{t}", "CI=1 NO_COLOR=1 FORCE_COLOR=0")


CONTROLS = [
    Control(
        "UI the suite change saves without the rotate step",
        ("T2.3",),
        [(SUITE_CONTROLS,
          '            if (p.rotationNeeded) setStep("rotate");\n            else onClose();',
          '            onClose(); // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): no rotate step.')],
        "",
        cmd=selftest_lanes.spec("e2e/1318-encryption-suites.spec.ts", "in that order"),
        needles=["patch, rotate, download and confirm, in that order", "1 failed"],
        timeout="20m",
    ),
    Control(
        "UI the secondary ink lightened under 4.5 to 1 in the light theme",
        ("T2.3",),
        [(TOKENS_CSS, "  --text-2: #5b5d5b;", "  --text-2: #9a9c99; /* PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py) */")],
        "",
        cmd=["python3", "scripts/contrast/check-ui-pairs.py"],
        needles=["check-ui-pairs:", ".esuite__desc", "text-2"],
    ),
    Control(
        "UI Verified recoverable drawn with the glyph and colours of Encrypted",
        ("N2.1",),
        [(CHIPS_TSX, '  verified: { icons: ["success", "success"] },',
          '  verified: { text: "\\u25A3" }, // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): the Encrypted glyph.'),
         (ENC_CSS, '.echip[data-state="verified"] { border-color: var(--ok); background: var(--ok-quiet); }\n.echip[data-state="verified"] .echip__glyph { color: var(--ok); }',
          '/* PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): verified draws as encrypted. */\n.echip[data-state="verified"] { border-style: solid; }')],
        "",
        cmd=selftest_lanes.spec("e2e/995-encryption-fields.spec.ts", "differ in edge, fill and glyph"),
        needles=["Encrypted and Verified recoverable differ in edge, fill and glyph in light", "2 failed"],
        timeout="20m",
    ),
    Control(
        "UI the per-set encryption control taken off the set card",
        ("N2.2",),
        [(SET_CARD, '  const resolved = mine.effectiveMode === "required" ? "required" : "off";\n\n  return frame(',
          '  const resolved = mine.effectiveMode === "required" ? "required" : "off";\n\n  if (resolved) return null; // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): no per-set control.\n  return frame(')],
        "",
        module="ui/shared",
        cmd=ui_vitest("src/test/encryption-995.test.tsx"),
        needles=["is what a set that inherits resolves to afterwards", "failed"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "UI a tooltip entry that nothing in the app names",
        ("N2.3", "NV.33"),
        [(TOOLTIPS_JSON, '  "encryption.state.verifiedRecoverable": {',
          '  "encryption.state.plantedOrphan": {\n    "label": "Planted orphan",\n    "html": "PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): an entry nothing names."\n  },\n  "encryption.state.verifiedRecoverable": {')],
        "",
        module="ui/shared",
        cmd=ui_vitest("src/test/registry-tooltips.test.tsx"),
        needles=["has no entry that nothing in the app names", "encryption.state.plantedOrphan"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "UI the suite field help without its effect",
        ("N2.3",),
        [(FIELD_HELP, '    example: "age-pq/v1",\n    effect:\n      "The policy',
          '    example: "age-pq/v1",\n    // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): the effect is gone.\n    effecz:\n      "The policy')],
        "",
        module="ui/shared",
        cmd=ui_tsc(),
        needles=["fieldHelpCopy.ts", "effect"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "UI pop-ups that never open with tooltips on",
        ("N2.3", "NV.33"),
        [(INFO_TOOLTIP, "useHoverPopover<HTMLSpanElement>({ enabled: visible })",
          "useHoverPopover<HTMLSpanElement>({ enabled: false && visible }) /* PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py) */")],
        "",
        module="ui/shared",
        cmd=ui_vitest("src/test/encryption-995.test.tsx"),
        needles=["shows a chip's pop-up on hover with tooltips on", "failed"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "UI the Encrypting stage dropped from the progress checklist",
        ("N2.4",),
        [(OPERATION_TS, "  return TRANSFER_STAGES.filter((s) => encrypted || !ENCRYPTED_STAGES.includes(s));",
          '  return TRANSFER_STAGES.filter((s) => s !== "encrypting" && (encrypted || !ENCRYPTED_STAGES.includes(s))); // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py)')],
        "",
        module="ui/shared",
        cmd=ui_vitest("src/test/progress-stages-encrypted-1290.test.tsx"),
        needles=["marks Encrypting as the one current step", "failed"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "UI following a running decrypt-verify starts a second one",
        ("N2.4",),
        [(VERIFY_DIALOG, "        if (!live) return;\n        setRecord(r);\n",
          "        if (!live) return;\n        setRecord(r);\n        // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): re-attaching starts another operation.\n        if (r.state === \"running\") void api.startDecryptVerify({ artifactId, medium: copy.medium, acknowledgeEgress: true });\n")],
        "",
        module="ui/shared",
        cmd=ui_vitest("src/test/encryption-995.test.tsx"),
        needles=["follows the operation from the durable record, once", "failed"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "UI the exported key material kept in local storage",
        ("N2.4",),
        [(CLIENT_TS, '  const receipt = { keyId: bundle.key_id, digest: bundle.digest, fileName };\n',
          '  const receipt = { keyId: bundle.key_id, digest: bundle.digest, fileName };\n  window.localStorage.setItem("retnd.lastBundle", bundle.material); // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py)\n')],
        "",
        module="ui/shared",
        cmd=ui_vitest("src/test/encryption-995.test.tsx"),
        needles=["is saved as a file by the client and only a receipt leaves it", "failed"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "UI the chip edge darkened under 3 to 1 in the dark theme",
        ("N2.10",),
        [(TOKENS_CSS, "  --edge-3: #767976;", "  --edge-3: #3a3c3a; /* PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py) */")],
        "",
        cmd=["python3", "scripts/contrast/check-ui-pairs.py"],
        needles=["check-ui-pairs:", ".echip", "edge-3"],
    ),
    Control(
        "docs site the secondary text lightened under 4.5 to 1 in the light theme",
        ("N2.10",),
        [(SITE_THEME, "  --text-light: #5b5d5b;\n  --text-faint: #696b68;",
          "  --text-light: #9a9c99; /* PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py) */\n  --text-faint: #696b68;")],
        "",
        # scripts/contrast/run-browser-check.sh is this same check with its own
        # docker run; in the lane the image is already the one it pins.
        cmd=["bash", selftest_lanes.LANE, "run", "sh", "-c",
             'CONTRAST_SITE="$PWD/docs/site" bash scripts/contrast/in-container.sh --only web-ui.html'],
        needles=["web-ui.html", "light"],
        timeout="20m",
    ),
]
