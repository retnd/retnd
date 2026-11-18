# EPIC U's catalogue, transport and parity rows (U1.3 to U1.6, UV.4 to UV.6,
# UV.8, UV.13, UV.19, UV.20, UC-03) (#1346, #1347, #1348). Exec'd by
# selftest.py with Control and PLANT defined.
#
# Each plant is one violation named in the row's Falsification cell. The red
# run has to name the test the row cites; the clean run has to pass it.

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    # Only for the type checker: at run time selftest.py execs this file
    # with both names already in scope.
    from bdtools.agents.selftest import PLANT, Control

TOOLS_GO = "core/agenttools/tools.go"
ROUTER_GO = "apps/common/webhost/router.go"
AGENTACCESS_GO = "apps/common/webhost/agentaccess.go"
AGENTSERVE_GO = "core/cmd/retnd/agentserve.go"
AGENTS_CFG = "core/internal/config/agents.go"
CONFIG_GO = "core/internal/config/config.go"
RESULT_GO = "core/agentmcp/result.go"
MCP_SERVER_GO = "core/agentmcp/server.go"
SETUP_GO = "core/cmd/retnd/setup.go"
AGENTTOKENS_SVC = "core/service/agenttokens.go"

CONTROLS = [
    Control(
        "a denied prefix dropped from the server side bearer path while the catalogue still lists it",
        ("U1.3",),
        [(AGENTACCESS_GO, "\t\tif agenttools.Denied(r.Method, p) {",
          "\t\tif !strings.HasPrefix(p, \"/encryption/keys\") && agenttools.Denied(r.Method, p) { " + PLANT.strip())],
        "./webhost/",
        ["TestABearerIsRefusedEveryRouteOnTheDenyListOnTheWire"],
        module="apps/common",
    ),
    Control(
        "a route registered with no row in the catalogue",
        ("U1.3",),
        [(ROUTER_GO, '\t\tr.Get("/agent/tools", h.listAgentTools)',
          '\t\tr.Get("/agent/tools", h.listAgentTools)\n\t\tr.Get("/agent/zz-planted", h.listAgentTools) ' + PLANT.strip())],
        "./webhost/",
        ["TestEveryAPIRouteIsAToolOrANamedRefusal"],
        module="apps/common",
    ),
    Control(
        "a route registered with neither a tool nor a refusal nor a CLI twin",
        ("UV.5", "UC-03"),
        [(ROUTER_GO, '\t\tr.Get("/agent/tools", h.listAgentTools)',
          '\t\tr.Get("/agent/tools", h.listAgentTools)\n\t\tr.Get("/agent/zz-planted", h.listAgentTools) ' + PLANT.strip())],
        "./webhost/",
        ["TestEveryAPIRouteNamesItsCLIEquivalentOrTheGap"],
        module="apps/common",
    ),
    Control(
        "a tool registered over the key export route",
        ("U1.3", "UV.4"),
        [(TOOLS_GO, "var toolRows = []Row{\n",
          'var toolRows = []Row{\n\t{Tool: "key_export", Method: "POST", Route: "/encryption/keys/{id}/export", Scope: ScopeOperate, Description: "x", Untrusted: []string{}}, ' + PLANT.strip() + "\n")],
        "./agenttools/",
        ["TestNoToolExistsOverADeniedRoute"],
    ),
    Control(
        "a backup set name interpolated into a tool description",
        ("U1.3", "UV.6"),
        [(TOOLS_GO, 'Description: "Read the health of every backup set: its state and the reason for it, the newest good backup, quarantined and stale counts, free space and copies away from home.",',
          'Description: "Read the health of every backup set: its state and the reason for it, the newest good backup, quarantined and stale counts, free space and copies away from home." + string(ScopeRead), ' + PLANT.strip())],
        "./agenttools/",
        ["TestToolDescriptionsAreStaticAndPinned"],
    ),
    Control(
        "a --token flag added to agent serve",
        ("U1.4", "UV.19"),
        [(AGENTSERVE_GO, '\ttokenFile = fs.String("token-file", "", "a file holding the agent token, readable by its owner only (mode 0600)")',
          '\ttokenFile = fs.String("token-file", "", "a file holding the agent token, readable by its owner only (mode 0600)")\n\tfs.String("token", "", "the token itself") ' + PLANT.strip())],
        "./cmd/retnd/",
        ["TestTheTokenNeverTravelsOnTheCommandLine"],
    ),
    Control(
        "a token file of mode 0644 accepted",
        ("U1.4", "UV.19"),
        [(AGENTSERVE_GO, "info.Mode().Perm()&0o077 != 0 {",
          "info.Mode().Perm()&0o077 != 0 && false { " + PLANT.strip())],
        "./cmd/retnd/",
        ["TestTokenFileMustBePrivate"],
    ),
    Control(
        "the agents HTTP transport defaulted to on",
        ("U1.4", "UV.20"),
        [(AGENTS_CFG, "\t\tHTTPTransport:           AgentsHTTPTransportOff,\n\t\tRateLimitPerMinute:",
          "\t\tHTTPTransport:           AgentsHTTPTransportOn, " + PLANT.strip() + "\n\t\tRateLimitPerMinute:")],
        "./internal/config/",
        ["TestAConfigWritingNothingResolvesTheAgentDefaults"],
    ),
    Control(
        "a Scope field added to the agents block so fixture 66 loads",
        ("U1.4",),
        [(AGENTS_CFG, "\tMaxPendingConfirmations *int      `yaml:\"max_pending_confirmations,omitempty\"`\n}",
          "\tMaxPendingConfirmations *int      `yaml:\"max_pending_confirmations,omitempty\"`\n\tScope                   string    `yaml:\"scope,omitempty\"` " + PLANT.strip() + "\n}")],
        "./internal/config/",
        ["TestAnUnknownKeyInTheAgentsBlockFailsLoad"],
    ),
    Control(
        "KnownFields dropped from the config loader so fixture 66 loads",
        ("U1.4",),
        [(CONFIG_GO, "\tdec := yaml.NewDecoder(bytes.NewReader(data))\n\tdec.KnownFields(true)\n\tif err := dec.Decode(&cfg); err != nil {\n\t\tif errors.Is(err, io.EOF) {\n\t\t\treturn nil, fmt.Errorf(\"parsing config %q: file is empty\"",
          "\tdec := yaml.NewDecoder(bytes.NewReader(data))\n\tdec.KnownFields(false) " + PLANT.strip() + "\n\tif err := dec.Decode(&cfg); err != nil {\n\t\tif errors.Is(err, io.EOF) {\n\t\t\treturn nil, fmt.Errorf(\"parsing config %q: file is empty\"")],
        "./internal/config/",
        ["TestAnUnknownKeyInTheAgentsBlockFailsLoad"],
    ),
    Control(
        "the mint accepting the scope word admin",
        ("U1.4",),
        [(AGENTTOKENS_SVC, "\tcase AgentScopeRead, AgentScopeOperate, AgentScopeConfigure:\n\tdefault:\n\t\treturn MintedAgentToken{}, fmt.Errorf(\"%w: scope:",
          "\tcase AgentScopeRead, AgentScopeOperate, AgentScopeConfigure, \"admin\": " + PLANT.strip() + "\n\tdefault:\n\t\treturn MintedAgentToken{}, fmt.Errorf(\"%w: scope:")],
        "./service/",
        ["TestScopeIsAClosedVocabularyAndReadIsTheDefault"],
    ),
    Control(
        "a destructive gate refusal mapped to exit 1 in the tool result",
        ("U1.5", "UV.8", "UC-03"),
        [(RESULT_GO, "\tcase \"DESTRUCTIVE_OPERATIONS_DISABLED\":\n\t\treturn ExitEngineGateShut",
          "\tcase \"DESTRUCTIVE_OPERATIONS_DISABLED\":\n\t\treturn ExitFailure " + PLANT.strip())],
        "./agentmcp/",
        ["TestARefusalIsAnIsErrorResultWithTheAPIsWords", "TestExitCodeIsTheCLIsFourCodeTable"],
    ),
    Control(
        "an insecure transport refusal mapped to exit 1 by the CLI",
        ("U1.5", "UV.8", "UC-03"),
        [(SETUP_GO, "\tcase apicontract.ErrorCodeInsecureTransportAckRequired:\n\t\treturn exitInsecureTransport, fmt.Sprintf(",
          "\tcase apicontract.ErrorCodeInsecureTransportAckRequired:\n\t\treturn exitFailure, fmt.Sprintf(")],
        "./cmd/retnd/",
        ["TestRoutedReview1250_TheGateExitsFiftyOnEveryRoutedCall", "TestAnExecutionsExitCodeIsTheOneEngineRefusalExitGives"],
    ),
    Control(
        "a destructive gate refusal mapped to exit 1 in the tool result, compared with the CLI",
        ("U1.5", "UV.8", "UC-03"),
        [(RESULT_GO, "\tcase \"DESTRUCTIVE_OPERATIONS_DISABLED\":\n\t\treturn ExitEngineGateShut",
          "\tcase \"DESTRUCTIVE_OPERATIONS_DISABLED\":\n\t\treturn ExitFailure " + PLANT.strip())],
        "./cmd/retnd/",
        ["TestAToolResultAnswersAsTheCLIWouldOnTheGateAndTransportExits_1378"],
    ),
    Control(
        "an insecure transport refusal mapped to exit 1 by the CLI, compared with the tool result",
        ("U1.5", "UV.8", "UC-03"),
        [(SETUP_GO, "\tcase apicontract.ErrorCodeInsecureTransportAckRequired:\n\t\treturn exitInsecureTransport, fmt.Sprintf(",
          "\tcase apicontract.ErrorCodeInsecureTransportAckRequired:\n\t\treturn exitFailure, fmt.Sprintf(")],
        "./cmd/retnd/",
        ["TestAToolResultAnswersAsTheCLIWouldOnTheGateAndTransportExits_1378"],
    ),
    Control(
        "the per token rate limiter removed",
        ("U1.6", "UV.13"),
        [(AGENTACCESS_GO, "if ok, wait := guard.allowCall(principal.TokenID); !ok {",
          "if ok, wait := guard.allowCall(principal.TokenID); !ok && false { " + PLANT.strip())],
        "./webhost/",
        ["TestTheSixtyFirstCallInAMinuteIsRefused", "TestTheConfiguredRateIsTheOneThatApplies"],
        module="apps/common",
    ),
    Control(
        "an oversize answer from a route with no cursor returned instead of refused",
        ("U1.6",),
        [(MCP_SERVER_GO, "\t\tif _, paged := body[\"next_cursor\"]; !paged {\n\t\t\tbreak\n\t\t}",
          "\t\tif _, paged := body[\"next_cursor\"]; !paged { " + PLANT.strip() + "\n\t\t\tdata, untrusted := splitUntrusted(decoded, t.Untrusted)\n\t\t\treturn Result{OK: true, Code: CodeOK, Message: staticOKMessage, CorrelationID: correlated, Command: command, Data: data, Untrusted: untrusted}\n\t\t}")],
        "./agentmcp/",
        ["TestAnOversizeAnswerFromARouteWithNoPageIsRefusedNotCut"],
    ),
    Control(
        "an oversize paged answer re-asked without a smaller limit",
        ("U1.6",),
        [(MCP_SERVER_GO, "\t\tquery.Set(\"limit\", strconv.Itoa(n/2))",
          "\t\t_ = n/2 " + PLANT.strip())],
        "./agentmcp/",
        ["TestAnOversizeAnswerIsPagedWithTheRoutesCursorAndNeverTruncated"],
    ),
]
