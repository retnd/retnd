/**
 * Create token, and the one time the token is shown (design 1350, sections 3
 * and 4).
 *
 * # Three fields, and read is the only default
 *
 * Name, Scope, Expires in. The scope group opens on `read`, and the other two
 * radios sit behind a fold that is closed by default and never recommends,
 * pre-selects or sorts them first. The browser validates none of it: the
 * ceiling and the shape of the expiry and the rules for a name are the
 * engine's refusals, and the dialog shows the engine's message as written.
 *
 * # The token has one home
 *
 * The secret exists in this component's state for the life of the reveal and
 * nowhere else: not in a list row, a tooltip, a toast, a live region, a title
 * attribute, an error message, or browser storage. The reveal has one way out,
 * "I have stored it": no close glyph, no Cancel, the backdrop does not
 * dismiss and Escape does nothing, because dismissing a shown-once secret is
 * losing it. Unmounting the reveal drops the secret with it. The same holds
 * before the reveal: once Create token is pressed, Cancel, Escape and the
 * backdrop do nothing until the answer comes (a failure keeps the form, a
 * success opens the reveal), because a closed form would drop the response.
 */
import { useEffect, useId, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useApi } from "@shared/api/ApiContext";
import { acknowledgeInsecureTransport } from "@shared/api/client";
import { describeFailure } from "@shared/api/failure";
import { ConfirmationDialog } from "@shared/components/ConfirmationDialog";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { insecureAckRequiredNow, useDestructiveGate } from "@shared/hooks/useDestructiveGate";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { TOOLTIPS } from "@shared/tooltips/tooltips";
import { copyText } from "@shared/utilities/copyText";
import type { AgentScope, MintedAgentToken } from "@shared/types/agents";
import { mintCommand } from "./agentCommands";
import { mintRefusalField } from "./agentWire";
import "./agents.css";

const DEFAULT_EXPIRY = "90d";

export function MintDialog({ onMinted, onClose }: { onMinted(): void; onClose(): void }) {
  const api = useApi();
  const gate = useDestructiveGate();
  const [name, setName] = useState("");
  const [scope, setScope] = useState<AgentScope>("read");
  const [fold, setFold] = useState(false);
  const [expiresIn, setExpiresIn] = useState(DEFAULT_EXPIRY);
  const [busy, setBusy] = useState(false);
  // While the consent question is up the form steps aside (its values are kept here): two aria-modal dialogs at once would hide the question from assistive technology.
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState<{ message: string; field: "name" | "expires_in" | "scope" | null } | null>(null);
  const echo = name.trim() === "" ? null : mintCommand({ name: name.trim(), scope, expiresIn: expiresIn.trim() });
  const [minted, setMinted] = useState<{ answer: MintedAgentToken; command: string } | null>(null);
  const nameId = useId();
  const expiryId = useId();

  if (minted) return <RevealDialog minted={minted.answer} command={minted.command} onStored={onClose} />;

  const create = async () => {
    setBusy(true);
    setError(null);
    // A token is the longest-lived secret this product hands to a browser and
    // the answer is the one time it crosses the connection, so a connection
    // that is not encrypted gets the same question a password gets.
    const ask = await insecureAckRequiredNow(api, gate);
    if (ask) setAsking(true);
    const yes = !ask || (await acknowledgeInsecureTransport());
    setAsking(false);
    if (!yes) {
      setError({ message: "Nothing was created. This connection is not encrypted, and the token comes back over this connection, so the request was not sent.", field: null });
      setBusy(false);
      return;
    }
    try {
      const answer = await api.mintAgentToken({ name: name.trim(), scope, expiresIn: expiresIn.trim() });
      setMinted({ answer, command: echo ?? "" });
      onMinted();
    } catch (e) {
      const message = describeFailure(e, "the token was not created").message;
      // Q16: every mint refusal starts with the request's field name and a colon.
      setError({ message, field: mintRefusalField(message) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <ConfirmationDialog
      open={!asking}
      eyebrow="Agents"
      title="Create token"
      confirmLabel="Create token"
      disabled={busy || name.trim() === "" || expiresIn.trim() === ""}
      onCancel={() => { if (!busy) onClose(); }}
      onConfirm={() => void create()}
    >
      <div data-mint-form="" style={{ display: "flex", flexDirection: "column", gap: 14 }}>
        <FieldHelp label="Name" help={FIELD_HELP.agentsMintName}>
          {(helpId) => (
            <div className="field">
              <label htmlFor={nameId}><InfoTooltip id="agents.mint.name"><span className="field__label">Name</span></InfoTooltip></label>
              <input
                id={nameId}
                className={"input input--mono input--strong-edge" + (error?.field === "name" ? " ag-input-error" : "")}
                value={name}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                aria-describedby={helpId}
                aria-invalid={error?.field === "name" ? true : undefined}
                onChange={(e) => setName(e.target.value)}
              />
              <span className="ag-secondary">
                It becomes <code className="mono">agent:{name.trim() || "name"}</code> wherever the product names who did something.
              </span>
              {error?.field === "name" ? <span className="ag-fielderr" role="alert" data-mint-error="name">{error.message}</span> : null}
            </div>
          )}
        </FieldHelp>

        <fieldset className="ag-radios" aria-label="Scope">
          <legend>
            <InfoTooltip id="agents.mint.scope"><span>Scope</span></InfoTooltip>
          </legend>
          <label>
            <input type="radio" name="agent-scope" checked={scope === "read"} onChange={() => setScope("read")} />
            <span>
              <code className="mono">read</code>
              <span className="ag-secondary">It can look, and it cannot start or change anything. Every scope includes it.</span>
            </span>
          </label>
          <InfoTooltip id="agents.mint.scope.more">
            <button
              type="button"
              className="btn btn--quiet btn--sm ag-fold"
              aria-expanded={fold}
              aria-controls="agent-scope-more"
              onClick={() => {
                // Closed means read (design 1350 section 3): a wider scope must not outlive the fold that offered it.
                if (fold) setScope("read");
                setFold((f) => !f);
              }}
            >
              Allow more than reading
            </button>
          </InfoTooltip>
          {fold ? (
            <div id="agent-scope-more" style={{ display: "flex", flexDirection: "column", gap: 8 }}>
              <label>
                <input type="radio" name="agent-scope" checked={scope === "operate"} onChange={() => setScope("operate")} />
                <span>
                  <code className="mono">operate</code>
                  <span className="ag-secondary">{TOOLTIPS["agents.mint.scope.operate"].html}</span>
                </span>
              </label>
              <label>
                <input type="radio" name="agent-scope" checked={scope === "configure"} onChange={() => setScope("configure")} />
                <span>
                  <code className="mono">configure</code>
                  <span className="ag-secondary">{TOOLTIPS["agents.mint.scope.configure"].html}</span>
                </span>
              </label>
            </div>
          ) : null}
          {error?.field === "scope" ? <span className="ag-fielderr" role="alert" data-mint-error="scope">{error.message}</span> : null}
        </fieldset>

        <FieldHelp label="Expires in" help={FIELD_HELP.agentsMintExpiry}>
          {(helpId) => (
            <div className="field">
              <label htmlFor={expiryId}><InfoTooltip id="agents.mint.expiry"><span className="field__label">Expires in</span></InfoTooltip></label>
              <input
                id={expiryId}
                className={"input input--mono input--strong-edge" + (error?.field === "expires_in" ? " ag-input-error" : "")}
                value={expiresIn}
                autoComplete="off"
                spellCheck={false}
                aria-describedby={helpId}
                aria-invalid={error?.field === "expires_in" ? true : undefined}
                onChange={(e) => setExpiresIn(e.target.value)}
              />
              {error?.field === "expires_in" ? <span className="ag-fielderr" role="alert" data-mint-error="expiry">{error.message}</span> : null}
            </div>
          )}
        </FieldHelp>

        {echo ? <CommandEcho label="Equivalent command" commands={[echo]} /> : null}

        {error && error.field === null ? (
          <p className="ag-err" role="alert" data-mint-error="form">{error.message}</p>
        ) : null}
      </div>
    </ConfirmationDialog>
  );
}

const FOCUSABLE = "textarea, button:not(:disabled)";

/** The token, once. The only way out is the button at the foot. */
function RevealDialog({ minted, command, onStored }: { minted: MintedAgentToken; command: string; onStored(): void }) {
  const field = useRef<HTMLTextAreaElement>(null);
  const box = useRef<HTMLDivElement>(null);
  const [copied, setCopied] = useState(false);

  // Focus lands on the token, so a keyboard user can copy before anything else.
  useEffect(() => {
    field.current?.focus();
    field.current?.select();
  }, []);

  // Escape does nothing here, and the trap keeps Tab inside.
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); return; }
    if (e.key !== "Tab" || !box.current) return;
    const items = [...box.current.querySelectorAll<HTMLElement>(FOCUSABLE)];
    if (items.length === 0) return;
    const first = items[0], last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  };

  const copy = async () => {
    setCopied((await copyText(minted.secret, field.current)) === "copied");
  };

  return (
    // No onClick on the scrim: the backdrop does not dismiss.
    <div className="dialog-scrim" data-token-reveal-scrim="">
      <div
        ref={box}
        role="dialog"
        aria-modal="true"
        aria-labelledby="agent-reveal-title"
        className="dialog"
        style={{ maxWidth: 520 }}
        onKeyDown={onKeyDown}
      >
        <div style={{ padding: "20px 22px 0" }}>
          <div className="eyebrow" style={{ fontSize: "var(--text-xs)", letterSpacing: "0.09em" }}>Create token</div>
          <h2 id="agent-reveal-title" style={{ margin: "8px 0 0", fontSize: 18 }}>
            Your token for agent:{minted.token.name}
          </h2>
        </div>
        <div className="ag-reveal" style={{ padding: "14px 22px 20px", fontSize: 13.5 }}>
          <div className="ag-reveal__row">
            <textarea
              ref={field}
              readOnly
              rows={3}
              className="input input--strong-edge ag-token"
              aria-label="Your token"
              data-agent-token=""
              value={minted.secret}
              spellCheck={false}
              onFocus={(e) => e.currentTarget.select()}
            />
            <InfoTooltip id="agents.mint.copy">
              <button type="button" className="btn btn--strong-edge" onClick={() => void copy()}>Copy</button>
            </InfoTooltip>
          </div>
          <span role="status" aria-live="polite" className="visually-hidden">{copied ? "Copied" : ""}</span>
          <p className="ag-statement">
            This is the only time this token is shown. If this page is closed before you store it, revoke the token and make another.
          </p>
          <p className="ag-note">
            Use it with <code className="mono">retnd agent serve --stdio --token-file PATH</code> (a file readable only by you) or the{" "}
            <code className="mono">RETND_AGENT_TOKEN</code> environment variable, and never on a command line.
          </p>
          {command ? <CommandEcho label="Equivalent command" commands={[command]} /> : null}
        </div>
        <div className="card__footer" style={{ display: "flex", justifyContent: "flex-end", borderRadius: "0 0 10px 10px" }}>
          <InfoTooltip id="agents.mint.stored">
            <button type="button" className="btn btn--primary" onClick={onStored}>I have stored it</button>
          </InfoTooltip>
        </div>
      </div>
    </div>
  );
}
