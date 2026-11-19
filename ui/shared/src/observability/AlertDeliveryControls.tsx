/**
 * The alert delivery controls in Settings' Observability card (EPIC O, #901;
 * FR-101).
 *
 * `delivery` selects exactly one channel, never several: `platform` is
 * today's behaviour (the platform's own notifier, which no shipped
 * deployment has), `webhook` posts each alert to one HTTP receiver. The
 * webhook URL is a credential, so it is not a field here and is never shown:
 * config.yaml names where it is kept (a file, an environment variable or a
 * command), and this card says only whether one is set.
 *
 * Every control saves as it changes (a select on change, a number on blur),
 * so the card needs no button, and a refusal from the engine is shown where
 * the change was made. Tooltip ids are `observability.alerts.*`, a prefix of
 * their own so the registry diff does not touch the other two issues' ids.
 */
import { useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { ObservabilitySettings, ObservabilitySettingsPatch } from "@shared/api/contracts";
import { describeFailure } from "@shared/api/failure";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { TooltipId } from "@shared/tooltips/tooltips";

type Alerts = ObservabilitySettings["alerts"];

export function AlertDeliveryControls({
  alerts,
  readOnly,
  onSaved
}: {
  alerts: Alerts;
  readOnly: boolean;
  onSaved: (next: ObservabilitySettings) => void;
}) {
  const api = useApi();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const webhook = alerts.delivery === "webhook";
  const refreshing = webhook && alerts.webhook.dialect === "alertmanager";

  async function save(patch: ObservabilitySettingsPatch) {
    setBusy(true);
    setError(null);
    try {
      onSaved(await api.patchObservabilitySettings(patch));
    } catch (e) {
      setError(describeFailure(e, "could not save the alert delivery settings").message);
    } finally {
      setBusy(false);
    }
  }

  const row = (id: string, tip: TooltipId, label: string, control: React.ReactNode) => (
    <div key={id} style={{ display: "contents" }}>
      <label htmlFor={id} style={{ color: "var(--text-2)" }}>{label}</label>
      <InfoTooltip id={tip}>{control}</InfoTooltip>
    </div>
  );
  const seconds = (id: string, tip: TooltipId, label: string, value: number, field: "webhookRefreshIntervalSeconds" | "webhookResolveTtlSeconds") =>
    row(id, tip, label,
      <input
        id={id}
        type="number"
        min={1}
        step={1}
        className="input input--strong-edge"
        style={{ width: 120 }}
        defaultValue={value}
        key={value}
        disabled={readOnly || busy}
        onBlur={(e) => {
          const n = Number(e.currentTarget.value);
          if (Number.isInteger(n) && n > 0 && n !== value) void save({ [field]: n });
        }}
      />
    );

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8 }} data-testid="alert-delivery">
      <div style={{ display: "grid", gridTemplateColumns: "max-content 1fr", gap: "6px 16px", alignItems: "center" }}>
        {row("obs-alert-delivery", "observability.alerts.delivery", "Alert delivery",
          <select id="obs-alert-delivery" className="select input--strong-edge" value={alerts.delivery} disabled={readOnly || busy}
            onChange={(e) => void save({ alertsDelivery: e.currentTarget.value })}>
            <option value="platform">platform</option>
            <option value="webhook">webhook</option>
          </select>
        )}
        {webhook && row("obs-alert-dialect", "observability.alerts.dialect", "Webhook format",
          <select id="obs-alert-dialect" className="select input--strong-edge" value={alerts.webhook.dialect} disabled={readOnly || busy}
            onChange={(e) => void save({ webhookDialect: e.currentTarget.value })}>
            <option value="generic">generic</option>
            <option value="alertmanager">alertmanager</option>
          </select>
        )}
        {webhook && row("obs-alert-http", "observability.alerts.insecure-http", "Plain http",
          <select id="obs-alert-http" className="select input--strong-edge" value={alerts.webhook.allowInsecureHttp ? "allow" : "refuse"} disabled={readOnly || busy}
            onChange={(e) => void save({ webhookAllowInsecureHttp: e.currentTarget.value === "allow" })}>
            <option value="refuse">refused (https only)</option>
            <option value="allow">allowed (trusted private network)</option>
          </select>
        )}
        {refreshing && seconds("obs-alert-refresh", "observability.alerts.refresh", "Refresh every (seconds)", alerts.webhook.refreshIntervalSeconds, "webhookRefreshIntervalSeconds")}
        {refreshing && seconds("obs-alert-ttl", "observability.alerts.resolve-ttl", "Expire after (seconds)", alerts.webhook.resolveTtlSeconds, "webhookResolveTtlSeconds")}
      </div>
      {webhook && (
        <p style={{ margin: 0, color: "var(--text-2)" }} data-testid="alert-webhook-url-state">
          {alerts.webhook.urlSet
            ? "The webhook URL is set. It is a credential, so it is never shown."
            : "No webhook URL is set. Name where it is kept (url_ref) in config.yaml, or no alert is delivered."}
        </p>
      )}
      {error && (
        <div role="alert" style={{ color: "var(--text)", border: "1px solid var(--danger)", background: "var(--danger-quiet)", borderRadius: "var(--radius-md)", padding: 8 }} data-testid="alert-delivery-error">
          {error}
        </div>
      )}
    </div>
  );
}
