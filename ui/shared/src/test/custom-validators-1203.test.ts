/**
 * Issue #1203: the words the engine refuses a validator in, said before the
 * upload is offered. The engine still decides; these only keep a press that
 * cannot succeed from being offered (the same split as assurance/builtinCheck).
 */
import { describe, expect, it } from "vitest";
import { scriptProblem, validatorNameProblem, matchWords } from "@shared/assurance/customValidators";

describe("validatorNameProblem", () => {
  it("accepts a bare identifier", () => {
    expect(validatorNameProblem("manifest-check")).toBeNull();
    expect(validatorNameProblem("db_2")).toBeNull();
  });

  it("says what is missing for an empty name", () => {
    expect(validatorNameProblem("")).toBe("A validator needs a name; it is the NAME in NAME.local.sh.");
  });

  it("refuses the product's own prefix in the engine's words", () => {
    expect(validatorNameProblem("retnd-x")).toBe(`"retnd-x" is not usable: ids beginning retnd- are the product's own built-in checks`);
  });

  it("refuses whitespace and path separators", () => {
    for (const bad of ["a b", "a/b", "a\\b", " a", "a\t"]) {
      expect(validatorNameProblem(bad), JSON.stringify(bad)).toMatch(/bare identifier|leading or trailing whitespace/);
    }
  });

  it("refuses a file name", () => {
    expect(validatorNameProblem("mine.sh")).toBe(`"mine.sh" is a file name; name the script without its .local.sh suffix`);
  });
});

describe("scriptProblem", () => {
  it("accepts a small script and refuses an empty one", () => {
    expect(scriptProblem("#!/bin/sh\nexit 0\n", 262144)).toBeNull();
    expect(scriptProblem("", 262144)).toBe("A validator needs a script.");
  });

  it("refuses a NUL, and a script over the limit in KiB", () => {
    expect(scriptProblem("a\u0000b", 262144)).toMatch(/NUL/);
    expect(scriptProblem("x".repeat(262145), 262144)).toBe("The script is 262145 bytes; this deployment takes at most 256 KiB.");
  });

  it("counts bytes, not characters", () => {
    expect(scriptProblem("é".repeat(10), 10)).toMatch(/20 bytes/);
  });
});

describe("matchWords", () => {
  it("says one backup, not one backups", () => {
    expect(matchWords(0)).toBe("0 backups");
    expect(matchWords(1)).toBe("1 backup");
    expect(matchWords(2)).toBe("2 backups");
  });
});
