# EPIC N, the crash-cell halves of N1.8, NC-06, NV.6, NC-14 and NV.13 (#1539).
# Exec'd by selftest.py, which defines Control and PLANT.
#
# Every control here is caught by a cell in core/tests/movecrash, which kills
# a real harness process and restarts the real engine over what it left. The
# rows name core/tests/crashmatrix, but the encrypted cells live in movecrash:
# crashmatrix is the plaintext transfer matrix and has no encrypting hop.

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    # Only for the type checker: at run time selftest.py execs this file
    # with both names already in scope.
    from bdtools.encryption.selftest import PLANT, Control

PLACE_ENC_CRASH = "core/internal/placement/encryption.go"
HARNESS_CRASH = "core/tests/movecrash/harness/main.go"

CONTROLS = [
    Control(
        "crash a re-copy trusts the ciphertext a killed attempt left at the key",
        ("N1.8", "NC-06", "NV.6", "NC-14"),
        [(PLACE_ENC_CRASH, "\tstaged, err := e.stagingPath(mv.Artifact)\n\tif err != nil {\n\t\treturn 0, err\n\t}\n\tdir := filepath.Dir(staged)",
          "\t" + PLANT + "an object already at the key is taken as the copy.\n"
          "\tif info, statErr := e.Store.StatObject(ctx, medium, mv.DestinationKey); statErr == nil {\n\t\treturn info.Size, nil\n\t}\n"
          "\tstaged, err := e.stagingPath(mv.Artifact)\n\tif err != nil {\n\t\treturn 0, err\n\t}\n\tdir := filepath.Dir(staged)")],
        "./tests/movecrash/",
        ["TestCrashMatrixOverAnEncryptedCopy/E3_the_instant_the_upload_returns,_before_anything_is_journaled"],
        timeout="25m",
    ),
    Control(
        "crash a re-copy uploads the finished ciphertext a killed attempt staged",
        ("N1.8", "NC-06", "NC-14"),
        [(PLACE_ENC_CRASH, "\tsrcErr := e.withEncryptionSource(ctx, mv, src, func(plaintext io.Reader) error {\n\t\tm := e.newMeter(StageEncrypting, mv.Artifact, total)",
          "\t_, plantedStatErr := os.Stat(staged)\n"
          "\tsrcErr := e.withEncryptionSource(ctx, mv, src, func(plaintext io.Reader) error {\n"
          "\t\tif plantedStatErr == nil {\n\t\t\t" + PLANT + "a finished staging object is resumed.\n\t\t\tpass = copycrypt.Staged{Path: staged}\n\t\t\treturn nil\n\t\t}\n"
          "\t\tm := e.newMeter(StageEncrypting, mv.Artifact, total)")],
        "./tests/movecrash/",
        ["TestCrashMatrixOverAnEncryptedCopy/E2_the_instant_the_staged_ciphertext_is_complete,_before_the_upload"],
        timeout="25m",
    ),
    Control(
        "crash the pre-upload kill hook disarmed in the harness",
        ("NC-14", "NV.13"),
        [(HARNESS_CRASH, "\tif s.killBeforeUpload {\n\t\tselfDestruct(",
          "\tif false && s.killBeforeUpload { " + PLANT.strip() + "\n\t\tselfDestruct(")],
        "./tests/movecrash/",
        ["TestTheEncryptedCrashCellsReallyEncryptSomething"],
        timeout="25m",
    ),
    Control(
        "crash the staging kill hook disarmed in the harness",
        ("NC-14", "NV.13"),
        [(HARNESS_CRASH, "\tif l.killAfterPut {\n\t\tselfDestruct(",
          "\tif false && l.killAfterPut { " + PLANT.strip() + "\n\t\tselfDestruct(")],
        "./tests/movecrash/",
        ["TestTheStagedCrashCellsReallyStageSomething"],
        timeout="25m",
    ),
    Control(
        "crash an unresolvable key downgraded to an existence pass on a real re-home",
        ("NC-14",),
        [(PLACE_ENC_CRASH, "\tcase !resolved:\n\t\treturn Result{}, fmt.Errorf(",
          "\tcase !resolved:\n\t\treturn Result{Class: Existence, Passed: true, At: now}, nil " + PLANT.strip() + "\n\t\treturn Result{}, fmt.Errorf(")],
        "./tests/movecrash/",
        ["TestAnEncryptedRehomeWithAnUnresolvableKeyNeverReachesTheSourceDelete_1539"],
        timeout="25m",
    ),
]
