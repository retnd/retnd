import { describe, expect, it } from "vitest";
import { LEGACY_SECTION_IDS, SECTION_IDS, sectionForHash } from "./settingsSections";
import { SETTINGS_SECTIONS, settingsLink } from "@shared/utilities/routes";

describe("sectionForHash (#1188)", () => {
  it("resolves a current id to itself", () => {
    expect(sectionForHash("#assurance")).toBe("assurance");
    expect(sectionForHash("#storage-destinations")).toBe("storage-destinations");
  });

  it("resolves the shipped #assurance-defaults to the Assurance section", () => {
    expect(sectionForHash("#assurance-defaults")).toBe(SETTINGS_SECTIONS.assurance);
  });

  it("names nothing for an empty, unknown or malformed hash instead of throwing", () => {
    expect(sectionForHash("")).toBeNull();
    expect(sectionForHash("#nope")).toBeNull();
    expect(() => sectionForHash("#%E0")).not.toThrow();
    expect(sectionForHash("#%E0")).toBeNull();
  });

  it("only maps old ids onto ids the page still has", () => {
    for (const id of Object.values(LEGACY_SECTION_IDS)) expect(SECTION_IDS.has(id)).toBe(true);
    for (const old of Object.keys(LEGACY_SECTION_IDS)) expect(SECTION_IDS.has(old)).toBe(false);
  });

  it("builds links from the same table", () => {
    expect(settingsLink("assurance")).toBe("/settings#assurance");
  });
});
