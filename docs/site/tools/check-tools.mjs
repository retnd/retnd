// The gate for the docs-site capture tooling (#926).
//
//     node docs/site/tools/check-tools.mjs
//
// Two unrelated breaks (an unbound FFMPEG in Clip.write, a placeholder
// EXAMPLE.port the wizard refuses since #864) meant nobody could re-record
// site media and nothing said so. #788 had already left the scripts driving
// deleted step names. This runs, with no browser and no dev server, far
// enough to prove: the harness loads, a clip encodes to a GIF, the port the
// wizard is filled with is one the wizard accepts, and every literal label
// a capture script drives still appears in the shipped UI source.
//
// Exit 0 ok, 1 broken, 3 no ffmpeg here (a skip, like the gate's other
// host-dependent steps).
import { spawnSync } from "node:child_process";
import { mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { deflateSync } from "node:zlib";

const HERE = dirname(fileURLToPath(import.meta.url));
const UI_SRC = resolve(HERE, "../../../ui/shared/src");
const WIZARD = resolve(UI_SRC, "pages/BackupSetWizardPage.tsx");

/** The string literals a capture script hands to a locator or a fill, and
 *  the ones inside a simple regex (`/Private key/`, `/^Password/`). A regex
 *  with any other metacharacter is computed, so it is skipped. */
export function captureLabels(src) {
  const out = new Set();
  const call = /(?:name:\s*|getByLabel\(|getByText\(|getByPlaceholder\(|railStep\(|\bfill\()\s*(?:"((?:[^"\\]|\\.)*)"|\/\^?([^/\\.*+?()[\]{}|$]+)\/(?![a-z]*\s*\.\s*test))/g;
  for (const m of src.matchAll(call)) out.add(m[1] ?? m[2]);
  return [...out];
}

/** Labels a script drives that the UI source no longer contains. Paths
 *  and values the fixtures derive (anything with a digit) are not UI copy. */
export function staleLabels(scriptSrc, uiSrc) {
  const ui = uiSrc.replaceAll("&amp;", "&").toLowerCase();
  // A label the UI builds ("Next: " + step, "Configure " + destination) is
  // checked in its fixed pieces: split on ": " and on fixture-looking
  // tokens (underscores, dots, digits, brackets), and every piece must be
  // somewhere in the source. Regexes are matched without regard to case,
  // so this does too.
  const present = (l) =>
    l.toLowerCase().split(/\s*:\s+|\S*[_.0-9[\]/]\S*/).map((c) => c.trim()).filter(Boolean).every((c) => ui.includes(c));
  return captureLabels(scriptSrc).filter((l) => !present(l));
}

/** Every non-test source file under ui/shared/src, concatenated. */
export function loadUiSource() {
  const parts = [];
  const walk = (dir) => {
    for (const n of readdirSync(dir)) {
      const p = join(dir, n);
      if (statSync(p).isDirectory()) walk(p);
      else if (/\.(ts|tsx)$/.test(n) && !/\.test\./.test(n)) parts.push(readFileSync(p, "utf8"));
    }
  };
  walk(UI_SRC);
  return parts.join("\n");
}

function png(w, h) {
  const crc = (buf) => {
    let c = ~0;
    for (const b of buf) {
      c ^= b;
      for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1));
    }
    return ~c >>> 0;
  };
  const chunk = (type, data) => {
    const t = Buffer.concat([Buffer.from(type), data]);
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const sum = Buffer.alloc(4);
    sum.writeUInt32BE(crc(t));
    return Buffer.concat([len, t, sum]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8;
  ihdr[9] = 2;
  const row = Buffer.concat([Buffer.from([0]), Buffer.alloc(w * 3, 0x80)]);
  const raw = Buffer.concat(Array.from({ length: h }, () => row));
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw)),
    chunk("IEND", Buffer.alloc(0))
  ]);
}

/** Drives a real Clip from the harness at `harnessUrl` through frame() and
 *  write() with a stand-in page, and rejects if no GIF comes out. */
export async function encodeProbe(harnessUrl) {
  const { Clip } = await import(harnessUrl);
  const out = mkdtempSync(join(tmpdir(), "926-encode-"));
  try {
    const page = {
      evaluate: async () => {},
      waitForTimeout: async () => {},
      viewportSize: () => ({ width: 64, height: 64 }),
      screenshot: async ({ path }) => writeFileSync(path, png(64, 64))
    };
    const clip = new Clip(page, "gate-probe", { clip: { x: 0, y: 0, width: 64, height: 64 }, width: 64, outDir: out });
    await clip.frame(0.1);
    await clip.frame(0.1);
    await clip.write();
    if (readFileSync(join(out, "gate-probe.gif")).subarray(0, 3).toString() !== "GIF") {
      throw new Error("the clip did not encode to a GIF");
    }
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}

/** Whether the wizard's own isPort accepts the harness's EXAMPLE.port. The
 *  function is lifted from the wizard source, so a change to the rule is
 *  what this reads, not a copy of it. */
export async function examplePortAccepted(harnessUrl) {
  const { EXAMPLE } = await import(harnessUrl);
  const m = readFileSync(WIZARD, "utf8").match(/function isPort\(value: string\): boolean \{([\s\S]*?)\n\}/);
  if (!m) throw new Error("isPort is no longer in " + WIZARD + "; point the gate at where the port rule lives now");
  return Boolean(new Function("value", m[1])(EXAMPLE.port));
}

async function main() {
  if (spawnSync("ffmpeg", ["-version"], { stdio: "ignore" }).status !== 0 && !process.env.FFMPEG) {
    const ok = ["/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg", "/usr/bin/ffmpeg"].some(
      (p) => spawnSync(p, ["-version"], { stdio: "ignore" }).status === 0
    );
    if (!ok) {
      console.error("check-tools: no ffmpeg here, cannot prove a clip encodes");
      process.exit(3);
    }
  }
  const harness = pathToFileURL(resolve(HERE, "harness.mjs")).href;
  const problems = [];
  try {
    await encodeProbe(harness);
  } catch (e) {
    problems.push("a clip does not encode: " + e.message);
  }
  if (!(await examplePortAccepted(harness))) problems.push("EXAMPLE.port is not a port the wizard accepts (isPort)");
  const ui = loadUiSource();
  for (const f of readdirSync(HERE).filter((n) => /^capture-.*\.mjs$/.test(n))) {
    const stale = staleLabels(readFileSync(join(HERE, f), "utf8"), ui);
    if (stale.length) problems.push(f + " drives labels the UI no longer has: " + stale.map((s) => JSON.stringify(s)).join(", "));
  }
  if (problems.length) {
    console.error("check-tools: " + problems.length + " problem(s)\n  " + problems.join("\n  "));
    process.exit(1);
  }
  console.log("check-tools: harness loads, a clip encodes, EXAMPLE.port passes isPort, every capture label exists");
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
