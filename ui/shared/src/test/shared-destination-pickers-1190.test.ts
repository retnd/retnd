/**
 * Issue #1190: "Folder under the staging root on the host" and "Configured
 * destination" are offered in one way only, by assurance/RecoveryTargetFields.
 * Issue #1191 put "Source host (SFTP)" in the same module, and this holds it
 * to the same rule: the radio, the note that says why it is off, and the
 * field with the destination, the tick and the password are drawn there and
 * nowhere else.
 *
 * The Playwright specs (e2e/1190-*.spec.ts) hold the behaviour on each screen
 * that offers the two choices today. This holds the tree: a screen added later
 * that spells either choice out itself, or asks for a destination id in a text
 * box, goes red here before anyone has to notice it behaves differently.
 *
 * The sources come in through `import.meta.glob` with `?raw` for the reason
 * protected-lookalikes.test.ts gives: this package ships no `@types/node`.
 */
import { describe, expect, it } from "vitest";

const sources = import.meta.glob<string>(["../**/*.tsx", "!../test/**", "!../**/*.test.tsx"], {
  query: "?raw",
  import: "default",
  eager: true
});

const SHARED = "../assurance/RecoveryTargetFields.tsx";
const OWN_CONTROLS: Array<[string, RegExp]> = [
  ["the staging choice spelled out", /staging root on the host|Staging path on the host/],
  ["the destination choice spelled out", /["'>]\s*Configured destination\s*["'<]/],
  ["a destination id typed into a text box", /aria-label="[^"]*destination id"/i],
  ["the folder browser used directly", /<HostFolderBrowser\b/],
  ["the destination drop-down used directly", /<DestinationSelect\b/],
  ["the source-host choice spelled out", /Source host \(SFTP\)/],
  ["the source-host destination drawn by hand", /data-testid="source-(destination|preflight|unavailable)"/],
  ["the source-host write acknowledgement worded by hand", /writes onto .* the machine this backup came from/]
];

describe("#1190 one way to offer the staging folder and a configured destination (and #1191's source host)", () => {
  it("the shared module exists and carries both choices", () => {
    const shared = sources[SHARED];
    expect(shared, SHARED + " is missing").toBeTypeOf("string");
    expect(shared).toContain("Folder under the staging root on the host");
    expect(shared).toContain("Configured destination");
    expect(shared).toMatch(/<HostFolderBrowser\b/);
    expect(shared).toMatch(/<DestinationSelect\b/);
    expect(shared).toContain("Source host (SFTP)");
    expect(shared).toMatch(/data-testid="source-destination"/);
    expect(shared).toMatch(/data-testid="source-unavailable"/);
  });

  it("no other screen builds either choice itself", () => {
    const offenders: string[] = [];
    for (const [file, text] of Object.entries(sources)) {
      if (file === SHARED) continue;
      for (const [what, pattern] of OWN_CONTROLS) {
        if (pattern.test(text)) offenders.push(file + ": " + what);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("the screens that offer the choices all use the shared fields", () => {
    for (const file of ["../assurance/RecoverArtifact.tsx", "../assurance/RecoverSet.tsx", "../pages/BackupsSelectionBar.tsx"]) {
      expect(sources[file], file).toMatch(/<RecoveryTargetChoices\b/);
      expect(sources[file], file).toMatch(/<RecoveryTargetField\b/);
      // #1191: and every one of them offers the source host from it, with the
      // set's own state handed in.
      expect(sources[file], file).toMatch(/<RecoverySourceChoice\b[^>]*source=\{sourceHost\}/);
      expect(sources[file], file).toMatch(/<RecoverySourceNote\b/);
      expect(sources[file], file).toMatch(/<RecoveryTargetField\b[^>]*source=\{\{/);
    }
  });
});
