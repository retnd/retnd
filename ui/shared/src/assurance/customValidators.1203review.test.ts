/**
 * Issue #1203, review round: the pure parts of the validators dialog. The
 * browser still never judges a pattern; these pin the things it does say.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { addCommand, readScriptFile, scriptBytes, suggestPattern, validatorNameProblem } from "./customValidators";

describe("addCommand", () => {
  it("quotes the name, the pattern and the set id for a shell", () => {
    expect(addCommand("a b", "^x$", "src/set 1")).toBe("retnd assurance validator add 'a b' --pattern '^x$' --backup-set 'src/set 1' --script FILE");
  });

  it("does not let a name or a set id end the line", () => {
    const line = addCommand("x; rm -rf ~", "^y$", "s$(id)");
    expect(line).toContain("'x; rm -rf ~'");
    expect(line).toContain("'s$(id)'");
  });

  it("leaves a plain name and set id bare", () => {
    expect(addCommand("billing-dump", "^b$", "production/billing-mysql")).toBe(
      "retnd assurance validator add billing-dump --pattern '^b$' --backup-set production/billing-mysql --script FILE"
    );
  });
});

describe("scriptBytes", () => {
  const Real = globalThis.TextEncoder;
  afterEach(() => { globalThis.TextEncoder = Real; });

  it("does not build a TextEncoder per call", () => {
    const ctor = vi.fn(function (this: unknown) { return new Real(); });
    globalThis.TextEncoder = ctor as unknown as typeof TextEncoder;
    scriptBytes("a");
    scriptBytes("bb");
    scriptBytes("ccc");
    expect(ctor).not.toHaveBeenCalled();
  });

  it("still counts UTF-8 bytes", () => {
    expect(scriptBytes("é")).toBe(2);
  });
});

describe("readScriptFile", () => {
  const bytes = (...n: number[]) => new Uint8Array(n).buffer;
  const file = (size: number, buf: ArrayBuffer) => ({ size, arrayBuffer: vi.fn(() => Promise.resolve(buf)) });

  it("refuses an oversize file before reading any of it", async () => {
    const f = file(300000, bytes(65));
    const got = await readScriptFile(f, 262144);
    expect(got).toEqual({ ok: false, problem: "That file is 300000 bytes; this deployment takes at most 256 KiB." });
    expect(f.arrayBuffer).not.toHaveBeenCalled();
  });

  it("refuses bytes that are not UTF-8 rather than writing U+FFFD", async () => {
    const got = await readScriptFile(file(2, bytes(0xff, 0xfe)), 262144);
    expect(got).toEqual({ ok: false, problem: "That file is not valid UTF-8, so it was not loaded." });
  });

  it("says so when reading fails, instead of rejecting", async () => {
    const f = { size: 1, arrayBuffer: () => Promise.reject(new Error("gone")) };
    const got = await readScriptFile(f, 262144);
    expect(got).toEqual({ ok: false, problem: "That file could not be read." });
  });

  it("loads text, and says when it turned CRLF line endings into LF", async () => {
    const plain = await readScriptFile(file(3, bytes(97, 10, 98)), 262144);
    expect(plain).toEqual({ ok: true, text: "a\nb", normalised: false });
    const crlf = await readScriptFile(file(4, bytes(97, 13, 10, 98)), 262144);
    expect(crlf).toEqual({ ok: true, text: "a\nb", normalised: true });
  });
});

// The Go cases, copied from core/service/assurancevalidators_1203_test.go
// (TestCustomValidators_SuggestArtifactPatternGeneralisesDigitRuns). If the
// engine's derivation changes, this vector has to change with it.
describe("suggestPattern against the engine's cases", () => {
  const vector: Record<string, string> = {
    "MANIFEST-20260902T153521Z.txt": String.raw`^MANIFEST-\d{8}T\d{6}Z\.txt$`,
    "db-2026-09-02.sql.gz": String.raw`^db-\d{4}-\d{2}-\d{2}\.sql\.gz$`,
    "plain.tar": String.raw`^plain\.tar$`,
    "a+b(1).zip": String.raw`^a\+b\(\d{1}\)\.zip$`
  };
  for (const [name, want] of Object.entries(vector)) {
    it(name, () => expect(suggestPattern(name)).toBe(want));
  }
});

// The engine's refusals of a name (core/internal/config/validate.go
// validValidatorID and assurance.go RecoveryTestScriptName). The three rules
// the browser does not repeat (a leading dot or dash, non-ASCII) are the
// engine's alone, and are listed so a change to either side shows up here.
describe("validatorNameProblem against the engine's cases", () => {
  const accepted = ["manifest-check", "db_2", "Billing.Dump"];
  const refused: Array<[string, RegExp]> = [
    ["a b", /bare identifier/], ["a/b", /bare identifier/], ["a\\b", /bare identifier/], ["a\tb", /bare identifier/],
    ["a\nb", /bare identifier/], ["a\0b", /bare identifier/], [" a", /leading or trailing whitespace/], ["a ", /leading or trailing whitespace/],
    [".", /directory reference/], ["..", /directory reference/], ["retnd-x", /built-in checks/], ["retnd-", /built-in checks/],
    ["x.local.sh", /without its \.local\.sh suffix/], ["x.sh", /without its \.local\.sh suffix/], ["", /needs a name/]
  ];
  for (const n of accepted) it("accepts " + JSON.stringify(n), () => expect(validatorNameProblem(n)).toBeNull());
  for (const [n, re] of refused) it("refuses " + JSON.stringify(n), () => expect(validatorNameProblem(n)).toMatch(re));
  for (const n of [".hidden", "-dash", "naïve"]) {
    it("leaves " + JSON.stringify(n) + " to the engine", () => expect(validatorNameProblem(n)).toBeNull());
  }
});
