/**
 * Custom recovery-test validators, as the browser states them (issue #1203).
 *
 * Every sentence a refusal gets is the engine's, word for word
 * (core/internal/config/assurance.go and validate.go for a name,
 * core/service/assurancevalidators.go for the rest), so an operator who sees
 * a problem before pressing Upload reads what they would have got back. The
 * browser refuses early only so that a press that cannot succeed is not
 * offered; the engine still decides, and a PATTERN is never judged here at
 * all: Go's RE2 is the engine that will match it, so the preview route asks it
 * (a browser's own RegExp would accept lookaheads RE2 refuses).
 */

/** The pattern an artifact's own name gives: every run of digits becomes
 *  \d{n}, everything else is matched literally, anchored at both ends. The
 *  same derivation as service.SuggestArtifactPattern, so the pattern chosen
 *  from a backup's page is one the engine also suggests. */
export function suggestPattern(name: string): string {
  let out = "";
  for (const part of name.split(/(\d+)/)) {
    out += /^\d+$/.test(part) ? "\\d{" + part.length + "}" : part.replace(/[\\.+*?()|[\]{}^$]/g, "\\$&");
  }
  return "^" + out + "$";
}

/** The reserved prefix of the product's own built-in checks. */
export const RESERVED_VALIDATOR_PREFIX = "retnd-";

/** What is wrong with a validator's name, or null. The empty name is said
 *  in the page's own words because the engine never sees it. */
export function validatorNameProblem(name: string): string | null {
  if (name === "") return "A validator needs a name; it is the NAME in NAME.local.sh.";
  if (name.trim() !== name) return `"${name}" must not have leading or trailing whitespace`;
  if (/[/\\\0\n\r \t]/.test(name)) return `"${name}" must be a bare identifier: no path separator, whitespace or control character`;
  if (name === "." || name === "..") return `"${name}" must not be a directory reference`;
  if (name.startsWith(RESERVED_VALIDATOR_PREFIX)) {
    return `"${name}" is not usable: ids beginning ${RESERVED_VALIDATOR_PREFIX} are the product's own built-in checks`;
  }
  if (name.endsWith(".sh")) return `"${name}" is a file name; name the script without its .local.sh suffix`;
  return null;
}

/** The size of a script as the engine counts it: its UTF-8 bytes. */
export const scriptBytes = (script: string): number => new TextEncoder().encode(script).length;

/** What is wrong with a script, or null. `max` is the deployment's own
 *  limit (the list route serves it). */
export function scriptProblem(script: string, max: number): string | null {
  if (script === "") return "A validator needs a script.";
  if (script.includes("\0")) return "The script holds a NUL byte, which a script file may not.";
  const n = scriptBytes(script);
  if (n > max) return `The script is ${n} bytes; this deployment takes at most ${kib(max)}.`;
  return null;
}

/** 262144 as "256 KiB". */
export const kib = (bytes: number): string => (bytes % 1024 === 0 ? bytes / 1024 : (bytes / 1024).toFixed(1)) + " KiB";

/** "1 backup", "2 backups". */
export const matchWords = (n: number): string => n + (n === 1 ? " backup" : " backups");

/** What the newest recorded test under a validator's script was. */
export function lastResultWords(r: { state: string; at: string } | undefined): string {
  if (!r) return "Not run yet";
  const word = r.state === "passed" ? "Passed" : r.state === "failed" ? "Failed" : r.state.replace(/_/g, " ");
  return word.charAt(0).toUpperCase() + word.slice(1) + " " + r.at.slice(0, 10);
}

/** The command that does the same upload from a shell on the host, for the
 *  notice a shut gate gets. */
export function addCommand(name: string, pattern: string, backupSetId: string): string {
  const q = (s: string) => "'" + s.replace(/'/g, `'\\''`) + "'";
  return (
    "retnd assurance validator add " + (name || "NAME") + " --pattern " + q(pattern || "PATTERN") +
    (backupSetId ? " --backup-set " + backupSetId : "") + " --script FILE"
  );
}
