/**
 * One assurance operation's live state: stage, bytes, the durable lines, and
 * cancel (issue #985).
 *
 * Everything shown is read off the feed through useOperation, so the same
 * panel is what a reload mounts and what a fresh start mounts. `data-stage`,
 * `data-bytes` and `data-state` publish what is drawn so a test can assert it.
 *
 * The outcome of a finished operation says what it was. A cancelled run is
 * "Cancelled", a failed one "Failed", and neither is drawn like a pass.
 *
 * A run read back from the durable record (#1200) is drawn the same way, from
 * its own lines: the start, the stage it reached and how it ended, with the
 * count and the bar at that stage. The panel never draws an empty terminal:
 * with no line to show it says so in words, and the Following toggle is only
 * offered while there is something live to follow.
 *
 * Cancel is a request, and the panel says so: once the service has accepted
 * one the run is "Cancelling" until its own end line arrives, and the button
 * is spent. A cancel the service refuses is shown in its words. Beside a
 * running operation the panel prints the terminal command that stops it
 * (`retnd assurance cancel <id>`), the same way the set page prints the
 * command for each button.
 */
import "./assurance.css";
import { Link } from "react-router-dom";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { bytes } from "@shared/utilities/format";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { assuranceCancelCommand } from "./assuranceCommands";
import { stageLabel, stageProgress } from "./stages";
import type { Operation, OperationEvent } from "./types";

const KIND_LABEL = { scrub: "Scrub", recovery_test: "Recovery test", recovery: "Recovery" } as const;
/** Where a finished run's answer lands, said when the page can no longer see it. */
const LANDS_ON = { scrub: "Content", recovery_test: "Recovery", recovery: "Recovery" } as const;

export function OperationPanel({
  operation, events, error, onCancel, readOnly, detailLink = true, cancellable = true, following = true, onFollowing,
  cancelError = null, terminalHref
}: {
  operation: Operation | null;
  events: OperationEvent[];
  error: string | null;
  onCancel(): void;
  readOnly?: boolean;
  detailLink?: boolean;
  /** False where the server offers no route to cancel, so no Cancel button
   *  is drawn that could only fail. */
  cancellable?: boolean;
  following?: boolean;
  /** Present on the detail view, which owns the Following toggle. */
  onFollowing?: (on: boolean) => void;
  /** The service's own words for a cancel it refused. */
  cancelError?: string | null;
  /** Where the run's output already is (the set's own terminal). When given,
   *  the panel draws no log of its own and points there instead, so one
   *  stream is never drawn twice (#1176). */
  terminalHref?: string;
}) {
  if (!operation) {
    return error ? <div role="alert" className="aop">{"Could not read progress: " + error}</div> : null;
  }
  const running = operation.state === "running";
  // Bytes when the run reports them; otherwise its position in the engine's
  // own stage list, because a recovery test names its stages on the feed and
  // sends no running byte total (#1176). Neither: the bar stays indeterminate.
  const staged = operation.totalBytes > 0 ? null : stageProgress(operation.kind, operation.stage);
  // A scrub names no stages, so its bar has no position while it runs; once
  // its pass line is in (it finished, found problems or not) the pass is
  // over and the bar is full, not left at 0% (#1200).
  const scrubDone = operation.kind === "scrub" && (operation.state === "complete" || operation.state === "failed");
  const pct = scrubDone ? 100 : operation.totalBytes > 0 ? Math.round((operation.bytes / operation.totalBytes) * 100) : staged?.pct ?? 0;
  const determinate = scrubDone || operation.totalBytes > 0 || staged !== null;
  const outcome =
    operation.state === "complete" ? KIND_LABEL[operation.kind] + " finished"
    : operation.state === "failed" ? KIND_LABEL[operation.kind] + " failed"
    : operation.state === "cancelled" ? KIND_LABEL[operation.kind] + " cancelled"
    : operation.state === "untracked" ? KIND_LABEL[operation.kind] + " is no longer on the live feed. Its result lands on the " + LANDS_ON[operation.kind] + " chip."
    : operation.state === "unfinished" ? KIND_LABEL[operation.kind] + " has no end on record. It may still be running in another process, or the process running it stopped before it finished."
    : null;
  return (
    <div
      className="aop"
      data-testid="operation-progress"
      data-state={operation.state}
      data-stage={operation.stage}
      data-bytes={operation.bytes}
      data-operation-id={operation.id}
      data-cancelling={operation.cancelling ? "true" : undefined}
    >
      <div style={{ display: "flex", gap: 8, alignItems: "baseline", flexWrap: "wrap" }}>
        <strong>{KIND_LABEL[operation.kind]}</strong>
        {running
          ? <span data-testid="stage-label">{operation.kind === "scrub" ? "Checking the set's copies" : "Stage: " + stageLabel(operation.stage)}</span>
          : <span className="aop__outcome" role="status">{outcome}</span>}
        {running && operation.cancelling ? <span className="aop__outcome" role="status" data-testid="operation-cancelling">Cancelling</span> : null}
        <span style={{ flex: 1 }} />
        {operation.totalBytes > 0 ? (
          <span className="mono" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
            {bytes(operation.bytes) + " of " + bytes(operation.totalBytes)}
          </span>
        ) : staged?.stage ? (
          <span className="mono" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)" }} data-testid="stage-count">
            {"Stage " + staged.stage + " of " + staged.of}
          </span>
        ) : null}
      </div>
      <div
        className="aop__bar"
        role="progressbar"
        aria-label={KIND_LABEL[operation.kind] + " progress"}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={determinate ? pct : undefined}
        data-indeterminate={!determinate && running ? "" : undefined}
      >
        <i style={determinate || !running ? { width: pct + "%" } : undefined} />
      </div>
      {operation.joined ? (
        <div data-testid="operation-joined" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
          A {KIND_LABEL[operation.kind].toLowerCase()} was already running for this set, so this joined it. Nothing new was started.
        </div>
      ) : null}
      {error ? <div role="status" style={{ fontSize: "var(--text-sm)" }}>{"Connection lost, retrying: " + error}</div> : null}
      {terminalHref ? (
        <a
          href={terminalHref}
          data-testid="set-terminal-link"
          onClick={(e) => {
            // Focus the terminal in place: a hash change alone would scroll
            // but leave keyboard focus behind in this card.
            const target = document.getElementById(terminalHref.replace(/^#/, ""));
            if (!target) return;
            e.preventDefault();
            target.focus();
            target.scrollIntoView({ block: "center" });
          }}
        >
          Output is in the set terminal
        </a>
      ) : (
        events.length > 0 ? (
          <ul className="aop__lines" aria-label="Operation lines" ref={(el) => { if (el && following) el.scrollTop = el.scrollHeight; }}>
            {events.map((e) => (
              <li key={e.seq} data-testid="operation-line" data-seq={e.seq}>{e.text}</li>
            ))}
          </ul>
        ) : (
          // Never an empty terminal strip (#1200): no line is a fact worth
          // saying, not a black box to stare at.
          <div className="aop__quiet" data-testid="operation-no-lines">
            {running ? "Waiting for the first line of this run." : "No line of this run is on record."}
          </div>
        )
      )}
      <div style={{ display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        {onFollowing && running ? (
          <InfoTooltip id="assurance.operation.follow">
            <label style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
              <input type="checkbox" checked={following} onChange={(e) => onFollowing(e.target.checked)} />
              Following
            </label>
          </InfoTooltip>
        ) : null}
        {detailLink ? (
          <InfoTooltip id="assurance.operation.detail">
            <Link to={"/assurance/operations/" + encodeURIComponent(operation.id)}>Operation detail</Link>
          </InfoTooltip>
        ) : null}
        <span style={{ flex: 1 }} />
        {running && cancellable ? (
          <InfoTooltip id="assurance.operation.cancel">
            <button className="btn btn--sm btn--destructive" disabled={readOnly || !!operation.cancelling} onClick={onCancel}>Cancel</button>
          </InfoTooltip>
        ) : null}
      </div>
      {cancelError ? <div role="alert" className="aop__alert">{cancelError}</div> : null}
      {running && cancellable ? <CommandEcho label="Stop it from the terminal" commands={[assuranceCancelCommand(operation.id)]} /> : null}
    </div>
  );
}
