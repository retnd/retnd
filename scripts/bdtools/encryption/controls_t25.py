# The EPIC T rows whose proof is the closing check itself: T2.5 (#1376). Exec'd
# by selftest.py with Control and PLANT defined.
#
# T2.5 says the epic closes only when no row is BLOCKED, the canonical
# deployment is byte-identical and the epic added only its own migrations. The
# guard is scripts/release/check-epic-t-closed.sh with its checker, so the
# control is the planted-violation shape one level up: break the guard, and the
# test that drives it has to go red naming the violation it exists to refuse.

CLOSED_SH = "scripts/release/check-epic-t-closed.sh"
GATE_TEST = "core/tests/encryptiongate/matrix_test.go"
SITE_REF = "docs/site/reference.html"


def closure_selftest() -> list[str]:
    return ["bash", "scripts/tests/epic-t-closure.test.sh"]


CONTROLS = [
    Control(
        "CLOSE the closing check stopped refusing a BLOCKED row",
        ("T2.5",),
        [(GATE_TEST,
          '				out = append(out, x.id+" is BLOCKED, and this epic does not close with a blocked row")',
          '				_ = x // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): a BLOCKED row is no longer refused while closing.')],
        "./tests/encryptiongate/",
        ["TestTheCheckerReadsEpicTRowsAndRefusesABlockedOneWhileClosing"],
    ),
    Control(
        "CLOSE the canonical deployment dropped container/compose.yaml from the guarded paths",
        ("T2.5",),
        [(CLOSED_SH, "paths=(\n  container/compose.yaml\n",
          "paths=(\n  # PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): compose.yaml is not guarded.\n")],
        "",
        cmd=closure_selftest(),
        needles=["FAIL the canonical deployment: an edit to container/compose.yaml is refused"],
    ),
    Control(
        "CLOSE a third migration let through by the closing check",
        ("T2.5",),
        [(CLOSED_SH, '&& [ -z "$unexpected" ] && [ -z "$touched" ]; then',
          '&& true && [ -z "$touched" ]; then # PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): extra migrations are not counted.')],
        "",
        cmd=closure_selftest(),
        needles=["FAIL migrations: a third new migration is refused, and named"],
    ),
    Control(
        "CLOSE reencrypt dropped from the site reference's encryption row",
        ("T2.5",),
        [(SITE_REF, "<code>reencrypt --suite ID | --key ID",
          "<code>re-encrypt --suite ID | --key ID")],
        "./packaging/",
        ["TestTheSiteReferenceEncryptionRowNamesEveryEncryptionVerb"],
        module="distribution",
    ),
]
