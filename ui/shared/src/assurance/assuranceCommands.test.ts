/**
 * The terminal lines the recovery dialogs echo (#1191, #1193's review). The Go
 * side (core/cliecho) spells the same requests the same way; these vectors are
 * the browser's half of that agreement.
 */
import { describe, expect, it } from "vitest";
import { recoverCommand, recoverManyCommand, recoverSetCommand, recoverSetPreflightCommand, stagingName } from "./assuranceCommands";

const ID = "production/postgres-primary/dump.zst";
const FOLDER = "retnd-recovered-20261015-101500-a1b2";

describe("the source host is spelled source:FOLDER", () => {
  it("one backup, with the write acknowledged", () => {
    expect(recoverCommand(ID, "source", FOLDER, false, true)).toBe(
      "retnd recover " + ID + " --to source:" + FOLDER + " --acknowledge-source-write");
  });
  it("one backup, not yet acknowledged, never prints the flag", () => {
    expect(recoverCommand(ID, "source", FOLDER, false, false)).toBe("retnd recover " + ID + " --to source:" + FOLDER);
  });
  it("a set run onto the source names newest only", () => {
    expect(recoverSetCommand("production/postgres-primary", "newest", "", "source", FOLDER, false, true)).toBe(
      "retnd recover --set production/postgres-primary --newest-only --to source:" + FOLDER + " --acknowledge-source-write");
  });
  it("a whole set onto the source says --all", () => {
    expect(recoverSetCommand("production/postgres-primary", "all", "", "source", FOLDER, false, true)).toBe(
      "retnd recover --set production/postgres-primary --all --to source:" + FOLDER + " --acknowledge-source-write");
  });
  it("a set since a date onto the source says --since", () => {
    expect(recoverSetCommand("production/postgres-primary", "since", "2026-10-01", "source", FOLDER, false, false)).toBe(
      "retnd recover --set production/postgres-primary --since 2026-10-01 --to source:" + FOLDER);
  });
  it("the pre-flight onto the source carries no acknowledgement", () => {
    expect(recoverSetPreflightCommand("production/postgres-primary", "newest", "", "source", FOLDER)).toBe(
      "retnd recover --set production/postgres-primary --newest-only --to source:" + FOLDER + " --preflight");
  });
  it("several backups onto the source", () => {
    expect(recoverManyCommand([ID, ID + "2"], "source", FOLDER, false, true)).toBe(
      "retnd recover " + ID + " " + ID + "2 --to source:" + FOLDER + " --acknowledge-source-write");
  });
  it("a whole set to staging keeps its old spelling, with no --all", () => {
    expect(recoverSetCommand("production/postgres-primary", "all", "", "staging", "restore", false)).toBe(
      "retnd recover --set production/postgres-primary --to restore");
  });
});

describe("a staging name the CLI would read as something else is ./NAME", () => {
  for (const [name, want] of [
    ["source", "source"],
    ["sources/today", "sources/today"],
    ["source:db", "./source:db"],
    ["medium:offsite", "./medium:offsite"],
    ["-", "./-"],
    ["--to", "./--to"],
    ["-dump", "./-dump"],
    ["restore/dump.zst", "restore/dump.zst"],
    ["", "<name>"]
  ] as const) {
    it(JSON.stringify(name) + " -> " + want, () => {
      expect(stagingName(name)).toBe(want);
    });
  }
  it("is used by every staging echo", () => {
    expect(recoverCommand(ID, "staging", "source:db", false)).toBe("retnd recover " + ID + " --to ./source:db");
    expect(recoverSetCommand("a/b", "all", "", "staging", "-x", false)).toBe("retnd recover --set a/b --to ./-x");
    expect(recoverManyCommand([ID], "staging", "medium:m", false)).toBe("retnd recover " + ID + " --to ./medium:m");
  });
});
