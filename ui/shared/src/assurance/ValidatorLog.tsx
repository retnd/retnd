/**
 * A recovery test's captured validator output, bounded and inert (issue #985).
 *
 * The output is untrusted terminal input. This does not emulate a terminal:
 * every control character is replaced by its visible escape (`\x1b`), so an
 * OSC title sequence or a screen-clear is shown as text and does nothing.
 * React text nodes are the only thing used to draw it, never markup.
 *
 * # The records, and the marker
 *
 * The server hands the output over as records, each either `output` or
 * `truncation`, and this branches on that kind and on nothing else. The
 * runner's truncation marker is drawn OUTSIDE the log element, from the
 * record of that kind, so a validator that prints "[output truncated]"
 * produces a line of output that looks nothing like the marker and is not in
 * the marker's place, and a record the validator could forge by printing the
 * marker's words is still `output`. With no `truncation` record there is no
 * marker, whatever the output says.
 *
 * # The verdict is not the output
 *
 * The state shown first is the engine's recorded verdict for that run. The
 * script's lines are shown under it as what the script printed, and nothing
 * in them is read for meaning.
 */
import "./assurance.css";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { stamp } from "@shared/utilities/format";
import { inert } from "./inert";
import type { ValidatorResult } from "./types";

const STATE_WORDS: Record<ValidatorResult["state"], string> = {
  passed: "passed",
  failed: "failed",
  refused_for_cost: "was refused because reading the copy would cost money",
  could_not_complete: "could not complete"
};

export function ValidatorLog({ result }: { result: ValidatorResult }) {
  const output = result.records.filter((r) => r.kind === "output");
  const marker = result.records.find((r) => r.kind === "truncation");
  return (
    <div className="avlog" data-testid="validator-output" data-state={result.state}>
      <InfoTooltip id="assurance.validator.log" block>
        <div style={{ fontSize: "var(--text-sm)", color: "var(--text-2)" }} data-testid="validator-facts">
          {"The newest recovery test of this set " + (STATE_WORDS[result.state] ?? "has the state " + result.state) +
            ", on the " + (result.medium || "unrecorded") + " copy of " + (result.artifactId || "an artifact") +
            (result.recordedAt ? ", recorded " + stamp(result.recordedAt) : "") + ". "}
          The engine records that verdict, not the script. What the script printed is shown below as plain text, and nothing in it changes the verdict.
        </div>
      </InfoTooltip>
      {output.length === 0 ? (
        <div data-testid="validator-empty" style={{ color: "var(--text)" }}>The validator printed nothing.</div>
      ) : (
        <pre className="avlog__pre" data-testid="validator-log" tabIndex={0} aria-label="Validator output">
          {inert(output.map((r) => r.text).join("\n"))}
        </pre>
      )}
      {marker ? (
        <div className="avlog__marker" data-testid="truncation-marker">{inert(marker.text)}</div>
      ) : null}
    </div>
  );
}
