/**
 * The Tokens region of the Agents card (design 1350, sections 2, 5 and 8):
 * the scope disclosure read from the catalogue, the nine-column table, and
 * Revoke, which asks first. A token has no edit: its name, scope and expiry
 * cannot change after minting, so the row has no pencil, no extend button and
 * no scope switcher.
 */
import { useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { describeFailure } from "@shared/api/failure";
import { ConfirmationDialog } from "@shared/components/ConfirmationDialog";
import { ErrorState } from "@shared/components/EmptyState";
import type { AsyncState } from "@shared/hooks/useAsync";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { AgentCatalogue, AgentScope, AgentToken } from "@shared/types/agents";
import { stamp } from "@shared/utilities/format";
import { ScopeChip, StatusChip } from "./AgentChips";
import { revokeCommand } from "./agentCommands";
import { relativeTime, useNow } from "./agentTime";
import { MintDialog } from "./MintDialog";
import "./agents.css";

const SCOPES: AgentScope[] = ["read", "operate", "configure"];

export function TokensRegion({
  tokens, catalogue, readOnly, onChanged
}: {
  tokens: AsyncState<AgentToken[]>;
  catalogue: AsyncState<AgentCatalogue>;
  readOnly: boolean;
  /** Called after a mint or a revoke, so the audit and the rest re-read. */
  onChanged(): void;
}) {
  const api = useApi();
  const now = useNow((tokens.data?.length ?? 0) > 0, 30_000);
  const [minting, setMinting] = useState(false);
  const [revoking, setRevoking] = useState<AgentToken | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  const revoke = async () => {
    if (!revoking) return;
    setBusy(true);
    setFailure(null);
    try {
      await api.revokeAgentToken(revoking.id);
      setRevoking(null);
      tokens.reload();
      onChanged();
    } catch (e) {
      setFailure(describeFailure(e, "the token was not revoked").message);
    } finally {
      setBusy(false);
    }
  };

  const list = tokens.data;
  return (
    <div className="ag-region" data-agents-region="tokens">
      <div className="ag-region__head">
        <InfoTooltip id="agents.tokens"><h3 className="ag-region__title">Tokens</h3></InfoTooltip>
        {readOnly ? null : (
          <InfoTooltip id="agents.token.create">
            <button type="button" className="btn btn--primary btn--sm" onClick={() => setMinting(true)}>Create token</button>
          </InfoTooltip>
        )}
      </div>

      <details data-agents-scopes="">
        {/* The summary has to be the details' own first child to toggle it, so
            the tooltip goes inside it rather than around it. */}
        <summary style={{ cursor: "pointer", fontSize: "var(--text-sm)", fontWeight: 600 }}>
          <InfoTooltip id="agents.scopes.list"><span>What each scope can call</span></InfoTooltip>
        </summary>
        {catalogue.error ? (
          <ErrorState message={catalogue.error.message} remediation={catalogue.error.remediation} correlationId={catalogue.error.correlationId} onRetry={catalogue.reload} />
        ) : catalogue.data === null ? (
          <p className="ag-note">Reading the catalogue...</p>
        ) : (
          <div className="ag-tools">
            {SCOPES.map((s) => (
              <div key={s} data-scope-tools={s}>
                <h4><code className="mono">{s}</code></h4>
                <ul>
                  {catalogue.data!.tools.filter((t) => t.scope === s).map((t) => <li key={t.name}><code>{t.name}</code></li>)}
                </ul>
              </div>
            ))}
          </div>
        )}
      </details>

      {tokens.error ? (
        <ErrorState message={tokens.error.message} remediation={tokens.error.remediation} correlationId={tokens.error.correlationId} onRetry={tokens.reload} />
      ) : list === null ? (
        <p className="ag-note">Reading tokens...</p>
      ) : list.length === 0 ? (
        <p className="ag-statement" data-agents-empty="tokens">No agent has a token. Nothing can connect to this deployment as an agent.</p>
      ) : (
        <table className="ag-table" data-agents-table="tokens">
          <caption className="visually-hidden">Agent tokens</caption>
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col"><InfoTooltip id="agents.token.prefix"><span>Prefix</span></InfoTooltip></th>
              <th scope="col">Scope</th>
              <th scope="col">Owner</th>
              <th scope="col">Created</th>
              <th scope="col">Expires</th>
              <th scope="col">Last used</th>
              <th scope="col">Status</th>
              <th scope="col"><span className="visually-hidden">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {list.map((t) => (
              <tr key={t.id} data-token={t.name} data-status={t.status}>
                <td data-label="Name">{t.name}<span className="ag-secondary mono">agent:{t.name}</span></td>
                <td data-label="Prefix" className="ag-cell-mono" data-token-prefix="">{t.prefix}</td>
                <td data-label="Scope"><ScopeChip scope={t.scope} /></td>
                <td data-label="Owner">{t.owner}</td>
                <td data-label="Created">{stamp(t.createdAt)}</td>
                <td data-label="Expires">{stamp(t.expiresAt)}<span className="ag-secondary">{relativeTime(t.expiresAt, now)}</span></td>
                <td data-label="Last used">{t.lastUsedAt ? stamp(t.lastUsedAt) : "never"}</td>
                <td data-label="Status"><StatusChip status={t.status} /></td>
                <td>
                  {!readOnly && t.status === "active" ? (
                    <InfoTooltip id="agents.token.revoke">
                      <button type="button" className="btn btn--destructive btn--sm" aria-label={"Revoke agent:" + t.name} onClick={() => { setFailure(null); setRevoking(t); }}>
                        Revoke
                      </button>
                    </InfoTooltip>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {minting ? <MintDialog onMinted={() => { tokens.reload(); onChanged(); }} onClose={() => setMinting(false)} /> : null}

      <ConfirmationDialog
        open={revoking !== null}
        eyebrow="Agents"
        title={"Revoke agent:" + (revoking?.name ?? "") + "?"}
        confirmLabel={"Revoke agent:" + (revoking?.name ?? "")}
        destructive
        disabled={busy}
        onCancel={() => setRevoking(null)}
        onConfirm={() => void revoke()}
      >
        <p style={{ margin: 0 }}>
          Its next request is refused. A revoked token cannot be brought back, only replaced by minting another.
        </p>
        {revoking ? <CommandEcho label="Equivalent command" commands={[revokeCommand(revoking.id)]} /> : null}
        {failure ? <p className="ag-err" role="alert" data-revoke-error="">{failure}</p> : null}
      </ConfirmationDialog>
    </div>
  );
}
