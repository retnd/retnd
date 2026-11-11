# EPIC N rows proven in round 2 of the 0.8.0 release PR (#1331): the crash
# cell for an unresolvable key, the two source guards, and the gate and brand
# guards. Exec'd by selftest.py with Control and PLANT defined.

PLACE_ENC_R2 = "core/internal/placement/encryption.go"
AGEX_R2 = "core/internal/copycrypt/agex25519.go"
KC_AGE_R2 = "core/internal/keycustody/age.go"

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
]
