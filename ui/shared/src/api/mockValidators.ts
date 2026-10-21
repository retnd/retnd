/**
 * The in-process stand-in for the custom recovery-test validator routes
 * (issue #1203), over the same fixture backups the rest of the mock serves.
 *
 * It is not a second implementation of the engine, and it keeps only the rules
 * the specs drive a screen through: the limit, the 409s, a pattern the engine
 * refuses (a lookahead, in Go's words), the collision between two patterns at
 * one scope, and the resolution of one backup. Name and script rules are not
 * here: the dialog refuses those before anything is sent, and the engine's own
 * words for them are pinned in customValidators.1203review.test.ts. The pattern
 * is judged by a browser RegExp here, which is why the real screen never does
 * that itself: it asks the preview route.
 */
import type {
  CustomValidator,
  CustomValidatorList,
  CustomValidatorPatterns,
  CustomValidatorPreview,
  RetndApi,
  ValidatorScriptState,
  ValidatorScript
} from "./contracts";
import { RetndError } from "./contracts";

export interface MockValidatorBackup {
  id: string;
  setId: string;
  filename: string;
}

export interface MockValidatorsEnv {
  /** Which scenario seeds the list and decides what is allowed. */
  scenario: string;
  backups(): MockValidatorBackup[];
  /** What the gate and the password check say to an upload or an edit, or
   *  null to let it through. */
  writeRefusal(password: string | undefined): Promise<never> | null;
  delay<T>(value: T): Promise<T>;
}

const MAX_VALIDATORS = 64;
const MAX_SCRIPT_BYTES = 262144;
const MAX_PATTERN_BYTES = 512;

export const MOCK_DEFAULT_SCRIPT = `#!/usr/bin/env bash
# A recovery-test validator, as retnd runs it.
#
# retnd restores one copy of a backup into a scratch directory, hands you the
# path to it and waits. Exit 0 says the copy is good; any other exit status
# says it is not, and what you print is kept as the test's output.
#
# What you are given:
#   RETND_ARTIFACT   the file name of the backup being checked
#   RETND_RECOVERED  the path of the restored copy
#
# Replace the body below with the check this kind of file deserves: a
# manifest wants its lines counted, a database dump wants a trial restore.
set -euo pipefail

file="\${RETND_RECOVERED:?no recovered copy was handed over}"

if [ ! -s "$file" ]; then
  echo "the recovered copy of \${RETND_ARTIFACT:-?} is empty" >&2
  exit 1
fi

echo "ok: \${RETND_ARTIFACT:-the copy} is $(wc -c <"$file") bytes"
`;

const te = new TextEncoder();
const byteLength = (s: string): number => te.encode(s).length;

async function sha256Hex(text: string): Promise<string> {
  const d = await crypto.subtle.digest("SHA-256", te.encode(text));
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function refusal(code: string, message: string): RetndError {
  return new RetndError({ code: code as never, message, correlationId: "cid_mockvalidator" });
}

/** The engine's own words when it cannot parse a pattern (Go's regexp). */
function parseProblem(pattern: string): string | null {
  if (pattern === "") return "a validator needs a pattern to match artifact names against";
  const perl = /\(\?(<=|<!|=|!)/.exec(pattern);
  if (perl) return "error parsing regexp: invalid or unsupported Perl syntax: `" + perl[0] + "`";
  try {
    new RegExp(pattern);
  } catch (e) {
    return "error parsing regexp: " + (e instanceof Error ? e.message.replace(/^Invalid regular expression: /, "") : String(e));
  }
  return null;
}

const isAnchored = (p: string): boolean => p.startsWith("^") && p.endsWith("$") && !p.endsWith("\\$");

const escapeLiteral = (s: string): string => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** Every run of digits generalised, everything else literal, anchored. */
function derive(name: string): string {
  let out = "";
  for (const part of name.split(/(\d+)/)) out += /^\d+$/.test(part) ? "\\d{" + part.length + "}" : escapeLiteral(part);
  return "^" + out + "$";
}

function seed(scenario: string): CustomValidator[] {
  if (scenario === "validators-none" || scenario === "validators-unavailable" || scenario === "validators-workflows-off") return [];
  if (scenario === "validators-default-root") return [];
  if (scenario === "validators-full") {
    return Array.from({ length: MAX_VALIDATORS }, (_, i) => {
      const n = String(i + 1).padStart(2, "0");
      return mk("seed-" + n, "^seed-" + n + "-x$", "");
    });
  }
  // #1230: the script states. "validators-states" holds one validator in each
  // state; "validators-state-unpinned", "-edited" and "-missing" put
  // billing-dump itself in that state, so a backup it covers can show it.
  const single = (
    { "validators-state-unpinned": "unpinned", "validators-state-edited": "edited_on_disk", "validators-state-missing": "missing" } as Record<
      string,
      ValidatorScriptState
    >
  )[scenario];
  const billing: CustomValidator = {
    ...mk("billing-dump", String.raw`^billing-\d{8}\.sql\.gz$`, "production/billing-mysql"),
    lastResult: { artifactId: "production/billing-mysql/billing-20260827.sql.gz", at: "2026-08-30T04:12:00+02:00", state: "passed" }
  };
  if (scenario === "validators-states") {
    return [
      billing,
      inState(mk("any-tar", String.raw`\.tar\.zst$`, ""), "unpinned"),
      inState(mk("manifest-check", String.raw`^manifest-\d{8}\.json$`, ""), "edited_on_disk"),
      inState(mk("restore-log", String.raw`^restore-\d{8}\.log$`, ""), "missing")
    ];
  }
  // #1233: billing-dump has opted in to the workflow environment.
  if (scenario === "validators-env-workflow") {
    return [{ ...billing, env: "workflow" }, mk("any-tar", String.raw`\.tar\.zst$`, "")];
  }
  return [
    single ? inState(billing, single) : billing,
    mk("any-tar", String.raw`\.tar\.zst$`, "")
  ];
}

/** The text a script holds after somebody edited it on the host. */
const EDITED_TAIL = "# edited on the host after it was uploaded\n";

/** A validator moved into one script state, the way the engine reports it:
 *  edited_on_disk keeps the pin and has the file hash to something else,
 *  missing has no file, unpinned has no pin. */
function inState(v: CustomValidator, state: ValidatorScriptState): CustomValidator {
  if (state === "unpinned") return { ...v, pinnedSha256: "", scriptState: "unpinned" };
  if (state === "edited_on_disk") {
    const edited = scriptFor(v.name) + EDITED_TAIL;
    return { ...v, scriptState: "edited_on_disk", scriptSha256: fakeDigest(edited), scriptSizeBytes: byteLength(edited) };
  }
  if (state === "missing") return { ...v, scriptState: "missing", scriptPresent: false, scriptSha256: "", scriptSizeBytes: 0, uploadedAt: "" };
  return v;
}

function mk(name: string, pattern: string, backupSetId: string): CustomValidator {
  const script = scriptFor(name);
  return {
    name, pattern, backupSetId, scope: backupSetId ? "backup_set" : "global", anchored: isAnchored(pattern), env: "minimal",
    scriptFile: name + ".local.sh", scriptPresent: true,
    // A fixed digest per name keeps the fixture deterministic; real ones
    // are computed on upload below.
    scriptSha256: fakeDigest(script), pinnedSha256: fakeDigest(script), scriptState: "pinned", scriptSizeBytes: byteLength(script), uploadedAt: "2026-08-29T09:30:00+02:00",
    matchCount: 0, matchSets: [], exampleName: ""
  };
}

const scriptFor = (name: string): string =>
  "#!/usr/bin/env bash\n# " + name + ": checks one kind of artifact.\nset -euo pipefail\n[ -s \"${RETND_RECOVERED:?}\" ]\n";

function fakeDigest(text: string): string {
  let h = 2166136261;
  let out = "";
  for (let round = 0; round < 8; round++) {
    for (let i = 0; i < text.length; i++) h = Math.imul(h ^ (text.charCodeAt(i) + round), 16777619) >>> 0;
    out += h.toString(16).padStart(8, "0");
  }
  return out;
}

export function createMockValidators(env: MockValidatorsEnv): Pick<
  RetndApi,
  | "listCustomValidators"
  | "listCustomValidatorPatterns"
  | "previewCustomValidator"
  | "createCustomValidator"
  | "updateCustomValidator"
  | "removeCustomValidator"
  | "getCustomValidatorScript"
  | "getDefaultValidatorScript"
> {
  // #1246: "validators-workflows-off" is a deployment whose operator cleared
  // the root (workflows.disabled), and "validators-default-root" one whose
  // config.yaml names no root and runs on the mounted /workflows.
  const turnedOff = env.scenario === "validators-workflows-off";
  const unavailable = env.scenario === "validators-unavailable" || turnedOff;
  const defaultRoot = env.scenario === "validators-default-root";
  const root = defaultRoot ? "/workflows" : "/var/lib/retnd/workflows";
  let validators = seed(env.scenario);
  const scripts = new Map<string, string>(
    validators
      .filter((v) => v.scriptPresent)
      .map((v) => [v.name, v.scriptState === "edited_on_disk" ? scriptFor(v.name) + EDITED_TAIL : scriptFor(v.name)])
  );

  /** The backups a scope can see: one set's, or every set's. */
  const inScope = (setId: string): MockValidatorBackup[] =>
    env.backups().filter((b) => setId === "" || b.setId === setId);
  const matching = (pattern: string, setId: string): MockValidatorBackup[] => {
    if (parseProblem(pattern) !== null) return [];
    const re = new RegExp(pattern);
    return inScope(setId).filter((b) => re.test(b.filename));
  };
  const setsOf = (bs: MockValidatorBackup[]): string[] => [...new Set(bs.map((b) => b.setId))].sort();
  const counted = (v: CustomValidator): CustomValidator => {
    const m = matching(v.pattern, v.backupSetId);
    return { ...v, matchCount: m.length, matchSets: setsOf(m), exampleName: m[0]?.filename ?? "" };
  };
  const knownSet = (setId: string): boolean => setId === "" || env.backups().some((b) => b.setId === setId);

  const conflictsOf = (pattern: string, setId: string, replacing: string) => {
    const mine = matching(pattern, setId);
    const out: Array<{ validator: string; example: string }> = [];
    for (const v of validators) {
      if (v.name === replacing || v.backupSetId !== setId) continue;
      const re = new RegExp(v.pattern);
      const hit = mine.find((b) => re.test(b.filename));
      if (hit) out.push({ validator: v.name, example: hit.filename });
    }
    return out;
  };

  const notThere = (name: string) => refusal("VALIDATOR_NOT_FOUND", "there is no custom validator named " + name);
  const noRoom = () =>
    refusal("VALIDATORS_UNAVAILABLE", "this deployment cannot keep custom validators: no workflow root is configured");

  return {
    listCustomValidators: (filter) => {
      const list: CustomValidatorList = {
        available: !unavailable,
        unavailableReason: turnedOff
          ? "workflows are turned off for this deployment (workflows.disabled), so there is nowhere to keep a validator script; set a workflow root under Settings > Workflow to turn them back on"
          : unavailable ? "no workflow root is configured, so there is nowhere to keep a validator script" : "",
        runnerConfigured: !unavailable,
        directory: unavailable ? "" : root + "/recovery-test-validators",
        workflowRoot: unavailable ? "" : root,
        workflowRootDefault: defaultRoot,
        scriptsDir: "",
        maxValidators: MAX_VALIDATORS,
        maxScriptBytes: MAX_SCRIPT_BYTES,
        maxPatternBytes: MAX_PATTERN_BYTES,
        // The mock refuses a collision when a validator is added, so none can arise.
        directoryRefused: "",
        ambiguities: [],
        validators: validators
          .filter((v) => !filter?.backupSetId || v.backupSetId === "" || v.backupSetId === filter.backupSetId)
          .sort((a, b) => Number(b.backupSetId !== "") - Number(a.backupSetId !== "") || a.name.localeCompare(b.name))
          .map(counted)
      };
      if (filter?.artifactId) {
        const b = env.backups().find((x) => x.id === filter.artifactId);
        if (!b) return Promise.reject(refusal("ARTIFACT_NOT_FOUND", "no backup with id " + filter.artifactId));
        const hits = (scope: string) => validators.filter((v) => v.backupSetId === scope && new RegExp(v.pattern).test(b.filename));
        const set = hits(b.setId);
        const global = hits("");
        const tier = set.length > 0 ? set : global;
        list.resolution = {
          artifactId: b.id,
          validator: tier.length === 1 ? tier[0].name : "",
          fallback: tier.length === 0 ? "builtin" : "",
          ambiguous: tier.length > 1 ? tier.map((v) => v.name) : []
        };
      }
      return env.delay(list);
    },

    listCustomValidatorPatterns: (backupSetId = "") => {
      if (!knownSet(backupSetId)) return Promise.reject(refusal("BACKUP_SET_NOT_FOUND", "there is no backup set " + backupSetId));
      const groups = new Map<string, MockValidatorBackup[]>();
      for (const b of inScope(backupSetId)) {
        const p = derive(b.filename);
        groups.set(p, [...(groups.get(p) ?? []), b]);
      }
      const out: CustomValidatorPatterns = {
        backupSetId,
        patterns: [...groups.entries()]
          .map(([pattern, bs]) => ({
            pattern, example: bs[0].filename, matchCount: bs.length, matchSets: setsOf(bs),
            validator: validators.find((v) => v.pattern === pattern && v.backupSetId === backupSetId)?.name ?? ""
          }))
          .sort((a, b) => b.matchCount - a.matchCount || a.pattern.localeCompare(b.pattern))
      };
      return env.delay(out);
    },

    previewCustomValidator: (req, opts) => {
      if (opts?.signal?.aborted) return Promise.reject(new DOMException("aborted", "AbortError"));
      const setId = req.backupSetId ?? "";
      if (!knownSet(setId)) return Promise.reject(refusal("BACKUP_SET_NOT_FOUND", "there is no backup set " + setId));
      const problem = parseProblem(req.pattern);
      const p: CustomValidatorPreview =
        problem !== null
          ? { valid: false, error: problem, anchored: false, matchCount: 0, matchSets: [], sample: [], conflicts: [] }
          : (() => {
              const m = matching(req.pattern, setId);
              return {
                valid: true, error: "", anchored: isAnchored(req.pattern), matchCount: m.length, matchSets: setsOf(m),
                sample: m.slice(0, 5).map((b) => b.filename), conflicts: conflictsOf(req.pattern, setId, req.replacing ?? "")
              };
            })();
      return env.delay(p);
    },

    createCustomValidator: async (req, opts) => {
      const denied = env.writeRefusal(opts?.stepUpPassword);
      if (denied) return denied;
      if (unavailable) throw noRoom();
      const bad = parseProblem(req.pattern);
      if (bad) throw refusal("INVALID_REQUEST", bad);
      const setId = req.backupSetId ?? "";
      if (!knownSet(setId)) throw refusal("BACKUP_SET_NOT_FOUND", "there is no backup set " + setId);
      if (validators.some((v) => v.name === req.name)) throw refusal("VALIDATOR_EXISTS", "there is already a validator named " + req.name);
      if (validators.length >= MAX_VALIDATORS) throw refusal("VALIDATOR_LIMIT_REACHED", "a deployment keeps at most " + MAX_VALIDATORS + " custom validators");
      const clash = conflictsOf(req.pattern, setId, "")[0];
      if (clash) {
        throw refusal(
          "VALIDATOR_PATTERN_CONFLICT",
          clash.validator + " already matches " + clash.example + " at this scope, so two validators would match one backup"
        );
      }
      const v: CustomValidator = {
        ...mk(req.name, req.pattern, setId), env: req.env ?? "minimal",
        scriptSha256: await sha256Hex(req.script), pinnedSha256: await sha256Hex(req.script),
        scriptSizeBytes: byteLength(req.script), uploadedAt: new Date().toISOString()
      };
      validators = [...validators, v];
      scripts.set(v.name, req.script);
      return env.delay(counted(v));
    },

    updateCustomValidator: async (name, req, opts) => {
      const denied = env.writeRefusal(opts?.stepUpPassword);
      if (denied) return denied;
      if (unavailable) throw noRoom();
      const current = validators.find((v) => v.name === name);
      if (!current) throw notThere(name);
      const bad = parseProblem(req.pattern);
      if (bad) throw refusal("INVALID_REQUEST", bad);
      // A replacement: the script is sent or kept by hash, one of the two.
      if ((req.script === undefined) === (req.keepScriptSha256 === undefined)) {
        throw refusal("INVALID_REQUEST", "send either script or script_sha256, one of the two");
      }
      // Keeping a script that was edited on disk would approve code nobody has
      // read, and a missing one has nothing to keep (#1222).
      if (req.keepScriptSha256 !== undefined && (current.scriptState === "edited_on_disk" || !current.scriptPresent)) {
        throw refusal(
          "VALIDATOR_SCRIPT_CHANGED",
          current.scriptPresent
            ? "the script was edited on disk since it was saved, so it cannot be kept; send the script to approve it"
            : "the script file is missing, so there is nothing to keep; send the script"
        );
      }
      if (req.keepScriptSha256 !== undefined && req.keepScriptSha256 !== current.scriptSha256) {
        throw refusal("VALIDATOR_SCRIPT_CHANGED", "the stored script is not the one with that hash any more");
      }
      const setId = req.backupSetId ?? "";
      if (!knownSet(setId)) throw refusal("BACKUP_SET_NOT_FOUND", "there is no backup set " + setId);
      const clash = conflictsOf(req.pattern, setId, name)[0];
      if (clash) {
        throw refusal(
          "VALIDATOR_PATTERN_CONFLICT",
          clash.validator + " already matches " + clash.example + " at this scope, so two validators would match one backup"
        );
      }
      const next: CustomValidator = {
        ...current, pattern: req.pattern, backupSetId: setId, scope: setId ? "backup_set" : "global", anchored: isAnchored(req.pattern),
        // A replacement: env left out is minimal, never what it had.
        env: req.env ?? "minimal",
        // Whatever is saved is pinned: a kept script is pinned at the hash it has.
        ...(req.script !== undefined
          ? {
              scriptSha256: await sha256Hex(req.script), pinnedSha256: await sha256Hex(req.script), scriptPresent: true,
              scriptState: "pinned" as const, scriptSizeBytes: byteLength(req.script), uploadedAt: new Date().toISOString()
            }
          : { pinnedSha256: current.scriptSha256, scriptState: "pinned" as const })
      };
      if (req.script !== undefined) scripts.set(name, req.script);
      validators = validators.map((v) => (v.name === name ? next : v));
      return env.delay(counted(next));
    },

    removeCustomValidator: (name) => {
      const current = validators.find((v) => v.name === name);
      if (!current) return Promise.reject(notThere(name));
      validators = validators.filter((v) => v.name !== name);
      scripts.delete(name);
      return env.delay(counted(current));
    },

    getCustomValidatorScript: async (name) => {
      const v = validators.find((x) => x.name === name);
      const content = scripts.get(name);
      if (!v) throw notThere(name);
      if (content === undefined) throw refusal("VALIDATOR_NOT_FOUND", "the script file " + v.scriptFile + " is not there");
      const s: ValidatorScript = { fileName: v.scriptFile, sha256: await sha256Hex(content), sizeBytes: byteLength(content), content };
      return env.delay(s);
    },

    getDefaultValidatorScript: async () =>
      env.delay({
        fileName: "default-validator.local.sh", sha256: await sha256Hex(MOCK_DEFAULT_SCRIPT),
        sizeBytes: byteLength(MOCK_DEFAULT_SCRIPT), content: MOCK_DEFAULT_SCRIPT
      })
  };
}
