# The EPIC U confirmation rows: U2.1, U2.1b, UC-04 and the UV.9 to UV.12
# ledger lines (#1349, #1353). Exec'd by selftest.py with Control and PLANT
# defined.

CNF_WEB = "apps/common/webhost/agentconfirmations.go"
CNF_ACCESS = "apps/common/webhost/agentaccess.go"
CNF_GATE = "apps/common/webhost/gate_profile.go"
CNF_SVC = "core/service/agentconfirmations.go"
CNF_STATE = "core/internal/state/agentconfirmations.go"
CNF_DENIED = "core/agenttools/denied.go"
CNF_SETUP = "core/cmd/retnd/setup.go"
WEB = "./webhost/"

CONTROLS = [
    Control(
        "the proposer lets the tool call fall through to the handler and run",
        ("U2.1", "UV.9"),
        [(CNF_ACCESS,
          "\t\t\taudit(principal.TokenID, principal.Name, status, code, correlationID)\n\t\t\treturn nil, false\n",
          "\t\t\taudit(principal.TokenID, principal.Name, status, code, correlationID)\n\t\t\treturn r, true " + PLANT + "the call runs.\n")],
        WEB,
        ["TestAnOperateToolCallCreatesAConfirmationRecordingTheRequestAndRunsNothing"],
        module="apps/common",
    ),
    Control(
        "approve accepts a body and no longer refuses a substituted one",
        ("U2.1", "UV.10", "UC-04"),
        [(CNF_WEB,
          "if first, _ := io.ReadAll(io.LimitReader(r.Body, 1)); len(first) > 0 {",
          "if first, _ := io.ReadAll(io.LimitReader(r.Body, 1)); len(first) > 0 && false { " + PLANT + "a body is let through.")],
        WEB,
        ["TestApprovalRefusesASubstitutedBody"],
        module="apps/common",
    ),
    Control(
        "approval does not compare the stored body with its recorded digest",
        ("U2.1", "UV.10", "UC-04"),
        [(CNF_SVC,
          "if subtle.ConstantTimeCompare([]byte(recorded), []byte(bodyDigest(row.Body))) != 1 {",
          "if subtle.ConstantTimeCompare([]byte(recorded), []byte(bodyDigest(row.Body))) == 2 { " + PLANT + "a changed body is replayed.")],
        "./service/",
        ["TestAStoredBodyThatChangedAfterCreationIsRefusedAtApproval"],
    ),
    Control(
        "approve skips the step-up password check",
        ("U2.1", "UV.11", "UC-04"),
        [(CNF_WEB,
          "if ref, refused := h.enforce.checkStepUp(r, stepUpAct{\n\t\tname: \"approving an agent's change\", plural: \"approvals\", onHost: \"retnd agent confirmations approve\",\n\t}); refused {",
          "if ref, refused := (gateRefusal{}), false; refused { " + PLANT + "no password is asked for.")],
        WEB,
        ["TestApprovalWithoutAStepUpIsRefused"],
        module="apps/common",
    ),
    Control(
        "the shut destructive gate is bypassed when a confirmation is replayed",
        ("U2.1",),
        [(CNF_GATE,
          "\tif !destructiveGatePassed(e.gate) {\n\t\tstate, reason := gateStatus(e.gate)\n\t\treturn newGateRefusal(http.StatusForbidden, \"DESTRUCTIVE_OPERATIONS_DISABLED\"",
          "\t_, replayed := agentReplayFrom(r.Context()) " + PLANT + "a replay skips the gate.\n\tif !destructiveGatePassed(e.gate) && !replayed {\n\t\tstate, reason := gateStatus(e.gate)\n\t\treturn newGateRefusal(http.StatusForbidden, \"DESTRUCTIVE_OPERATIONS_DISABLED\"")],
        WEB,
        ["TestRunCycleApprovedMeetsTheShutGateAsAPersonWould"],
        module="apps/common",
    ),
    Control(
        "a bearer is let through to the approve and deny routes",
        ("U2.1", "UC-04"),
        [(CNF_DENIED,
          "\t{\"POST\", \"/agent/confirmations/*\"},\n",
          "\t// " + PLANT + "the approve and deny routes are off the deny list.\n")],
        WEB,
        ["TestATokenCannotApproveItsOwnAsk"],
        module="apps/common",
    ),
    Control(
        "the proposer accepts a tool name on a route that is not the tool's",
        ("U2.1b",),
        [(CNF_WEB,
          "\tscope, ok := p.resolver.ResolveAgentTool(tool, r.Method, rel, act.Action)\n\tif !ok {",
          "\tscope, ok := p.resolver.ResolveAgentTool(tool, r.Method, rel, act.Action)\n\tif !ok && tool == \"\" { " + PLANT + "any tool name stands for any route.")],
        WEB,
        ["TestAToolHeaderNamingAnotherRouteCreatesNothing"],
        module="apps/common",
    ),
    Control(
        "a confirmation refusal is given a different exit code by the CLI",
        ("U2.1b",),
        [(CNF_SETUP,
          "\tcase apicontract.ErrorCodeInsecureTransportAckRequired:\n\t\treturn exitInsecureTransport, fmt.Sprintf(",
          "\tcase apicontract.ErrorCodeAgentConfirmationDecided: " + PLANT + "exit code 3.\n\t\treturn 3, \"\"\n\tcase apicontract.ErrorCodeInsecureTransportAckRequired:\n\t\treturn exitInsecureTransport, fmt.Sprintf(")],
        "./cmd/retnd/",
        ["TestAnExecutionsExitCodeIsTheOneEngineRefusalExitGives"],
    ),
    Control(
        "the service gives a confirmation refusal a different exit code from the CLI",
        ("U2.1b",),
        [(CNF_SVC,
          "\tcase status >= 400:\n\t\treturn 1\n",
          "\tcase code == string(apicontract.ErrorCodeAgentConfirmationNotFound):\n\t\treturn 3 " + PLANT + "not the CLI's exit.\n\tcase status >= 400:\n\t\treturn 1\n")],
        "./cmd/retnd/",
        ["TestAnExecutionsExitCodeIsTheOneEngineRefusalExitGives"],
    ),
    Control(
        "restart marks no approval without a replay result as interrupted",
        ("U2.1b",),
        [(CNF_SVC,
          "\tif len(rows) == 0 {\n\t\treturn 0, nil\n\t}\n\tnames, err := b.agentTokenNames(ctx)\n\tif err != nil {\n\t\treturn 0, err\n\t}\n\tfor _, row := range rows {\n\t\terr := b.journal.InsertAgentAudit(",
          "\tif len(rows) == 0 || true { " + PLANT + "nothing is marked.\n\t\treturn 0, nil\n\t}\n\tnames, err := b.agentTokenNames(ctx)\n\tif err != nil {\n\t\treturn 0, err\n\t}\n\tfor _, row := range rows {\n\t\terr := b.journal.InsertAgentAudit(")],
        "./service/",
        ["TestAnApprovalThatNeverFinishedIsMarkedInterruptedAtStartAndNeverRerun"],
    ),
    Control(
        "restart writes no replay row, so the same approval is marked again each start",
        ("U2.1b",),
        [(CNF_SVC,
          "Tool: row.Tool, Transport: auditTransportReplay,\n\t\t\tArguments: agentInterruptedMessage",
          "Tool: row.Tool, Transport: auditTransportConfirmation, " + PLANT + "the row is not a replay row.\n\t\t\tArguments: agentInterruptedMessage")],
        "./service/",
        ["TestAnApprovalThatNeverFinishedIsMarkedInterruptedAtStartAndNeverRerun"],
    ),
    Control(
        "a sixth pending confirmation is let in for one token",
        ("U2.1", "UV.12", "UC-04"),
        [(CNF_STATE, "\tif open >= maxPending {", "\tif open >= maxPending && false { " + PLANT + "no cap.")],
        "./internal/state/",
        ["TestTheSixthPendingConfirmationIsRefused"],
    ),
    Control(
        "an approval is accepted after the confirmation's time has passed",
        ("UV.12",),
        [(CNF_SVC, "\tif !at.Before(row.ExpiresAt) {", "\tif !at.Before(row.ExpiresAt) && false { " + PLANT + "an expired approval stands.")],
        "./service/",
        ["TestAConfirmationThatExpiresDuringTheApprovalIsNotApproved"],
    ),
    Control(
        "Proposes accepting any catalogue tool name on any route",
        ("U2.1b",),
        [("core/agenttools/tools.go",
          "if !ok || r.Scope == ScopeRead || r.Method != method || !matches(r.Route, p) || (r.Action != \"\" && r.Action != action) {",
          "if !ok || r.Scope == ScopeRead { " + PLANT.strip())],
        "./agenttools/",
        ["TestProposesAnswersOnlyForTheToolsOwnRoute"],
    ),
]
