/**
 * An explicit "I understand" tick that a destructive act waits for
 * (issues #1163 and #1164): a restore writing into a directory, a hold being
 * released. The act's button stays off until it is ticked, and the tick is
 * sent in the request, so the engine can refuse an act whose client skipped
 * it.
 */
import type { ReactNode } from "react";

export function Acknowledgement({
  checked,
  onChange,
  children,
  disabled = false
}: {
  checked: boolean;
  onChange(next: boolean): void;
  children: ReactNode;
  /** Switched off with the reason said elsewhere (a shut gate). */
  disabled?: boolean;
}) {
  return (
    <label style={{ display: "flex", gap: 8, alignItems: "flex-start", fontSize: "var(--text-sm)", color: "var(--text)", cursor: "pointer" }}>
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        style={{ marginTop: 3 }}
      />
      <span>{children}</span>
    </label>
  );
}
