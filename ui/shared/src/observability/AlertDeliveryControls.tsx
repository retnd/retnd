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
 * A URL the engine cannot use is never a quiet state: `urlStatus` is what the
 * engine's last read of the reference found, and `unresolvable` (it does not
 * read) or `invalid` (it reads, and is not an acceptable URL) is an alert in
 * the card, because both mean nobody is being told anything. The engine holds
 * the alerts and retries, and the alert goes away on its own once it heals.
 *
 * Every control saves as it changes (a select on change, a number on blur),
 * so the card needs no button, and a refusal from the engine is shown where
 * the change was made. Tooltip ids are `observability.alerts.*`, a prefix of
 * their own so the registry diff does not touch the other two issues' ids.
 *
 * Each input has a three-part field help entry on the control and its
 * registry tooltip on the label, and the block ends with the terminal line
 * for every input on screen, from the twin builders in observabilityCommands
 * (#1604).
 */
import { useState } from "react";
import { useMounted } from "@shared/hooks/useMounted";
import { useApi } from "@shared/api/ApiContext";
import type { ObservabilitySettings, ObservabilitySettingsPatch } from "@shared/api/contracts";
import { describeFailure } from "@shared/api/failure";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP, type FieldHelpCopy } from "@shared/components/fieldHelpCopy";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { TooltipId } from "@shared/tooltips/tooltips";
import {
  alertsDeliveryCommand,
  webhookAllowInsecureHttpCommand,
  webhookDialectCommand,
  webhookRefreshIntervalCommand,
  webhookResolveTtlCommand
} from "./observabilityCommands";

type Alerts = ObservabilitySettings["alerts"];

const alertBox: React.CSSProperties = {
  color: "var(--text)",
  border: "1px solid var(--danger)",
  background: "var(--danger-quiet)",
  borderRadius: "var(--radius-md)",
  padding: 8
};

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
  const mountedRef = useMounted();
  const webhook = alerts.delivery === "webhook";
  const refreshing = webhook && alerts.webhook.dialect === "alertmanager";

  async function save(patch: ObservabilitySettingsPatch) {
    setBusy(true);
    setError(null);
    try {
      const saved = await api.patchObservabilitySettings(patch);
      // The parent's setter is as gone as this control when the page is (#1599).
      if (!mountedRef.current) return;
      onSaved(saved);
    } catch (e) {
      if (mountedRef.current) setError(describeFailure(e, "could not save the alert delivery settings").message);
    } finally {
      if (mountedRef.current) setBusy(false);
    }
  }

  // The tooltip sits on the label and the three-part help on the control, as
  // on the rest of the card, so one hover opens one pop-up.
  const row = (id: string, tip: TooltipId, label: string, help: FieldHelpCopy, control: (helpId: string) => React.ReactNode) => (
    <div key={id} style={{ display: "contents" }}>
      <label htmlFor={id} style={{ color: "var(--text-2)" }}>
        <InfoTooltip id={tip}><span>{label}</span></InfoTooltip>
      </label>
      <FieldHelp label={label} help={help} style={{ justifySelf: "start" }}>{control}</FieldHelp>
    </div>
  );
  const seconds = (id: string, tip: TooltipId, label: string, help: FieldHelpCopy, value: number, field: "webhookRefreshIntervalSeconds" | "webhookResolveTtlSeconds") =>
    row(id, tip, label, help, (helpId) =>
      <input
        id={id}
        type="number"
        min={1}
        step={1}
        className="input input--strong-edge"
        style={{ width: 120 }}
        defaultValue={value}
        key={value}
        aria-describedby={helpId}
        disabled={readOnly || busy}
        onBlur={(e) => {
          const n = Number(e.currentTarget.value);
          if (Number.isInteger(n) && n > 0 && n !== value) void save({ [field]: n });
        }}
      />
    );

  // The terminal line for each input on screen, as it is saved now.
  const echoed = [alertsDeliveryCommand(alerts.delivery)];
  if (webhook) {
    echoed.push(webhookDialectCommand(alerts.webhook.dialect), webhookAllowInsecureHttpCommand(alerts.webhook.allowInsecureHttp));
  }
  if (refreshing) {
    echoed.push(webhookRefreshIntervalCommand(alerts.webhook.refreshIntervalSeconds), webhookResolveTtlCommand(alerts.webhook.resolveTtlSeconds));
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8 }} data-testid="alert-delivery">
      <div style={{ display: "grid", gridTemplateColumns: "max-content 1fr", gap: "6px 16px", alignItems: "center" }}>
        {row("obs-alert-delivery", "observability.alerts.delivery", "Alert delivery", FIELD_HELP.observabilityAlertsDelivery, (helpId) =>
          <select id="obs-alert-delivery" className="select input--strong-edge" value={alerts.delivery} disabled={readOnly || busy}
            aria-describedby={helpId} onChange={(e) => void save({ alertsDelivery: e.currentTarget.value })}>
            <option value="platform">platform</option>
            <option value="webhook">webhook</option>
          </select>
        )}
        {webhook && row("obs-alert-dialect", "observability.alerts.dialect", "Webhook format", FIELD_HELP.observabilityAlertsDialect, (helpId) =>
          <select id="obs-alert-dialect" className="select input--strong-edge" value={alerts.webhook.dialect} disabled={readOnly || busy}
            aria-describedby={helpId} onChange={(e) => void save({ webhookDialect: e.currentTarget.value })}>
            <option value="generic">generic</option>
            <option value="alertmanager">alertmanager</option>
          </select>
        )}
        {webhook && row("obs-alert-http", "observability.alerts.insecure-http", "Plain http", FIELD_HELP.observabilityAlertsInsecureHttp, (helpId) =>
          <select id="obs-alert-http" className="select input--strong-edge" value={alerts.webhook.allowInsecureHttp ? "allow" : "refuse"} disabled={readOnly || busy}
            aria-describedby={helpId} onChange={(e) => void save({ webhookAllowInsecureHttp: e.currentTarget.value === "allow" })}>
            <option value="refuse">refused (https only)</option>
            <option value="allow">allowed (trusted private network)</option>
          </select>
        )}
        {refreshing && seconds("obs-alert-refresh", "observability.alerts.refresh", "Refresh every (seconds)", FIELD_HELP.observabilityAlertsRefresh, alerts.webhook.refreshIntervalSeconds, "webhookRefreshIntervalSeconds")}
        {refreshing && seconds("obs-alert-ttl", "observability.alerts.resolve-ttl", "Expire after (seconds)", FIELD_HELP.observabilityAlertsResolveTtl, alerts.webhook.resolveTtlSeconds, "webhookResolveTtlSeconds")}
      </div>
      <CommandEcho label={echoed.length > 1 ? "Equivalent commands" : "Equivalent command"} commands={echoed} />
      {webhook && (
        <p style={{ margin: 0, color: "var(--text-2)" }} data-testid="alert-webhook-url-state">
          {alerts.webhook.urlSet
            ? "The webhook URL is set. It is a credential, so it is never shown."
            : "No webhook URL is set. Name where it is kept (url_ref) in config.yaml, or no alert is delivered."}
        </p>
      )}
      {webhook && alerts.webhook.urlStatus === "unresolvable" && (
        <div role="alert" style={alertBox} data-testid="alert-webhook-unresolvable">
          The webhook URL cannot be read (observability.alerts.webhook.url_ref in config.yaml). Alerts are held and
          retried, and go out as soon as it reads again.
        </div>
      )}
      {webhook && alerts.webhook.urlStatus === "invalid" && (
        <div role="alert" style={alertBox} data-testid="alert-webhook-invalid">
          The webhook URL reference reads, but what it holds is not an acceptable URL (https, or http with plain http
          allowed, and no user or password in it). Alerts are held and retried until it is.
        </div>
      )}
      {error && (
        <div role="alert" style={alertBox} data-testid="alert-delivery-error">
          {error}
        </div>
      )}
    </div>
  );
}
