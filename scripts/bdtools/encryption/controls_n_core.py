# EPIC N, the halves #1539 closes for N1.X1292, N1.12, N1.19 and NC-15.
# Exec'd by selftest.py, which defines Control and PLANT.

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    # Only for the type checker: at run time selftest.py execs this file
    # with both names already in scope.
    from bdtools.encryption.selftest import PLANT, Control

EXTREG_N = "core/internal/keycustody/externalregistry.go"
PLACE_ENC_C = "core/internal/placement/encryption.go"
BUNDLE_N = "core/internal/keycustody/bundle.go"
DECRYPT_N = "core/cmd/retnd/encryptiondecrypt.go"
ENCEXIST_STATE = "core/internal/state/encryptexisting.go"
WEBHOST_ROUTER = "apps/common/webhost/router.go"
WEBHOST_ENC = "apps/common/webhost/handlers_encryption.go"

CONTROLS = [
    # N1.X1292: the registry row carries the recipient and nothing else.
    Control(
        "N1.X1292 the external key's reference written into its registry row",
        ("N1.X1292",),
        [(EXTREG_N, "\trec := KeyRecord{ID: id, Provider: ProviderExternal, Recipient: recipient, Created: now.UTC(), Status: StatusActive, SuiteID: suiteID}",
          "\trec := KeyRecord{ID: id, Provider: ProviderExternal, Recipient: recipient + \" from \" + ref.File, Created: now.UTC(), Status: StatusActive, SuiteID: suiteID} " + PLANT.strip())],
        "./internal/keycustody/",
        ["TestExternalRegistryRowsCarryNoMaterialCanary"],
    ),
    Control(
        "N1.X1292 the external key's material written into its registry row",
        ("N1.X1292",),
        [(EXTREG_N, "\t\tr, id, err := s.recipientOfAnySuite(key)\n\t\trecipient, suiteID = r, id",
          "\t\tr, id, err := s.recipientOfAnySuite(key)\n\t\trecipient, suiteID = r+\" \"+string(key), id " + PLANT.strip())],
        "./internal/keycustody/",
        ["TestExternalRegistryRowsCarryNoMaterialCanary"],
    ),
    # N1.X1292: a reference that does not resolve never reaches the upload.
    # Encrypting needs only a recipient, so the plant hands the pass one that
    # no declared reference ever yielded, which is what "the copy went ahead
    # with a key nobody resolved" looks like from the medium.
    Control(
        "N1.X1292 an unresolvable external key let through to the upload",
        ("N1.X1292",),
        [(PLACE_ENC_C, "\trec, err := ek.RegisterExternal(ctx, ref, e.now())\n\tif err != nil {\n\t\treturn keycustody.KeyRecord{}, fmt.Errorf(",
          "\trec, err := ek.RegisterExternal(ctx, ref, e.now())\n\tif err != nil {\n\t\t" + PLANT + "copy under a recipient nobody resolved.\n"
          "\t\t_, r, _ := copycrypt.AgeX25519{}.Generate()\n"
          "\t\treturn keycustody.KeyRecord{ID: \"x00planted000000\", Provider: keycustody.ProviderExternal, Recipient: r, Status: keycustody.StatusActive, SuiteID: copycrypt.AgeX25519ID}, nil\n"
          "\t}\n\tif err != nil {\n\t\treturn keycustody.KeyRecord{}, fmt.Errorf(")],
        "./internal/placement/",
        ["TestAnExternalKeyThatDoesNotResolveIsRefusedBeforeAnyUpload_1292"],
    ),
    Control(
        "N1.X1292 an external key that changed under its reference handed out for the old row",
        ("N1.X1292",),
        [(EXTREG_N, "\t\t\trecipient, err := f.Recipient(key)\n\t\t\tif err != nil || recipient != rec.Recipient {",
          "\t\t\t_, err = f.Recipient(key)\n\t\t\tif err != nil { " + PLANT.strip())],
        "./internal/placement/",
        ["TestAnExternalKeyThatChangedUnderItsReferenceIsNotHandedOutForTheOldRow_1292"],
    ),
    # N1.19: the feed volume is one durable event per copy, never per chunk.
    Control(
        "N1.19 the encrypting event logged once per chunk read",
        ("N1.19",),
        [(PLACE_ENC_C, "\t\tpass, encErr = copycrypt.EncryptToStaging(ctx, dir, filepath.Base(staged), m.wrap(plaintext), suite, key.Recipient, src.Hash)",
          "\t\tpass, encErr = copycrypt.EncryptToStaging(ctx, dir, filepath.Base(staged), m.wrap(io.TeeReader(plaintext, plantedChunkLog{e, ctx, mv})), suite, key.Recipient, src.Hash) " + PLANT.strip()),
         (PLACE_ENC_C, "// ExternalKeys is what custody adds for an external provider (#1292):",
          PLANT + "one durable event per chunk read.\ntype plantedChunkLog struct {\n\te   *Engine\n\tctx context.Context\n\tmv  state.Move\n}\n\n"
          "func (p plantedChunkLog) Write(b []byte) (int, error) {\n\tp.e.Logger.CopyEncrypting(p.ctx, p.mv.Artifact.String(), p.mv.DestinationMedium, obs.ResultSuccess)\n\treturn len(b), nil\n}\n\n"
          "// ExternalKeys is what custody adds for an external provider (#1292):")],
        "./internal/placement/",
        ["TestAnEncryptedCopyEmitsOneEventPerStageNotPerChunk_992"],
    ),
    # N1.12: the exported bundle is what decrypts a copy on a clean machine.
    Control(
        "N1.12 the exported bundle carries a key other than the one it names",
        ("N1.12",),
        [(BUNDLE_N, "\tif err := s.WithKey(ctx, id, func(key []byte) error {\n\t\tb.Material = string(key)\n\t\treturn nil\n\t}); err != nil {\n\t\treturn Receipt{}, err",
          "\tif err := s.WithKey(ctx, id, func(key []byte) error {\n\t\t" + PLANT + "the bundle holds some other key.\n"
          "\t\tother, _, err := AgeFormat{}.Generate()\n\t\tb.Material = string(other)\n\t\treturn err\n\t}); err != nil {\n\t\treturn Receipt{}, err")],
        "./cmd/retnd/",
        ["TestDecryptRoundTripsThroughTheBundleAndTheSidecar_1289"],
    ),
    # NC-15: the clean-machine decrypt needs the bundle, the binary and the
    # copy, and never this deployment's configuration or state database.
    Control(
        "NC-15 decrypt refuses on a machine without the deployment's configuration and state",
        ("NC-15", "N1.12"),
        [(DECRYPT_N, "\tobject := operands[0]\n",
          "\tobject := operands[0]\n\tif _, err := os.Stat(defaultConfigPath); err != nil { " + PLANT.strip() + "\n"
          "\t\treturn fail(fmt.Errorf(\"decrypt reads this deployment's state database, and there is none here: %w\", err))\n\t}\n")],
        "./cmd/retnd/",
        ["TestDecryptRoundTripsThroughTheBundleAndTheSidecar_1289"],
    ),
    # NF.1 (#1291): an opt-in belongs to one move, never to the artifact.
    Control(
        "NF.1 the encrypt-existing opt-in read back through the artifact",
        ("NF.1",),
        [(ENCEXIST_STATE, "\terr := j.db.QueryRowContext(ctx, `SELECT 1 FROM encrypt_existing_consents WHERE move_id = ?`, moveID).Scan(&one)",
          "\terr := j.db.QueryRowContext(ctx, `SELECT 1 FROM encrypt_existing_consents c JOIN placement_moves m ON m.id = c.move_id "
          "WHERE m.artifact_id = (SELECT artifact_id FROM placement_moves WHERE id = ?) LIMIT 1`, moveID).Scan(&one) " + PLANT.strip())],
        "./internal/state/",
        ["TestAnOptInIsNotInheritedByALaterMoveOfTheSameArtifact_1291"],
    ),
    # NF.8 (#1291): the plaintext is streamed off its medium into the pass.
    Control(
        "NF.8 the engine reads the whole plaintext object before encrypting it",
        ("NF.8",),
        [("core/internal/placement/retroencrypt.go", "\t\"errors\"\n\t\"fmt\"\n\t\"io\"\n\t\"strings\"\n\t\"time\"",
          "\t\"bytes\"\n\t\"errors\"\n\t\"fmt\"\n\t\"io\"\n\t\"strings\"\n\t\"time\""),
         ("core/internal/placement/retroencrypt.go",
          "\tif err := e.sourceCanBeStaged(ctx, medium, src); err != nil {\n\t\treturn nil, err\n\t}\n\treturn e.Store.OpenObject(ctx, medium, src.Location)\n}",
          "\tif err := e.sourceCanBeStaged(ctx, medium, src); err != nil {\n\t\treturn nil, err\n\t}\n"
          "\t" + PLANT + "the whole object is read into memory first.\n"
          "\trc, err := e.Store.OpenObject(ctx, medium, src.Location)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tdefer rc.Close()\n"
          "\twhole, err := io.ReadAll(rc)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn io.NopCloser(bytes.NewReader(whole)), nil\n}")],
        "./internal/placement/",
        ["TestEncryptExistingStreamsThePlaintextIntoTheEncryptingPass_1291"],
    ),
]

# N2.6 / NC-17: the screenshot surface's permanent negative control. The
# capture script plants one value per kind in its own scanner on every run;
# a scanner gone blind to canaries has to stop it. Plain node, no browser.
CONTROLS += [
    Control(
        "N2.6 the capture scanner blind to a planted canary",
        ("N2.6", "NC-17", "NV.32"),
        [("docs/site/tools/capture-encryption.mjs",
          "const KEY_MATERIAL = /AGE-SECRET-KEY|AGE-PLUGIN-|BEGIN [A-Z ]*PRIVATE KEY|PRIVATE KEY-----|CANARY|",
          "// PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): canaries are no longer matched.\n"
          "const KEY_MATERIAL = /AGE-SECRET-KEY|AGE-PLUGIN-|BEGIN [A-Z ]*PRIVATE KEY|PRIVATE KEY-----|")],
        "",
        cmd=["node", "docs/site/tools/capture-encryption.mjs", "--scanner-control"],
        needles=["the scanner control failed", "the scanner did not name: a planted canary"],
        timeout="2m",
    ),
]

# N2.4: a reload mid-operation restores the stage from the service's feed and
# starts nothing. Plain vitest from ui/shared (node_modules linked in), one
# file, no browser.
RELOAD_VITEST = ["./node_modules/.bin/vitest", "run", "src/test/encryption-reload-mid-operation-1539.test.tsx", "--no-cache"]
CONTROLS += [
    Control(
        "N2.4 a page load resumes the operation the feed shows running",
        ("N2.4",),
        [("ui/shared/src/pages/DashboardActivity.tsx",
          "import { ErrorState } from \"@shared/components/EmptyState\";",
          "import { useEffect } from \"react\";\nimport { useApi } from \"@shared/api/ApiContext\";\nimport { ErrorState } from \"@shared/components/EmptyState\";"),
         ("ui/shared/src/pages/DashboardActivity.tsx",
          "  const feed = useActivityFeed();\n",
          "  const feed = useActivityFeed();\n"
          "  // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): a page load resumes what the feed shows running.\n"
          "  const plantedApi = useApi();\n"
          "  useEffect(() => {\n"
          "    for (const a of feed.held.values()) if (a.active) void plantedApi.startDecryptVerify({ artifactId: a.setId + \"/\" + (a.artifact ?? \"\"), medium: \"offsite\", acknowledgeEgress: true }).catch(() => undefined);\n"
          "  }, [feed.held, plantedApi]);\n")],
        "",
        module="ui/shared",
        cmd=RELOAD_VITEST,
        needles=["FAIL", "starts nothing"],
        links=["ui/shared/node_modules"],
        timeout="5m",
    ),
    Control(
        "N2.4 a fresh page load waits for the timer instead of reading the feed",
        ("N2.4",),
        [("ui/shared/src/state/terminalStore.ts",
          "    reader = new TerminalReader(api);\n    void reader.poll();\n",
          "    reader = new TerminalReader(api);\n    // PLANTED VIOLATION (scripts/bdtools/encryption/selftest.py): no reading until the timer.\n")],
        "",
        module="ui/shared",
        cmd=RELOAD_VITEST,
        needles=["FAIL", "starts nothing"],
        links=["ui/shared/node_modules"],
        timeout="5m",
    ),
]

# NF.6 (#1291): the run is a CSRF write, decoded strictly, and a backend
# without the feature answers 503. The guards are webhost tests, so these
# run from apps/common.
CONTROLS += [
    Control(
        "NF.6 the encrypt-existing run registered without requireCSRF",
        ("NF.6",),
        [(WEBHOST_ROUTER, "\t\tr.With(requireCSRF, requireRouteGate(h.enforce)).Post(\"/encryption/encrypt-existing\", h.startEncryptExisting)",
          "\t\tr.With(requireRouteGate(h.enforce)).Post(\"/encryption/encrypt-existing\", h.startEncryptExisting) " + PLANT.strip())],
        "./webhost/",
        ["TestTheEncryptExistingRunNeedsCSRFAndTheDryRunDoesNot_1291"],
        module="apps/common",
    ),
    Control(
        "NF.6 the encrypt-existing run decoded leniently",
        ("NF.6",),
        [(WEBHOST_ENC, "import (\n\t\"context\"\n\t\"errors\"",
          "import (\n\t\"context\"\n\t\"encoding/json\"\n\t\"errors\""),
         (WEBHOST_ENC, "\tvar body apicontract.StartEncryptExistingRequest\n\tif !decodeStrict(w, r, &body) {\n\t\treturn\n\t}",
          "\tvar body apicontract.StartEncryptExistingRequest\n\tif err := json.NewDecoder(r.Body).Decode(&body); err != nil { " + PLANT.strip() + "\n"
          "\t\twriteError(w, http.StatusBadRequest, \"INVALID_REQUEST\", \"the request body is not JSON\")\n\t\treturn\n\t}")],
        "./webhost/",
        ["TestEncryptExistingRefusesUnknownFields_1291"],
        module="apps/common",
    ),
    Control(
        "NF.6 a backend without encrypt-existing answers 404 instead of 503",
        ("NF.6",),
        [(WEBHOST_ENC, "\tb, ok := h.backend.(EncryptExistingBackend)\n\tif !ok {\n\t\twriteError(w, http.StatusServiceUnavailable, \"ENCRYPTION_UNAVAILABLE\",",
          "\tb, ok := h.backend.(EncryptExistingBackend)\n\tif !ok {\n\t\twriteError(w, http.StatusNotFound, \"NOT_FOUND\", " + PLANT.strip() + "\n\t\t\t")],
        "./webhost/",
        ["TestABackendWithoutEncryptExistingAnswersUnavailable_1291"],
        module="apps/common",
    ),
]
