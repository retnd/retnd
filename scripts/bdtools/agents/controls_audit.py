# The EPIC U rows about what an agent call leaves behind and what must never
# reach an agent: the audit trail and the feed (U1.7, UV.14, UV.15, UC-06), the
# exfiltration corpus and the projection (U1.8, UV.16, UC-05) and the injection
# corpus (UV.17, UC-02) (#1348, #1352). Exec'd by selftest.py with Control and
# PLANT defined.

AUDIT = "core/service/agentaudit.go"
PROJECT = "core/agenttools/project.go"
SERVER = "core/agentmcp/server.go"
TOOLS = "core/agenttools/tools.go"
CANARY = "core/tests/encryptedretention/canary_test.go"

SVC = "./service/"
RET = "./tests/encryptedretention/"
INJ = "./tests/agentinjection/"
EXF = "./tests/agentexfiltration/"

RECORD_TAIL = (
    "\treturn b.audit.enqueueNamed(ctx, agentAuditItem{row: state.AgentAudit{\n"
    "\t\tAt: now(), TokenID: c.TokenID, Tool: c.Tool, Transport: c.Transport,\n"
)
FEED_GATE = '\tif c.Mutating || c.Refused || c.ConfirmationState != "" {\n\t\tb.emitAgentAction(ctx, c)\n\t}\n'

CONTROLS = [
    # ---------------------------------------------------------- U1.7, UV.14
    Control(
        "RecordAgentCall returns before storing a read",
        ("U1.7", "UV.14"),
        [(AUDIT, FEED_GATE + RECORD_TAIL,
          FEED_GATE + "\tif !c.Mutating && !c.Refused && c.ConfirmationState == \"\" {\n\t\treturn nil " + PLANT.strip() + "\n\t}\n" + RECORD_TAIL)],
        SVC,
        ["TestAReadIsAnAuditRow", "TestEachCallIsExactlyOneRow", "TestAMutationIsOnTheFeedWithAResultAndAReadIsNot"],
    ),
    Control(
        "RecordAgentCall stores a second row for the same call",
        ("U1.7", "UV.14"),
        [(AUDIT, FEED_GATE + RECORD_TAIL,
          FEED_GATE + "\t_ = b.audit.enqueueNamed(ctx, agentAuditItem{row: state.AgentAudit{At: now(), TokenID: c.TokenID, Tool: c.Tool, Transport: c.Transport, Status: c.Status}}) " + PLANT.strip() + "\n" + RECORD_TAIL)],
        SVC,
        ["TestAReadIsAnAuditRow", "TestEachCallIsExactlyOneRow"],
    ),
    # ----------------------------------------------------------- U1.7, UV.15
    Control(
        "an agent_action line written with no Result on it",
        ("U1.7", "UV.15"),
        [(AUDIT, "\tb.logger.Completed(ctx, result, obs.EventAgentAction, message, attrs...)\n",
          "\tb.logger.Event(ctx, obs.LevelInfo, obs.EventAgentAction, message, attrs...) " + PLANT.strip() + "\n\t_ = result\n")],
        SVC,
        ["TestAMutationIsOnTheFeedWithAResultAndAReadIsNot", "TestARefusalBeforeTheHandlerIsStillAudited"],
    ),
    Control(
        "a served read put on the feed as agent_action",
        ("U1.7", "UV.15", "UC-06"),
        [(AUDIT, FEED_GATE,
          "\tif true { " + PLANT.strip() + "\n\t\tb.emitAgentAction(ctx, c)\n\t}\n")],
        SVC,
        ["TestAMutationIsOnTheFeedWithAResultAndAReadIsNot"],
    ),
    Control(
        "a refusal before the handler kept off the feed",
        ("U1.7", "UV.15"),
        [("core/service/agenttokens.go", "\tif emit {\n\t\tb.emitAgentAction(ctx, AgentCall{\n",
          "\tif false && emit { " + PLANT.strip() + "\n\t\tb.emitAgentAction(ctx, AgentCall{\n")],
        SVC,
        ["TestARefusalBeforeTheHandlerIsStillAudited"],
    ),
    Control(
        "an agent_action line with no set on its strip",
        ("U1.7",),
        [(AUDIT, "\tif c.BackupSetID != \"\" && b.configuresBackupSetID(c.BackupSetID) {\n",
          "\tif false && c.BackupSetID != \"\" && b.configuresBackupSetID(c.BackupSetID) { " + PLANT.strip() + "\n")],
        SVC,
        ["TestAnAgentActionAboutASetLandsOnThatSetsStrip"],
    ),
    Control(
        "the audit page never hands back a cursor",
        ("U1.7",),
        [(AUDIT, "\tif len(rows) == q.Limit {\n",
          "\tif false && len(rows) == q.Limit { " + PLANT.strip() + "\n")],
        SVC,
        ["TestThePageFiltersByTokenAndTimeAndWalksBackByCursor"],
    ),
    Control(
        "rows past the retention left in the audit table",
        ("U1.7",),
        [(AUDIT, "\t\t\treturn w.journal.PruneAgentAuditByAge(c, cutoff, agentAuditPruneBatch)\n",
          "\t\t\t_ = cutoff " + PLANT.strip() + "\n\t\t\treturn 0, nil\n")],
        SVC,
        ["TestRowsPastTheRetentionArePruned"],
    ),
    Control(
        "garbage-token refusals from one address pruned by age only",
        ("U1.7",),
        [(AUDIT, "\t\t\treturn w.journal.PruneAgentAuditOverCount(c, keep, agentAuditPruneBatch)\n",
          "\t\t\t_ = keep " + PLANT.strip() + "\n\t\t\treturn 0, nil\n")],
        SVC,
        ["TestTheTableIsBoundedByRowsAsWellAsByAge"],
    ),
    Control(
        "every refusal that names no token let onto the feed",
        ("U1.7", "UC-06"),
        [(AUDIT, "\tif !f.last.IsZero() && t.Sub(f.last) < agentRefusalFeedEvery {\n",
          "\tif false && !f.last.IsZero() && t.Sub(f.last) < agentRefusalFeedEvery { " + PLANT.strip() + "\n")],
        SVC,
        ["TestRefusalsThatNameNoTokenCannotFillTheFeed"],
    ),
    # --------------------------------------------- U1.8, UV.16, UC-05 (leaks)
    Control(
        "the audit writer stores a tool call's arguments unprojected",
        ("U1.8", "UV.16", "UC-05"),
        [(AUDIT, "\tprojected := agenttools.Project(decoded)\n",
          "\tprojected := decoded " + PLANT.strip() + "\n\t_ = agenttools.Project\n")],
        RET,
        ["TestAPlantedToolArgumentNeverReachesTheAuditRow_1348"],
    ),
    Control(
        "the projection keeps a denied field in a key-list result",
        ("U1.8", "UV.16", "UC-05"),
        [(PROJECT, '\t\t\tif deniedField(k) || snake(k) == "ssh_key_id" {\n',
          '\t\t\tif snake(k) == "ssh_key_id" { ' + PLANT.strip() + "\n")],
        RET,
        ["TestTheProjectionLeavesNoDeniedFieldOrShape"],
    ),
    Control(
        "the projection leaves a canary recipient in a free-text string",
        ("U1.8", "UV.16", "UC-05"),
        [(PROJECT, "\t\tfor _, shape := range CanaryShapes {\n\t\t\tif shape.MatchString(x) {\n",
          "\t\tfor _, shape := range CanaryShapes {\n\t\t\tif false && shape.MatchString(x) { " + PLANT.strip() + "\n")],
        RET,
        ["TestTheProjectionLeavesNoDeniedFieldOrShape"],
    ),
    Control(
        "the projection stops at the first list so a canary three levels down survives",
        ("U1.8", "UV.16", "UC-05"),
        [(PROJECT, "\t\tfor i, val := range x {\n\t\t\tout[i] = Project(val)\n",
          "\t\tfor i, val := range x {\n\t\t\tout[i] = val " + PLANT.strip() + "\n")],
        RET,
        ["TestTheProjectionLeavesNoDeniedFieldOrShape"],
    ),
    Control(
        "a canary recipient planted in a key-list result over stdio",
        ("U1.8", "UV.16", "UC-05"),
        [(SERVER, "\t\tdecoded = agenttools.Project(decoded)\n",
          "\t\t_ = agenttools.Project " + PLANT.strip() + "\n")],
        EXF,
        ["TestNoKeyMaterialOnAnyAgentSurface_1352", "TestNoKeyMaterialOnAnyAgentSurface_1352_AllAtOnce"],
        timeout="25m",
    ),
    Control(
        "a canary row's _WouldCatchOne control removed",
        ("U1.8",),
        [(CANARY, "func TestAPlantingTurnsExactlyItsOwnRowRed_996_WouldCatchOne(",
          "func TestAPlantingTurnsExactlyItsOwnRowRed_996_Removed(")],
        RET,
        ["TestEveryCanaryRowHasADetectorAndItsControlsExist_1348"],
    ),
    Control(
        "a tool registered over an encryption key export route",
        ("UC-05",),
        [(TOOLS, "var toolRows = []Row{\n",
          "var toolRows = []Row{\n\t{Tool: \"key_export\", Method: \"GET\", Route: \"/encryption/keys/{id}/export\", Scope: ScopeRead, Description: \"Export a key.\", Untrusted: []string{}}, " + PLANT.strip() + "\n")],
        "./agenttools/",
        ["TestNoToolExistsOverADeniedRoute"],
    ),
    # ----------------------------------------------------- UV.17, UC-02 (text)
    Control(
        "a tool result's untrusted fields left in data instead of the untrusted object",
        ("UV.17", "UC-02"),
        [(SERVER, "\t\t\tdata, untrusted := splitUntrusted(decoded, t.Untrusted)\n",
          "\t\t\tdata, untrusted := splitUntrusted(decoded, nil) " + PLANT.strip() + "\n")],
        INJ,
        ["TestAnUntrustedStringArrivesOnlyAsData", "TestAnUntrustedStringArrivesOnlyAsData_AllOriginsAtOnce"],
        timeout="25m",
    ),
    Control(
        "a file name put into the tool result's message text",
        ("UV.17", "UC-02"),
        [(SERVER, "res := Result{OK: true, Code: CodeOK, Message: staticOKMessage, CorrelationID: correlated,",
          "res := Result{OK: true, Code: CodeOK, Message: staticOKMessage + fmt.Sprint(untrusted), CorrelationID: correlated,")],
        INJ,
        ["TestAnUntrustedStringArrivesOnlyAsData", "TestAnUntrustedStringArrivesOnlyAsData_AllOriginsAtOnce"],
        timeout="25m",
    ),
]
