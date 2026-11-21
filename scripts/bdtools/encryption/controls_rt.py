# The retnd-tests half of EPIC T's T2.4 and the in-repo checker controls T2.4
# names (#1331), plus EPIC N's in-repo Suite C control (#1565). exec'd by
# selftest.py with Control and PLANT already defined.
#
# T2.4's Suite A controls run scripts/bdtools/encryption/retnd_tests_case.sh,
# which builds retnd from the planted copy and runs one case of the
# retnd-tests checkout at the pinned sha against it. They need RETND_TESTS_DIR
# (that checkout) and RETND_COMMIT in the environment.
# scripts/bdtools/encryption/lanes.txt pins them to the retnd-tests lane, so a
# host without that checkout skips them into the gate's skip ledger (#1538).
# No gate shard hands them one yet (#1548). EPIC N's control is plain go test
# and needs no lane: N's suites are proved in this repository, because
# retnd-tests cannot make an encrypted retained copy (its suites/cli/
# COVERAGE.md, Known gaps).

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    # Only for the type checker: at run time selftest.py execs this file
    # with both names already in scope.
    from bdtools.encryption.selftest import PLANT, Control

NEGATIVES_TEST = "core/tests/encryptionsuites/negatives_test.go"
SUITES_TEST = "core/tests/encryptionsuites/suites_test.go"
CONFIG_RCE_RT = "core/internal/config/retainedcopyencryption.go"
KEYSTORE_RT = "core/internal/keycustody/store.go"
ENCRYPTION_CLI_RT = "core/cmd/retnd/encryption.go"
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
        "RT the policy line always reports age-pq/v1 as the suite in force, under Suite A",
        ("T2.4",),
        [("core/cmd/retnd/encryption.go", "fmt.Printf(\"  suite: %s (configured: %s)\\n\", p.Suite, p.SuiteConfigured)",
          "fmt.Printf(\"  suite: %s (configured: %s)\\n\", \"age-pq/v1\", p.SuiteConfigured) " + PLANT.strip())],
        "",
        cmd=["bash", CASE, "a", "encryption/a-key-created-under-age-v1-stays-age-v1"],
        needles=["--- FAIL: TestCLIContract/encryption/a-key-created-under-age-v1-stays-age-v1"],
        timeout="30m",
    ),
    # EPIC N's Suite C exit-code clause: the engine route (the API) and the
    # direct route have to exit alike. The plant leaves the answer and the
    # configuration identical and changes only the engine route's exit code on
    # a success, so nothing but the exit-code comparison can catch it.
    Control(
        "RT the engine route exits 1 where the direct route succeeds, under Suite C",
        ("N2.5",),
        [(ENCRYPTION_CLI_RT, "\t\tgot, err := route.ResetEncryptionPolicy(ctx)\n\t\tif err != nil {\n\t\t\treturn fail(err)\n\t\t}\n\t\treturn encryptionAnswer(*asJSON, got, func() { printEncryptionPolicy(got) })",
          "\t\tgot, err := route.ResetEncryptionPolicy(ctx)\n\t\tif err != nil {\n\t\t\treturn fail(err)\n\t\t}\n"
          "\t\tcode := encryptionAnswer(*asJSON, got, func() { printEncryptionPolicy(got) })\n"
          "\t\tif _, viaEngine := route.(*engineRoute); viaEngine { " + PLANT.strip() + "\n\t\t\treturn exitFailure\n\t\t}\n\t\treturn code")],
        "./cmd/retnd/",
        ["TestEveryEncryptionWriteLeavesTheSameConfigurationOnBothRoutes_996"],
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
