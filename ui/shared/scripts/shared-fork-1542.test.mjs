// @vitest-environment node
import { describe, expect, it } from "vitest";
import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * Issue #1542: every file in this suite has to pass the same way whether it
 * runs alone or shares a vitest fork with other files.
 *
 * The default pool gives each file its own process, so nothing a file leaves
 * behind outside its own modules can reach the next one, and the leak stays
 * invisible. It showed up on MARS, where files shared a fork: two
 * registry-tooltips cases failed because the file before them had opted out
 * of tooltips, and that opt-out was still sitting in storage when
 * registry-tooltips imported tooltipNodes and seeded the node from it.
 *
 * This runs a nested vitest with `poolOptions.forks.singleFork` and a
 * sequencer that pins the order: the file that leaves the opt-out (and
 * more) behind goes first, registry-tooltips second, and a file that checks
 * it started clean goes last. Same fork, same jsdom window, fresh modules
 * per file: exactly the conditions the failure needs, every time, instead
 * of whenever the scheduler happens to pair the files.
 *
 * A subprocess, as bundle-fixtures.test.mjs uses for its build: the nested
 * run needs its own pool and its own config, and starting vitest inside
 * this worker would share both with the run that is asking. It lives here
 * rather than in src/test/ because it needs Node's types, which src/ does
 * not get (see tsconfig.json).
 */

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const ORDER = [
  "src/test/shared-fork-1542-leaves-opt-out.test.tsx",
  "src/test/registry-tooltips.test.tsx",
  "src/test/shared-fork-1542-starts-clean.test.tsx"
];

const RUNNER = `
import { startVitest } from "vitest/node";

const order = JSON.parse(process.env.SHARED_FORK_ORDER);
const rank = (spec) => order.findIndex((file) => spec.moduleId.endsWith("/" + file));

class InOrder {
  sort(specs) { return [...specs].sort((a, b) => rank(a) - rank(b)); }
  shard(specs) { return specs; }
}

const ctx = await startVitest("test", order, {
  watch: false,
  cache: false,
  pool: "forks",
  poolOptions: { forks: { singleFork: true } },
  sequence: { sequencer: InOrder },
  reporters: [{}]
});

const failed = [];
function walk(task, path) {
  const name = path ? path + " > " + task.name : task.name;
  if (task.result?.state === "fail" && (task.type === "test" || task.result.errors?.length)) {
    const why = String(task.result.errors?.[0]?.message ?? "").split("\\n")[0];
    failed.push(name + ": " + why);
  }
  for (const child of task.tasks ?? []) walk(child, name);
}

const files = ctx.state.getFiles();
for (const file of files) walk(file, "");

function count(task) {
  const self = task.type === "test" && task.result?.state ? 1 : 0;
  return self + (task.tasks ?? []).reduce((sum, child) => sum + count(child), 0);
}

const ran = files.map((file) => ({
  file: order.find((name) => file.filepath.endsWith("/" + name)) ?? file.filepath,
  count: count(file)
}));

console.log("SHARED-FORK-RESULT " + JSON.stringify({ ran, failed }));
process.exit(0);
`;

describe("files sharing one vitest fork (#1542)", () => {
  it("each start clean after a file that opted out of tooltips", () => {
    const env = {};
    for (const [key, value] of Object.entries(process.env)) {
      // The nested run is its own vitest, not a worker of this one.
      if (value !== undefined && !key.startsWith("VITEST")) env[key] = value;
    }
    env.SHARED_FORK_ORDER = JSON.stringify(ORDER);

    const run = spawnSync(process.execPath, ["--input-type=module", "-e", RUNNER], {
      cwd: root,
      encoding: "utf8",
      env,
      timeout: 200_000
    });

    const line = (run.stdout ?? "").split("\n").find((text) => text.startsWith("SHARED-FORK-RESULT "));
    if (!line) {
      throw new Error("the nested vitest run reported nothing (status " + run.status + "):\n" + run.stdout + run.stderr);
    }
    const result = JSON.parse(line.slice("SHARED-FORK-RESULT ".length));

    // Every file, in this order, each with tests that actually ran: a
    // filter that matched nothing would otherwise pass with no failures.
    expect(result.ran.map(({ file }) => file)).toEqual(ORDER);
    for (const { file, count } of result.ran) expect(count, file).toBeGreaterThan(0);

    expect(result.failed).toEqual([]);
  }, 240_000);
});
