# The EPIC N and EPIC T rows whose proof is a browser spec, a vitest test or
# the contrast checker (#1331, round 2). Exec'd by selftest.py with Control and
# PLANT defined.
#
# The Playwright specs, vitest, tsc and the browser contrast check run in the
# Chromium lane (scripts/e2e/ui-browser.sh) from the planted copy: the lane
# mounts the copy read-only at its own path, makes its own linux install of
# ui/shared (never the host's node_modules, which on a Mac hold darwin
# binaries), and brings the pinned Playwright image, a cap and a stall check,
# so nothing here names a machine (#1543). The pair checker is plain python.

from typing import TYPE_CHECKING

from bdtools import selftest_lane

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


CONTROLS = [
    Control(
        "UI the suite change saves without the rotate step",
        ("T2.3",),
        [(SUITE_CONTROLS,
          '            if (p.rotationNeeded) setStep("rotate");\n            else onClose();',
          '            onClose(); // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): no rotate step.')],
        "",
        cmd=selftest_lane.spec("e2e/1318-encryption-suites.spec.ts", "in that order"),
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
        cmd=selftest_lane.spec("e2e/995-encryption-fields.spec.ts", "differ in edge, fill and glyph"),
        needles=["Encrypted and Verified recoverable differ in edge, fill and glyph in light", "2 failed"],
        timeout="20m",
    ),
    Control(
        "UI the per-set encryption control taken off the set card",
        ("N2.2",),
        [(SET_CARD, '  const resolved = mine.effectiveMode === "required" ? "required" : "off";\n\n  return frame(',
          '  const resolved = mine.effectiveMode === "required" ? "required" : "off";\n\n  if (resolved) return null; // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): no per-set control.\n  return frame(')],
        "",
        cmd=selftest_lane.in_ui_shared("npx vitest run src/test/encryption-995.test.tsx"),
        needles=["is what a set that inherits resolves to afterwards", "failed"],
        timeout="20m",
    ),
    Control(
        "UI a tooltip entry that nothing in the app names",
        ("N2.3", "NV.33"),
        [(TOOLTIPS_JSON, '  "encryption.state.verifiedRecoverable": {',
          '  "encryption.state.plantedOrphan": {\n    "label": "Planted orphan",\n    "html": "PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): an entry nothing names."\n  },\n  "encryption.state.verifiedRecoverable": {')],
        "",
        cmd=selftest_lane.in_ui_shared("npx vitest run src/test/registry-tooltips.test.tsx"),
        needles=["has no entry that nothing in the app names", "encryption.state.plantedOrphan"],
        timeout="20m",
    ),
    Control(
        "UI the suite field help without its effect",
        ("N2.3",),
        [(FIELD_HELP, '    example: "age-pq/v1",\n    effect:\n      "The policy',
          '    example: "age-pq/v1",\n    // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): the effect is gone.\n    effecz:\n      "The policy')],
        "",
        cmd=selftest_lane.in_ui_shared("npx tsc --noEmit --pretty false"),
        needles=["fieldHelpCopy.ts", "effect"],
        timeout="20m",
    ),
    Control(
        "UI pop-ups that never open with tooltips on",
        ("N2.3", "NV.33"),
        [(INFO_TOOLTIP, "useHoverPopover<HTMLSpanElement>({ enabled: visible })",
          "useHoverPopover<HTMLSpanElement>({ enabled: false && visible }) /* PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py) */")],
        "",
        cmd=selftest_lane.in_ui_shared("npx vitest run src/test/encryption-995.test.tsx"),
        needles=["shows a chip's pop-up on hover with tooltips on", "failed"],
        timeout="20m",
    ),
    Control(
        "UI the Encrypting stage dropped from the progress checklist",
        ("N2.4",),
        [(OPERATION_TS, "  return TRANSFER_STAGES.filter((s) => encrypted || !ENCRYPTED_STAGES.includes(s));",
          '  return TRANSFER_STAGES.filter((s) => s !== "encrypting" && (encrypted || !ENCRYPTED_STAGES.includes(s))); // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py)')],
        "",
        cmd=selftest_lane.in_ui_shared("npx vitest run src/test/progress-stages-encrypted-1290.test.tsx"),
        needles=["marks Encrypting as the one current step", "failed"],
        timeout="20m",
    ),
    Control(
        "UI following a running decrypt-verify starts a second one",
        ("N2.4",),
        [(VERIFY_DIALOG, "        if (!live) return;\n        setRecord(r);\n",
          "        if (!live) return;\n        setRecord(r);\n        // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): re-attaching starts another operation.\n        if (r.state === \"running\") void api.startDecryptVerify({ artifactId, medium: copy.medium, acknowledgeEgress: true });\n")],
        "",
        cmd=selftest_lane.in_ui_shared("npx vitest run src/test/encryption-995.test.tsx"),
        needles=["follows the operation from the durable record, once", "failed"],
        timeout="20m",
    ),
    Control(
        "UI the exported key material kept in local storage",
        ("N2.4",),
        [(CLIENT_TS, '  const receipt = { keyId: bundle.key_id, digest: bundle.digest, fileName };\n',
          '  const receipt = { keyId: bundle.key_id, digest: bundle.digest, fileName };\n  window.localStorage.setItem("retnd.lastBundle", bundle.material); // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py)\n')],
        "",
        cmd=selftest_lane.in_ui_shared("npx vitest run src/test/encryption-995.test.tsx"),
        needles=["is saved as a file by the client and only a receipt leaves it", "failed"],
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
        cmd=["bash", selftest_lane.LANE, "run", "sh", "-c",
             'CONTRAST_SITE="$PWD/docs/site" bash scripts/contrast/in-container.sh --only web-ui.html'],
        needles=["web-ui.html", "light"],
        timeout="20m",
    ),
]
