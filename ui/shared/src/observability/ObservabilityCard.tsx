/**
 * The Observability card on Settings (EPIC O, #898; the shell lands with
 * #899).
 *
 * Three integrations an operator points other software at: a Prometheus
 * scrape endpoint, a log stream and an alert channel. This is the shell, and
 * it is deliberately read-only: each line states a fact the engine reports,
 * and none is a control, so none needs a tooltip (a field whose effect the
 * code cannot yet state gets no pop-up, docs/epic-checklist.md section 5).
 * #899 adds the scrape controls to the first sub-section, #900 the log level
 * and #901 the alert delivery, each under its own tooltip prefix
 * (`observability.metrics.*`, `observability.logs.*`,
 * `observability.alerts.*`) so the registry diff does not conflict.
 *
 * Nothing here is per backup set: a scrape endpoint, a log stream and an
 * alert channel are properties of a deployment.
 */
import { useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { ObservabilitySettings } from "@shared/api/contracts";
import { LogLevelControl } from "./LogLevelControl";
import { apiErrorOf, describeFailure, isNotConfigured } from "@shared/api/failure";

export function ObservabilityCard({ readOnly }: { readOnly: boolean }) {
  const api = useApi();
  const [settings, setSettings] = useState<ObservabilitySettings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notConfigured, setNotConfigured] = useState(false);

  useEffect(() => {
    let live = true;
    api.getObservabilitySettings().then(
      (s) => { if (live) setSettings(s); },
      (e: unknown) => {
        if (!live) return;
        if (isNotConfigured(apiErrorOf(e))) setNotConfigured(true);
        else setError(describeFailure(e, "could not read the observability settings").message);
      }
    );
    return () => { live = false; };
  }, [api]);

  const rows: Array<[string, string, string]> = settings
    ? [
        ["metrics", "Prometheus scrape endpoint",
          (settings.metrics.enabled ? "on" : "off") + ", " +
          (settings.metrics.scrapeToken.exists ? "credential minted" : "no credential minted")],
        ["logs", "Log level",
          settings.logs.levelInForce + " (from " + settings.logs.levelSource + ")"],
        ["alerts", "Alert delivery", settings.alerts.delivery]
      ]
    : [];

  return (
    <section className="card" tabIndex={-1} data-observability="" aria-label="Observability" data-read-only={readOnly ? "" : undefined}>
      <div className="card__header">
        <h2 className="eyebrow">Observability</h2>
      </div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 12 }}>
        <p style={{ margin: 0, color: "var(--text-2)" }}>
          What this deployment offers to Prometheus, a log shipper and an alert receiver. All of it is off until you turn it on, and none of it is per backup set.
        </p>
        {notConfigured ? (
          <p style={{ margin: 0 }} data-testid="observability-not-configured">Nothing is configured yet, so there are no observability settings to show.</p>
        ) : error ? (
          <div role="alert" style={{ color: "var(--text)", border: "1px solid var(--danger)", background: "var(--danger-quiet)", borderRadius: "var(--radius-md)", padding: 8 }} data-testid="observability-error">{error}</div>
        ) : !settings ? (
          <p style={{ margin: 0, color: "var(--text-2)" }} data-testid="observability-loading">Reading the settings...</p>
        ) : (
          <dl style={{ margin: 0, display: "grid", gridTemplateColumns: "max-content 1fr", gap: "6px 16px" }} data-testid="observability-summary">
            {rows.map(([key, label, value]) => (
              <div key={key} style={{ display: "contents" }}>
                <dt style={{ color: "var(--text-2)" }}>{label}</dt>
                <dd style={{ margin: 0 }} data-observability-row={key}>{value}</dd>
              </div>
            ))}
          </dl>
        )}
        {settings ? <LogLevelControl logs={settings.logs} readOnly={readOnly} onSaved={setSettings} /> : null}
      </div>
    </section>
  );
}
