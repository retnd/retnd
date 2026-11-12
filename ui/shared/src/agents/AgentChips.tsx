/**
 * The scope, status and outcome chips of the Agents card (design 1350,
 * sections 5 and 7). The ink is always the body text: the state is carried by
 * the glyph, the edge style and the word, and colour is the redundant fourth
 * cue. Text glyphs only, never emoji, so nothing is drawn in its own colours.
 */
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { TooltipId } from "@shared/tooltips/tooltips";
import type { AgentAuditRow, AgentScope, AgentTokenStatus } from "@shared/types/agents";
import "./agents.css";

const SCOPE_GLYPH: Record<AgentScope, string> = { read: "○", operate: "▶", configure: "≡" };
const SCOPE_TIP: Record<AgentScope, TooltipId> = {
  read: "agents.scope.read", operate: "agents.scope.operate", configure: "agents.scope.configure"
};
const STATUS_TIP: Record<AgentTokenStatus, TooltipId> = {
  active: "agents.status.active", expired: "agents.status.expired", revoked: "agents.status.revoked"
};
const STATUS_GLYPH: Record<AgentTokenStatus, string> = { active: "✓", expired: "◷", revoked: "⊘" };

export function ScopeChip({ scope }: { scope: AgentScope }) {
  return (
    <InfoTooltip id={SCOPE_TIP[scope]}>
      <span className="agchip" data-chip="scope" data-scope={scope}>
        <span className="agchip__glyph" aria-hidden="true">{SCOPE_GLYPH[scope]}</span>
        {scope}
      </span>
    </InfoTooltip>
  );
}

export function StatusChip({ status }: { status: AgentTokenStatus }) {
  return (
    <InfoTooltip id={STATUS_TIP[status]}>
      <span className="agchip" data-chip="status" data-state={status}>
        <span className="agchip__glyph" aria-hidden="true">{STATUS_GLYPH[status]}</span>
        {status}
      </span>
    </InfoTooltip>
  );
}

/** What the API answered for a call: a success, a call waiting for a person, or a refusal. */
export function OutcomeChip({ row }: { row: Pick<AgentAuditRow, "status" | "code"> }) {
  const waiting = row.code === "AGENT_CONFIRMATION_REQUIRED";
  const state = waiting ? "waiting" : row.status < 400 && row.code === "" ? "ok" : "refused";
  const glyph = waiting ? "◇" : state === "ok" ? "✓" : "✕";
  return (
    <InfoTooltip id="agents.audit.outcome">
      <span className="agchip" data-chip="outcome" data-state={state}>
        <span className="agchip__glyph" aria-hidden="true">{glyph}</span>
        {row.status}
        {row.code !== "" ? " " + row.code : ""}
      </span>
    </InfoTooltip>
  );
}
