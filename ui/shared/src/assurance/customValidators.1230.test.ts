/**
 * Issue #1230: the words a validator's script state gets in the browser.
 *
 * The CLI prints the same four states from `retnd assurance validators list`
 * (core/cmd/retnd/assurancevalidators.go, printCustomValidator). The browser
 * has to say the same things in the same words where it matters, so an
 * operator who reads the CLI one day and the Settings card the next is not
 * left wondering whether "edited on disk" and "Edited on disk since you
 * uploaded it" are the same fault. The phrases pinned below are lifted from
 * that function.
 */
import { describe, expect, it } from "vitest";
import { canKeepStoredScript, scriptStateWords, shortHash } from "./customValidators";

const STATES = ["pinned", "unpinned", "edited_on_disk", "missing"] as const;

describe("scriptStateWords", () => {
  it("uses the words the issue asks for as each state's label", () => {
    expect(scriptStateWords("pinned").label).toBe("Pinned");
    expect(scriptStateWords("unpinned").label).toBe("Not pinned yet (saving it pins it)");
    expect(scriptStateWords("edited_on_disk").label).toBe("Edited on disk since you uploaded it");
    expect(scriptStateWords("missing").label).toBe("Script missing");
  });

  it("marks only the two failure states as failing", () => {
    expect(STATES.filter((s) => scriptStateWords(s).failing)).toEqual(["edited_on_disk", "missing"]);
  });

  it("says what to do about each failure, in words", () => {
    const edited = scriptStateWords("edited_on_disk").action;
    expect(edited).toMatch(/could not complete/);
    expect(edited).toMatch(/approve/);
    expect(edited).toMatch(/Edit/);
    const missing = scriptStateWords("missing").action;
    expect(missing).toMatch(/cannot complete/);
    expect(missing).toMatch(/Restore/);
    expect(missing).toMatch(/upload/);
  });

  it("has nothing to add for the healthy states beyond the label", () => {
    expect(scriptStateWords("pinned").action).toBe("");
    expect(scriptStateWords("unpinned").action).toMatch(/runs as it did/);
  });

  it("agrees with the CLI's list output on the facts that matter", () => {
    // The CLI: "not pinned in config.yaml (written before pinning); it runs as it did, and saving the validator again pins it"
    const unpinned = (scriptStateWords("unpinned").label + " " + scriptStateWords("unpinned").action).toLowerCase();
    expect(unpinned).toContain("not pinned");
    expect(unpinned).toContain("runs as it did");
    expect(unpinned).toContain("pins it");
    // The CLI: "EDITED ON DISK: ... ends could_not_complete without running it; read the script and send it again ... to approve it"
    const edited = (scriptStateWords("edited_on_disk").label + " " + scriptStateWords("edited_on_disk").action).toLowerCase();
    expect(edited).toContain("edited on disk");
    expect(edited).toContain("could not complete");
    expect(edited).toContain("without running it");
    expect(edited).toContain("approve it");
    // The CLI: "MISSING, so a test it applies to cannot complete"
    const missing = (scriptStateWords("missing").label + " " + scriptStateWords("missing").action).toLowerCase();
    expect(missing).toContain("missing");
    expect(missing).toContain("cannot complete");
  });

  it("never leans on punctuation or colour alone: every label has letters and no em dash", () => {
    for (const s of STATES) {
      const w = scriptStateWords(s);
      expect(w.label).toMatch(/[A-Za-z]{4}/);
      expect(w.label + w.action).not.toContain("—");
    }
  });
});

describe("canKeepStoredScript", () => {
  it("is true only where the engine accepts keeping the stored script", () => {
    expect(canKeepStoredScript({ scriptState: "pinned", scriptPresent: true })).toBe(true);
    expect(canKeepStoredScript({ scriptState: "unpinned", scriptPresent: true })).toBe(true);
    expect(canKeepStoredScript({ scriptState: "edited_on_disk", scriptPresent: true })).toBe(false);
    expect(canKeepStoredScript({ scriptState: "missing", scriptPresent: false })).toBe(false);
  });
});

describe("shortHash", () => {
  it("shows twelve characters of a digest and says so when there is none", () => {
    expect(shortHash("0123456789abcdef0123456789abcdef")).toBe("0123456789ab");
    expect(shortHash("")).toBe("none");
  });
});
