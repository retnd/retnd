/**
 * The log level control inside the Observability card (EPIC O, FR-100, #900).
 *
 * `observability.logs.level` is the engine's configured log floor. The
 * environment outranks it (RETND_DEBUG=1, then a LOG_LEVEL that names a
 * level), so the line under the select says which of the two is in force:
 * "why is it not debug" is never a guess. A save takes effect on the next
 * line with no restart, and it moves the engine only, because the web-ui
 * container has no config.yaml and reads the environment alone.
 *
 * The terminal line for the saved level sits under the control, from
 * logsLevelCommand, and the select's options are that module's LOG_LEVELS:
 * the list the twin fixture is written from and core/cmd/retnd holds to the
 * levels config.yaml accepts.
 */
import { useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { ObservabilitySettings } from "@shared/api/contracts";
import { describeFailure } from "@shared/api/failure";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { LOG_LEVELS, logsLevelCommand } from "./observabilityCommands";

export function LogLevelControl({ logs, readOnly, onSaved }: {
  logs: ObservabilitySettings["logs"];
  readOnly: boolean;
  onSaved: (s: ObservabilitySettings) => void;
}) {
  const api = useApi();
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  const change = (level: string) => {
    setSaving(true);
    setProblem(null);
    api.patchObservabilitySettings({ logsLevel: level }).then(
      (s) => { setSaving(false); onSaved(s); },
      (e: unknown) => { setSaving(false); setProblem(describeFailure(e, "could not save the log level").message); }
    );
  };

  const overridden = logs.levelSource !== "config" && logs.levelInForce !== logs.level;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 6 }} data-testid="logs-level-control" data-scope="deployment-wide">
      <FieldHelp label="Log level" help={FIELD_HELP.observabilityLogsLevel}>
        {(helpId) => (
          <select className="input" aria-label="Log level" aria-describedby={helpId} value={logs.level}
            disabled={readOnly || saving} onChange={(e) => change(e.target.value)}>
            {LOG_LEVELS.map((l) => <option key={l} value={l}>{l}</option>)}
          </select>
        )}
      </FieldHelp>
      <InfoTooltip id="observability.logs.level" block>
        <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }} data-testid="logs-level-in-force">
          In force: <b className="mono">{logs.levelInForce}</b> (from {logs.levelSource}).
          {overridden ? " The environment is overriding the saved level, so changing it here has no effect until RETND_DEBUG and LOG_LEVEL are unset." : ""}
          {" "}Applies to the engine on the next line, with no restart. The web-ui container reads the environment alone.
        </div>
      </InfoTooltip>
      <CommandEcho label="Equivalent command" commands={[logsLevelCommand(logs.level)]} />
      {problem ? <div role="alert" style={{ color: "var(--text)" }}>{problem}</div> : null}
    </div>
  );
}
