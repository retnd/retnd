/**
 * The scope, status and outcome chips of the Agents card (design 1350,
 * sections 5 and 7). The ink is always the body text: the state is carried by
 * the glyph, the edge style and the word, and colour is the redundant fourth
 * cue. A mark is the design system's icon artwork where one exists (the
 * retired geometric characters are gone from the shipped UI, #621) and a
 * plain text glyph otherwise, never emoji, so nothing is drawn in its own
 * colours: the artwork fills with currentColor.
 */
import { Icon, type IconName } from "@shared/design-system/icons";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { TooltipId } from "@shared/tooltips/tooltips";
import type { AgentAuditRow, AgentScope, AgentTokenStatus } from "@shared/types/agents";
import "./agents.css";

/** A chip's mark: icon artwork by name, or a text glyph that is not one of
 *  the retired icon characters. */
type Mark = { icon: IconName } | { text: string };

function MarkGlyph({ mark }: { mark: Mark }) {
  return (
    <span className="agchip__glyph" aria-hidden="true">
      {"icon" in mark ? <Icon name={mark.icon} size="0.9em" /> : mark.text}
    </span>
  );
}

const SCOPE_MARK: Record<AgentScope, Mark> = { read: { icon: "status-idle" }, operate: { text: "▶" }, configure: { icon: "settings" } };
const SCOPE_TIP: Record<AgentScope, TooltipId> = {
  read: "agents.scope.read", operate: "agents.scope.operate", configure: "agents.scope.configure"
};
const STATUS_TIP: Record<AgentTokenStatus, TooltipId> = {
  active: "agents.status.active", expired: "agents.status.expired", revoked: "agents.status.revoked"
};
const STATUS_MARK: Record<AgentTokenStatus, Mark> = { active: { icon: "success" }, expired: { text: "◷" }, revoked: { icon: "failure" } };

export function ScopeChip({ scope }: { scope: AgentScope }) {
  return (
    <InfoTooltip id={SCOPE_TIP[scope]}>
      <span className="agchip" data-chip="scope" data-scope={scope}>
        <MarkGlyph mark={SCOPE_MARK[scope]} />
        {scope}
      </span>
    </InfoTooltip>
  );
}

export function StatusChip({ status }: { status: AgentTokenStatus }) {
  return (
    <InfoTooltip id={STATUS_TIP[status]}>
      <span className="agchip" data-chip="status" data-state={status}>
        <MarkGlyph mark={STATUS_MARK[status]} />
        {status}
      </span>
    </InfoTooltip>
  );
}

/** What the API answered for a call: a success, a call waiting for a person, or a refusal. */
export function OutcomeChip({ row }: { row: Pick<AgentAuditRow, "status" | "code"> }) {
  const waiting = row.code === "AGENT_CONFIRMATION_REQUIRED";
  const state = waiting ? "waiting" : row.status < 400 && row.code === "" ? "ok" : "refused";
  const mark: Mark = waiting ? { text: "◷" } : state === "ok" ? { icon: "success" } : { icon: "failure" };
  return (
    <InfoTooltip id="agents.audit.outcome">
      <span className="agchip" data-chip="outcome" data-state={state} {...(state === "refused" ? { "data-agent-refusal": "" } : {})}>
        <MarkGlyph mark={mark} />
        {row.status}
        {row.code !== "" ? " " + row.code : ""}
      </span>
    </InfoTooltip>
  );
}
