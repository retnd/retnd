// @vitest-environment node
// A production build must not carry the mock API or its fixtures (#1216).
//
// createApp used to import @shared/api/mock statically, so several thousand
// lines of fixtures landed in every shipped bundle even though only the dev
// server and the browser specs ever run them. This builds the real
// production bundle with Vite's own API (into a temp directory, so the
// checkout's dist/ is left alone) and greps every emitted file for strings
// that exist only in the fixtures. When a new fixture file lands, add one of
// its strings to FIXTURE_MARKERS so it is held to the same rule.
import { describe, it, expect, beforeAll } from "vitest";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readdirSync, readFileSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { findFixtureMarkers } from "./bundle-fixtures.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

function walk(dir) {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}

describe("production bundle", () => {
  let files = [];
  beforeAll(async () => {
    const outDir = mkdtempSync(resolve(tmpdir(), "retnd-bundle-"));
    // A subprocess, not vite's JS API: vitest runs with NODE_ENV=test, which
    // would make import.meta.env.DEV true and build the development composition.
    const r = spawnSync(process.execPath, [resolve(root, "node_modules/vite/bin/vite.js"), "build", "--outDir", outDir, "--emptyOutDir"], {
      cwd: root,
      encoding: "utf8",
      env: { ...process.env, NODE_ENV: "production" }
    });
    if (r.status !== 0) throw new Error("vite build failed:\n" + r.stdout + r.stderr);
    files = walk(outDir).filter((f) => /\.(js|css|html)$/.test(f));
  }, 280_000);

  it("emits an app", () => {
    expect(files.some((f) => f.endsWith("index.html"))).toBe(true);
  });

  it("contains no mock fixture strings in any emitted file", () => {
    const hits = files.flatMap((f) => findFixtureMarkers(readFileSync(f, "utf8")).map((m) => `${f.split("/").pop()}: ${m}`));
    expect(hits).toEqual([]);
  });
});
