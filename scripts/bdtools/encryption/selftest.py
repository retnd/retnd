#!/usr/bin/env python3
"""Positive controls for EPIC N and EPIC T's encryption guards (#998, #1320).

docs/conformance/epic-n-matrix.md and docs/conformance/epic-t-matrix.md tie
every claim the two epics make to a test, and a row only says `PASS` once
the mutation it names has been run and watched to fail. This file is where
those mutations live, so "run and watched to fail" is a command anybody can
repeat rather than a sentence in a table.

Each control plants one violation in a real product file, runs the narrowed
test the matrix row cites, and must see it fail AND name that test: a mutant
that broke the build for an unrelated reason does not count as a catch. The
file is then put back and the same narrowed test has to pass, so a test that
was red before anything was planted cannot read as a catch either. The
controls whose claim IS a build failure (a compile-time assertion) say so,
and must fail to build naming the file that holds the assertion.

core/tests/encryptiongate/matrix_test.go reads this file: a `PASS` row cites
a control here by its label, verbatim, in double quotes, beside the date it
was run, and a label that is not in this file is refused.

Every mutation is anchored to a verbatim copy of product source, tabs and
all. An anchor that no longer matches is STALE ANCHOR: the control is
skipped rather than run against a tree with nothing planted in it, and the
run carries on so one run names every stale control (#458).
`--check-anchors` checks every anchor against the real tree, building
nothing.

Unlike the other ported selftests, every control runs in ONE copy of the
tree, planting before its run and restoring after it. The Go build cache is
keyed by directory, so a fresh copy per control would rebuild core's test
binaries from cold well over a hundred times. The restore is unconditional
(a `finally`), and the copy is never the real tree.

  --check-anchors   check every anchor, build nothing
  --only TEXT       run only controls whose label or row id contains TEXT
                    (repeatable)
  --race            run each narrowed test with -race
  --record FILE     append one dated line per control to FILE: the label,
                    its rows, the verdict, the red excerpt, the green
                    verdict, and the commit

Docker-backed controls (the movecrash cells) need the daemon the cells
already need; everything else is plain `go test`.
"""

from __future__ import annotations

import datetime
import os
import re
import shutil
import sys
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools import harness, selftest_swap

PROGRAM = "encryption selftest"

PLANT = "// PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): "


@dataclass
class Control:
    """One planted violation. `label` is what a matrix row quotes; `rows`
    are the row ids it proves; `plants` are (file, old, new) swaps; `pkg` is
    the Go package under core/; `names` are the tests that must fail (a
    `Parent/sub` name matches that subtest). `build` is a needle instead:
    the plant must stop the package building, naming it."""

    label: str
    rows: tuple[str, ...]
    plants: list[tuple[str, str, str]]
    pkg: str
    names: list[str] = field(default_factory=list)
    build: str = ""
    timeout: str = "15m"
    module: str = "core"


# ---------------------------------------------------------------- EPIC T

AGEX = "core/internal/copycrypt/agex25519.go"
AGEPQ = "core/internal/copycrypt/agemlkem.go"
SUITE_GO = "core/internal/copycrypt/suite.go"
STATE_ENC = "core/internal/state/encryption.go"
MIG15 = "core/migrations/0015_retained_copy_encryption.sql"
PLACEMENT_ENC = "core/internal/placement/encryption.go"
CONFIG_RCE = "core/internal/config/retainedcopyencryption.go"
APP_DEFAULTSUITE = "core/internal/app/defaultsuite.go"
BUNDLE_GO = "core/internal/keycustody/bundle.go"
STORE_GO = "core/internal/keycustody/store.go"
SUITEHEALTH_GO = "core/internal/keycustody/suitehealth.go"
CLIECHO_ROUTES = "core/cliecho/routes.go"
DECRYPT_GO = "core/cmd/retnd/encryptiondecrypt.go"
SERVICE_ENC = "core/service/encryption.go"
METRICS_GO = "core/internal/metrics/metrics.go"

CONTROLS: list[Control] = [
    Control(
        "the age/v1 status changed and the golden list left as it was",
        ("T1.1", "TV.1"),
        [(AGEX, "func (AgeX25519) Status() Status { return StatusSupported }",
          "func (AgeX25519) Status() Status { return StatusDeprecated } " + PLANT.strip())],
        "./internal/copycrypt/",
        ["TestSuiteIDsAreStableAgainstTheGoldenList"],
    ),
    Control(
        "the build default moved to age/v1 and the golden list left as it was",
        ("T1.1", "TV.1"),
        [(SUITE_GO, "const defaultID = AgeMLKEM768X25519ID", "const defaultID = AgeX25519ID " + PLANT.strip())],
        "./internal/copycrypt/",
        ["TestSuiteIDsAreStableAgainstTheGoldenList", "TestTheDefaultIsAgePQ"],
    ),
    Control(
        "a second default row in the suite table",
        ("T1.1", "TV.2"),
        [(SUITE_GO, "var suites = []Suite{\n\tAgeX25519{},\n\tAgeMLKEM768X25519{},\n}",
          "var suites = []Suite{\n\tAgeX25519{},\n\tAgeMLKEM768X25519{},\n\tAgeMLKEM768X25519{}, " + PLANT.strip() + "\n}")],
        "./internal/copycrypt/",
        ["TestTheRegistryHasExactlyOneDefaultAndItIsSupported"],
    ),
    Control(
        "the default suite marked deprecated",
        ("TV.2",),
        [(AGEPQ, "func (AgeMLKEM768X25519) Status() Status { return StatusSupported }",
          "func (AgeMLKEM768X25519) Status() Status { return StatusDeprecated } " + PLANT.strip())],
        "./internal/copycrypt/",
        ["TestTheRegistryHasExactlyOneDefaultAndItIsSupported"],
    ),
    Control(
        "a marketing word in the age/v1 sentence",
        ("TC-01", "TV.9"),
        [(AGEX, '"128-bit security level. Not post-quantum. Opens with `age -d -i key.txt`."',
          '"128-bit security level, 256-bit military-grade. Not post-quantum. Opens with `age -d -i key.txt`."')],
        "./internal/copycrypt/",
        ["TestDescribeSentencesCarryNoMarketingWords"],
    ),
    Control(
        "the age/v1 adapter takes an age-pq/v1 key as its own",
        ("TV.7",),
        [(AGEX, "func (AgeX25519) Recipient(secret []byte) (string, error) {\n\tid, err := parseX25519Identity(secret)\n\tif err != nil {\n\t\treturn \"\", err\n\t}",
          "func (AgeX25519) Recipient(secret []byte) (string, error) {\n\tid, err := parseX25519Identity(secret)\n\tif err != nil {\n\t\t" + PLANT + "fall through to the other suite.\n\t\treturn AgeMLKEM768X25519{}.Recipient(secret)\n\t}")],
        "./internal/copycrypt/suitetest/",
        ["TestEveryRegisteredSuitePassesTheVectors/age/v1"],
    ),
    Control(
        "the age/v1 reader turns a cut tail into a clean end",
        ("T1.8", "TC-06", "TV.8"),
        [(AGEX, "\tr, err := age.Decrypt(ciphertext, id)\n\tif err != nil {\n\t\treturn nil, fmt.Errorf(\"keycustody: opening the age stream: %w\", err)\n\t}\n\treturn r, nil",
          "\tr, err := age.Decrypt(ciphertext, id)\n\tif err != nil {\n\t\treturn nil, fmt.Errorf(\"keycustody: opening the age stream: %w\", err)\n\t}\n\treturn plantedSwallow{r}, nil"),
         (AGEX, "var _ Suite = AgeX25519{}",
          "var _ Suite = AgeX25519{}\n\n" + PLANT + "a read error becomes io.EOF.\ntype plantedSwallow struct{ r io.Reader }\n\n"
          "func (s plantedSwallow) Read(p []byte) (int, error) {\n\tn, err := s.r.Read(p)\n\tif err != nil && !errors.Is(err, io.EOF) {\n\t\treturn n, io.EOF\n\t}\n\treturn n, err\n}")],
        "./internal/copycrypt/suitetest/",
        ["TestEveryRegisteredSuitePassesTheVectors/age/v1", "TestAnAdapterThatAcceptsTruncationIsCaught"],
    ),
    Control(
        "config suite text written into the copy record unchecked",
        ("T1.2", "TV.3"),
        [(STATE_ENC, "\tsuite, ok := copycrypt.Lookup(e.FormatID)\n\tif !ok {\n\t\treturn fmt.Errorf(\"%w: record placement encryption: format id %q\", ErrUnknownSuite, e.FormatID)\n\t}\n\tif e.FormatVersion != suite.Version() {",
          "\tsuite, ok := copycrypt.Lookup(e.FormatID)\n\tif !ok {\n\t\t" + PLANT + "any format id is recorded.\n\t\treturn nil\n\t}\n\tif e.FormatVersion != suite.Version() {")],
        "./internal/state/",
        ["TestRecordPlacementEncryptionRefusesAnUnregisteredSuite"],
    ),
    Control(
        "a key registered under a suite the registry does not have",
        ("T1.4", "TV.6"),
        [(STATE_ENC, "\tif _, ok := copycrypt.Lookup(suite); !ok {\n\t\treturn fmt.Errorf(\"%w: register encryption key %s: suite %q\", ErrUnknownSuite, k.ID, suite)\n\t}",
          "\t" + PLANT + "any suite id is registered.\n\t_ = copycrypt.Lookup")],
        "./internal/state/",
        ["TestAddEncryptionKeyRefusesAnUnregisteredSuite"],
    ),
    Control(
        "the one-active-managed-key index dropped",
        ("T1.4", "TC-03", "TV.5"),
        [(MIG15, "CREATE UNIQUE INDEX idx_encryption_keys_one_active_managed\n    ON encryption_keys (provider)\n    WHERE provider = 'managed' AND status = 'active';",
          "-- PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): the one-active index is gone.\nCREATE INDEX idx_encryption_keys_one_active_managed\n    ON encryption_keys (provider)\n    WHERE provider = 'managed' AND status = 'active';")],
        "./internal/state/",
        ["TestTwoActiveManagedKeysAreRefusedWhateverTheirSuites", "TestARotationAcrossSuitesIsOneWriteAndTwoActiveAreRefused"],
    ),
    Control(
        "an unknown format id read as age/v1",
        ("TV.4",),
        [(PLACEMENT_ENC, "\tsuite, ok := copycrypt.Lookup(id)\n\tif !ok {\n\t\treturn nil, fmt.Errorf(\"%w: %w: %q\", ErrClassUnavailable, copycrypt.ErrUnknownSuite, id)\n\t}",
          "\tsuite, ok := copycrypt.Lookup(id)\n\tif !ok {\n\t\t" + PLANT + "guess age/v1.\n\t\tsuite = copycrypt.AgeX25519{}\n\t}")],
        "./internal/placement/",
        ["TestAnUnknownSuiteOnARecordIsClassUnavailable"],
    ),
    Control(
        "an active key of another suite stands in for the pinned one",
        ("T1.6", "TV.11"),
        [(PLACEMENT_ENC, "\tif pinned && keySuite(rec) != want {\n\t\treturn keycustody.KeyRecord{}, fmt.Errorf(\"%w: %w: backup set %s: no active key for suite %s (the active key %s is %s)",
          "\tif false && pinned && keySuite(rec) != want { " + PLANT.strip() + "\n\t\treturn keycustody.KeyRecord{}, fmt.Errorf(\"%w: %w: backup set %s: no active key for suite %s (the active key %s is %s)")],
        "./internal/placement/",
        ["TestAnActiveKeyOfAnotherSuiteIsNotASubstitute"],
    ),
    Control(
        "any non-empty suite id accepted by Validate",
        ("T1.5", "TV.10"),
        [(CONFIG_RCE, "\ts, ok := lookupSuite(declared)\n\tif !ok {\n\t\tvar known []string",
          "\ts, ok := lookupSuite(declared)\n\tif !ok {\n\t\treturn declared, false " + PLANT.strip() + "\n\t}\n\tif !ok {\n\t\tvar known []string")],
        "./internal/config/",
        ["TestAnUnknownSuiteFailsLoad"],
    ),
    Control(
        "a retired policy suite loads",
        ("TV.10",),
        [(CONFIG_RCE, "\tswitch s.Status() {\n\tcase copycrypt.StatusRetired:",
          "\tswitch s.Status() {\n\tcase \"planted-never\": " + PLANT.strip())],
        "./internal/config/",
        ["TestARetiredSuiteFailsLoad"],
    ),
    Control(
        "the default-changed event emitted on every cycle",
        ("TV.12",),
        [(APP_DEFAULTSUITE, "\tif err == nil && changed {", "\tif err == nil && (changed || !changed) { " + PLANT.strip())],
        "./internal/app/",
        ["TestTheDefaultSuiteChangeEventFiresOnceAndOnlyOnAChange"],
    ),
    Control(
        "the pinned age reads an age-pq/v1 copy with an age/v1 bundle",
        ("T1.3",),
        [(BUNDLE_GO, "\tif err := s.WithKey(ctx, id, func(key []byte) error {\n\t\tb.Material = string(key)\n\t\treturn nil",
          "\tif err := s.WithKey(ctx, id, func(key []byte) error {\n\t\tb.Material = string(key)\n\t\tif b.FormatID == copycrypt.AgeMLKEM768X25519ID {\n\t\t\t" + PLANT + "the bundle carries an age/v1 identity.\n\t\t\tv1, _, _ := copycrypt.AgeX25519{}.Generate()\n\t\t\tb.Material = string(v1)\n\t\t}\n\t\treturn nil")],
        "./tests/encryptionsuites/",
        ["TestACleanMachineRecoversEachSuitesCopyWithThePinnedAgeAndTheBundle_1319/age-pq/v1"],
    ),
    Control(
        "Generate taken out of the Suite interface",
        ("TC-02",),
        [(SUITE_GO, "\t// Generate mints a fresh key from a CSPRNG: the secret a key file holds\n\t// and its public recipient.\n\tGenerate() (secret []byte, recipient string, err error)\n",
          "\t" + PLANT + "Generate is gone.\n")],
        "./internal/keycustody/",
        build="format.go",
    ),
    Control(
        "the suites route dropped from the CLI echo table",
        ("T1.7", "TV.13"),
        [(CLIECHO_ROUTES, "\tkey(\"GET\", \"/encryption/suites\"): {\n\t\tbuild:    func(a Action) *cmd { return newCmd(\"encryption\", \"suites\") },\n\t\texamples: []Action{{}},\n\t},\n",
          "\t" + PLANT + "the suites route has no entry.\n")],
        "./cliecho/",
        ["TestTheEncryptionRoutesNameTheirCommands_993/listing_the_suites_(#1315)", "TestEveryRouteOutcomeIsInTheGolden"],
    ),
    Control(
        "a bundle under an unregistered suite imported as age/v1",
        ("TV.14",),
        [(BUNDLE_GO, "\tf, err := s.formatOf(b.FormatID)\n\tif err != nil {\n\t\treturn KeyRecord{}, false, fmt.Errorf(\"keycustody: the bundle was exported under format %q, which this build cannot open: %w\", b.FormatID, err)",
          "\tf, err := s.formatOf(b.FormatID)\n\tif err != nil {\n\t\tf, err = AgeFormat{}, nil " + PLANT.strip() + "\n\t}\n\tif err != nil {\n\t\treturn KeyRecord{}, false, fmt.Errorf(\"keycustody: the bundle was exported under format %q, which this build cannot open: %w\", b.FormatID, err)")],
        "./internal/keycustody/",
        ["TestImportRefusesABundleUnderASuiteThisBuildDoesNotHave_1331"],
    ),
    Control(
        "decrypt reads every identity file as age/v1",
        ("TV.15",),
        [(DECRYPT_GO, "\tfor _, candidate := range copycrypt.Suites() {\n\t\tif _, err := candidate.Recipient(identity); err == nil {",
          "\treturn identity, \"\", copycrypt.AgeX25519{}, nil " + PLANT.strip() + "\n\tfor _, candidate := range copycrypt.Suites() {\n\t\tif _, err := candidate.Recipient(identity); err == nil {")],
        "./cmd/retnd/",
        ["TestDecryptRoundTripsThroughAnIdentityFileAndExplicitDigests_1289"],
    ),
    Control(
        "decrypt takes --suite as a choice and not a confirmation",
        ("TV.15",),
        [(DECRYPT_GO, "\t\tif *suiteFlag != suite.ID() {", "\t\tif false && *suiteFlag != suite.ID() { " + PLANT.strip())],
        "./cmd/retnd/",
        ["TestDecryptTakesTheSuiteFromTheRecordAndRefusesToGuess"],
    ),
    Control(
        "a canary identity planted in the suites listing",
        ("TV.16",),
        [(SERVICE_ENC, "\t\t\tDescribe: string(s.Describe()), ActiveCopies: copies[s.ID()], Keys: keysBySuite[s.ID()],",
          "\t\t\tDescribe: string(s.Describe()) + \" AGE-SECRET-KEY-PQ-1PLANTEDCANARYQQQQQQQQQQQQ\", ActiveCopies: copies[s.ID()], Keys: keysBySuite[s.ID()], " + PLANT.strip())],
        "./tests/encryptionsuites/",
        ["TestNoKeyMaterialOnAnySuiteSurface_1319"],
    ),
    Control(
        "the active key id written as a label on the copies family",
        ("TV.17",),
        [(METRICS_GO, "\twriteEncryptedCopiesBySuite(b, h.EncryptedCopiesBySuite)",
          "\twriteEncryptedCopiesBySuite(b, h.EncryptedCopiesBySuite)\n\twriteSample(b, namePrefix+\"retained_copy_encrypted_copies\", []label{{\"key_id\", h.ActiveKeyID}}, 1) " + PLANT.strip())],
        "./internal/metrics/",
        ["TestNoKeyIdIsALabelOnTheSuiteFamily_1315"],
    ),
    Control(
        "the engine writes under a retired suite",
        ("T2.1", "TV.18"),
        [(PLACEMENT_ENC, "\tif err := copycrypt.CheckWrite(suite); err != nil {\n\t\treturn 0, fmt.Errorf(",
          "\tif err := copycrypt.CheckWrite(suite); false && err != nil { " + PLANT.strip() + "\n\t\treturn 0, fmt.Errorf(")],
        "./internal/placement/",
        ["TestAWriteUnderARetiredSuiteIsRefusedBeforeAnyDestructiveStep"],
    ),
    Control(
        "a retired suite's copies refused on read",
        ("T2.1", "TV.18"),
        [(PLACEMENT_ENC, "\tsuite, err := suiteOf(suiteID)\n\tif err != nil {\n\t\treturn Result{}, fmt.Errorf(\"opening %q on %q: %w\", p.Location, p.Medium, err)",
          "\tsuite, err := suiteOf(suiteID)\n\tif err == nil {\n\t\terr = copycrypt.CheckWrite(suite) " + PLANT.strip() + "\n\t}\n\tif err != nil {\n\t\treturn Result{}, fmt.Errorf(\"opening %q on %q: %w\", p.Location, p.Medium, err)")],
        "./internal/placement/",
        ["TestReadsOfARetiredSuiteStillWork"],
    ),
    Control(
        "a deprecated suite with copies reads healthy",
        ("T2.1",),
        [(SUITEHEALTH_GO, "\tcase copycrypt.StatusDeprecated:\n\t\tif h.ActiveCopies == 0 && !h.ActiveKey {",
          "\tcase copycrypt.StatusDeprecated:\n\t\tif true { " + PLANT.strip())],
        "./internal/health/",
        ["TestADeprecatedSuiteDegradesWithCounts"],
    ),
    Control(
        "a copy that fails to decrypt counted as verified",
        ("T2.2", "TV.19"),
        [(PLACEMENT_ENC, "\t\treturn Result{Class: Content, Passed: false, At: now, Detail: fmt.Sprintf(\n\t\t\t\"%s on %s did not decrypt to the artifact: %v\"",
          "\t\treturn Result{Class: Content, Passed: true, At: now, Detail: fmt.Sprintf( " + PLANT.strip() + "\n\t\t\t\"%s on %s did not decrypt to the artifact: %v\"")],
        "./internal/placement/",
        ["TestReencryptRetiresTheSourceOnlyAfterDecryptVerify_1317"],
    ),
    Control(
        "a rotation with no suite named moves to the build default",
        ("TC-04",),
        [(STORE_GO, "\tif suiteID == \"\" {\n\t\tsuiteID = suiteOf(active)\n\t}",
          "\tif suiteID == \"\" {\n\t\tsuiteID = copycrypt.Default().ID() " + PLANT.strip() + "\n\t}")],
        "./internal/keycustody/",
        ["TestAnExistingAgeV1KeyIsUntouchedByTheNewDefault"],
    ),
]


# EPIC N's controls sit beside this file (controls_n_a.py for N1.n and NC-nn,
# controls_n_b.py for NV.n and N2.n), each defining CONTROLS, and are folded in
# here. core/tests/encryptiongate reads all three files for labels.
for _extra in sorted(Path(__file__).resolve().parent.glob("controls_*.py")):
    _ns: dict[str, object] = {"Control": Control, "PLANT": PLANT}
    exec(compile(_extra.read_text(), str(_extra), "exec"), _ns)
    CONTROLS.extend(_ns.get("CONTROLS", []))  # type: ignore[arg-type]


# ---------------------------------------------------------------- the run


EXPLORE = False


def gate(work: Path, c: Control, race: bool) -> tuple[int, str]:
    """The narrowed run, never raising on its own non-zero status: an
    expected failure has to become a value this file inspects."""
    env = dict(os.environ)
    env["GOWORK"] = "off"
    tops = sorted({n.split("/", 1)[0] for n in c.names})
    pattern = "^(" + "|".join(re.escape(t) for t in tops) + ")$" if tops else "^$"
    if EXPLORE:
        pattern = "."
    argv = ["go", "test", "-count=1", "-timeout", c.timeout]
    if race:
        argv.append("-race")
    argv += ["-run", pattern, c.pkg]
    proc = harness.sh(argv, check=False, capture=True, cwd=work / c.module, env=env)
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def caught(c: Control, code: int, out: str) -> tuple[bool, str]:
    """Whether a red run is a catch: the build failure the control names, or
    every named test reported as failing."""
    if code == 0:
        return False, "the narrowed test PASSED against a planted violation"
    if c.build:
        if c.build in out and ("build failed" in out or "setup failed" in out or "FAIL" in out):
            line = next((x.strip() for x in out.splitlines() if c.build in x), "")
            return True, line[:300]
        return False, f"the build did not fail naming {c.build}"
    if "[build failed]" in out or "[setup failed]" in out:
        return False, "the package did not build, so nothing was caught"
    missing = [n for n in c.names if f"--- FAIL: {n}" not in out]
    if missing:
        return False, "it failed, but never named " + ", ".join(missing)
    lines = [x.strip() for x in out.splitlines() if x.strip().startswith("--- FAIL:")]
    detail = next((x.strip() for x in out.splitlines() if "_test.go:" in x), "")
    return True, "; ".join(lines)[:400] + (f" | {detail[:300]}" if detail else "")


def selected(c: Control, only: list[str]) -> bool:
    return not only or any(o in c.label or o in c.rows for o in only)


def commit_of(root: Path) -> str:
    proc = harness.sh(["git", "rev-parse", "--short=12", "HEAD"], check=False, capture=True, cwd=root)
    dirty = harness.sh(["git", "status", "--porcelain", "--untracked-files=no"], check=False, capture=True, cwd=root)
    head = (proc.stdout or "").strip() or "unknown"
    return head + ("+dirty" if (dirty.stdout or "").strip() else "")


def body(root: Path, dry_run: bool, only: list[str], race: bool, record: Path | None) -> int:
    tally = selftest_swap.Tally()
    controls = [c for c in CONTROLS if selected(c, only)]
    labels = [c.label for c in CONTROLS]
    dupes = sorted({x for x in labels if labels.count(x) > 1})
    if dupes:
        harness.die("two controls share a label, so a matrix row citing it proves neither", *dupes)
    host = os.uname().nodename
    commit = commit_of(root)

    with tempfile.TemporaryDirectory(prefix="retnd-encryption-selftest.") as tmp_name:
        tmp = Path(tmp_name)
        tracker = selftest_swap.AnchorTracker(dry_run=dry_run, root=root, tmp=tmp)
        work = selftest_swap.mutant(root, tmp, "work", dry_run=dry_run)

        for c in controls:
            print()
            print(f"==> {c.label} ({', '.join(c.rows)})")
            saved = {rel: (work / rel).read_bytes() for rel, _, _ in c.plants if (work / rel).exists()}
            try:
                for rel, old, new in c.plants:
                    tracker.swap(work / rel, old, new)
                if tracker.stale_verdict(c.label) or tracker.anchors_only(c.label):
                    continue
                code, out = gate(work, c, race)
                if EXPLORE:
                    fails = [x.strip() for x in out.splitlines() if "--- FAIL" in x or "build failed" in x or x.startswith("FAIL")]
                    print("EXPLORE " + c.label + ":\n    " + "\n    ".join(fails[:40]))
                    if "build failed" in out:
                        print("\n".join(out.splitlines()[:15]))
                    continue
                ok, why = caught(c, code, out)
            finally:
                if not dry_run:
                    for rel, data in saved.items():
                        (work / rel).write_bytes(data)
            if not ok:
                tally.bad(f"{c.label}. {why}.", out[-6000:])
                verdict, green = "NOT CAUGHT", "-"
            else:
                gcode, gout = gate(work, c, race)
                if gcode != 0:
                    tally.bad(f"{c.label}. The narrowed test FAILED on the unplanted tree, so its red means nothing.", gout[-6000:])
                    verdict, green = "RED ON A CLEAN TREE", "red"
                else:
                    tally.ok(f"(caught): {c.label}", why)
                    verdict = "caught"
                    green = next((x.strip() for x in gout.splitlines() if x.startswith("ok")), "ok")
            if record is not None:
                stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
                with record.open("a") as fh:
                    fh.write(
                        f"{stamp}\t{host}\t{commit}\t{' '.join(c.rows)}\t{c.label}\t{verdict}\t"
                        f"go test -count=1{' -race' if race else ''} -run <{','.join(c.names) or c.build}> {c.pkg}\t"
                        f"RED: {why}\tGREEN: {green}\n"
                    )

        tracker.stale_summary()
        stale = tracker.stale_count
        checked = tracker.anchors_checked
        shutil.rmtree(tmp / "work", ignore_errors=True)

    total = tally.passed + tally.failed + stale
    if tally.failed or stale:
        print(file=sys.stderr)
        print(
            f"FAIL: {tally.failed + stale} of {total} encryption controls did not behave as required "
            f"({tally.failed} reached the wrong verdict, {stale} could not plant their violation at all).",
            file=sys.stderr,
        )
        return harness.EXIT_FAILED
    print()
    if dry_run:
        print(f"OK: all {checked} encryption mutation anchors still name code that is in this tree.")
    else:
        print(f"OK: all {tally.passed} encryption controls went red naming their test and green again unplanted.")
    return harness.EXIT_OK


def main(argv: list[str]) -> int:
    harness.set_program(PROGRAM)
    harness.install_signal_handlers()
    rest: list[str] = []
    only: list[str] = []
    race = False
    record: Path | None = None
    it = iter(argv)
    for arg in it:
        if arg == "--only":
            only.append(next(it, ""))
        elif arg == "--explore":
            global EXPLORE
            EXPLORE = True
        elif arg == "--race":
            race = True
        elif arg == "--record":
            record = Path(next(it, "")).resolve()
        else:
            rest.append(arg)
    dry_run = selftest_swap.parse_selftest_args(rest, "selftest.py")
    root = harness.repo_root(Path(__file__))
    return harness.finish(lambda: body(root, dry_run, only, race, record))


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
