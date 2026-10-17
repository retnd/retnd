// A production build must not carry the mock API or its fixtures (#1216).
//
// createApp used to import @shared/api/mock statically, so several thousand
// lines of fixtures landed in every shipped bundle even though only the dev
// server and the browser specs ever run them. This builds the real
// production bundle with Vite's own API (into a temp directory, so the
// checkout's dist/ is left alone) and greps every emitted file for strings
// that exist only in the fixtures. The size cap is a second net: the
// fixtures were roughly a third of the bundle, so a regression that drags
// them back in shows up even if someone renames every marker.
import { describe, it, expect, beforeAll } from "vitest";
import { build } from "vite";
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
    await build({ root, logLevel: "silent", configFile: resolve(root, "vite.config.ts"), build: { outDir, emptyOutDir: true } });
    files = walk(outDir).filter((f) => /\.(js|css|html)$/.test(f));
  }, 280_000);

  it("emits an app", () => {
    expect(files.some((f) => f.endsWith("index.html"))).toBe(true);
  });

  it("contains no mock fixture strings in any emitted file", () => {
    const hits = files.flatMap((f) => findFixtureMarkers(readFileSync(f, "utf8")).map((m) => `${f.split("/").pop()}: ${m}`));
    expect(hits).toEqual([]);
  });

  it("keeps the whole emitted JS under 450 kB", () => {
    const total = files.filter((f) => f.endsWith(".js")).reduce((n, f) => n + statSync(f).size, 0);
    expect(total).toBeLessThan(450 * 1024);
  });
});
