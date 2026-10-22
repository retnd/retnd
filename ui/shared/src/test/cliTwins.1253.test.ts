/**
 * Every `retnd` line the browser's own twin modules print, written to a
 * fixture the CLI parses (issue #1253).
 *
 * core/cliecho's lines are fed through the real dispatcher by
 * core/cmd/retnd's TestEveryEchoedCommandParses, so a flag renamed in the
 * CLI breaks there. The four modules below print the same kind of line
 * BEFORE a request is made, beside the control, and they were only ever
 * compared with strings written by hand in their own tests. A string
 * compared with a string agrees with whoever wrote both, which is how the
 * destination wizard printed `medium add --backend` for months: the test
 * said that was the line, and the CLI said there was no such flag.
 *
 * So this writes every line those modules produce, for a fixed set of
 * inputs that reaches each branch, to core/cmd/retnd/testdata/
 * ui_twins_1253.json, and core/cmd/retnd's TestEveryUITwinParses1253 runs
 * each one through the same dispatcher. A change on this side that the
 * fixture hasn't caught up with fails here; a fixture line the CLI refuses
 * fails there.
 *
 * Re-pin, from ui/shared, after reading what changed:
 *
 *   npx vitest run -u src/test/cliTwins.1253.test.ts
 *
 * Outside CI vitest writes a MISSING fixture on its own, so a deleted file
 * comes back green locally; under CI=true it is a failure, which is the
 * run that counts.
 */
import { describe, expect, it } from "vitest";
import type { BackendManifest } from "@shared/api/contracts";
import * as assurance from "@shared/assurance/assuranceCommands";
import * as snapshot from "@shared/pages/snapshotCommands";
import * as storage from "@shared/pages/storageDestinationCommands";
import * as configure from "@shared/pages/destinationConfigureCommands";

const FIXTURE = "../../../../core/cmd/retnd/testdata/ui_twins_1253.json";
const REPIN = "npx vitest run -u src/test/cliTwins.1253.test.ts (from ui/shared)";

const SET = "api-server/var-backups";
const ARTIFACT = "api-server/var-backups/dump-2026-10-01.tar.gz";

const S3: BackendManifest = {
  id: "s3",
  label: "S3 or S3-compatible",
  summary: "a bucket",
  role: "object_store",
  configurable: true,
  fields: [
    { id: "credentials", label: "Access key", kind: "credential", required: true },
    { id: "bucket", label: "Bucket", kind: "string", required: true },
    { id: "region", label: "Region", kind: "string", required: false },
    { id: "endpoint", label: "Endpoint", kind: "url", required: false },
    { id: "prefix", label: "Key namespace", kind: "key_prefix", required: false },
    { id: "storage_class", label: "Storage class", kind: "enum", required: false }
  ],
  probe: { steps: [] }
};

const LOCAL_VOLUME: BackendManifest = {
  id: "local_volume",
  label: "Local volume",
  summary: "a directory on this machine",
  role: "local_volume",
  configurable: true,
  fields: [
    { id: "path", label: "Directory", kind: "path", required: true },
    { id: "prefix", label: "Subdirectory", kind: "key_prefix", required: false },
    { id: "upload_verification", label: "How a copy is proven", kind: "enum", required: false }
  ],
  probe: { steps: [] }
};

const S3_SPEC = {
  id: "offsite_s3",
  type: "s3",
  region: "eu-central-1",
  endpoint: "https://s3.example.test",
  bucket: "acme backups",
  prefix: "retnd/",
  storageClass: "STANDARD_IA",
  uploadVerification: "content_md5"
};

/** One line, and which builder printed it for which input. */
interface Twin {
  module: string;
  fn: string;
  case: string;
  line: string;
}

type Case = [fn: string, name: string, line: () => string];

/**
 * The inputs, by module. A builder with branches gets a case per branch
 * (each recovery target, each narrowing, each credential spelling), for
 * the reason core/cliecho's examples do: a shape nobody exercises is a
 * shape nobody has checked.
 */
const CASES: Record<string, Case[]> = {
  assuranceCommands: [
    ["scrubCommand", "a set", () => assurance.scrubCommand(SET)],
    ["scrubContentCommand", "a set", () => assurance.scrubContentCommand(SET)],
    ["recoveryTestCommand", "a set", () => assurance.recoveryTestCommand(SET)],
    ["recoveryTestArtifactCommand", "one backup", () => assurance.recoveryTestArtifactCommand(SET, ARTIFACT)],
    ["recoveryTestFollowCommand", "a set", () => assurance.recoveryTestFollowCommand(SET)],
    ["retryCommand", "one backup", () => assurance.retryCommand(ARTIFACT)],
    ["artifactCommand", "one backup", () => assurance.artifactCommand(ARTIFACT)],
    ["artifactsInSetCommand", "a set", () => assurance.artifactsInSetCommand(SET)],
    ["setTerminalCommand", "a set", () => assurance.setTerminalCommand(SET)],
    ["assuranceCancelCommand", "an operation", () => assurance.assuranceCancelCommand("op_01JABCDEF")],
    ["assuranceShowCommand", "an operation", () => assurance.assuranceShowCommand("op_01JABCDEF")],
    ["showOutputCommand", "a set", () => assurance.showOutputCommand(SET)],
    ["recoverCommand", "download", () => assurance.recoverCommand(ARTIFACT, "download", "", false)],
    ["recoverCommand", "download, cost acknowledged", () => assurance.recoverCommand(ARTIFACT, "download", "", true)],
    ["recoverCommand", "staging", () => assurance.recoverCommand(ARTIFACT, "staging", "restore/dump", false)],
    ["recoverCommand", "staging, a name that reads as a flag", () => assurance.recoverCommand(ARTIFACT, "staging", "-dump", true)],
    ["recoverCommand", "staging, no name yet", () => assurance.recoverCommand(ARTIFACT, "staging", "", false)],
    ["recoverCommand", "storage", () => assurance.recoverCommand(ARTIFACT, "storage", "offsite_s3", true)],
    ["recoverCommand", "storage, no destination yet", () => assurance.recoverCommand(ARTIFACT, "storage", "", false)],
    ["recoverCommand", "source", () => assurance.recoverCommand(ARTIFACT, "source", "retnd-recovered-1", false)],
    ["recoverCommand", "source, acknowledged", () => assurance.recoverCommand(ARTIFACT, "source", "retnd-recovered-1", true, true)],
    ["recoverToFileCommand", "one backup", () => assurance.recoverToFileCommand(ARTIFACT, "dump-2026-10-01.tar.gz")],
    ["recoverSetCommand", "all, staging", () => assurance.recoverSetCommand(SET, "all", "", "staging", "restore", false)],
    ["recoverSetCommand", "newest, storage", () => assurance.recoverSetCommand(SET, "newest", "", "storage", "offsite_s3", true)],
    ["recoverSetCommand", "since, staging", () => assurance.recoverSetCommand(SET, "since", "2026-10-01", "staging", "restore", false)],
    ["recoverSetCommand", "since, no date yet", () => assurance.recoverSetCommand(SET, "since", "", "staging", "restore", false)],
    ["recoverSetCommand", "all, source", () => assurance.recoverSetCommand(SET, "all", "", "source", "retnd-recovered-1", false, true)],
    ["recoverManyCommand", "staging", () => assurance.recoverManyCommand([ARTIFACT, ARTIFACT + ".2"], "staging", "restore", false)],
    ["recoverManyCommand", "storage", () => assurance.recoverManyCommand([ARTIFACT], "storage", "offsite_s3", true)],
    ["recoverManyCommand", "source", () => assurance.recoverManyCommand([ARTIFACT], "source", "retnd-recovered-1", false, true)],
    ["recoverSetPreflightCommand", "since, storage", () => assurance.recoverSetPreflightCommand(SET, "since", "2026-10-01", "storage", "offsite_s3")],
    ["snapshotRestoreCommand", "a set", () => assurance.snapshotRestoreCommand(SET)],
    ["thawCommand", "one backup", () => assurance.thawCommand(ARTIFACT)],
    ["settingsCommand", "nothing pending", () => assurance.settingsCommand()],
    [
      "settingsCommand",
      "every field",
      () =>
        assurance.settingsCommand({
          scrubCadenceSeconds: 172800,
          scrubStrategy: "content",
          recoveryTestMode: "custom",
          recoveryTestCadenceSeconds: 90,
          recoveryTestValidatorId: "pg-dump-check",
          recoveryTestBuiltinCheck: "structure"
        })
    ],
    ["setAssuranceCommand", "reset", () => assurance.setAssuranceCommand(SET, "reset")],
    ["setAssuranceCommand", "own policy", () => assurance.setAssuranceCommand(SET, { scrubCadenceSeconds: 3600, recoveryTestMode: "builtin" })]
  ],
  snapshotCommands: [
    ["snapshotVerifyCommand", "newest", () => snapshot.snapshotVerifyCommand(SET)],
    ["snapshotVerifyCommand", "one run", () => snapshot.snapshotVerifyCommand(SET, "run_1")],
    ["snapshotHoldCommand", "newest", () => snapshot.snapshotHoldCommand(SET, "kept for the incident review")],
    ["snapshotHoldCommand", "one run", () => snapshot.snapshotHoldCommand(SET, "it's evidence", "run_1")],
    ["snapshotUnholdCommand", "one hold", () => snapshot.snapshotUnholdCommand(SET, "hold_1")],
    ["snapshotRestoreCommand", "the whole tree", () => snapshot.snapshotRestoreCommand({ setId: SET, targetPath: "/srv/restored" })],
    [
      "snapshotRestoreCommand",
      "every option",
      () => snapshot.snapshotRestoreCommand({ setId: SET, targetPath: "/srv/restored here", snapshotId: "k1234", sourcePath: "var/lib/pg", conflict: "skip" })
    ],
    ["retentionApplyCommand", "a set", () => snapshot.retentionApplyCommand(SET)]
  ],
  storageDestinationCommands: [
    ["importCredentialsCommand", "no argument", () => storage.importCredentialsCommand()],
    ["testConnectionCandidateCommand", "by reference", () => storage.testConnectionCandidateCommand({ ...S3_SPEC, credentials: { credentialsId: "cred_1" } })],
    ["addCommand", "a credentials file", () => storage.addCommand({ ...S3_SPEC, credentials: { file: "/etc/retnd/s3.ini" } })],
    ["addCommand", "a credentials variable", () => storage.addCommand({ ...S3_SPEC, credentials: { env: "RETND_S3" } })],
    ["addCommand", "a credentials command", () => storage.addCommand({ ...S3_SPEC, credentials: { command: ["pass", "show", "s3"] } })],
    ["addCommand", "an endpoint with userinfo", () => storage.addCommand({ ...S3_SPEC, endpoint: "https://AKIA:secret@minio.internal:9000" })],
    ["addCommand", "an endpoint the cut can't read", () => storage.addCommand({ ...S3_SPEC, endpoint: "https://u:pa/ss@minio.internal" })],
    ["editCommand", "the bare minimum", () => storage.editCommand({ id: "offsite_s3", type: "s3", bucket: "acme" })],
    ["testConnectionCommand", "a destination", () => storage.testConnectionCommand("offsite_s3")],
    ["setDefaultCommand", "a destination", () => storage.setDefaultCommand("offsite_s3")],
    ["tierMediumCommand", "local", () => storage.tierMediumCommand("daily", "local")],
    ["tierMediumCommand", "elsewhere", () => storage.tierMediumCommand("monthly", "offsite_s3")],
    ["removeCommand", "a destination", () => storage.removeCommand("offsite_s3")],
    ["showCommand", "a destination", () => storage.showCommand("offsite_s3")]
  ],
  destinationConfigureCommands: [
    ["configureCommand", "a local volume", () => configure.configureCommand(LOCAL_VOLUME, "usb_disk", { path: "/mnt/usb disk", prefix: "retnd" })],
    [
      "configureCommand",
      "s3, by reference",
      () =>
        configure.configureCommand(
          S3,
          "cold",
          { bucket: "acme", region: "eu-central-1", endpoint: "https://s3.example.test", storage_class: "DEEP_ARCHIVE", credentials: "never printed" },
          "cred_1"
        )
    ],
    ["configureCommand", "s3, an endpoint the cut can't read", () => configure.configureCommand(S3, "cold", { bucket: "acme", endpoint: "https://u:pa/ss@minio.internal" })],
    ["preflightCommand", "s3", () => configure.preflightCommand(S3, "cold", { bucket: "acme", region: "eu-central-1" }, "cred_1")],
    ["importCredentialsCommand", "no argument", () => configure.importCredentialsCommand()]
  ]
};

/**
 * What each module exports that is not a twin, and why. Everything else a
 * module exports that is a function has to have a case above, so a twin
 * added to one of these files is in the fixture the day it lands rather
 * than the day somebody remembers.
 */
const NOT_TWINS: Record<string, Record<string, string>> = {
  assuranceCommands: {
    shellQuote: "the quoting every twin uses, not a line of its own",
    durationFlag: "one flag's value, checked inside settingsCommand's lines",
    stagingName: "one flag's value, checked inside recoverCommand's lines"
  },
  snapshotCommands: {
    shellQuote: "the quoting every twin uses, not a line of its own"
  },
  storageDestinationCommands: {},
  destinationConfigureCommands: {
    endpointNote: "says how a line differs from its request; prints no command",
    endpointWithoutUserinfo: "the userinfo cut, checked against core/cliecho's vectors in its own suite"
  }
};

const MODULES: Record<string, Record<string, unknown>> = {
  assuranceCommands: assurance,
  snapshotCommands: snapshot,
  storageDestinationCommands: storage,
  destinationConfigureCommands: configure
};

function everyTwin(cases: Record<string, Case[]>): Twin[] {
  const out: Twin[] = [];
  for (const module of Object.keys(cases).sort()) {
    for (const [fn, name, line] of cases[module]) {
      out.push({ module, fn, case: name, line: line() });
    }
  }
  return out;
}

/** The exported functions of a module that have no case and no reason. */
function uncovered(module: string, exports: Record<string, unknown>, cases: Record<string, Case[]>): string[] {
  const covered = new Set((cases[module] ?? []).map(([fn]) => fn));
  const exempt = NOT_TWINS[module] ?? {};
  return Object.keys(exports)
    .filter((name) => typeof exports[name] === "function")
    .filter((name) => !covered.has(name) && !exempt[name])
    .sort();
}

describe("the browser's command twins, as the CLI will read them (#1253)", () => {
  it("has a case for every twin each module exports", () => {
    for (const [module, exports] of Object.entries(MODULES)) {
      expect(uncovered(module, exports, CASES), `${module} exports a twin with no case in cliTwins.1253.test.ts`).toEqual([]);
    }
  });

  it("would notice a twin with no case (the control)", () => {
    const withNew = { ...assurance, brandNewCommand: () => "retnd brand-new" };
    expect(uncovered("assuranceCommands", withNew, CASES)).toEqual(["brandNewCommand"]);
  });

  it("names no exemption for a function that is gone", () => {
    for (const [module, reasons] of Object.entries(NOT_TWINS)) {
      for (const name of Object.keys(reasons)) {
        expect(typeof MODULES[module][name], `${module}.${name} is exempt in NOT_TWINS and no longer exported`).toBe("function");
      }
    }
  });

  it("matches the fixture core/cmd/retnd parses", async () => {
    const fixture = {
      about: "Every line the browser's twin modules print, for the inputs in ui/shared/src/test/cliTwins.1253.test.ts. core/cmd/retnd's TestEveryUITwinParses1253 runs each through the dispatcher. Re-pin with: " + REPIN,
      twins: everyTwin(CASES)
    };
    await expect(JSON.stringify(fixture, null, 2) + "\n", `the twins have changed; re-pin with ${REPIN}`).toMatchFileSnapshot(FIXTURE);
  });
});
