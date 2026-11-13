# The run-proposal revision guard: the UV.22 ledger line (#1387, #1380).
# Exec'd by selftest.py with Control and PLANT defined.

REV_CONFIRMATIONS = "apps/common/webhost/agentconfirmations.go"
REV_ERRORS = "apps/common/webhost/errors.go"
REV_SERVICE = "core/service/agentconfirmations.go"

CONTROLS = [
    Control(
        "the engine's revision no longer stamped into a run proposal",
        ("UV.22",),
        [(REV_CONFIRMATIONS,
          "		body = stampRevision(r.Method, rel, act.Action, body, p.revision())\n",
          "		_ = stampRevision " + PLANT + "the body is recorded as sent.\n")],
        "./webhost/",
        ["TestAnApprovedRunWithNoRevisionSuppliedIsQueued"],
        module="apps/common",
    ),
    Control(
        "a stale revision answered 400 instead of 409",
        ("UV.22",),
        [(REV_ERRORS,
          "	writeJSON(w, http.StatusConflict, resp)\n}\n\n// responseCorrelationID",
          "	writeJSON(w, http.StatusBadRequest, resp) " + PLANT + "stale is not a conflict.\n}\n\n// responseCorrelationID")],
        "./webhost/",
        ["TestAConfigChangeBetweenProposeAndApproveIsRefusedAsStale"],
        module="apps/common",
    ),
    Control(
        "a revision planted in a change that is not a run shown as the recorded one",
        ("UV.22",),
        [(REV_SERVICE,
          "route == \"/operations\" && json.Unmarshal(c.Body, &recorded) == nil &&\n\t\t(recorded.Action == ActionRunCycle || recorded.Action == ActionRunBackupSet) {",
          "route != \"\" && json.Unmarshal(c.Body, &recorded) == nil { " + PLANT + "any body is read.")],
        "./cmd/retnd/",
        ["TestShowNamesTheRevisionARunWasRecordedAgainstAndNoOther"],
    ),
]
