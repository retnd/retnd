/**
 * Issue #1109: the project's former name is gone from the UI, the
 * Synology package and the UGOS / generic / Proxmox frontends.
 *
 * The name is built from pieces on purpose. A literal here would make this
 * file the one place the sweep it asserts cannot reach.
 */
import { describe, expect, it } from "vitest";

import { isReservedEnvName } from "@shared/components/workflowPresentation";

const FORMER = ["BACK", "UPD"].join("");
const FormerCase = ["Back", "upd"].join("");
const former = FORMER.toLowerCase();

/** The scanner's own definition of an occurrence: the upper-case name, the
 *  title-case name or the lower-case name not followed by a lower-case
 *  letter (BackupDetailPage is the domain word `backup` + `Detail`), and
 *  the capital-D camel-case form under the same rule. */
const OCCURRENCE = new RegExp(
  FORMER + "|[Bb]" + former.slice(1) + "(?![a-z])|" + FormerCase.slice(0, 6) + "D(?![a-z])"
);

const uiSources = import.meta.glob("../**/*.{ts,tsx,css,json,html}", {
  query: "?raw",
  import: "default",
  eager: true
}) as Record<string, string>;

const appSources = import.meta.glob(
  [
    "../../../../apps/ugos/frontend/**/*",
    "../../../../apps/generic/frontend/**/*",
    "../../../../apps/proxmox/frontend/**/*",
    "../../../../apps/synology/frontend/**/*"
  ],
  { query: "?raw", import: "default", eager: true }
) as Record<string, string>;

function offenders(sources: Record<string, string>): string[] {
  const out: string[] = [];
  for (const [path, text] of Object.entries(sources)) {
    text.split("\n").forEach((line, i) => {
      if (OCCURRENCE.test(line)) out.push(path + ":" + (i + 1));
    });
  }
  return out;
}

describe("#1109 the former name is gone", () => {
  it("scans a real set of files, so an empty glob cannot pass", () => {
    expect(Object.keys(uiSources).length).toBeGreaterThan(100);
    expect(Object.keys(appSources).length).toBeGreaterThan(0);
  });

  it("is not in any file under ui/shared/src", () => {
    expect(offenders(uiSources)).toEqual([]);
  });

  it("is not in the ugos, generic, proxmox or synology frontends", () => {
    expect(offenders(appSources)).toEqual([]);
  });

  it("the occurrence rule itself still recognises each spelling", () => {
    for (const s of [FORMER, FORMER + "_RUN_ID", FormerCase, "x " + former + "_y"]) {
      expect(OCCURRENCE.test(s), s).toBe(true);
    }
    expect(OCCURRENCE.test("BackupDetailPage")).toBe(false);
  });
});

describe("#1109 the former name is no longer a reserved environment name", () => {
  it("still reserves the RETND namespace", () => {
    expect(isReservedEnvName("RETND")).toBe(true);
    expect(isReservedEnvName("RETND_RUN_ID")).toBe(true);
    expect(isReservedEnvName("RETND_SOMETHING_NEW")).toBe(true);
  });

  it("does not reserve the former bare name or its prefix", () => {
    expect(isReservedEnvName(FORMER)).toBe(false);
    expect(isReservedEnvName(FORMER + "_RUN_ID")).toBe(false);
  });
});
