/**
 * A validator's captured output, bounded and inert (issue #985).
 *
 * The output is untrusted terminal input. This does not emulate a terminal:
 * every control character is replaced by its visible escape (`\x1b`), so an
 * OSC title sequence or a screen-clear is shown as text and does nothing.
 * React text nodes are the only thing used to draw it, never markup.
 *
 * The truncation marker is drawn OUTSIDE the content element and from the
 * separately carried byte count, never from the content. A validator that
 * prints "[output truncated]" produces a line of content, which looks
 * nothing like the marker and is not in the marker's place.
 */
import "./assurance.css";
import { inert } from "./inert";
import type { ValidatorResult } from "./types";

export function ValidatorLog({ result }: { result: ValidatorResult }) {
  return (
    <div className="avlog">
      <div className="muted" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)" }} data-testid="validator-facts">
        {"Your script " + (result.validatorName ? result.validatorName + " " : "") + "exited " + result.exitStatus + " after " + (result.durationMs / 1000).toFixed(1) + " s. "}
        The engine records the claim, not the script: it re-read the recovered copy and checked its digest before it ran the script, and the exit status is one input to the verdict.
      </div>
      {result.validatorSha256 ? (
        <div className="mono" style={{ fontSize: "var(--text-xs)", color: "var(--text-2)", wordBreak: "break-all" }} data-testid="validator-hash">
          {"Script hash recorded by the engine: " + result.validatorSha256 + (result.artifactId ? ", artifact " + result.artifactId : "") + (result.copy ? ", copy " + result.copy : "")}
        </div>
      ) : null}
      {result.truncatedBytes > 0 ? (
        <div className="avlog__marker" data-testid="truncation-marker">
          {"Output truncated: " + result.truncatedBytes.toLocaleString("en-US") + " bytes before this point are not shown."}
        </div>
      ) : null}
      <pre className="avlog__pre" data-testid="validator-log" tabIndex={0} aria-label="Validator output">{inert(result.output)}</pre>
    </div>
  );
}
