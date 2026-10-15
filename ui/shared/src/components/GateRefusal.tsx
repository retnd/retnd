/**
 * What a screen says when the destructive gate refuses an act it offered
 * (issue #1157, and #1165 for where the reason comes from).
 *
 * It is a notice, not an error panel, and it has no Try again, deliberately:
 * the gate is shut in every shipped build, so a retry is the one thing that
 * cannot help. It says what was refused, that nothing changed, and gives the
 * command that does the same act from a shell on the host, in selectable
 * text so it can be copied.
 *
 * `data-gate-refusal` is how the browser specs find it, and it wraps the
 * whole notice so a contrast measurement covers every word in it.
 */
import { Banner } from "@shared/components/Banner";
import type { GateRefusalNotice } from "@shared/api/gate";

export function GateRefusal({ notice }: { notice: GateRefusalNotice }) {
  return (
    <div data-gate-refusal>
      <Banner tone="warn" role="alert" dismissible={false} style={{ flexDirection: "column", alignItems: "stretch", gap: 8 }}>
        <div style={{ fontWeight: 600, fontSize: 13.5 }}>{notice.headline}</div>
        <div style={{ fontSize: "var(--text-sm)" }}>{notice.why}</div>
        <div style={{ fontSize: "var(--text-sm)" }}>Run this on the host instead:</div>
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
          {notice.command}
        </code>
        {notice.correlationId ? (
          <div style={{ fontSize: "var(--text-xs)", color: "var(--text-2)" }}>
            {"Correlation id " + notice.correlationId}
          </div>
        ) : null}
      </Banner>
    </div>
  );
}
