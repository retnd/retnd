/**
 * The per-set Assurance section (issue #985), beside the Retention card and
 * in its shape: which policy is in force and what it resolves to, then the
 * operations, then what the set currently claims.
 *
 * `Inherit` shows the value it resolves to, so a global edit is visibly the
 * thing that moved the set. Custom shows the inputs a set may own, and the
 * four host-scoped knobs are not among them: they belong to the host, so the
 * section says so in words and offers no control (the written "no").
 *
 * Scrub now and Run recovery test attach to an operation by asking the
 * service for the set's running one first, so a reload mounts the live
 * progress instead of starting another run.
 */
import "./assurance.css";
import { useCallback, useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { ClaimChips } from "./ClaimChips";
import { OperationPanel } from "./OperationPanel";
import { ValidatorLog } from "./ValidatorLog";
import { recoveryTestCommand, scrubCommand } from "./assuranceCommands";
import { useOperation } from "./useOperation";
import type { ArtifactAssurance, OperationKind, SetAssurance, SetAssuranceMode, ValidatorResult } from "./types";

const MODES: Array<{ mode: SetAssuranceMode; label: string }> = [
  { mode: "inherit", label: "Inherit" },
  { mode: "off", label: "Off" },
  { mode: "required", label: "Required" },
  { mode: "custom", label: "Custom" }
];

export function AssuranceSection({ setId, readOnly }: { setId: string; readOnly: boolean }) {
  const api = useApi().assurance;
  const [set, setSet] = useState<SetAssurance | null>(null);
  const [latest, setLatest] = useState<ArtifactAssurance | undefined>(undefined);
  const [draft, setDraft] = useState<SetAssuranceMode>("inherit");
  const [every, setEvery] = useState("");
  const [validator, setValidator] = useState("");
  const [opId, setOpId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [validatorResult, setValidatorResult] = useState<ValidatorResult | null | undefined>(undefined);

  const refresh = useCallback(async () => {
    if (!api) return;
    const s = await api.getSetAssurance(setId);
    setSet(s);
    setLatest(s.latest);
    setDraft(s.mode);
    setEvery(s.customScrubEvery ?? "");
    setValidator(s.customValidator ?? "");
  }, [api, setId]);

  // Mount: read the set, and attach to a running operation if there is one.
  useEffect(() => {
    if (!api) return;
    let live = true;
    void (async () => {
      try {
        await refresh();
        const running = await api.activeOperation(setId);
        if (live && running) setOpId(running.id);
      } catch (e) {
        if (live) setError(e instanceof Error ? e.message : "could not read assurance");
      }
    })();
    return () => { live = false; };
  }, [api, setId, refresh]);

  const op = useOperation(api, opId, () => { void refresh(); });
  const running = op.operation?.state === "running";

  const start = async (kind: OperationKind) => {
    if (!api) return;
    setError(null);
    try {
      const o = kind === "scrub" ? await api.startScrub(setId) : await api.startRecoveryTest(setId);
      setOpId(o.id);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not start");
    }
  };

  const save = async () => {
    if (!api) return;
    setError(null);
    try {
      await api.putSetAssurance(setId, draft, draft === "custom" ? { scrubEvery: every, validator } : undefined);
      await refresh();
      setSaved(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not save");
    }
  };

  const showValidator = async () => {
    if (!api) return;
    setValidatorResult(await api.getValidatorResult(setId));
  };

  return (
    <section className="card" aria-label="Assurance">
      <div className="card__header">
        <InfoTooltip id="assurance.set.section"><h2 className="eyebrow">Assurance</h2></InfoTooltip>
      </div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 12 }}>
        {!api ? (
          <p style={{ margin: 0 }}>This server does not report assurance yet, so nothing here is claimed.</p>
        ) : null}
        <ClaimChips assurance={latest} available={!!api} checking={running} />

        {api ? (
          <>
            <InfoTooltip id="assurance.set.mode" block>
              <div role="radiogroup" aria-label="Assurance policy for this set" style={{ display: "inline-flex", gap: 14, flexWrap: "wrap" }}>
                {MODES.map((m) => (
                  <label key={m.mode} style={{ display: "inline-flex", gap: 5, alignItems: "center" }}>
                    <input
                      type="radio"
                      name={"assurance-mode-" + setId}
                      checked={draft === m.mode}
                      disabled={readOnly}
                      onChange={() => { setDraft(m.mode); setSaved(false); }}
                    />
                    {m.label}
                  </label>
                ))}
              </div>
            </InfoTooltip>
            {draft === "inherit" ? (
              <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                {"Inheriting: scrub every " + (set?.resolvedScrubEvery ?? "") + " (from the deployment setting)."}
              </div>
            ) : null}
            {draft === "off" ? (
              <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                Assurance is off for this set, so its claims will read Not configured rather than passing.
              </div>
            ) : null}
            {draft === "custom" ? (
              <div style={{ display: "flex", gap: 12, flexWrap: "wrap" }}>
                <FieldHelp label="Scrub every" help={FIELD_HELP.assuranceSetScrubEvery}>
                  {(helpId) => (
                    <input className="input input--mono" aria-label="Scrub every" aria-describedby={helpId} value={every} disabled={readOnly}
                      onChange={(e) => { setEvery(e.target.value); setSaved(false); }} style={{ width: 90 }} />
                  )}
                </FieldHelp>
                <FieldHelp label="Validator reference" help={FIELD_HELP.assuranceSetValidator}>
                  {(helpId) => (
                    <input className="input input--mono" aria-label="Validator reference" aria-describedby={helpId} value={validator} disabled={readOnly}
                      onChange={(e) => { setValidator(e.target.value); setSaved(false); }} style={{ width: 220 }} />
                  )}
                </FieldHelp>
              </div>
            ) : null}
            <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
              Staging root, staging TTL, concurrency ceilings and the download memory budget belong to the host. They are shown on Settings and have no per-set control.
            </div>
            <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
              <button className="btn btn--sm btn--primary" disabled={readOnly || draft === set?.mode && !(draft === "custom")} onClick={() => void save()}>Save policy</button>
              {saved ? <span role="status">Saved</span> : null}
            </div>

            <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
              <InfoTooltip id="assurance.scrub.now">
                <button className="btn" disabled={readOnly || running} onClick={() => void start("scrub")}>Scrub now</button>
              </InfoTooltip>
              <InfoTooltip id="assurance.recoveryTest.run">
                <button className="btn" disabled={readOnly || running} onClick={() => void start("recovery_test")}>Run recovery test</button>
              </InfoTooltip>
              <button className="btn btn--quiet" onClick={() => void showValidator()}>Show validator output</button>
            </div>
            <CommandEcho label="Same thing from the terminal" commands={[scrubCommand(setId), recoveryTestCommand(setId)]} />
            <OperationPanel operation={op.operation} events={op.events} error={op.error} onCancel={() => void op.cancel()} readOnly={readOnly} />
            {validatorResult === null ? <div role="status">No recovery test has run for this set yet.</div> : null}
            {validatorResult ? <ValidatorLog result={validatorResult} /> : null}
          </>
        ) : null}
        {error ? <div role="alert" style={{ color: "var(--text)", border: "1px solid var(--danger)", background: "var(--danger-quiet)", borderRadius: "var(--radius-md)", padding: 8 }}>{error}</div> : null}
      </div>
    </section>
  );
}
