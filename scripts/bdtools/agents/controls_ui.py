# EPIC U's rows whose proof is a browser spec, the contrast checker, the
# closing check's own self-test, the docs guards and the adversarial corpora
# (U2.3, U2.4, U2.5; #1351, #1352, #1353). Exec'd by selftest.py with Control
# and PLANT defined.
#
# U2.2 has no control here: nothing in the tree reads docs/design/1350-agents.html
# (no guard, no test), so there is no proof for a planted violation to turn red.
#
# Playwright runs only through the workspace lock, one run at a time, in the
# pinned e2e image, with the copy mounted at its own path and the real tree's
# ui/shared/node_modules linked in (the same arrangement as
# scripts/bdtools/encryption/controls_ui.py). The Go and shell controls run
# wherever the driver runs.

UI_LOCK = "/Users/rom/workspace/xdg-pw-lock.sh"
UI_TREE = "/Users/rom/workspace/retnd-lanes/rel080-matrix-wt"
UI_PW_IMAGE = "mcr.microsoft.com/playwright:v1.62.1-noble@sha256:dcc5531e97840b9b5e794f2814476b21571c5124a3fca2267d73041f56e7580e"
UI_LINKS = ["ui/shared/node_modules"]

MINT_DIALOG = "ui/shared/src/agents/MintDialog.tsx"
TOKENS_REGION = "ui/shared/src/agents/TokensRegion.tsx"
CONFIRM_REGION = "ui/shared/src/agents/ConfirmationsRegion.tsx"
AGENTS_CSS = "ui/shared/src/agents/agents.css"
MOCK_AGENTS = "ui/shared/src/agents/mockAgents.ts"
REFERENCE_HTML = "docs/site/reference.html"
CLOSED_SH = "scripts/release/check-epic-u-closed.sh"
AGENTGATE = "core/tests/agentgate/matrix_test.go"
MCP_SERVER = "core/agentmcp/server.go"
PROJECT_GO = "core/agenttools/project.go"
WEBHOST_MCP = "apps/common/webhost/agentmcp.go"


def ui_playwright(spec: str, grep: str = "") -> list[str]:
    g = f' -g "{grep}"' if grep else ""
    return [UI_LOCK, "sh", "-c",
            'w=$(cd ../.. && pwd -P); l=$(readlink "$w/ui/shared/node_modules" || echo "$w"); docker run --rm --init --cpus 3 --memory 4g --shm-size 1g --user 501:20 '
            f'-e HOME=/tmp/h -e CI=1 -v "$w:$w" -v "$l:$l" -v {UI_TREE}:{UI_TREE} -w "$w/ui/shared" {UI_PW_IMAGE} '
            f'npx playwright test -c e2e/playwright.config.ts --output /tmp/h/pw-out {spec}{g}']

CAP_TREE = "/Users/rom/workspace/retnd-lanes/rel080-cap-wt"
CAP_TAG = "retnd-capture:1.62.1"
CAP_HARNESS = "docs/site/tools/harness.mjs"


def capture(args: str) -> list[str]:
    # From the copy's root; the linked node_modules resolves into the real tree.
    # The image is the pinned Playwright image plus ffmpeg (retnd-capture:1.62.1).
    return [UI_LOCK, "sh", "-c",
            'w=$(pwd -P) && l=$(readlink "$w/ui/shared/node_modules" || echo "$w") && docker run --rm --init --cpus 3 --memory 5g --shm-size 1g --user 501:20 '
            f'-e HOME=/tmp/h -v "$w:$w" -v "$l:$l" -v {CAP_TREE}:{CAP_TREE} -w "$w" {CAP_TAG} '
            f'node docs/site/tools/capture-agents.mjs {args}']


CONTROLS = [
    # ------------------------------------------------------------ U2.3
    Control(
        "AGENTS the token kept in the list row after the dialog closes",
        ("U2.3",),
        [(MINT_DIALOG, "      onMinted();\n    } catch (e) {",
          "      onMinted(answer.secret as never); // " + PLANT.split("// ", 1)[1] + "the token goes to the list.\n    } catch (e) {"),
         (TOKENS_REGION, "onMinted={() => { tokens.reload(); onChanged(); }}",
          "onMinted={(s?: string) => { (globalThis as any).__lastSecret = s ?? \"\"; tokens.reload(); onChanged(); }}"),
         (TOKENS_REGION, "data-token-prefix=\"\">{t.prefix}</td>",
          "data-token-prefix=\"\">{t.prefix}<i hidden>{(globalThis as any).__lastSecret}</i></td>")],
        "",
        module="ui/shared",
        cmd=ui_playwright("e2e/1351-agents-mint.spec.ts", "after I have stored it the token is nowhere"),
        needles=["after I have stored it the token is nowhere", "1 failed"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "AGENTS Approve and run enabled with no password typed",
        ("U2.3",),
        [(CONFIRM_REGION, "disabled={busy || !hasPassword} onClick={() => void approve()}",
          "disabled={busy} onClick={() => void approve()} /* " + PLANT.split("// ", 1)[1] + "no password needed. */")],
        "",
        module="ui/shared",
        cmd=ui_playwright("e2e/1351-agents-confirmations.spec.ts", "pressing Approve asks for the password and runs nothing"),
        needles=["pressing Approve asks for the password and runs nothing", "1 failed"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "AGENTS one tool hard-coded into the scope disclosure",
        ("U2.3",),
        [(TOKENS_REGION, "{catalogue.data!.tools.filter((t) => t.scope === s).map((t) => <li key={t.name}><code>{t.name}</code></li>)}",
          "{catalogue.data!.tools.filter((t) => t.scope === s).map((t) => <li key={t.name}><code>{t.name}</code></li>)}{s === \"read\" ? <li key=\"planted\"><code>planted_tool</code></li> : null}{/* " + PLANT.split("// ", 1)[1] + "a tool the catalogue does not list. */}")],
        "",
        module="ui/shared",
        cmd=ui_playwright("e2e/1351-agents-card.spec.ts", "lists what the catalogue lists, by scope"),
        needles=["lists what the catalogue lists, by scope", "1 failed"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "AGENTS the secondary ink lightened under 4.5 to 1 on the card",
        ("U2.3",),
        [(AGENTS_CSS, ".ag-secondary { color: var(--text-2); font-size: var(--text-xs); }",
          ".ag-secondary { color: var(--border); font-size: var(--text-xs); } /* " + PLANT.split("// ", 1)[1] + "ink under 4.5 to 1. */")],
        "",
        module="ui/shared",
        cmd=ui_playwright("e2e/1351-agents-contrast.spec.ts", "the tokens region, the scope list and the transport line"),
        needles=["the tokens region, the scope list and the transport line", "1 failed"],
        links=UI_LINKS,
        timeout="20m",
    ),
    Control(
        "AGENTS the secondary ink token swapped for a pale one in the pair checker",
        ("U2.3",),
        [("ui/shared/src/design-system/tokens.css", "  --text-2: #5b5d5b;",
          "  --text-2: #9a9c99; /* " + PLANT.split("// ", 1)[1] + "*/")],
        "",
        cmd=["python3", "scripts/contrast/check-ui-pairs.py"],
        needles=["check-ui-pairs:", ".ag-secondary", "text-2"],
    ),
    # ------------------------------------------------------------ U2.4
    Control(
        "AGENTS a file name put into the text of a tool result",
        ("U2.4",),
        [(MCP_SERVER, "Message: staticOKMessage, CorrelationID: correlated, Command: command, Data: data, Untrusted: untrusted}",
          "Message: staticOKMessage + string(resp.Body), CorrelationID: correlated, Command: command, Data: data, Untrusted: untrusted} // " + PLANT.split("// ", 1)[1] + "data in the text.")],
        "./tests/agentinjection/",
        ["TestAnUntrustedStringArrivesOnlyAsData"],
        timeout="20m",
    ),
    Control(
        "AGENTS the projection skipped so a recipient leaves encryption_state",
        ("U2.4",),
        [(MCP_SERVER, "		decoded = agenttools.Project(decoded)\n",
          "		_ = agenttools.Project // " + PLANT.split("// ", 1)[1] + "the projection is not applied.\n")],
        "./tests/agentexfiltration/",
        ["TestNoKeyMaterialOnAnyAgentSurface_1352_AllAtOnce"],
        timeout="20m",
    ),
    Control(
        "AGENTS key shapes no longer withheld from free text",
        ("U2.4",),
        [(PROJECT_GO, "		for _, shape := range CanaryShapes {\n			if shape.MatchString(x) {",
          "		for _, shape := range CanaryShapes[:0] { // " + PLANT.split("// ", 1)[1] + "no shape is matched.\n			if shape.MatchString(x) {")],
        "./tests/agentexfiltration/",
        ["TestNoKeyShapeInFreeTextOnAnyAgentSurface_1352"],
        timeout="20m",
    ),
    Control(
        "AGENTS a revoked token's event stream kept open",
        ("U2.4",),
        [(WEBHOST_MCP, "errors.Is(err, service.ErrAgentTokenInvalid) {\n				cancel()\n				return",
          "errors.Is(err, service.ErrAgentTokenInvalid) {\n				_ = cancel // " + PLANT.split("// ", 1)[1] + "the stream stays open.\n				return")],
        "./webhost/",
        ["TestARevokedTokenMidStreamIsRefusedAndTheStreamCloses"],
        module="apps/common",
        timeout="20m",
    ),
    Control(
        "AGENTS the gate sentence reworded on one surface",
        ("U2.4",),
        [(MOCK_AGENTS, '"destructive operations are refused over HTTP in this release,',
          '"destructive operations are blocked over HTTP in this release,')],
        "",
        module="ui/shared",
        cmd=ui_playwright("e2e/1352-agent-equivalence.spec.ts", "an approved run_cycle at the shut gate"),
        needles=["an approved run_cycle at the shut gate", "1 failed"],
        links=UI_LINKS,
        timeout="20m",
    ),
    # ------------------------------------------------------------ U2.5
    Control(
        "AGENTS confirmations dropped from the site reference row",
        ("U2.5",),
        [(REFERENCE_HTML, "<code>agent confirmations list [--json]</code>", "<code>agent list [--json]</code>"),
         (REFERENCE_HTML, "<code>agent confirmations show ID [--json]</code>", "<code>agent show ID [--json]</code>"),
         (REFERENCE_HTML, "<code>agent confirmations approve ID</code>", "<code>agent approve ID</code>"),
         (REFERENCE_HTML, "<code>agent confirmations deny ID</code>", "<code>agent deny ID</code>")],
        "./packaging/",
        ["TestTheSiteReferenceAgentRowNamesEveryAgentVerb"],
        module="distribution",
        timeout="20m",
    ),
    Control(
        "AGENTS the compose edit guard left out of the closing check",
        ("U2.5",),
        [(CLOSED_SH, 'if [ -n "$moved" ]; then\n  bad "the canonical container contract moved',
          'if false; then # ' + PLANT.split("// ", 1)[1] + "the contract guard is off.\n  bad \"the canonical container contract moved")],
        "",
        cmd=["bash", "scripts/tests/epic-u-closure.test.sh"],
        needles=["FAIL the canonical deployment: an edit to container/compose.yaml is refused"],
    ),
    Control(
        "AGENTS the second-migration guard left out of the closing check",
        ("U2.5",),
        [(CLOSED_SH, 'if printf \'%s\\n\' "$added" | grep -qxF "$want" && [ -z "$unexpected" ] && [ -z "$touched" ]; then',
          'if printf \'%s\\n\' "$added" | grep -qxF "$want"; then # ' + PLANT.split("// ", 1)[1] + "extra migrations pass.")],
        "",
        cmd=["bash", "scripts/tests/epic-u-closure.test.sh"],
        needles=["FAIL migrations: a second new migration is refused, and named"],
    ),
    Control(
        "AGENTS the BLOCKED row guard left out of the closing check",
        ("U2.5",),
        [(CLOSED_SH, 'if [ -n "$blocked" ]; then',
          'if false; then # ' + PLANT.split("// ", 1)[1] + "a BLOCKED row passes.")],
        "",
        cmd=["bash", "scripts/tests/epic-u-closure.test.sh"],
        needles=["FAIL the matrix: a BLOCKED row is refused, and named"],
    ),
    Control(
        "AGENTS a BLOCKED row let through by the closing mode of the matrix guard",
        ("U2.5",),
        [(AGENTGATE, '		case "BLOCKED":\n			if closing {',
          '		case "BLOCKED":\n			if false && closing { // ' + PLANT.split("// ", 1)[1] + "closing mode lets a BLOCKED row by.")],
        "./tests/agentgate/",
        ["TestTheMatrixCheckerCanLie"],
        timeout="20m",
    ),
    Control(
        "AGENTS the capture clock left unpinned",
        ("U2.5",),
        [(CAP_HARNESS, "  await page.clock.setFixedTime(FROZEN_TIME);",
          "  // " + PLANT.split("// ", 1)[1] + "the clock is not pinned.")],
        "",
        cmd=capture("--check --only audit"),
        needles=["NOT REPRODUCIBLE", "agents-audit"],
        links=UI_LINKS,
        timeout="20m",
    ),
]
