/**
 * The Agents card on Settings (EPIC U, issue #1351; design
 * docs/design/1350-agents.html).
 *
 * One card in the shape of the Retained-copy encryption card: an eyebrow
 * heading carrying its tooltip id, a statement, then four regions separated by
 * rules, in the order a person uses them: Tokens (who can connect), Pending
 * confirmations (what needs me now), Audit (what happened), and the HTTP
 * transport statement, which is a fact about the deployment and not a task.
 * When something is waiting the regions are NOT reordered (a list that moves
 * itself moves Approve out from under a cursor already travelling to it);
 * the heading carries a count that links to the region instead.
 *
 * # Every region fails on its own
 *
 * Tokens, confirmations, the catalogue and the audit are separate routes, so a
 * failed audit leaves the tokens drawn.
 *
 * # What it will not do
 *
 * No transport toggle (nothing patches `agents:` from the browser, and the
 * field is a one-way door for an older build), no per-tool switch, no token
 * edit, no "always allow", no bulk approve, no request body. See section 12 of
 * the design note.
 *
 * # The transport line says no more than the code can read
 *
 * No route carries whether the HTTP transport is on, and the MCP endpoint
 * refuses a browser's cookie for another reason, so the card cannot probe it.
 * Design 1350 section 9 allows drawing neither state in that case, and the one
 * sentence it draws is true in both: off is the default, and the configuration
 * file is where it turns on.
 *
 * # No claim the engine has not earned
 *
 * The statement says what an agent is, what it can do by default, what it can
 * never do without you, and what it is never handed. It says nothing about
 * safety: the words are FR-92, FR-95, FR-97 and FR-98 and nothing stronger.
 */
import { useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { useAsync } from "@shared/hooks/useAsync";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { AuditRegion } from "./AuditRegion";
import { ConfirmationsRegion } from "./ConfirmationsRegion";
import { TokensRegion } from "./TokensRegion";
import "./agents.css";

export function AgentsCard({ readOnly }: { readOnly: boolean }) {
  const api = useApi();
  const tokens = useAsync(() => api.listAgentTokens(), [api]);
  const catalogue = useAsync(() => api.listAgentCatalogue(), [api]);
  const confirmations = useAsync(() => api.listAgentConfirmations(), [api]);
  // Bumped when something the audit shows has changed.
  const [auditRevision, setAuditRevision] = useState(0);
  const changed = () => setAuditRevision((n) => n + 1);
  const waiting = confirmations.data?.length ?? 0;

  return (
    <section className="card" aria-labelledby="agents-heading" data-agents-card="">
      <div className="card__header">
        <InfoTooltip id="agents.card">
          <h2 className="eyebrow" id="agents-heading">Agents</h2>
        </InfoTooltip>
        {waiting > 0 ? (
          <a className="ag-link" href="#agents-confirmations" data-agents-waiting="">{waiting} waiting for you</a>
        ) : null}
      </div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 16 }}>
        <p className="ag-statement" data-agents-statement="">
          An agent is a client of this deployment with a token of its own. By default it can look, and nothing else.
          Anything an <code className="mono">operate</code> or <code className="mono">configure</code> token asks for waits
          in the confirmations list until you approve it with your password. An agent is not given key material,
          recipients, credentials or secrets, and the audit records every call it makes, reads included.
        </p>

        <TokensRegion tokens={tokens} catalogue={catalogue} readOnly={readOnly} onChanged={() => { confirmations.reload(); changed(); }} />
        <ConfirmationsRegion
          confirmations={confirmations}
          catalogue={catalogue}
          readOnly={readOnly}
          onDecided={() => { tokens.reload(); changed(); }}
        />
        <AuditRegion tokens={tokens.data ?? []} tools={catalogue.data?.tools ?? []} revision={auditRevision} />

        <p className="ag-note" data-agents-transport="">
          Agents connect with <code className="mono">retnd agent serve --stdio</code>, which runs beside this deployment.
          The HTTP transport is off unless the configuration file sets <code className="mono">http_transport: on</code>{" "}
          under <code className="mono">agents:</code>. This page does not change it: a configuration that writes it cannot
          be read by an older build.
        </p>
      </div>
    </section>
  );
}
