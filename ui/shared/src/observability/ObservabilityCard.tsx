/**
 * The Observability card on Settings (EPIC O, #898; the metrics half is #899).
 *
 * Three integrations an operator points other software at: a Prometheus
 * scrape endpoint, a log stream and an alert channel. #899 lands the first
 * sub-section (the endpoint's switch, its report cache and its credential);
 * the log level (#900) and the alert delivery (#901) are added under their
 * own tooltip prefixes (`observability.logs.*`, `observability.alerts.*`) so
 * the registry diff does not conflict. Until then those two are read-only
 * statements of fact, which need no tooltip: a field whose effect cannot yet
 * be stated gets no pop-up (docs/epic-checklist.md section 5).
 *
 * Nothing here is per backup set: a scrape endpoint, a log stream and an alert
 * channel are properties of a deployment.
 *
 * # The credential has one home
 *
 * The scrape token exists in this component's state for the life of the reveal
 * and nowhere else: not in a list, a tooltip, a toast, a live region, a title
 * attribute, an error message or browser storage. "I have stored it" drops it,
 * and so does unmounting. The mint is a POST because an operator with only a
 * browser has to be able to wire Prometheus.
 */
import { useEffect, useId, useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { acknowledgeInsecureTransport } from "@shared/api/client";
import type { MintedScrapeToken, ObservabilitySettings } from "@shared/api/contracts";
import { apiErrorOf, describeFailure, isNotConfigured } from "@shared/api/failure";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { insecureAckRequiredNow, useDestructiveGate } from "@shared/hooks/useDestructiveGate";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { copyText } from "@shared/utilities/copyText";
import { LogLevelControl } from "./LogLevelControl";
import { metricsEnabledCommand, mintScrapeTokenCommand, metricsIntervalCommand } from "./observabilityCommands";

export function ObservabilityCard({ readOnly }: { readOnly: boolean }) {
  const api = useApi();
  const gate = useDestructiveGate();
  const [settings, setSettings] = useState<ObservabilitySettings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notConfigured, setNotConfigured] = useState(false);
  const [busy, setBusy] = useState(false);
  const [interval, setInterval] = useState("");
  const [minted, setMinted] = useState<MintedScrapeToken | null>(null);
  const [copied, setCopied] = useState(false);
  const enabledId = useId();
  const intervalId = useId();
  const tokenField = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    let live = true;
    api.getObservabilitySettings().then(
      (s) => { if (live) { setSettings(s); setInterval(String(s.metrics.minRebuildIntervalSeconds)); } },
      (e: unknown) => {
        if (!live) return;
        if (isNotConfigured(apiErrorOf(e))) setNotConfigured(true);
        else setError(describeFailure(e, "could not read the observability settings").message);
      }
    );
    return () => { live = false; };
  }, [api]);

  const apply = async (patch: Parameters<typeof api.patchObservabilitySettings>[0]) => {
    setBusy(true);
    setError(null);
    try {
      const s = await api.patchObservabilitySettings(patch);
      setSettings(s);
      setInterval(String(s.metrics.minRebuildIntervalSeconds));
    } catch (e) {
      setError(describeFailure(e, "could not save").message);
    } finally {
      setBusy(false);
    }
  };

  const mint = async () => {
    setBusy(true);
    setError(null);
    // The token comes back over this connection, so a connection that is not
    // encrypted gets the same question a password gets.
    const ask = await insecureAckRequiredNow(api, gate);
    if (ask && !(await acknowledgeInsecureTransport())) {
      setError("Nothing was minted. This connection is not encrypted, and the token comes back over this connection, so the request was not sent.");
      setBusy(false);
      return;
    }
    try {
      const answer = await api.mintScrapeToken();
      setMinted(answer);
      setCopied(false);
      setSettings(await api.getObservabilitySettings());
    } catch (e) {
      setError(describeFailure(e, "the scrape token was not minted").message);
    } finally {
      setBusy(false);
    }
  };

  const intervalSeconds = Number(interval);
  const intervalProblem = interval.trim() === "" || !Number.isInteger(intervalSeconds) || intervalSeconds < 1
    ? "A whole number of seconds, at least 1." : null;
  const intervalChanged = settings !== null && interval !== String(settings.metrics.minRebuildIntervalSeconds);
  const token = settings?.metrics.scrapeToken;

  return (
    <section className="card" tabIndex={-1} data-observability="" aria-label="Observability">
      <div className="card__header">
        <InfoTooltip id="observability.card"><h2 className="eyebrow">Observability</h2></InfoTooltip>
      </div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 14 }}>
        <p style={{ margin: 0, color: "var(--text-2)" }}>
          What this deployment offers to Prometheus, a log shipper and an alert receiver. All of it is off until you turn it on, and none of it is per backup set.
        </p>
        {notConfigured ? (
          <p style={{ margin: 0 }} data-testid="observability-not-configured">Nothing is configured yet, so there are no observability settings to show.</p>
        ) : !settings ? (
          error ? null : <p style={{ margin: 0, color: "var(--text-2)" }} data-testid="observability-loading">Reading the settings...</p>
        ) : (
          <>
            <div data-observability-section="metrics" style={{ display: "flex", flexDirection: "column", gap: 10 }}>
              <h3 style={{ margin: 0, fontSize: "var(--text-sm)" }}>Prometheus scrape endpoint</h3>
              <FieldHelp label="Serve the scrape endpoint" help={FIELD_HELP.observabilityMetricsEnabled}>
                {(helpId) => (
                  <label htmlFor={enabledId} style={{ display: "flex", alignItems: "center", gap: 10 }}>
                    <input
                      id={enabledId}
                      type="checkbox"
                      checked={settings.metrics.enabled}
                      disabled={readOnly || busy}
                      aria-describedby={helpId}
                      style={{ accentColor: "var(--accent)" }}
                      onChange={(e) => void apply({ metricsEnabled: e.target.checked })}
                    />
                    <InfoTooltip id="observability.metrics.enabled"><span>Serve the scrape endpoint</span></InfoTooltip>
                  </label>
                )}
              </FieldHelp>
              <CommandEcho label="Equivalent command" commands={[metricsEnabledCommand(!settings.metrics.enabled)]} />

              <FieldHelp label="Report cache" help={FIELD_HELP.observabilityMetricsInterval}>
                {(helpId) => (
                  <div className="field">
                    <label htmlFor={intervalId}><InfoTooltip id="observability.metrics.interval"><span className="field__label">Report cache (seconds)</span></InfoTooltip></label>
                    <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
                      <input
                        id={intervalId}
                        className="input input--mono input--strong-edge"
                        inputMode="numeric"
                        value={interval}
                        disabled={readOnly || busy}
                        aria-describedby={helpId}
                        aria-invalid={intervalChanged && intervalProblem ? true : undefined}
                        style={{ width: 96 }}
                        onChange={(e) => setInterval(e.target.value)}
                      />
                      <button
                        type="button"
                        className="btn btn--strong-edge"
                        disabled={readOnly || busy || !intervalChanged || intervalProblem !== null}
                        onClick={() => void apply({ metricsMinRebuildIntervalSeconds: intervalSeconds })}
                      >Save</button>
                    </div>
                    {intervalChanged && intervalProblem ? <span role="alert" className="field__error">{intervalProblem}</span> : null}
                  </div>
                )}
              </FieldHelp>
              {intervalChanged && !intervalProblem ? <CommandEcho label="Equivalent command" commands={[metricsIntervalCommand(intervalSeconds)]} /> : null}

              <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
                <span data-testid="scrape-token-state" style={{ color: "var(--text)" }}>
                  {token?.exists
                    ? "Scrape token minted " + (token.createdAt ?? "") + ", starting " + token.prefix + "."
                    : "No scrape token has been minted, so nothing can scrape yet."}
                </span>
                <div>
                  <InfoTooltip id="observability.metrics.mint">
                    <button type="button" className="btn btn--strong-edge" disabled={readOnly || busy} onClick={() => void mint()}>
                      {token?.exists ? "Mint a new scrape token" : "Mint scrape token"}
                    </button>
                  </InfoTooltip>
                </div>
                {token?.exists ? (
                  <span style={{ color: "var(--text-2)" }}>Minting again replaces the current token at once.</span>
                ) : null}
                <CommandEcho label="Equivalent command" commands={[mintScrapeTokenCommand()]} />
              </div>

              {minted ? (
                <div data-scrape-reveal="" role="group" aria-label="Scrape token, shown once" style={{ display: "flex", flexDirection: "column", gap: 8 }}>
                  <div style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
                    <textarea
                      ref={tokenField}
                      readOnly
                      rows={2}
                      className="input input--strong-edge"
                      aria-label="Your scrape token"
                      data-scrape-token=""
                      value={minted.token}
                      spellCheck={false}
                      style={{ width: "100%", resize: "none", fontFamily: "var(--font-mono)" }}
                      onFocus={(e) => e.currentTarget.select()}
                    />
                    <InfoTooltip id="observability.metrics.copy">
                      <button type="button" className="btn btn--strong-edge" onClick={() => void copyText(minted.token, tokenField.current).then((r) => setCopied(r === "copied"))}>Copy</button>
                    </InfoTooltip>
                  </div>
                  <span role="status" aria-live="polite" className="visually-hidden">{copied ? "Copied" : ""}</span>
                  <p style={{ margin: 0 }}>
                    This is the only time this token is shown. Put it in Prometheus's credentials file and reload Prometheus; the previous token has already stopped working.
                  </p>
                  <div>
                    <InfoTooltip id="observability.metrics.stored">
                      <button type="button" className="btn btn--primary" onClick={() => { setMinted(null); setCopied(false); }}>I have stored it</button>
                    </InfoTooltip>
                  </div>
                </div>
              ) : null}
            </div>

            <dl style={{ margin: 0, display: "grid", gridTemplateColumns: "max-content 1fr", gap: "6px 16px" }} data-testid="observability-summary">
              <dt style={{ color: "var(--text-2)" }}>Log level</dt>
              <dd style={{ margin: 0 }} data-observability-row="logs">{settings.logs.levelInForce + " (from " + settings.logs.levelSource + ")"}</dd>
              <dt style={{ color: "var(--text-2)" }}>Alert delivery</dt>
              <dd style={{ margin: 0 }} data-observability-row="alerts">{settings.alerts.delivery}</dd>
            </dl>
          </>
        )}
        {settings ? <LogLevelControl logs={settings.logs} readOnly={readOnly} onSaved={setSettings} /> : null}
        {error ? (
          <div role="alert" style={{ color: "var(--text)", border: "1px solid var(--danger)", background: "var(--danger-quiet)", borderRadius: "var(--radius-md)", padding: 8 }} data-testid="observability-error">{error}</div>
        ) : null}
      </div>
    </section>
  );
}
