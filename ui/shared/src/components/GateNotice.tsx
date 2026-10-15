/**
 * Said before anything is pressed: this deployment's destructive gate is
 * shut, so the control below will be refused, and here is what to run on the
 * host instead (issue #1165).
 *
 * It renders nothing unless the engine reported the gate shut. An open gate
 * and a gate whose state could not be read both draw nothing, which is what
 * keeps an open deployment's screens exactly as they were. The words come
 * from the field: `reading.reason` is the engine's, and nothing here carries
 * a sentence of its own about why.
 *
 * It does not disable the control it sits above. The control still has to be
 * reachable for the engine to refuse it in its own words, and a gate that
 * opens mid-session should not leave a button greyed out.
 */
import { Banner } from "@shared/components/Banner";
import type { GateReading } from "@shared/hooks/useDestructiveGate";

export function GateNotice({ gate, headline, command }: { gate: GateReading; headline: string; command: string }) {
  if (gate.state !== "shut") return null;
  return (
    <div data-gate-notice data-gate-state="shut" style={{ marginBottom: 14 }}>
      <Banner tone="info" role="status" dismissible={false} style={{ flexDirection: "column", alignItems: "stretch", gap: 8 }}>
        <div style={{ fontWeight: 600, fontSize: 13.5 }}>{headline}</div>
        <div style={{ fontSize: "var(--text-sm)" }}>
          {(gate.reason !== "" ? gate.reason + ". " : "") + "Pressing the control will not work. Run this on the host instead:"}
        </div>
        <code
          className="mono"
          style={{
            display: "block",
            fontSize: "var(--text-xs)",
            color: "var(--text)",
            background: "var(--surface-2)",
            border: "1px solid var(--border)",
            borderRadius: "var(--radius-md)",
            padding: "6px 9px",
            overflowWrap: "anywhere",
            userSelect: "all"
          }}
        >
          {command}
        </code>
      </Banner>
    </div>
  );
}
