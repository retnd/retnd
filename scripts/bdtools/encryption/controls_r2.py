# EPIC N rows proven in round 2 of the 0.8.0 release PR (#1331): the crash
# cell for an unresolvable key, the two source guards, and the gate and brand
# guards. Exec'd by selftest.py with Control and PLANT defined.

PLACE_ENC_R2 = "core/internal/placement/encryption.go"
AGEX_R2 = "core/internal/copycrypt/agex25519.go"
KC_AGE_R2 = "core/internal/keycustody/age.go"
GATE_TEST_R2 = "scripts/tests/ci-local-gate.test.sh"

# The copy has no .git (the selftest copies what git lists), and both shell
# guards ask git what is tracked, so the command makes the copy a repository
# first. Nothing is pushed or fetched; it lives and dies with the copy.
# Built from fragments so this file is not itself an old-brand occurrence.
OLD_BRAND_R2 = "R" + "M_KEY_DIR"
AS_REPO_R2 = "git init -q . && git add -A >/dev/null && git -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m copy && "

CONTROLS = [
    Control(
        "N1.3 an unreadable key let through to the encrypting pass",
        ("N1.3", "NC-14", "NV.13"),
        [(PLACE_ENC_R2, "keySuite(rec), want)\n\t}\n\tif err := e.Keys.WithKey(ctx, rec.ID, func([]byte) error { return nil }); err != nil {",
          "keySuite(rec), want)\n\t}\n\tif err := e.Keys.WithKey(ctx, rec.ID, func([]byte) error { return nil }); false && err != nil { " + PLANT.strip())],
        "./tests/movecrash/",
        ["TestAnUnresolvableKeyRefusesTheCopyBeforeAnythingIsWritten_1331"],
        timeout="20m",
    ),
    Control(
        "NC-01 the transform takes the key-file envelope from the rclone transport",
        ("NC-01",),
        [(AGEX_R2, "import (\n\t\"errors\"\n\t\"fmt\"\n\t\"io\"\n\t\"strings\"\n\n\t\"filippo.io/age\"\n)",
          "import (\n\t\"errors\"\n\t\"fmt\"\n\t\"io\"\n\t\"strings\"\n\n\t\"filippo.io/age\"\n\n\t" + PLANT + "the key-file envelope.\n\t_ \"github.com/retnd/retnd/core/internal/transport/rclone\"\n)")],
        "./tests/encryptiongate/",
        ["TestTheTransformImportsAgeAndNothingOfTheKeyFileEnvelope_1331"],
    ),
    Control(
        "NC-16 a key-custody type that wraps the material in an obs.Secret",
        ("NC-16",),
        [(KC_AGE_R2, "import \"github.com/retnd/retnd/core/internal/copycrypt\"\n",
          "import (\n\t\"github.com/retnd/retnd/core/internal/copycrypt\"\n\t\"github.com/retnd/retnd/core/internal/obs\"\n)\n\n" + PLANT + "material held in a renderable wrapper.\ntype plantedHeldKey struct{ material obs.Secret }\n\nvar _ = plantedHeldKey{}\n")],
        "./tests/encryptiongate/",
        ["TestNoKeyCustodyTypeHoldsAnObsSecret_1331"],
    ),
    Control(
        "N2 the epic closure stubs dropped from the gate fixture",
        ("N2.12",),
        [(GATE_TEST_R2, "publish-image-guards published-provenance-guards epic-n-closure epic-t-closure epic-u-closure; do",
          "publish-image-guards published-provenance-guards; do  # " + PLANT.strip())],
        "",
        cmd=["sh", "-c", AS_REPO_R2 + "GATE_LOCK_TIMEOUT=60 bash scripts/tests/ci-local-gate.test.sh"],
        needles=["epic-n-closure.test.sh"],
        timeout="60m",
    ),
    Control(
        "N2 a first-brand environment variable added to key custody",
        ("N2.12",),
        [(KC_AGE_R2, "const AgeFormatID = copycrypt.AgeX25519ID\n",
          "const AgeFormatID = copycrypt.AgeX25519ID\n\n" + PLANT + "an old-brand name.\nconst plantedKeyDirEnv = \"" + OLD_BRAND_R2 + "\"\n")],
        "",
        cmd=["sh", "-c", AS_REPO_R2 + "bash scripts/rename/check-brand-drift.sh"],
        needles=[OLD_BRAND_R2],
    ),
    Control(
        "N2 the Synology store screenshot row recorded as done",
        ("N2.8",),
        [("docs/conformance/submission-preflight.md",
          "| Store screenshots | OPERATOR | docs/acceptance/store-submission-preflight.md's Synology DSM section",
          "| Store screenshots | PASS | docs/acceptance/store-submission-preflight.md's Synology DSM section")],
        "./tests/encryptiongate/",
        ["TestTheSubmissionAndCompliancePagesSayWhatEncryptionChanged_1331"],
    ),
]
