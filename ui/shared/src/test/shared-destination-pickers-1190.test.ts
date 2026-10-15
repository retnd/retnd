/**
 * Issue #1190: the words for the staging folder and the configured destination
 * live once, in assurance/recoveryPlaces.ts, and no other file in src spells
 * them out. Issue #1191 put "Source host (SFTP)" in the same constant, so the
 * source-host radio is held to the same rule: only RecoveryTargetFields draws
 * it, from that constant.
 *
 * This is the copy half of the rule only. The other half, that no screen
 * reaches the folder browser or the destination drop-down except through
 * assurance/RecoveryTargetFields, is an import rule in eslint.config.js, since
 * that follows the dependency and a text search can't. The Playwright specs
 * (e2e/1190-*.spec.ts) hold the behaviour on each screen.
 *
 * Every .ts and .tsx file is parsed with TypeScript, and only its string
 * literals, template text and JSX text are matched, so a comment that quotes a
 * label never counts and a label in a .ts helper does. The labels come from the
 * constant itself, so renaming one doesn't leave this checking the old words.
 *
 * The sources come in through `import.meta.glob` with `?raw` for the reason
 * protected-lookalikes.test.ts gives: this package ships no `@types/node`.
 */
import ts from "typescript";
import { describe, expect, it } from "vitest";
import { RECOVERY_PLACE_LABELS } from "@shared/assurance/recoveryPlaces";

const sources = import.meta.glob<string>(["../**/*.{ts,tsx}", "!../test/**", "!../**/*.test.{ts,tsx}"], {
  query: "?raw",
  import: "default",
  eager: true
});

const OWNER = "../assurance/recoveryPlaces.ts";
/** The staging choice's names before #1190 settled on one. */
const RETIRED = ["Staging path on the host", "Folder under the staging root on the host"];

/** The text a file could put on screen: string literals, template text and
 *  JSX text, with every comment left out. */
function renderedText(file: string, text: string): string[] {
  const kind = file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, false, kind);
  const out: string[] = [];
  const visit = (n: ts.Node) => {
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n) || ts.isTemplateLiteralToken(n)) out.push(n.text);
    else if (ts.isJsxText(n)) out.push(n.text.replace(/\s+/g, " ").trim());
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return out;
}

function spelledOut(words: string[]) {
  const offenders: string[] = [];
  for (const [file, text] of Object.entries(sources)) {
    if (file === OWNER) continue;
    for (const literal of renderedText(file, text)) {
      for (const w of words) if (literal.includes(w)) offenders.push(file + ": " + JSON.stringify(w));
    }
  }
  return offenders;
}

describe("#1190 the staging folder, a configured destination and #1191's source host have one set of words", () => {
  it("the labels live in assurance/recoveryPlaces.ts", () => {
    expect(sources[OWNER], OWNER + " is missing").toBeTypeOf("string");
    expect(Object.keys(RECOVERY_PLACE_LABELS).sort()).toEqual(["source", "staging", "storage"]);
  });

  it("no other file spells any of the labels out", () => {
    expect(spelledOut(Object.values(RECOVERY_PLACE_LABELS))).toEqual([]);
  });

  it("no file still uses a retired name for the staging choice", () => {
    expect(spelledOut(RETIRED)).toEqual([]);
  });

  it("matches strings and JSX text but never a comment", () => {
    const sample = [
      "// Under the staging root on this retnd host",
      "/* Configured destination */",
      "const a = 'quoted';",
      "const b = `tmpl ${a} tail`;",
      "export const C = () => <label>Seen  here</label>;"
    ].join("\n");
    expect(renderedText("x.tsx", sample)).toEqual(["quoted", "tmpl ", " tail", "Seen here"]);
  });
});
