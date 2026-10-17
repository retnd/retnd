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

import type { ValidatorScriptState } from "@shared/api/contracts";
import { shellQuote } from "./assuranceCommands";

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

const encoder = new TextEncoder();

/** The size of a script as the engine counts it: its UTF-8 bytes. The encoder
 *  is built once; a script can be 256 KiB and this runs on every keystroke. */
export const scriptBytes = (script: string): number => encoder.encode(script).length;

/** What is wrong with a script, or null. `max` is the deployment's own
 *  limit (the list route serves it). `bytes` is the script's size when the
 *  caller already has it, so a large script is not encoded twice. */
export function scriptProblem(script: string, max: number, bytes?: number): string | null {
  if (script === "") return "A validator needs a script.";
  if (script.includes("\0")) return "The script holds a NUL byte, which a script file may not.";
  const n = bytes ?? scriptBytes(script);
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
 *  notice a shut gate gets. Every operand a person typed goes through
 *  shellQuote, so the line can be pasted as it stands. */
export function addCommand(name: string, pattern: string, backupSetId: string): string {
  return (
    "retnd assurance validators add " + shellQuote(name || "NAME") + " --pattern " + shellQuote(pattern || "PATTERN") +
    (backupSetId ? " --backup-set " + shellQuote(backupSetId) : "") + " --script FILE"
  );
}

export type ScriptFileRead = { ok: true; text: string; normalised: boolean } | { ok: false; problem: string };

/** What the Choose a file control makes of a file. The size is checked before
 *  any of it is read, the bytes must be UTF-8 (a lossy decode would upload
 *  something other than the file), and CRLF line endings become LF, which is
 *  what the textarea would do anyway, so the result says when it happened. */
export async function readScriptFile(file: { size: number; arrayBuffer(): Promise<ArrayBuffer> }, max: number): Promise<ScriptFileRead> {
  if (file.size > max) return { ok: false, problem: `That file is ${file.size} bytes; this deployment takes at most ${kib(max)}.` };
  let buf: ArrayBuffer;
  try {
    buf = await file.arrayBuffer();
  } catch {
    return { ok: false, problem: "That file could not be read." };
  }
  let text: string;
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(buf);
  } catch {
    return { ok: false, problem: "That file is not valid UTF-8, so it was not loaded." };
  }
  const normalised = text.includes("\r");
  return { ok: true, text: normalised ? text.replace(/\r\n?/g, "\n") : text, normalised };
}

/** What a validator's script state is called in the browser, and what to do
 *  about it (issue #1230). The words follow `retnd assurance validators list`
 *  (core/cmd/retnd/assurancevalidators.go), so the terminal and the page tell
 *  one story. `failing` is the two states in which a recovery test this
 *  validator applies to cannot complete; they are bold as well as coloured. */
export interface ScriptStateWords {
  label: string;
  action: string;
  failing: boolean;
}

export function scriptStateWords(state: ValidatorScriptState): ScriptStateWords {
  switch (state) {
    case "pinned":
      return { label: "Pinned", action: "", failing: false };
    case "unpinned":
      return {
        label: "Not pinned yet (saving it pins it)",
        action: "It was written before pinning and runs as it did.",
        failing: false
      };
    case "edited_on_disk":
      return {
        label: "Edited on disk since you uploaded it",
        action:
          "A recovery test it applies to ends could not complete without running it. Read the script (View), then Edit and send it again to approve it.",
        failing: true
      };
    case "missing":
      return {
        label: "Script missing",
        action:
          "A recovery test it applies to cannot complete. Restore the script file on the host, or Edit and upload the script again.",
        failing: true
      };
  }
}

/** Whether "keep the stored script" can be offered: the engine refuses it for
 *  a script edited on disk (VALIDATOR_SCRIPT_CHANGED) and there is nothing to
 *  keep when the file is missing. */
export function canKeepStoredScript(v: { scriptState: ValidatorScriptState; scriptPresent: boolean }): boolean {
  return v.scriptPresent && (v.scriptState === "pinned" || v.scriptState === "unpinned");
}

/** The first twelve characters of a digest, enough to tell two apart. */
export const shortHash = (sha: string): string => (sha === "" ? "none" : sha.slice(0, 12));
