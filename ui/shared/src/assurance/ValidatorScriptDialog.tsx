/**
 * An uploaded validator's script, read back (issue #1203). Read only: it shows
 * the bytes the engine would run, with the digest and size that identify them,
 * so an operator can check that what is on the host is what they uploaded.
 */
import { useRef } from "react";
import type { ValidatorScript } from "@shared/api/contracts";
import { useDialogFocus } from "./useDialogFocus";

export function ValidatorScriptDialog({ name, script, onClose }: { name: string; script: ValidatorScript; onClose(): void }) {
  const box = useRef<HTMLDivElement>(null);
  useDialogFocus(box, onClose);
  return (
    <div className="dialog-scrim" style={{ gridTemplateColumns: "minmax(0, 1fr)" }} onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div
        ref={box}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby="validator-script-title"
        className="dialog"
        data-assurance=""
        style={{ maxWidth: 720, minWidth: 0, maxHeight: "calc(100vh - 48px)", overflowY: "auto" }}
      >
        <div style={{ padding: "20px 22px 0" }}>
          <h2 id="validator-script-title" style={{ margin: 0, fontSize: 18 }}>Script {name}</h2>
          <div className="mono" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)", marginTop: 4, wordBreak: "break-all" }}>
            {script.fileName} · {script.sizeBytes} bytes · sha256 <span data-script-sha256="">{script.sha256}</span>
          </div>
        </div>
        <div style={{ padding: "14px 22px 18px" }}>
          <pre
            className="mono"
            tabIndex={0}
            aria-label={"Contents of " + script.fileName}
            style={{
              margin: 0, padding: 12, maxHeight: 420, overflow: "auto", fontSize: "var(--text-xs)", color: "var(--text)",
              background: "var(--surface-2)", border: "1px solid var(--border-strong)", borderRadius: "var(--radius-md)", whiteSpace: "pre"
            }}
          >
            {script.content}
          </pre>
        </div>
        <div className="card__footer" style={{ display: "flex", justifyContent: "flex-end", gap: 9, borderRadius: "0 0 10px 10px" }}>
          <button className="btn btn--primary" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  );
}
