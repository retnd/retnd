/**
 * The global Assurance card on Settings (issue #985).
 *
 * It says what the default is in a sentence an operator can read without a
 * manual, edits the one knob that is per-deployment AND per-set (scrub
 * cadence), and shows the four host-scoped knobs as deployment-wide readouts
 * that are not inputs. The readouts are drawn on purpose: the written "no"
 * made visible, so nobody later "fixes" an absence by adding a per-set
 * control a single staging filesystem and HTTP server could not honour.
 */
import "./assurance.css";
import { useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { AssuranceSettings } from "./types";

export function AssuranceSettingsCard({ readOnly }: { readOnly: boolean }) {
  const api = useApi().assurance;
  const [settings, setSettings] = useState<AssuranceSettings | null>(null);
  const [every, setEvery] = useState("");
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!api) return;
    let live = true;
    api.getSettings().then((s) => { if (live) { setSettings(s); setEvery(s.scrubEvery); } }, (e: unknown) => {
      if (live) setError(e instanceof Error ? e.message : "could not read");
    });
    return () => { live = false; };
  }, [api]);

  const save = async () => {
    if (!api) return;
    setError(null);
    try {
      const s = await api.putSettings(every);
      setSettings(s);
      setEvery(s.scrubEvery);
      setSaved(true);
    } catch (e) {
      setSaved(false);
      setError(e instanceof Error ? e.message : "could not save");
    }
  };

  const knobs: Array<[string, string | undefined]> = [
    ["Recovery staging root", settings?.host.stagingRoot],
    ["Staging TTL", settings?.host.stagingTtl],
    ["Concurrency ceilings", settings?.host.concurrency],
    ["Download memory budget", settings?.host.downloadMemoryBudget]
  ];

  return (
    <section className="card" aria-label="Assurance">
      <div className="card__header">
        <InfoTooltip id="assurance.settings.card"><h2 className="eyebrow">Assurance</h2></InfoTooltip>
      </div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 12 }}>
        <p style={{ margin: 0 }}>
          By default every backup set is scrubbed on a schedule, using only reads that cost nothing, and gets a recovery test
          when it has a validator configured. A scheduled pass never thaws an archive and never reads across a link that costs egress.
        </p>
        {!api ? <p style={{ margin: 0 }}>This server does not report assurance yet, so there is nothing to configure here.</p> : null}
        {api ? (
          <>
            <FieldHelp label="Scrub every" help={FIELD_HELP.assuranceScrubEvery}>
              {(helpId) => (
                <input className="input input--mono" aria-label="Scrub every" aria-describedby={helpId} value={every}
                  disabled={readOnly || !settings} style={{ width: 100 }}
                  onChange={(e) => { setEvery(e.target.value); setSaved(false); }} />
              )}
            </FieldHelp>
            {knobs.map(([label, value]) => (
              <div key={label} className="aknob" data-testid="host-knob" data-scope="deployment-wide">
                <span className="aknob__scope">Deployment-wide</span>
                <span>{label}</span>
                <b className="mono">{value ?? "unknown"}</b>
              </div>
            ))}
            <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
              These four belong to the host and the process. They are read here and have no per-set control.
            </div>
            <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
              <button className="btn btn--primary" disabled={readOnly || !settings || every === settings.scrubEvery} onClick={() => void save()}>Save</button>
              {saved ? <span role="status">Saved</span> : null}
            </div>
          </>
        ) : null}
        {error ? <div role="alert" style={{ color: "var(--text)", border: "1px solid var(--danger)", background: "var(--danger-quiet)", borderRadius: "var(--radius-md)", padding: 8 }}>{error}</div> : null}
      </div>
    </section>
  );
}
