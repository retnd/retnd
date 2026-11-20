# EPIC N, the halves #1539 closes for the docs and parity rows: N1.2, N1.15,
# N1.16, N1.17, N2.7, NC-04, NC-07, NC-10 and NV.34. Exec'd by selftest.py,
# which defines Control and PLANT.

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    # Only for the type checker: at run time selftest.py execs this file
    # with both names already in scope.
    from bdtools.encryption.selftest import PLANT, Control

ENC_COPIES_DOC = "docs/encrypted-copies.md"
ENGINE_GO_D = "core/internal/placement/engine.go"
MIG15_D = "core/migrations/0015_retained_copy_encryption.sql"
ENCRYPTION_GO_D = "core/cmd/retnd/encryption.go"
ENCRYPTION_ROUTE_D = "core/cmd/retnd/encryptionroute.go"
CLIECHO_ROUTES_D = "core/cliecho/routes.go"
REFERENCE_D = "docs/site/reference.html"
GO_BINDING_D = "core/apicontract/contract.gen.go"
TS_BINDING_D = "ui/shared/src/api/generated/contract.ts"

CONTROLS = [
    # ---- N1.15, NC-10: content, not names --------------------------------
    Control(
        "N1.15 the README stops saying encryption hides contents and not names",
        ("N1.15", "NC-10"),
        [("README.md", "- **It hides contents, not names.** The source,",
          "- **It hides contents.** The source,")],
        "./tests/encryptiongate/",
        ["TestTheDocsSayEncryptionProtectsContentAndNotNames_1539"],
    ),
    Control(
        "NC-10 the encrypted copies page drops the reason the key layout stays",
        ("NC-10",),
        [(ENC_COPIES_DOC, "The layout does not change for encrypted copies, because\nchanging it would orphan every plaintext object already in the bucket (ADR 0028).",
          "The layout does not change for encrypted copies (ADR 0028).")],
        "./tests/encryptiongate/",
        ["TestTheDocsSayEncryptionProtectsContentAndNotNames_1539"],
    ),
    # ---- NC-04: what a managed key protects against ------------------------
    Control(
        "NC-04 the encrypted copies page stops saying a managed key does not protect against the machine owner",
        ("NC-04",),
        [(ENC_COPIES_DOC, "the key, so a managed key does **not** protect your copies from whoever owns this machine. It\n",
          "the key, and a managed key protects your copies. It\n")],
        "./tests/encryptiongate/",
        ["TestTheDocsSayWhatAManagedKeyProtectsAgainst_1539"],
    ),
    # ---- N2.7, NC-07: the two falsified digest comments --------------------
    Control(
        "N2 the engine comment says again that a copy is the same bytes under the same hash",
        ("N2.7", "NC-07"),
        [(ENGINE_GO_D, "// Everything else comes from the source row: the hash it carries is the\n// artifact's digest, and that is what every placements row has always\n// held",
          "// Everything else comes from the source row, since a copy is the same\n// bytes under the same hash, and that is what every placements row has always\n// held")],
        "./tests/encryptiongate/",
        ["TestTheTwoFalsifiedDigestCommentsStayCorrected_1539"],
    ),
    Control(
        "N2 migration 0015 stops correcting what 0007 says placements.hash holds",
        ("N2.7", "NC-07"),
        [(MIG15_D, "-- correction goes here: placements.hash has only ever held the ARTIFACT'S\n-- digest, copied onto each copy",
          "-- correction goes here: placements.hash holds a digest, copied onto each copy")],
        "./tests/encryptiongate/",
        ["TestTheTwoFalsifiedDigestCommentsStayCorrected_1539"],
    ),
    # ---- N1.16, NV.34: parity both ways ------------------------------------
    Control(
        "N1.16 a terminal-only encryption verb nobody declared",
        ("N1.16",),
        [(ENCRYPTION_GO_D, "\t\"suites\":     cmdEncryptionSuites,\n",
          "\t\"suites\":     cmdEncryptionSuites,\n\t\"rekey\":      cmdEncryptionSuites, " + PLANT.strip() + "\n")],
        "./tests/encryptiongate/",
        ["TestEveryEncryptionVerbHasABrowserTwinOrSaysWhyNot_1539"],
    ),
    Control(
        "NV the key retire route left without an echo, seen from the webhost",
        ("N1.16", "NV.34"),
        [(CLIECHO_ROUTES_D, "\tkey(\"POST\", \"/encryption/keys/{id}/retire\"): {\n\t\tbuild: func(a Action) *cmd {\n\t\t\treturn newCmd(\"encryption\", \"key\", \"retire\", a.Params[\"id\"])\n\t\t},\n\t\texamples: []Action{{Params: map[string]string{\"id\": \"k0123456789abcdef\"}}},\n\t},\n",
          "\t" + PLANT + "the retire route has no entry.\n")],
        "./webhost/",
        ["TestEveryAPIRouteNamesItsCLIEquivalentOrTheGap"],
        module="apps/common",
    ),
    Control(
        "NV a gap sentence on the policy patch route naming the encryption verb",
        ("N1.16", "NV.34"),
        [(CLIECHO_ROUTES_D, "\t\twhy: \"there is no verb that patches the encryption policy from a request body that does not decode\",",
          "\t\twhy: \"there is no `encryption policy patch` for a request body that does not decode\", " + PLANT.strip())],
        "./cmd/retnd/",
        ["TestNoGapClaimsAVerbThisBinaryShips"],
    ),
    Control(
        "NV the copies verb left off the reference page's encryption row",
        ("N1.16", "NV.34"),
        [(REFERENCE_D, "<code>copies &lt;source/backup-set/backup&gt;</code>",
          "the copies of one backup")],
        "./packaging/",
        ["TestTheSiteReferenceEncryptionRowNamesEveryEncryptionVerb"],
        module="distribution",
    ),
    # ---- N1.17: the generated bindings are generated -----------------------
    Control(
        "N1.17 a field added by hand to the Go binding of the key reference",
        ("N1.17",),
        [(GO_BINDING_D, "type EncryptionKeyReference struct {\n\tCommand []string `json:\"command,omitempty\"`\n",
          "type EncryptionKeyReference struct {\n\tInline  string   `json:\"inline,omitempty\"` " + PLANT.strip() + "\n\tCommand []string `json:\"command,omitempty\"`\n")],
        "",
        cmd=["bash", "scripts/api/check-contract-drift.sh"],
        needles=["FAIL: the checked-in Go binding does not match"],
    ),
    Control(
        "N1.17 a field added by hand to the TypeScript binding of the key reference",
        ("N1.17",),
        [(TS_BINDING_D, "export interface WireEncryptionKeyReference {\n  command?: string[];\n",
          "export interface WireEncryptionKeyReference {\n  inline?: string; // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py)\n  command?: string[];\n")],
        "",
        cmd=["bash", "scripts/api/check-contract-drift.sh"],
        needles=["FAIL: the checked-in TypeScript binding does not match"],
    ),
    # ---- N1.2: the same refusal on both routes -----------------------------
    Control(
        "N1.2 the engine route loses the reason it refused required with no provider",
        ("N1.2",),
        [(ENCRYPTION_ROUTE_D, "\treturn r.client.PatchEncryptionPolicy(ctx, req)\n}",
          "\tgot, err := r.client.PatchEncryptionPolicy(ctx, req)\n\tif err != nil {\n\t\terr = fmt.Errorf(\"the engine refused the policy patch\") " + PLANT.strip() + "\n\t}\n\treturn got, err\n}")],
        "./cmd/retnd/",
        ["TestARefusalIsTheSameOnBothRoutes/require_encryption_with_no_key_provider"],
    ),
    Control(
        "N1.2 the engine route exits as if another process held the deployment",
        ("N1.2",),
        [(ENCRYPTION_ROUTE_D, "\treturn r.client.PatchEncryptionPolicy(ctx, req)\n}",
          "\tgot, err := r.client.PatchEncryptionPolicy(ctx, req)\n\tif err != nil {\n\t\terr = fmt.Errorf(\"%w: %w\", errEngineHoldsDeployment, err) " + PLANT.strip() + "\n\t}\n\treturn got, err\n}")],
        "./cmd/retnd/",
        ["TestARefusalIsTheSameOnBothRoutes/require_encryption_with_no_key_provider"],
    ),
]
