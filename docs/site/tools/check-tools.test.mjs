// Red-first tests for the capture-tooling gate (#926). Run with:
//
//     node --test docs/site/tools/check-tools.test.mjs
//
// Each test plants one of the breaks the issue names (or the stale step
// names of #788) and asserts the gate notices it, then asserts the tree as
// it stands passes. No browser and no dev server: the gate has to be cheap
// enough for every PR that touches ui/shared or docs/site/tools.
import assert from "node:assert/strict";
import { mkdtempSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import { captureLabels, encodeProbe, examplePortAccepted, loadUiSource, staleLabels } from "./check-tools.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const HARNESS = resolve(HERE, "harness.mjs");

/** A copy of the harness with one edit, loadable on its own because it only
 *  imports node builtins. */
function mutatedHarness(from, to) {
  const src = readFileSync(HARNESS, "utf8");
  assert.ok(src.includes(from), "the harness no longer contains " + JSON.stringify(from) + ", fix this test's anchor");
  const dir = mkdtempSync(join(tmpdir(), "926-harness-"));
  const file = join(dir, "harness.mjs");
  writeFileSync(file, src.replace(from, to));
  return pathToFileURL(file).href;
}

test("a clip encodes with the harness as it stands", async () => {
  await encodeProbe(pathToFileURL(HARNESS).href);
});

test("an unbound FFMPEG in Clip.write is caught (break 1 of #926)", async () => {
  const url = mutatedHarness("const bin = ffmpeg();", "const bin = FFMPEG;");
  await assert.rejects(encodeProbe(url), /FFMPEG is not defined/);
});

test("EXAMPLE.port is a port the wizard accepts", async () => {
  assert.equal(await examplePortAccepted(pathToFileURL(HARNESS).href), true);
});

test("the placeholder port <your-ssh-port> is caught (break 2 of #926)", async () => {
  const url = mutatedHarness('port: "22"', 'port: "<your-ssh-port>"');
  assert.equal(await examplePortAccepted(url), false);
});

test("a label for a step the UI no longer has is reported (#788)", () => {
  const ui = 'const steps = ["Source", "Connection test", "Review"];';
  const script = 'await railStep("Credentials").click();\nawait page.getByRole("button", { name: "Review" }).click();';
  assert.deepEqual(staleLabels(script, ui), ["Credentials"]);
});

test("literal regex labels are checked, computed ones are not", () => {
  assert.deepEqual(captureLabels('getByLabel(/Private key/) getByText(/^Password/) getByText(/SHA256:/) getByText(/a.*b/)').sort(), [
    "Password",
    "Private key",
    "SHA256:"
  ]);
});

test("every label a capture script drives still exists in the shipped UI", () => {
  const ui = loadUiSource();
  const bad = {};
  for (const f of readdirSync(HERE).filter((n) => /^capture-.*\.mjs$/.test(n))) {
    const stale = staleLabels(readFileSync(join(HERE, f), "utf8"), ui);
    if (stale.length) bad[f] = stale;
  }
  assert.deepEqual(bad, {});
});
