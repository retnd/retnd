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
 */
import "./assurance.css";
import { Link } from "react-router-dom";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { bytes } from "@shared/utilities/format";
import { cancelOperationCommand } from "./assuranceCommands";
import type { Operation, OperationEvent } from "./types";

const KIND_LABEL = { scrub: "Scrub", recovery_test: "Recovery test" } as const;

export function OperationPanel({
  operation, events, error, onCancel, readOnly, detailLink = true, following = true, onFollowing
}: {
  operation: Operation | null;
  events: OperationEvent[];
  error: string | null;
  onCancel(): void;
  readOnly?: boolean;
  detailLink?: boolean;
  following?: boolean;
  /** Present on the detail view, which owns the Following toggle. */
  onFollowing?: (on: boolean) => void;
}) {
  if (!operation) {
    return error ? <div role="alert" className="aop">{"Could not read progress: " + error}</div> : null;
  }
  const running = operation.state === "running";
  const pct = operation.totalBytes > 0 ? Math.round((operation.bytes / operation.totalBytes) * 100) : 0;
  const outcome =
    operation.state === "complete" ? KIND_LABEL[operation.kind] + " finished"
    : operation.state === "failed" ? KIND_LABEL[operation.kind] + " failed"
    : operation.state === "cancelled" ? KIND_LABEL[operation.kind] + " cancelled"
    : null;
  return (
    <div
      className="aop"
      data-testid="operation-progress"
      data-state={operation.state}
      data-stage={operation.stage}
      data-bytes={operation.bytes}
      data-operation-id={operation.id}
    >
      <div style={{ display: "flex", gap: 8, alignItems: "baseline", flexWrap: "wrap" }}>
        <strong>{KIND_LABEL[operation.kind]}</strong>
        {running ? <span>{"stage: " + operation.stage}</span> : <span className="aop__outcome">{outcome}</span>}
        <span style={{ flex: 1 }} />
        <span className="mono" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
          {bytes(operation.bytes) + " of " + bytes(operation.totalBytes)}
        </span>
      </div>
      <div
        className="aop__bar"
        role="progressbar"
        aria-label={KIND_LABEL[operation.kind] + " progress"}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={pct}
      >
        <i style={{ width: pct + "%" }} />
      </div>
      {error ? <div role="status" style={{ fontSize: "var(--text-sm)" }}>{"Connection lost, retrying: " + error}</div> : null}
      <ul className="aop__lines" aria-label="Operation lines" ref={(el) => { if (el && following) el.scrollTop = el.scrollHeight; }}>
        {events.map((e) => (
          <li key={e.seq} data-testid="operation-line" data-seq={e.seq}>{e.text}</li>
        ))}
      </ul>
      <div style={{ display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        {onFollowing ? (
          <InfoTooltip id="assurance.operation.follow">
            <label style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
              <input type="checkbox" checked={following} onChange={(e) => onFollowing(e.target.checked)} />
              Following
            </label>
          </InfoTooltip>
        ) : null}
        {detailLink ? <Link to={"/assurance/operations/" + encodeURIComponent(operation.id)}>Operation detail</Link> : null}
        <span style={{ flex: 1 }} />
        {running ? (
          <InfoTooltip id="assurance.operation.cancel">
            <button className="btn btn--sm btn--destructive" disabled={readOnly} onClick={onCancel}>Cancel</button>
          </InfoTooltip>
        ) : null}
      </div>
      {running ? <CommandEcho label="Same thing from the terminal" commands={[cancelOperationCommand(operation.id)]} /> : null}
    </div>
  );
}
