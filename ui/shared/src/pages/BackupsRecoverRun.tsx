/**
 * The progress of one multi-file recovery (issue #1107): a line per backup, a
 * running total, one cancel, and a summary that says which failed and why.
 *
 * Cancel stops the backups that have not started. The one being read cannot be
 * taken back (the host is mid-comparison against its recorded digest and
 * either places the whole file or nothing), so it finishes and is reported as
 * whatever it became.
 */
import { useEffect, useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { recoverMany } from "@shared/assurance/recoverMany";
import type { RunItem, RunTarget } from "@shared/assurance/recoverMany";
import { bytes } from "@shared/utilities/format";

const WORDS: Record<RunItem["state"], string> = {
  queued: "Waiting",
  running: "Recovering",
  recovered: "Recovered",
  failed: "Failed",
  restoring: "Provider restore requested",
  cancelled: "Not started"
};

export function BackupsRecoverRun({
  files, target, acknowledged, onClose
}: {
  files: Array<{ id: string; filename: string }>;
  target: RunTarget;
  acknowledged: boolean;
  onClose(): void;
}) {
  const api = useApi();
  const [items, setItems] = useState<RunItem[]>(() => files.map((f) => ({ id: f.id, filename: f.filename, state: "queued" as const })));
  const [finished, setFinished] = useState(false);
  const stop = useRef(false);
  const [stopped, setStopped] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const started = useRef(false);

  useEffect(() => {
    const back = document.activeElement as HTMLElement | null;
    box.current?.focus();
    return () => back?.focus?.();
  }, []);

  useEffect(() => {
    // Strict mode runs effects twice; a recovery must start once.
    if (started.current) return;
    started.current = true;
    void recoverMany(api, files.map((f) => f.id), target, acknowledged, {
      update: (id, patch) => setItems((cur) => cur.map((x) => (x.id === id ? { ...x, ...patch } : x))),
      cancelled: () => stop.current
    }).then(() => setFinished(true));
  }, [api, files, target, acknowledged]);

  const n = (s: RunItem["state"]) => items.filter((x) => x.state === s).length;
  const done = n("recovered") + n("failed") + n("restoring");
  const failed = items.filter((x) => x.state === "failed");
  const bytesDone = items.reduce((t, x) => t + (x.bytes ?? 0), 0);

  return (
    <div className="dialog-scrim" style={{ gridTemplateColumns: "minmax(0, 1fr)" }}>
      <div
        ref={box}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby="bulk-run-title"
        className="dialog"
        data-phase={finished ? "finished" : "running"}
        style={{ maxWidth: 680, minWidth: 0 }}
      >
        <div style={{ padding: "20px 22px 0" }}>
          <h2 id="bulk-run-title" style={{ margin: 0, fontSize: 18 }}>Recovering backups</h2>
          <div className="mono" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)", marginTop: 4 }}>
            {target.kind === "staging" ? "Staging name " + target.where : "Destination " + target.where}
          </div>
        </div>
        <div style={{ padding: "14px 22px 20px", display: "flex", flexDirection: "column", gap: 12, fontSize: 13.5 }}>
          <div data-testid="run-progress" role="status" aria-live="polite">
            {finished
              ? ""
              : (stopped ? "Cancelling after the current backup: " : "") + (done + 1 > items.length ? items.length : done + 1) + " of " + items.length + " in progress · " + done + " done · " + bytes(bytesDone) + " placed"}
          </div>
          <ul style={{ listStyle: "none", margin: 0, padding: 0, maxHeight: 320, overflowY: "auto", display: "flex", flexDirection: "column", gap: 4 }}>
            {items.map((x) => (
              <li key={x.id} data-state={x.state} className="runitem">
                <span className="mono" style={{ wordBreak: "break-all" }}>{x.filename}</span>
                <span className="runitem__state">{WORDS[x.state]}</span>
                {x.state === "failed" ? <span className="runitem__why">{x.reason}</span> : null}
                {x.state === "recovered" ? (
                  <span className="mono runitem__why" data-testid="placed-digest">{"sha256:" + x.sha256}</span>
                ) : null}
              </li>
            ))}
          </ul>
          {finished ? (
            <div data-testid="run-summary" role="status" className="aop">
              <strong>
                {n("recovered") + " recovered, " + failed.length + " failed" +
                  (n("restoring") ? ", " + n("restoring") + " restore requested" : "") +
                  (n("cancelled") ? ", " + n("cancelled") + " not started (cancelled)" : "")}
              </strong>
              {n("restoring") ? <div>A restore was asked of the provider for each archived backup. Recover those again once they are readable.</div> : null}
              <div style={{ color: "var(--text-2)" }}>Each recovered file was matched to the digest recorded at ingest before the host placed it. Putting an application back stays yours to do.</div>
            </div>
          ) : null}
          {finished && failed.length > 0 ? (
            <div role="alert" className="runfail">
              <strong>{failed.length === 1 ? "1 backup was not recovered" : failed.length + " backups were not recovered"}</strong>
              <ul style={{ margin: "4px 0 0", paddingLeft: 18 }}>
                {failed.map((x) => <li key={x.id}><span className="mono">{x.filename}</span>{": " + x.reason}</li>)}
              </ul>
            </div>
          ) : null}
        </div>
        <div className="card__footer" style={{ display: "flex", justifyContent: "flex-end", gap: 9, borderRadius: "0 0 10px 10px" }}>
          {!finished ? (
            <button type="button" className="btn" disabled={stopped} onClick={() => { stop.current = true; setStopped(true); }}>Cancel remaining</button>
          ) : (
            <button type="button" className="btn btn--primary" onClick={onClose}>Close</button>
          )}
        </div>
      </div>
    </div>
  );
}
