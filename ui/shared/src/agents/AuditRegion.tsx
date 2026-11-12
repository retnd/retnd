/**
 * The audit (design 1350, section 7): newest first, filtered by token and
 * time, reads included, paged by the cursor the route gives.
 *
 * Six columns, plus a Details disclosure for the two fields the table has no
 * room for. There is no exit-code column (the audit row does not store one), no
 * total and no page number (the route is a cursor, not an offset), and no
 * infinite scroll: Show older appends explicitly, so the row under the pointer
 * does not move. The arguments are text an agent supplied, so they are drawn
 * as inert text in a bounded block, never as HTML and never linkified.
 */
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { describeFailure, apiErrorOf } from "@shared/api/failure";
import { ErrorState } from "@shared/components/EmptyState";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { AgentAuditRow, AgentTool, AgentToken } from "@shared/types/agents";
import { stamp } from "@shared/utilities/format";
import { OutcomeChip } from "./AgentChips";
import { auditCommand } from "./agentCommands";
import "./agents.css";

type Failure = { message: string; remediation?: string; correlationId?: string };

export function AuditRegion({ tokens, tools, revision }: { tokens: AgentToken[]; tools: AgentTool[]; revision: number }) {
  const api = useApi();
  const [tokenId, setTokenId] = useState("");
  const [sinceDraft, setSinceDraft] = useState("");
  const [since, setSince] = useState("");
  const [rows, setRows] = useState<AgentAuditRow[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [older, setOlder] = useState(false);
  const [open, setOpen] = useState<ReadonlySet<number>>(new Set());
  const [nonce, setNonce] = useState(0);
  const latest = useRef(0);
  const tokenSelectId = useId();
  const sinceId = useId();

  // One read per filter or revision: the first page, replacing what is shown.
  useEffect(() => {
    const mine = ++latest.current;
    api.listAgentAudit({ tokenId: tokenId || undefined, since: since || undefined }).then(
      (page) => {
        if (mine !== latest.current) return;
        setRows(page.rows);
        setCursor(page.nextCursor);
        setFailure(null);
        setOpen(new Set());
      },
      (e) => {
        if (mine !== latest.current) return;
        const err = apiErrorOf(e);
        const f = describeFailure(e, "the audit could not be read");
        setFailure({ message: f.message, remediation: err?.remediation, correlationId: err?.correlationId });
      }
    );
  }, [api, tokenId, since, revision, nonce]);

  const showOlder = useCallback(async () => {
    if (!cursor) return;
    setOlder(true);
    try {
      const page = await api.listAgentAudit({ tokenId: tokenId || undefined, since: since || undefined, cursor });
      setRows((r) => [...(r ?? []), ...page.rows]);
      setCursor(page.nextCursor);
    } catch (e) {
      const err = apiErrorOf(e);
      setFailure({ message: describeFailure(e, "older calls could not be read").message, correlationId: err?.correlationId });
    } finally {
      setOlder(false);
    }
  }, [api, cursor, tokenId, since]);

  const toggle = (seq: number) =>
    setOpen((o) => { const n = new Set(o); if (n.has(seq)) n.delete(seq); else n.add(seq); return n; });
  const filtered = tokenId !== "" || since !== "";
  const commitSince = () => setSince(sinceDraft.trim());

  return (
    <div className="ag-region" data-agents-region="audit">
      <div className="ag-region__head">
        <InfoTooltip id="agents.audit"><h3 className="ag-region__title">Audit</h3></InfoTooltip>
      </div>

      <form className="ag-filters" onSubmit={(e) => { e.preventDefault(); commitSince(); }}>
        <FieldHelp label="Token" help={FIELD_HELP.agentsAuditToken}>
          {(helpId) => (
            <div className="field">
              <label htmlFor={tokenSelectId}><InfoTooltip id="agents.audit.token"><span className="field__label">Token</span></InfoTooltip></label>
              <select id={tokenSelectId} className="select input--strong-edge" value={tokenId} aria-describedby={helpId} onChange={(e) => setTokenId(e.target.value)}>
                <option value="">All tokens</option>
                {tokens.map((t) => <option key={t.id} value={t.id}>agent:{t.name}</option>)}
              </select>
            </div>
          )}
        </FieldHelp>
        <FieldHelp label="Since" help={FIELD_HELP.agentsAuditSince}>
          {(helpId) => (
            <div className="field">
              <label htmlFor={sinceId}><InfoTooltip id="agents.audit.since"><span className="field__label">Since</span></InfoTooltip></label>
              <input
                id={sinceId}
                className="input input--mono input--strong-edge"
                value={sinceDraft}
                placeholder="24h"
                autoComplete="off"
                spellCheck={false}
                aria-describedby={helpId}
                onChange={(e) => setSinceDraft(e.target.value)}
                onBlur={commitSince}
              />
            </div>
          )}
        </FieldHelp>
      </form>
      <CommandEcho label="Equivalent command" commands={[auditCommand({ tokenId: tokenId || undefined, since: since || undefined })]} />

      {failure && rows === null ? (
        <ErrorState message={failure.message} remediation={failure.remediation} correlationId={failure.correlationId} onRetry={() => { setFailure(null); setNonce((n) => n + 1); }} />
      ) : rows === null ? (
        <p className="ag-note">Reading the audit...</p>
      ) : rows.length === 0 ? (
        filtered ? (
          <div data-agents-empty="audit-filter">
            <p className="ag-statement">No calls match this filter.</p>
            <button type="button" className="btn btn--sm" onClick={() => { setTokenId(""); setSinceDraft(""); setSince(""); }}>Clear filters</button>
          </div>
        ) : (
          <p className="ag-statement" data-agents-empty="audit">No agent has called anything yet.</p>
        )
      ) : (
        <>
          <table className="ag-table" data-agents-table="audit">
            <caption className="visually-hidden">Agent calls, newest first</caption>
            <thead>
              <tr>
                <th scope="col">Time</th>
                <th scope="col">Token</th>
                <th scope="col">Tool</th>
                <th scope="col">Needs</th>
                <th scope="col">Outcome</th>
                <th scope="col">Correlation id</th>
                <th scope="col"><span className="visually-hidden">Details</span></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <AuditRow
                  key={r.seq}
                  r={r}
                  who={r.tokenId === "" ? "no token matched" : r.tokenName === "" ? r.tokenId : "agent:" + r.tokenName}
                  needs={tools.find((t) => t.name === r.tool)?.scope ?? ""}
                  open={open.has(r.seq)}
                  onToggle={() => toggle(r.seq)}
                />
              ))}
            </tbody>
          </table>
          {failure ? <p className="ag-err" role="alert" data-audit-error="">{failure.message}</p> : null}
          {cursor ? (
            <InfoTooltip id="agents.audit.older">
              <button type="button" className="btn btn--strong-edge" style={{ alignSelf: "flex-start" }} disabled={older} onClick={() => void showOlder()}>Show older</button>
            </InfoTooltip>
          ) : null}
        </>
      )}
      <p className="ag-note">The trail is bounded: older rows are pruned.</p>
    </div>
  );
}

function AuditRow({ r, who, needs, open, onToggle }: { r: AgentAuditRow; who: string; needs: string; open: boolean; onToggle(): void }) {
  const rowId = "audit-details-" + r.seq;
  return (
    <>
      <tr data-audit-row={r.seq} data-tool={r.tool}>
        <td data-label="Time">{stamp(r.at)}</td>
        <td data-label="Token" className="mono">{who}</td>
        <td data-label="Tool" className="ag-cell-mono">{r.tool === "" ? "no tool" : r.tool}</td>
        <td data-label="Needs">{needs}</td>
        <td data-label="Outcome"><OutcomeChip row={r} /></td>
        <td data-label="Correlation id" className="ag-cell-mono">{r.correlationId}</td>
        <td>
          <InfoTooltip id="agents.audit.details">
            <button type="button" className="btn btn--quiet btn--sm" aria-expanded={open} aria-controls={rowId} aria-label={"Details of " + r.tool + " " + r.correlationId} onClick={onToggle}>
              Details
            </button>
          </InfoTooltip>
        </td>
      </tr>
      {open ? (
        <tr id={rowId} data-audit-details={r.seq}>
          <td colSpan={7}>
            <div className="ag-row__meta">
              <span>transport <span className="mono" data-audit-transport="">{r.transport}</span></span>
            </div>
            {r.arguments !== "" ? <pre className="ag-args" data-audit-arguments="">{r.arguments}</pre> : null}
          </td>
        </tr>
      ) : null}
    </>
  );
}
