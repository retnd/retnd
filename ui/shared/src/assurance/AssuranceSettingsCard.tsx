/**
 * The global Assurance card on Settings (issue #985, over #982's routes).
 *
 * It says what the default is in a sentence an operator can read without a
 * manual, edits the knobs that are per-deployment AND per-set (scrub cadence
 * and strategy, the recovery test's mode, cadence and validator), and shows
 * the host-scoped knobs as deployment-wide readouts with the server's own
 * written reason a backup set cannot override each one. The readouts are
 * drawn on purpose: the written "no" made visible, so nobody later "fixes"
 * an absence by adding a per-set control a single staging filesystem and HTTP
 * server could not honour.
 *
 * Nothing here needs the assurance seam: these are plain settings routes, so
 * the card works against any server that has them.
 */
import { rememberAssuranceSettings } from "./assuranceSettings";
import "./assurance.css";
import { useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { AssuranceSettings, AssuranceSettingsPatch } from "@shared/api/contracts";
import { apiErrorOf, describeFailure, isNotConfigured } from "@shared/api/failure";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { settingsCommand } from "./assuranceCommands";
import { BUILTIN_CHECKS, asBuiltinCheck, builtinOffProblem, reservedValidatorProblem } from "./builtinCheck";
import { noteAssuranceChanged } from "./verificationModel";
import { MIN_RECOVERY_TEST_CADENCE_SECONDS, cadenceProblem, formatCadence, parseCadence } from "./duration";

const STRATEGIES = ["auto", "existence", "content"] as const;

interface Draft { scrubEvery: string; strategy: string; mode: string; testEvery: string; validator: string; builtinCheck: string }

const draftOf = (s: AssuranceSettings): Draft => ({
  scrubEvery: formatCadence(s.scrub.cadenceSeconds),
  strategy: s.scrub.strategy || "auto",
  mode: s.recoveryTest.mode || "off",
  testEvery: formatCadence(s.recoveryTest.cadenceSeconds),
  validator: s.recoveryTest.validatorId,
  builtinCheck: asBuiltinCheck(s.recoveryTest.builtinCheck)
});

export function AssuranceSettingsCard({ readOnly }: { readOnly: boolean }) {
  const api = useApi();
  const [settings, setSettings] = useState<AssuranceSettings | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notConfigured, setNotConfigured] = useState(false);

  useEffect(() => {
    let live = true;
    api.getAssuranceSettings().then((s) => { if (live) { setSettings(s); setDraft(draftOf(s)); } }, (e: unknown) => {
      if (!live) return;
      // An instance with no configuration has nothing behind this card yet;
      // say that, the way the other cards on this page do.
      if (isNotConfigured(apiErrorOf(e))) setNotConfigured(true);
      else setError(describeFailure(e, "could not read the assurance settings").message);
    });
    return () => { live = false; };
  }, [api]);

  const set = (patch: Partial<Draft>) => { setDraft((d) => (d ? { ...d, ...patch } : d)); setSaved(false); };

  const scrubProblem = draft ? cadenceProblem(draft.scrubEvery) : null;
  const testProblem = draft && draft.testEvery !== "" ? cadenceProblem(draft.testEvery, MIN_RECOVERY_TEST_CADENCE_SECONDS) : null;
  const requiredNeedsValidator = !!draft && draft.mode === "required" && draft.validator.trim() === "";
  // The two refusals the server gives (#1111), said before Save is offered.
  const builtinProblem = draft ? builtinOffProblem(draft.mode, draft.builtinCheck) : null;
  const reservedProblem = draft ? reservedValidatorProblem(draft.validator) : null;

  const changes = (): AssuranceSettingsPatch => {
    if (!settings || !draft) return {};
    const was = draftOf(settings);
    const p: AssuranceSettingsPatch = {};
    if (draft.scrubEvery !== was.scrubEvery) p.scrubCadenceSeconds = parseCadence(draft.scrubEvery) ?? 0;
    if (draft.strategy !== was.strategy) p.scrubStrategy = draft.strategy;
    if (draft.mode !== was.mode) p.recoveryTestMode = draft.mode;
    if (draft.testEvery !== was.testEvery) p.recoveryTestCadenceSeconds = parseCadence(draft.testEvery) ?? 0;
    if (draft.validator !== was.validator) p.recoveryTestValidatorId = draft.validator;
    if (draft.builtinCheck !== was.builtinCheck) p.recoveryTestBuiltinCheck = draft.builtinCheck;
    return p;
  };
  const pending = Object.keys(changes()).length > 0;

  const save = async () => {
    setError(null);
    try {
      const s = await api.patchAssuranceSettings(changes());
      setSettings(s);
      setDraft(draftOf(s));
      // The screens that only show the settings read them once (#1190); this
      // is what they read next.
      rememberAssuranceSettings(api, s);
      // Every set that follows the default now resolves to something else.
      noteAssuranceChanged();
      setSaved(true);
    } catch (e) {
      setSaved(false);
      setError(describeFailure(e, "could not save").message);
    }
  };

  const reason = (name: string) => settings?.hostKnobs.find((k) => k.name === name)?.reason ?? "";
  const knobs: Array<[string, string, string | undefined]> = [
    ["staging_root", "Recovery staging root", settings?.stagingRoot],
    ["staging_ttl", "Staging TTL", settings ? formatCadence(settings.stagingTtlSeconds) : undefined],
    ["max_concurrent_full_reads", "Concurrent full reads", settings ? String(settings.maxConcurrentFullReads) : undefined],
    ["max_per_destination", "Concurrent reads per destination", settings ? String(settings.maxPerDestination) : undefined]
  ];

  return (
    <section className="card" tabIndex={-1} data-assurance="" aria-label="Assurance">
      <div className="card__header">
        <InfoTooltip id="assurance.settings.card"><h2 className="eyebrow">Assurance</h2></InfoTooltip>
      </div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 12 }}>
        <p style={{ margin: 0 }} data-testid="assurance-what-runs">
          The running service (and <span className="mono">retnd daemon</span>) scrubs on every poll: it checks each copy that is due, at the cadence below. A copy on this host is re-read and re-hashed; a copy on a storage medium gets an existence check only, with no download. A scheduled pass never thaws an archive and never reads across a link that costs egress.
          A recovery test runs on its own cadence only when its mode is required and a validator script is named, or when its mode is builtin, which runs the product&apos;s own structure check; with the mode off none runs. A set that requires a recovery test is degraded until one passes, and a built-in pass never counts as that.
        </p>
        {notConfigured ? (
          <p style={{ margin: 0 }}>
            Assurance is part of a configuration this instance has not been given yet. It becomes editable once the first backup set has been added.
          </p>
        ) : null}
        {settings && !settings.configured ? (
          <p style={{ margin: 0 }} data-testid="assurance-first-run">
            This deployment has no assurance block in its configuration, so everything below is a default nobody had to set. The scrub already runs on these defaults, and nothing needs setting up first. Recovery tests are off until you choose otherwise.
          </p>
        ) : null}
        {draft ? (
          <>
            <div style={{ display: "flex", gap: 14, flexWrap: "wrap", alignItems: "flex-start" }}>
              <FieldHelp label="Scrub every" help={FIELD_HELP.assuranceScrubEvery}>
                {(helpId) => (
                  <input className="input input--mono" aria-label="Scrub every" aria-describedby={helpId} aria-invalid={scrubProblem !== null}
                    value={draft.scrubEvery} disabled={readOnly} style={{ width: 100 }} onChange={(e) => set({ scrubEvery: e.target.value })} />
                )}
              </FieldHelp>
              <FieldHelp label="Scrub reads" help={FIELD_HELP.assuranceScrubStrategy}>
                {(helpId) => (
                  <select className="input" aria-label="Scrub reads" aria-describedby={helpId} value={draft.strategy} disabled={readOnly}
                    onChange={(e) => set({ strategy: e.target.value })}>
                    {STRATEGIES.map((s) => <option key={s} value={s}>{s}</option>)}
                  </select>
                )}
              </FieldHelp>
            </div>
            {scrubProblem ? <div role="alert" style={{ color: "var(--text)" }}>{scrubProblem}</div> : null}
            <div style={{ display: "flex", gap: 14, flexWrap: "wrap", alignItems: "flex-start" }}>
              <FieldHelp label="Recovery test" help={FIELD_HELP.assuranceRecoveryTestMode}>
                {(helpId) => (
                  <select className="input" aria-label="Recovery test" aria-describedby={helpId} value={draft.mode} disabled={readOnly}
                    onChange={(e) => set({ mode: e.target.value })}>
                    <option value="off">off</option>
                    <option value="builtin">builtin</option>
                    <option value="required">required</option>
                  </select>
                )}
              </FieldHelp>
              <FieldHelp label="Recovery test every" help={FIELD_HELP.assuranceRecoveryTestEvery}>
                {(helpId) => (
                  <input className="input input--mono" aria-label="Recovery test every" aria-describedby={helpId} aria-invalid={testProblem !== null}
                    value={draft.testEvery} disabled={readOnly} style={{ width: 100 }} onChange={(e) => set({ testEvery: e.target.value })} />
                )}
              </FieldHelp>
              <FieldHelp label="Validator" help={FIELD_HELP.assuranceValidator}>
                {(helpId) => (
                  <input className="input input--mono" aria-label="Validator" aria-describedby={helpId}
                    value={draft.validator} disabled={readOnly} style={{ width: 220 }} onChange={(e) => set({ validator: e.target.value })} />
                )}
              </FieldHelp>
            </div>
            <div style={{ display: "flex", gap: 14, flexWrap: "wrap", alignItems: "flex-start" }}>
              <FieldHelp label="Built-in check when no validator" help={FIELD_HELP.assuranceBuiltinCheck}>
                {(helpId) => (
                  <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
                    <span aria-hidden="true" style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>Built-in check when no validator</span>
                    <select className="input" aria-label="Built-in check when no validator" aria-describedby={helpId} value={draft.builtinCheck} disabled={readOnly}
                    onChange={(e) => set({ builtinCheck: e.target.value })}>
                    {BUILTIN_CHECKS.map((c) => <option key={c} value={c}>{c}</option>)}
                  </select>
                  </div>
                )}
              </FieldHelp>
            </div>
            {testProblem ? <div role="alert" style={{ color: "var(--text)" }}>{testProblem}</div> : null}
            {builtinProblem ? <div role="alert" style={{ color: "var(--text)" }}>{builtinProblem}</div> : null}
            {reservedProblem ? <div role="alert" style={{ color: "var(--text)" }}>{reservedProblem}</div> : null}
            <InfoTooltip id="assurance.settings.builtinCheck" block>
              <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }} data-testid="builtin-check-note">
                With structure, a set that declares no validator can still be tested: the engine recovers its newest known good copy, matches the digest, and reads the file end to end as gzip, zstd or tar, extracting nothing. It runs when you press Run recovery test or run <span className="mono">retnd recovery-test</span>, and on a schedule only under the builtin mode. It never satisfies the required mode, and a validator you declare always runs instead. A file type it has no reader for is refused before anything is staged.
              </div>
            </InfoTooltip>
            {requiredNeedsValidator ? (
              <div role="alert" style={{ color: "var(--text)" }}>A required recovery test needs a validator, so the server will refuse this until one is named.</div>
            ) : null}
            <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
              The validator is the name of a script of yours: NAME.local.sh under recovery-test/&lt;source&gt;/&lt;set&gt; in the workflow root, written without the .local.sh suffix. The engine re-reads the recovered copy and checks its digest first, then runs the script, and records the claim itself: the script never writes Content verified or Recovery tested.
            </div>
            {knobs.map(([name, label, value]) => (
              <div key={name} className="aknob" data-testid="host-knob" data-scope="deployment-wide" data-knob={name}>
                <span className="aknob__scope">Deployment-wide</span>
                <span>{label}</span>
                <b className="mono">{value ?? "unknown"}</b>
                {reason(name) ? <span style={{ flexBasis: "100%", color: "var(--text-2)" }}>{reason(name)}</span> : null}
              </div>
            ))}
            <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
              These belong to the host and the process. They are read here, changed in the configuration file, take effect when the serving process next starts, and have no per-set control. The staging directory is one place shared by recoveries and recovery tests: unless staging_root says otherwise it is <span className="mono">recovery-staging</span> beside the state database.
            </div>
            <CommandEcho label="Same thing from the terminal" commands={[settingsCommand(changes())]} />
            <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
              <button className="btn btn--primary"
                disabled={readOnly || !pending || scrubProblem !== null || testProblem !== null || requiredNeedsValidator || builtinProblem !== null || reservedProblem !== null}
                onClick={() => void save()}>Save</button>
              {saved ? <span role="status">Saved</span> : null}
            </div>
          </>
        ) : null}
        {error ? <div role="alert" style={{ color: "var(--text)", border: "1px solid var(--danger)", background: "var(--danger-quiet)", borderRadius: "var(--radius-md)", padding: 8 }}>{error}</div> : null}
      </div>
    </section>
  );
}
