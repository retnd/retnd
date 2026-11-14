# EPIC N, rows N1.1 to N1.20 and NC-01 to NC-19. Exec'd by selftest.py,
# which defines Control and PLANT.

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    # Only for the type checker: at run time selftest.py execs this file
    # with both names already in scope.
    from bdtools.encryption.selftest import PLANT, Control

CONFIG_RCE_N = "core/internal/config/retainedcopyencryption.go"
CONFIG_GO_N = "core/internal/config/config.go"
WORKFLOWS_GO_N = "core/internal/config/workflows.go"

CONTROLS = [
    Control(
        "the deployment mode defaults to required when nothing is written",
        ("N1.1",),
        [(CONFIG_RCE_N, "\tif out.Mode == \"\" {\n\t\tout.Mode = RetainedCopyEncryptionOff\n\t}",
          "\tif out.Mode == \"\" {\n\t\tout.Mode = RetainedCopyEncryptionRequired " + PLANT.strip() + "\n\t}")],
        "./internal/config/",
        ["TestRetainedCopyEncryption_ANilBlockResolvesEverySetToOff"],
    ),
    Control(
        "the encryption block is marshalled even when it is unset",
        ("N1.1",),
        [(CONFIG_GO_N, "\tRetainedCopyEncryptionBlock *RetainedCopyEncryptionBlock `yaml:\"retained_copy_encryption,omitempty\"`",
          "\tRetainedCopyEncryptionBlock *RetainedCopyEncryptionBlock `yaml:\"retained_copy_encryption\"` " + PLANT.strip())],
        "./internal/config/",
        ["TestRetainedCopyEncryption_AConfigThatMentionsNothingSurvivesAReMarshalByteIdentically"],
    ),
    Control(
        "mode required accepted with no key provider",
        ("N1.2",),
        [(CONFIG_RCE_N, "\tif out.Mode == RetainedCopyEncryptionRequired && out.KeyProvider.IsZero() {",
          "\tif false && out.Mode == RetainedCopyEncryptionRequired && out.KeyProvider.IsZero() { " + PLANT.strip())],
        "./internal/config/",
        ["TestRetainedCopyEncryption_Refusals/required_with_no_provider_names_the_key_at_fault"],
    ),
    Control(
        "an algorithm knob added to the deployment block",
        ("NC-02",),
        [(CONFIG_RCE_N, "\tSuite string `yaml:\"suite,omitempty\"`\n}",
          "\tSuite string `yaml:\"suite,omitempty\"`\n\n\tAlgorithm string `yaml:\"algorithm,omitempty\"` " + PLANT.strip() + "\n}")],
        "./internal/config/",
        ["TestRetainedCopyEncryption_TheSchemaHasNoCryptoKnob"],
    ),
    Control(
        "a per-set chunk size knob added to the override",
        ("NC-19",),
        [(CONFIG_RCE_N, "\tKeyProvider *RetainedCopyKeyProvider `yaml:\"key_provider,omitempty\"`\n}\n",
          "\tKeyProvider *RetainedCopyKeyProvider `yaml:\"key_provider,omitempty\"`\n\n\tChunk string `yaml:\"chunk,omitempty\"` " + PLANT.strip() + "\n}\n")],
        "./internal/config/",
        ["TestRetainedCopyEncryption_TheSchemaHasNoCryptoKnob"],
    ),
    Control(
        "a fourth source field on the external key reference",
        ("NC-03",),
        [(WORKFLOWS_GO_N, "\tCommand []string `yaml:\"command,omitempty\"`\n}",
          "\tCommand []string `yaml:\"command,omitempty\"`\n\tInline  string   `yaml:\"inline,omitempty\"` " + PLANT.strip() + "\n}")],
        "./internal/secretref/",
        ["TestRefFieldSetMatchesTheConfiguredOnes"],
    ),
    Control(
        "an inline identity field accepted on the key provider",
        ("N1.18", "NC-18"),
        [(CONFIG_RCE_N, "\tExternal *SecretSource `yaml:\"external,omitempty\"`\n}",
          "\tExternal *SecretSource `yaml:\"external,omitempty\"`\n\tIdentity string        `yaml:\"identity,omitempty\"` " + PLANT.strip() + "\n}")],
        "./internal/config/",
        ["TestRetainedCopyEncryption_InlineKeyMaterialIsRefusedAtLoad"],
    ),
]

KC_STORE_N = "core/internal/keycustody/store.go"
KC_BUNDLE_N = "core/internal/keycustody/bundle.go"
KEYRETIRE_N = "core/internal/placement/keyretire.go"
ALERT_N = "core/internal/alert/alert.go"
MANIFEST_N = "core/internal/recovery/manifest.go"
STATE_ENC_N = "core/internal/state/encryption.go"
PLACEMENT_ENC_N = "core/internal/placement/encryption.go"
SECRETREF_N = "core/internal/secretref/secretref.go"

CONTROLS += [
    Control(
        "a managed key file read without the custody check",
        ("N1.10",),
        [(KC_STORE_N, "\tsecret, err := secretref.Resolve(ctx, secretref.Ref{File: s.keyPath(id)})\n\tif err != nil {\n\t\treturn err\n\t}\n\tkey := []byte(secret.Reveal())",
          "\t" + PLANT + "the key file is read whatever its mode or link.\n\t_ = secretref.Resolve\n\traw, err := os.ReadFile(s.keyPath(id))\n\tif err != nil {\n\t\treturn err\n\t}\n\tkey := []byte(strings.TrimSpace(string(raw)))")],
        "./internal/keycustody/",
        ["TestAKeyFileThatGainsGroupReadIsRefusedByName", "TestASymlinkedKeyFileIsRefused"],
    ),
    Control(
        "a group-readable key directory accepted",
        ("N1.10", "NC-04"),
        [(KC_STORE_N, "\tif mode := li.Mode().Perm(); mode&0o077 != 0 {",
          "\tif mode := li.Mode().Perm(); mode&0o002 != 0 { " + PLANT.strip())],
        "./internal/keycustody/",
        ["TestAKeyDirectoryThatGainsGroupReadIsRefusedByName"],
    ),
    Control(
        "a key an ACTIVE copy records is retired anyway",
        ("N1.11",),
        [(KEYRETIRE_N, "\tif len(copies) > 0 {\n\t\treturn &KeyInUseError{KeyID: keyID, Copies: copies}\n\t}",
          "\tif false && len(copies) > 0 { " + PLANT.strip() + "\n\t\treturn &KeyInUseError{KeyID: keyID, Copies: copies}\n\t}")],
        "./internal/placement/",
        ["TestRetiringAKeyAnActivePlacementRecordsIsRefusedNamingTheCopies_992"],
    ),
    Control(
        "a bundle confirmation that is never written down",
        ("N1.12", "NC-12"),
        [(KC_BUNDLE_N, "\treturn s.registry.Confirm(ctx, Confirmation{KeyID: id, Digest: digest, ConfirmedAt: now.UTC()})",
          "\treturn nil " + PLANT.strip())],
        "./internal/keycustody/",
        ["TestEnablingManagedEncryptionWithNoConfirmedBundleIsDurablyDegraded"],
    ),
    Control(
        "a second alert kind for the unconfirmed bundle",
        ("N1.13", "NC-13"),
        [(ALERT_N, "\tEncryptionKeyUnresolvable,\n}",
          "\tEncryptionKeyUnresolvable,\n\tKind(\"ENCRYPTION_BUNDLE_UNCONFIRMED\"), " + PLANT.strip() + "\n}")],
        "./internal/alert/",
        ["TestKindsAreExactlyTheDeliberatelyChosenConditions"],
    ),
    Control(
        "a key identity field in the recovery sidecar",
        ("N1.14",),
        [(MANIFEST_N, "\tKeyID string `json:\"key_id\"`",
          "\tKeyID string `json:\"key_id\"`\n\n\tKeyIdentity string `json:\"key_identity,omitempty\"` " + PLANT.strip())],
        "./internal/recovery/",
        ["TestManifestFieldsExcludeSecrets"],
    ),
    Control(
        "the key id left out of the recovery sidecar",
        ("N1.14", "NC-15"),
        [(MANIFEST_N, "\tKeyID string `json:\"key_id\"`", "\tKeyID string `json:\"-\"` " + PLANT.strip())],
        "./internal/app/",
        ["TestRebuildIdentifiesAnEncryptedCopyAndNamesTheMissingManagedKey_992"],
    ),
    Control(
        "the plaintext digest accepted as the stored-bytes digest",
        ("N1.4",),
        [(STATE_ENC_N, "\tif (localHash != \"\" && e.StoredHash == localHash) || (pHash != \"\" && e.StoredHash == pHash) {",
          "\tif false && ((localHash != \"\" && e.StoredHash == localHash) || (pHash != \"\" && e.StoredHash == pHash)) { " + PLANT.strip())],
        "./internal/state/",
        ["TestRecordPlacementEncryption_RefusesThePlaintextDigest"],
    ),
    Control(
        "the engine records the plaintext digest in the stored-bytes column",
        ("N1.4",),
        [(PLACEMENT_ENC_N, "\t\t\tStoredHash: hex.EncodeToString(stored.h.Sum(nil)), StoredHashAlg: string(transport.SHA256), StoredSize: stored.n,",
          "\t\t\tStoredHash: func() string { _ = hex.EncodeToString(stored.h.Sum(nil)); return p.Hash }(), StoredHashAlg: string(transport.SHA256), StoredSize: stored.n, " + PLANT.strip())],
        "./internal/placement/",
        ["TestBothDigestColumnsAreWrittenInTheSameAdvanceAndNeverConfused"],
    ),
    Control(
        "local_hash overwritten with the stored-bytes digest on an encrypt",
        ("NC-07",),
        [(STATE_ENC_N, "\tif size.Valid && e.StoredSize <= size.Int64 {",
          "\t" + PLANT + "local_hash takes the ciphertext digest.\n\tif _, err := tx.ExecContext(ctx, `UPDATE artifacts SET local_hash = ? WHERE id = (SELECT artifact_id FROM placements WHERE id = ?)`, e.StoredHash, id); err != nil {\n\t\treturn err\n\t}\n\tif size.Valid && e.StoredSize <= size.Int64 {")],
        "./internal/state/",
        ["TestRecordPlacementEncryption_RefusesThePlaintextDigest"],
    ),
    Control(
        "an encrypted copy whose key does not resolve passes at existence",
        ("N1.5", "NC-08"),
        [(PLACEMENT_ENC_N, "\tcase !resolved:\n\t\treturn Result{}, fmt.Errorf(\"%w: %w: key %s cannot be resolved: %w\", ErrClassUnavailable, ErrKeyUnresolvable, keyID, err)",
          "\tcase !resolved:\n\t\treturn Result{Class: Existence, Passed: true, At: now, Detail: \"planted\"}, nil " + PLANT.strip())],
        "./internal/placement/",
        ["TestContentOfAnEncryptedCopyWithoutItsKeyIsUnavailableNotWeaker_992"],
    ),
    Control(
        "a ciphertext checksum accepted as proof for an encrypted copy",
        ("N1.5", "NC-08"),
        [(PLACEMENT_ENC_N, "\tdefault:\n\t\treturn Result{}, fmt.Errorf(\"%w: %s verification cannot show that an encrypted copy decrypts to the artifact\", ErrClassUnavailable, want)",
          "\tdefault:\n\t\treturn Verify(ctx, store, medium, p, want, now) " + PLANT.strip())],
        "./internal/placement/",
        ["TestAttestedIsUnavailableForAnEncryptedCopy_992"],
    ),
    Control(
        "the key reference reads back with its arguments",
        ("NC-18",),
        [(SECRETREF_N, "\tcase len(r.Command) > 0:\n\t\treturn \"command \" + r.Command[0]\n\tdefault:\n\t\treturn \"no source\"",
          "\tcase len(r.Command) > 0:\n\t\treturn \"command \" + strings.Join(r.Command, \" \") " + PLANT.strip() + "\n\tdefault:\n\t\treturn \"no source\"")],
        "./service/",
        ["TestAKeyReferenceReadsBackAsItsDescriptionAndCannotRedeclareAKey_993"],
    ),
]

COPYCRYPT_N = "core/internal/copycrypt/copycrypt.go"
REVAL_N = "core/internal/revalidate/checks.go"
MEDIUM_N = "core/internal/transport/medium.go"
OBS_EVENTS_N = "core/internal/obs/events.go"
OPRECORD_N = "core/service/assuranceoperation_record.go"
CLIECHO_N = "core/cliecho/routes.go"
CONTRACT_GEN_N = "core/apicontract/contract.gen.go"

CONTROLS += [
    Control(
        "a failed decrypt hands back the plaintext bytes it read",
        ("N1.6",),
        [(COPYCRYPT_N, "\tn, err := io.Copy(out, r)\n\tif err != nil {\n\t\treturn 0, classify(err, out)",
          "\tn, err := io.Copy(out, r)\n\tif err != nil {\n\t\treturn n, classify(err, out) " + PLANT.strip())],
        "./internal/copycrypt/",
        ["TestEveryNegativeFailsClosedAndWritesNoPartialPlaintext"],
    ),
    Control(
        "a failed decrypt to a file leaves its temporary plaintext",
        ("N1.6",),
        [(COPYCRYPT_N, "\tif _, err := decrypt(ctx, src, suite, identity, f, wantPlaintextHash, nil); err != nil {\n\t\tf.Close()\n\t\tos.Remove(f.Name())",
          "\tif _, err := decrypt(ctx, src, suite, identity, f, wantPlaintextHash, nil); err != nil {\n\t\tf.Close() " + PLANT.strip())],
        "./internal/copycrypt/",
        ["TestEveryNegativeFailsClosedAndWritesNoPartialPlaintext"],
    ),
    Control(
        "the decrypt pass reads the whole artifact into memory",
        ("N1.7", "NC-11"),
        [(COPYCRYPT_N, "\tn, err := io.Copy(out, r)\n\tif err != nil {\n\t\treturn 0, classify(err, out)",
          "\t" + PLANT + "the whole plaintext is buffered.\n\tbuf, err := io.ReadAll(r)\n\tvar n int64\n\tif err == nil {\n\t\tvar m int\n\t\tm, err = out.Write(buf)\n\t\tn = int64(m)\n\t}\n\tif err != nil {\n\t\treturn 0, classify(err, out)")],
        "./internal/copycrypt/",
        ["TestAMultiGiBObjectEncryptsAndDecryptVerifiesInsideAnAllocationBudget"],
        timeout="30m",
    ),
    Control(
        "an interrupted encrypt is published as a finished object",
        ("N1.8", "NC-06"),
        [(COPYCRYPT_N, "\tn, err := io.Copy(io.MultiWriter(aw, plain), in)\n\tif err != nil {\n\t\treturn fail(err)\n\t}",
          "\tn, err := io.Copy(io.MultiWriter(aw, plain), in)\n\tif err != nil {\n\t\t" + PLANT + "what was written so far is published.\n\t\t_ = aw.Close()\n\t\tpath, ferr := finish(f, dir, name)\n\t\treturn Staged{Path: path, StoredHash: hex.EncodeToString(stored.Sum(nil)), SuiteID: suite.ID(), Version: suite.Version()}, ferr\n\t}")],
        "./internal/copycrypt/",
        ["TestAnInterruptedEncryptCannotLeaveAGoodLookingObject"],
    ),
    Control(
        "an interrupted encrypt reaches the medium as a good copy",
        ("N1.8", "NC-06"),
        [(COPYCRYPT_N, "\tn, err := io.Copy(io.MultiWriter(aw, plain), in)\n\tif err != nil {\n\t\treturn fail(err)\n\t}",
          "\tn, err := io.Copy(io.MultiWriter(aw, plain), in)\n\tif err != nil {\n\t\t" + PLANT + "what was written so far is published.\n\t\t_ = aw.Close()\n\t\tpath, ferr := finish(f, dir, name)\n\t\treturn Staged{Path: path, StoredHash: hex.EncodeToString(stored.Sum(nil)), SuiteID: suite.ID(), Version: suite.Version()}, ferr\n\t}")],
        "./internal/placement/",
        ["TestAnInterruptedEncryptCannotBecomeACataloguedGoodCopy"],
    ),
    Control(
        "the automatic revalidation ceiling raised to content",
        ("N1.9", "NC-09"),
        [(REVAL_N, "const automaticMediumClass = placement.Existence", "const automaticMediumClass = placement.Content " + PLANT.strip())],
        "./internal/revalidate/",
        ["TestTheAutomaticCeilingIsAClassThisPassMayRunAgainstEveryMedium", "TestTheAutomaticCeilingIsStillExistenceAndAnUnattendedPassNeverDecryptsAnEncryptedCopy_992", "TestRevalidationOfAMediumPlacementIsExistenceAndSaysSo"],
    ),
    Control(
        "the object key of a copy gains an encrypted suffix",
        ("N1.15", "NC-10"),
        [(MEDIUM_N, "\tsegments = append(segments, artifact.Set.Source, artifact.Set.Set, artifact.Name)",
          "\tsegments = append(segments, artifact.Set.Source, artifact.Set.Set, artifact.Name+\".age\") " + PLANT.strip())],
        "./internal/transport/",
        ["TestMediumKeyLayout/with_a_prefix"],
    ),
    Control(
        "the key retired event renamed",
        ("N1.19",),
        [(OBS_EVENTS_N, "\tEventKeyRetired = \"key_retired\"", "\tEventKeyRetired = \"encryption_key_retired\" " + PLANT.strip())],
        "./internal/obs/",
        ["TestEventNamesAreStable"],
    ),
    Control(
        "a cancelled pass event read back as passed",
        ("N1.20",),
        [(OPRECORD_N, "\t\tcase fieldValue(pass.Fields, \"cancelled\") == \"true\":\n\t\t\trec.State = AssuranceOpCancelled",
          "\t\tcase false && fieldValue(pass.Fields, \"cancelled\") == \"true\": " + PLANT.strip() + "\n\t\t\trec.State = AssuranceOpCancelled")],
        "./service/",
        ["TestACancelFromEitherSurfaceLeavesTheSameDurableState_993"],
    ),
    Control(
        "the key retire route dropped from the CLI echo table",
        ("N1.16",),
        [(CLIECHO_N, "\tkey(\"POST\", \"/encryption/keys/{id}/retire\"): {\n\t\tbuild: func(a Action) *cmd {\n\t\t\treturn newCmd(\"encryption\", \"key\", \"retire\", a.Params[\"id\"])\n\t\t},\n\t\texamples: []Action{{Params: map[string]string{\"id\": \"k0123456789abcdef\"}}},\n\t},\n",
          "\t" + PLANT + "the retire route has no entry.\n")],
        "./cliecho/",
        ["TestTheEncryptionRoutesNameTheirCommands_993/retiring", "TestEveryRouteOutcomeIsInTheGolden"],
    ),
    Control(
        "the key reference type accepts whatever JSON it is handed",
        ("N1.17", "NC-18"),
        [(CONTRACT_GEN_N, "type EncryptionKeyReference struct {\n\tCommand []string `json:\"command,omitempty\"`\n\tEnv     string   `json:\"env,omitempty\"`\n\tFile    string   `json:\"file,omitempty\"`\n}\n",
          "type EncryptionKeyReference struct {\n\tCommand []string `json:\"command,omitempty\"`\n\tEnv     string   `json:\"env,omitempty\"`\n\tFile    string   `json:\"file,omitempty\"`\n}\n\n" + PLANT + "a read path into the reference.\nfunc (r *EncryptionKeyReference) UnmarshalJSON([]byte) error { return nil }\n")],
        "./service/",
        ["TestAKeyReferenceReadsBackAsItsDescriptionAndCannotRedeclareAKey_993"],
    ),
    Control(
        "key_encryption extended with a retained-copy field",
        ("NC-05", "NV.4"),
        [(CONFIG_GO_N, "\tCommand []string `yaml:\"command\"`\n}\n\n// isZero reports whether none of KeyEncryption's three sources are set:",
          "\tCommand []string `yaml:\"command\"`\n\n\tRetainedCopies bool `yaml:\"retained_copies,omitempty\"` " + PLANT.strip() + "\n}\n\n// isZero reports whether none of KeyEncryption's three sources are set:")],
        "./internal/config/",
        ["TestKeyEncryptionIsNeitherExtendedNorMoved_1331"],
    ),
]

