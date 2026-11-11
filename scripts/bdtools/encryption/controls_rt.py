# The retnd-tests half of EPIC T's T2.4 and EPIC N's N2.5 (#1331), and the
# in-repo checker controls T2.4 names. exec'd by selftest.py with Control and
# PLANT already defined.
#
# The Suite A and Suite C controls run scripts/bdtools/encryption/
# retnd_tests_case.sh, which builds retnd and retnd-web from the planted copy
# and runs one case of the retnd-tests checkout at the pinned sha against
# them. They need RETND_TESTS_DIR (that checkout, with Suite C's npm
# dependencies and Playwright browser installed) and RETND_COMMIT in the
# environment, and Suite C needs ui/shared's node_modules in the real tree.

NEGATIVES_TEST = "core/tests/encryptionsuites/negatives_test.go"
SUITES_TEST = "core/tests/encryptionsuites/suites_test.go"
CONFIG_RCE_RT = "core/internal/config/retainedcopyencryption.go"
KEYSTORE_RT = "core/internal/keycustody/store.go"
SCRUB_CLI = "core/cmd/retnd/scrub.go"
CASE = "scripts/bdtools/encryption/retnd_tests_case.sh"
# The checker's loop over the negative cases; a plant here makes it skip a kind.
LOOP = "\tfor i, c := range cases {\n\t\tdir := filepath.Join(base.root, fmt.Sprintf(\"recovered-%d\", i))"

CONTROLS = [
    Control(
        "RT the negatives checker skips every truncation case",
        ("T2.4",),
        [(NEGATIVES_TEST, LOOP, LOOP + "\n\t\tif bytes.HasPrefix([]byte(c.name), []byte(\"truncated\")) { " + PLANT.strip() + "\n\t\t\tcontinue\n\t\t}")],
        "./tests/encryptionsuites/",
        ["TestTheNegativesCatchAnAdapterThatAcceptsTruncation_1319"],
    ),
    Control(
        "RT the negatives checker skips every cross-suite case",
        ("T2.4",),
        [(NEGATIVES_TEST, LOOP, LOOP + "\n\t\tif bytes.Contains([]byte(c.name), []byte(\"cross-suite\")) { " + PLANT.strip() + "\n\t\t\tcontinue\n\t\t}")],
        "./tests/encryptionsuites/",
        ["TestTheNegativesCatchAKeyLookupThatAcceptsAnySuitesKey_1319"],
    ),
    Control(
        "RT any suite id passes Validate, under Suite A",
        ("T2.4",),
        [(CONFIG_RCE_RT, "\ts, ok := lookupSuite(declared)\n\tif !ok {\n\t\tvar known []string",
          "\ts, ok := lookupSuite(declared)\n\tif !ok {\n\t\treturn declared, false " + PLANT.strip() + "\n\t}\n\tif !ok {\n\t\tvar known []string")],
        "",
        cmd=["bash", CASE, "a", "encryption/an-unknown-suite-fails-load"],
        needles=["--- FAIL: TestCLIContract/encryption/an-unknown-suite-fails-load"],
        timeout="30m",
    ),
    Control(
        "RT the policy line reports the build default and not the suite in force, under Suite A",
        ("T2.4",),
        [("core/cmd/retnd/encryption.go", "fmt.Printf(\"  suite: %s (configured: %s)\\n\", p.Suite, p.SuiteConfigured)",
          "fmt.Printf(\"  suite: %s (configured: %s)\\n\", copycrypt.Default().ID(), p.SuiteConfigured) " + PLANT.strip())],
        "",
        cmd=["bash", CASE, "a", "encryption/a-key-created-under-age-v1-stays-age-v1"],
        needles=["--- FAIL: TestCLIContract/encryption/a-key-created-under-age-v1-stays-age-v1"],
        timeout="30m",
    ),
    Control(
        "RT the CLI exits 0 on a failing scrub, under Suite C",
        ("N2.5",),
        [(SCRUB_CLI, "\tif sum.Failed+sum.CouldNotComplete+sum.SkippedHeld+sum.SkippedLeased+sum.Refused > 0 {\n\t\treturn 1\n\t}",
          "\tif sum.Failed+sum.CouldNotComplete+sum.SkippedHeld+sum.SkippedLeased+sum.Refused > 0 {\n\t\treturn 0 " + PLANT.strip() + "\n\t}")],
        "",
        cmd=["bash", CASE, "c", "specs/exit-codes.986.spec.ts", "a corrupted copy fails its scrub"],
        needles=["1 failed", "a failed scrub must not exit 0"],
        links=["ui/shared/node_modules"],
        timeout="30m",
    ),
]

CONTROLS.append(
    Control(
        "RT age-pq/v1 dropped from the suites the T2.4 tests walk",
        ("T2.4",),
        [(SUITES_TEST, "var shipped = []string{ageV1, agePQV1}", "var shipped = []string{ageV1} " + PLANT.strip())],
        "./tests/encryptionsuites/",
        ["TestAFeatureOffDeploymentIsUntouchedByEverySuiteRead_1319", "TestACleanMachineRecoversEachSuitesCopyWithThePinnedAgeAndTheBundle_1319/age/v1"],
    )
)
