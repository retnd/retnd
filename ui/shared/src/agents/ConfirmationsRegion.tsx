/**
 * Pending confirmations (design 1350, section 6): who asked, what, until
 * when, and Approve or Deny.
 *
 * # Approve opens the step-up and calls nothing
 *
 * Pressing Approve expands the row to a password field and "Approve and run",
 * which stays off until the field has text. The route is not called until
 * that second button is pressed. The password rides the step-up header of that
 * one request and is forgotten as soon as it is sent. Approval sends no body:
 * it replays the recorded request byte for byte, so the UI has no body to send.
 *
 * # Deny is one click and no password
 *
 * Denying runs nothing and cannot hurt, so it asks for nothing, and it is a
 * neutral button of the same size as Approve, so the row never recommends
 * running the request.
 *
 * # The engine is the judge of expiry
 *
 * The countdown is the browser formatting a server time. It never disables
 * Approve: a late press is refused by the engine with its own sentence, shown
 * in the row as written. A refusal after approval (the shut operation gate)
 * is a result strip with the gate's words, its code, the exit code the CLI
 * would have used and the correlation id; it has no control, because the
 * gate is shut by construction and there is nothing to retry.
 *
 * # Nothing here is composed
 *
 * The sentence is the catalogue's static description carried by the record,
 * and the command is the record's own `command`. The browser builds neither.
 */
import { useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { acknowledgeInsecureTransport } from "@shared/api/client";
import { apiErrorOf, describeFailure } from "@shared/api/failure";
import { ErrorState } from "@shared/components/EmptyState";
import { StepUpPassword, stepUpMessage } from "@shared/components/StepUpFields";
import type { AsyncState } from "@shared/hooks/useAsync";
import { useDestructiveGate } from "@shared/hooks/useDestructiveGate";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { AgentConfirmation, AgentOutcome } from "@shared/types/agents";
import { clock, stamp } from "@shared/utilities/format";
import { countdown, useNow } from "./agentTime";
import "./agents.css";

type Failure = { message: string; code: string | null; correlationId: string | null };

export function ConfirmationsRegion({
  confirmations, readOnly, onDecided
}: {
  confirmations: AsyncState<AgentConfirmation[]>;
  readOnly: boolean;
  /** Called after an approval or a denial landed, so the rest re-reads. */
  onDecided(): void;
}) {
  const list = confirmations.data;
  const now = useNow((list?.length ?? 0) > 0);
  // The result of a request the operator just decided, kept until the page is
  // next loaded: the record has left the list and is in the audit, but the
  // engine's refusal is what the operator needs to read.
  const [strips, setStrips] = useState<Array<{ id: string; confirmation: AgentConfirmation; outcome: AgentOutcome }>>([]);
  const [notice, setNotice] = useState("");

  return (
    <div className="ag-region" id="agents-confirmations" data-agents-region="confirmations">
      <div className="ag-region__head">
        <InfoTooltip id="agents.confirmations"><h3 className="ag-region__title">Pending confirmations</h3></InfoTooltip>
      </div>
      <span role="status" aria-live="polite" className="visually-hidden" data-agents-notice="">{notice}</span>

      {confirmations.error ? (
        <ErrorState message={confirmations.error.message} remediation={confirmations.error.remediation} correlationId={confirmations.error.correlationId} onRetry={confirmations.reload} />
      ) : list === null ? (
        <p className="ag-note">Reading what is waiting...</p>
      ) : list.length === 0 && strips.length === 0 ? (
        <p className="ag-statement" data-agents-empty="confirmations">Nothing is waiting for you.</p>
      ) : (
        <ul className="ag-list" data-agents-list="confirmations">
          {strips.map((s) => (
            <li key={"strip-" + s.id} className="ag-row" data-confirmation-result={s.id}>
              <p className="ag-row__sentence">{s.confirmation.description}</p>
              <div className="ag-strip" role="alert" data-result-strip="">
                <span>{s.outcome.message ?? "The engine refused the request after approval."}</span>
                <span className="ag-strip__facts">
                  {s.outcome.code ? <span data-strip-code="">{s.outcome.code}</span> : null}
                  {s.outcome.exitCode !== null ? <span data-strip-exit="">exit {s.outcome.exitCode}</span> : null}
                  {s.outcome.correlationId ? <span data-strip-cid="">{s.outcome.correlationId}</span> : null}
                </span>
              </div>
            </li>
          ))}
          {list.map((c) => (
            <ConfirmationRow
              key={c.id}
              c={c}
              now={now}
              readOnly={readOnly}
              onApproved={(outcome) => {
                const refused = outcome !== null && (outcome.status >= 400 || outcome.code !== null);
                if (refused) setStrips((s) => [...s, { id: c.id, confirmation: c, outcome }]);
                setNotice(refused ? "The request was approved and the engine refused it." : "Approved. The request ran.");
                confirmations.reload();
                onDecided();
              }}
              onDenied={() => {
                setNotice("Denied. Nothing ran.");
                confirmations.reload();
                onDecided();
              }}
            />
          ))}
        </ul>
      )}
    </div>
  );
}

function ConfirmationRow({
  c, now, readOnly, onApproved, onDenied
}: {
  c: AgentConfirmation;
  now: number;
  readOnly: boolean;
  onApproved(outcome: AgentOutcome | null): void;
  onDenied(): void;
}) {
  const api = useApi();
  const gate = useDestructiveGate();
  const [asking, setAsking] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure | null>(null);
  const password = useRef("");
  const [hasPassword, setHasPassword] = useState(false);
  const [passwordKey, setPasswordKey] = useState(0);
  const forget = () => { password.current = ""; setHasPassword(false); setPasswordKey((k) => k + 1); };

  const left = Date.parse(c.expiresAt) - now;
  const urgent = left < 60_000;

  const approve = async () => {
    setBusy(true);
    setFailure(null);
    // The password is never the first thing to cross a connection the
    // operator has not been told about.
    if (gate.insecureTransportAckRequired && !(await acknowledgeInsecureTransport())) {
      setFailure({ message: stepUpMessage("INSECURE_TRANSPORT_ACK_REQUIRED", "agent") ?? "Nothing was sent.", code: null, correlationId: null });
      setBusy(false);
      return;
    }
    const stepUpPassword = password.current;
    forget();
    try {
      const decision = await api.approveAgentConfirmation(c.id, { stepUpPassword });
      onApproved(decision.outcome);
    } catch (e) {
      const err = apiErrorOf(e);
      const own = stepUpMessage(err?.code, "agent");
      setFailure({
        message: own ?? describeFailure(e, "the request was not approved").message,
        code: own ? null : err?.code ?? null,
        correlationId: own ? null : err?.correlationId ?? null
      });
      setBusy(false);
    }
  };

  const deny = async () => {
    setBusy(true);
    setFailure(null);
    try {
      await api.denyAgentConfirmation(c.id);
      onDenied();
    } catch (e) {
      const err = apiErrorOf(e);
      setFailure({ message: describeFailure(e, "the request was not denied").message, code: err?.code ?? null, correlationId: err?.correlationId ?? null });
      setBusy(false);
    }
  };

  return (
    <li className="ag-row" data-confirmation={c.id} data-tool={c.tool} data-urgent={urgent ? "" : undefined}>
      <p className="ag-row__sentence">{c.description}</p>
      <div className="ag-row__meta">
        <span>asked by <span className="mono" data-confirmation-token="">agent:{c.tokenName}</span></span>
        {c.backupSet ? <span>backup set <span className="mono">{c.backupSet}</span></span> : null}
        <span>asked {stamp(c.createdAt)}</span>
        <InfoTooltip id="agents.confirm.expires">
          <span data-confirmation-expires="">
            {urgent ? <span aria-hidden="true">◷ </span> : null}
            expires {clock(c.expiresAt).slice(0, 5)} (in {countdown(c.expiresAt, now)})
          </span>
        </InfoTooltip>
      </div>
      <code className="ag-code" data-confirmation-command="">{c.command}</code>
      {c.approveCommand || c.denyCommand ? (
        <CommandEcho label="Equivalent command" commands={[c.approveCommand, c.denyCommand].filter((x): x is string => x !== null)} />
      ) : null}

      {readOnly ? (
        <p className="ag-note" data-readonly-note="">Approving or denying a request needs a page that can be written to.</p>
      ) : asking ? (
        <div className="ag-stepup" data-step-up="">
          <InfoTooltip id="agents.confirm.stepup" block>
            <StepUpPassword
              key={passwordKey}
              act="agent"
              id={"agent-step-up-" + c.id}
              onPassword={(next) => { password.current = next; setHasPassword(next !== ""); }}
            />
          </InfoTooltip>
          <div className="ag-actions">
            <InfoTooltip id="agents.confirm.run">
              <button type="button" className="btn btn--primary" disabled={busy || !hasPassword} onClick={() => void approve()}>Approve and run</button>
            </InfoTooltip>
            <button type="button" className="btn" disabled={busy} onClick={() => { forget(); setAsking(false); setFailure(null); }}>Cancel</button>
          </div>
        </div>
      ) : (
        <div className="ag-actions">
          <InfoTooltip id="agents.confirm.approve">
            <button type="button" className="btn btn--strong-edge" disabled={busy} onClick={() => setAsking(true)}>Approve</button>
          </InfoTooltip>
          <InfoTooltip id="agents.confirm.deny">
            <button type="button" className="btn btn--strong-edge" disabled={busy} onClick={() => void deny()}>Deny</button>
          </InfoTooltip>
        </div>
      )}

      {failure ? (
        <div className="ag-strip" role="alert" data-approve-error="">
          <span>{failure.message}</span>
          {failure.code || failure.correlationId ? (
            <span className="ag-strip__facts">
              {failure.code ? <span>{failure.code}</span> : null}
              {failure.correlationId ? <span>{failure.correlationId}</span> : null}
            </span>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}
