/**
 * A validator's script state on a row of the Settings card (issue #1230): the
 * state in words first, the pin when there is one, and what to do when the
 * script cannot run. The two failure states are bold and in the danger colour,
 * but the words carry them: nobody has to tell two colours apart.
 */
import type { CustomValidator } from "@shared/api/contracts";
import { scriptStateWords, shortHash } from "./customValidators";

export function ScriptStateCell({ v }: { v: CustomValidator }) {
  const words = scriptStateWords(v.scriptState);
  return (
    <div data-script-state={v.scriptState} role={words.failing ? "status" : undefined} style={{ minWidth: 180 }}>
      <span
        data-script-state-label=""
        style={{ color: words.failing ? "var(--danger)" : "var(--text)", fontWeight: words.failing ? 700 : 400 }}
      >
        {words.label}
      </span>
      {v.scriptState === "pinned" ? (
        <div className="mono" style={{ fontSize: "var(--text-xs)", color: "var(--text-2)" }}>sha256 {shortHash(v.pinnedSha256)}</div>
      ) : null}
      {words.action ? (
        <div style={{ fontSize: "var(--text-xs)", color: words.failing ? "var(--danger)" : "var(--text-2)", marginTop: 2 }}>
          {words.action}
        </div>
      ) : null}
    </div>
  );
}
