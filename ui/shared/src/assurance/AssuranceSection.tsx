/**
 * The per-set Assurance section (issue #985, over #982's routes), beside the
 * Retention card and in its shape: which policy is in force and what it
 * resolves to, then the schedule, then the operations, then what the set
 * currently claims.
 *
 * # What the wire can say about a policy
 *
 * A set either follows the deployment or carries its OWN assurance block
 * (`is_override`). The server cannot say, field by field, which values a set
 * pinned, so this does not pretend to: following shows what the deployment
 * resolves to, and an own policy pins every value it shows. Saving "follow"
 * removes the block, which is what DELETE does.
 *
 * The four host-scoped knobs are not among the fields, and the section says
 * why in words: they belong to the host.
 *
 * # Starting a scrub or a recovery test
 *
 * The contract has no route for either, so on a server without them
 * (`offers.operations` false) this offers the terminal command that exists
 * and says the schedule does the rest. It never starts a fake run.
 */
import "./assurance.css";
import { useCallback, useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { AssuranceDue, BackupSetAssurance, BackupSetAssurancePatch } from "@shared/api/contracts";
import { describeFailure } from "@shared/api/failure";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { stamp } from "@shared/utilities/format";
import { ClaimChips } from "./ClaimChips";
import { OperationPanel } from "./OperationPanel";
import { ValidatorLog } from "./ValidatorLog";
import { scrubCommand, scrubContentCommand } from "./assuranceCommands";
import { cadenceProblem, formatCadence, parseCadence } from "./duration";
import { useOperation } from "./useOperation";
import type { ArtifactAssurance, OperationKind, ValidatorResult } from "./types";

type Policy = "follow" | "own";
type TestMode = "off" | "required" | "custom";

interface Draft { policy: Policy; scrubEvery: string; strategy: string; mode: TestMode; testEvery: string; validator: string }

const draftOf = (s: BackupSetAssurance): Draft => ({
  policy: s.isOverride ? "own" : "follow",
  scrubEvery: formatCadence(s.scrub.cadenceSeconds),
  strategy: s.scrub.strategy || "auto",
  mode: s.recoveryTest.mode === "required" || s.recoveryTest.mode === "custom" ? s.recoveryTest.mode : "off",
  testEvery: formatCadence(s.recoveryTest.cadenceSeconds),
  validator: s.recoveryTest.validatorId
});

function splitSet(setId: string): [string, string] {
  const i = setId.indexOf("/");
  return [setId.slice(0, i), setId.slice(i + 1)];
}

export function AssuranceSection({ setId, readOnly }: { setId: string; readOnly: boolean }) {
  const root = useApi();
  const api = root.assurance;
  const [source, setName] = splitSet(setId);
  const [policy, setPolicy] = useState<BackupSetAssurance | null>(null);
  const [due, setDue] = useState<AssuranceDue | null>(null);
  const [latest, setLatest] = useState<ArtifactAssurance | undefined>(undefined);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [opId, setOpId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [settled, setSettled] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [validatorResult, setValidatorResult] = useState<ValidatorResult | null | undefined>(undefined);
  const operations = !!api?.offers.operations;

  const refresh = useCallback(async () => {
    const p = await root.getBackupSetAssurance(source, setName);
    setPolicy(p);
    setDraft(draftOf(p));
    root.listAssuranceDue({ backupSetId: setId }).then(setDue, () => setDue(null));
    if (api) {
      try {
        const [artifacts, claims] = await Promise.all([root.listArtifacts(setId), api.listArtifactAssurance(setId)]);
        const newest = [...artifacts].sort((a, b) => b.producedAt.localeCompare(a.producedAt))[0];
        setLatest(newest ? claims.find((c) => c.artifactId === newest.id) : undefined);
      } catch { setLatest(undefined); }
    }
  }, [root, api, setId, source, setName]);

  // Mount: read the set, and attach to a running operation if there is one.
  useEffect(() => {
    let live = true;
    void (async () => {
      try {
        await refresh();
        const running = operations && api ? await api.activeOperation(setId) : null;
        if (live && running) setOpId(running.id);
      } catch (e) {
        if (live) setError(describeFailure(e, "could not read assurance").message);
      }
    })();
    return () => { live = false; };
  }, [api, operations, setId, refresh]);

  // The old answer stays marked as "being re-checked" from the start of a
  // run until the claims have been re-read AFTER it ended, so a failing
  // scrub never leaves a pass on screen for the gap between the two.
  const op = useOperation(operations ? api : undefined, opId, (o) => { void refresh().finally(() => setSettled(o.id)); });
  const running = op.operation?.state === "running";
  const checking = opId !== null && settled !== opId;

  const start = async (kind: OperationKind) => {
    if (!api || starting) return;
    setError(null);
    setStarting(true);
    try {
      const o = kind === "scrub" ? await api.startScrub(setId) : await api.startRecoveryTest(setId);
      setOpId(o.id);
    } catch (e) {
      setError(describeFailure(e, "could not start").message);
    } finally {
      setStarting(false);
    }
  };

  const set = (p: Partial<Draft>) => { setDraft((d) => (d ? { ...d, ...p } : d)); setSaved(false); };

  const own = draft?.policy === "own";
  const scrubProblem = own && draft ? cadenceProblem(draft.scrubEvery) : null;
  const testProblem = own && draft && draft.mode === "custom" ? cadenceProblem(draft.testEvery) : null;
  const validatorMissing = own && !!draft && draft.mode !== "off" && draft.validator.trim() === "" && !policy?.recoveryTest.validatorId;
  const customNeedsValidator = own && !!draft && draft.mode === "custom" && draft.validator.trim() === "";

  const save = async () => {
    if (!draft) return;
    setError(null);
    setSaved(false);
    try {
      if (draft.policy === "follow") {
        await root.resetBackupSetAssurance(source, setName);
      } else {
        const patch: BackupSetAssurancePatch = {
          scrubCadenceSeconds: parseCadence(draft.scrubEvery) ?? 0,
          scrubStrategy: draft.strategy,
          recoveryTestMode: draft.mode,
          recoveryTestCadenceSeconds: draft.mode === "custom" ? parseCadence(draft.testEvery) ?? 0 : 0,
          recoveryTestValidatorId: draft.mode === "off" ? "" : draft.validator
        };
        await root.patchBackupSetAssurance(source, setName, patch);
      }
      await refresh();
      setSaved(true);
    } catch (e) {
      setError(describeFailure(e, "could not save").message);
    }
  };

  const showValidator = async () => {
    if (!api) return;
    try { setValidatorResult(await api.getValidatorResult(setId)); }
    catch (e) { setError(describeFailure(e, "could not read the validator's output").message); }
  };

  const overdue = due?.items.filter((i) => i.overdue).length ?? 0;
  const nextDue = due?.items.map((i) => i.nextDueAt).filter(Boolean).sort()[0];
  const never = due?.items.filter((i) => !i.lastAssuredAt).length ?? 0;

  return (
    <section className="card" data-assurance="" aria-label="Assurance">
      <div className="card__header">
        <InfoTooltip id="assurance.set.section"><h2 className="eyebrow">Assurance</h2></InfoTooltip>
      </div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 12 }}>
        {!api ? (
          <p style={{ margin: 0 }}>This server does not report assurance yet, so nothing here is claimed.</p>
        ) : null}
        <ClaimChips assurance={latest} available={!!api} checking={checking} recoveryReported={!!api?.offers.recoveryClaim} />

        {draft && policy ? (
          <>
            <InfoTooltip id="assurance.set.mode" block>
              <div role="radiogroup" aria-label="Assurance policy for this set" style={{ display: "inline-flex", gap: 14, flexWrap: "wrap" }}>
                <label style={{ display: "inline-flex", gap: 5, alignItems: "center" }}>
                  <input type="radio" name={"assurance-policy-" + setId} checked={draft.policy === "follow"} disabled={readOnly}
                    onChange={() => set({ policy: "follow" })} />
                  Follow the deployment
                </label>
                <label style={{ display: "inline-flex", gap: 5, alignItems: "center" }}>
                  <input type="radio" name={"assurance-policy-" + setId} checked={draft.policy === "own"} disabled={readOnly}
                    onChange={() => set({ policy: "own" })} />
                  This set&apos;s own policy
                </label>
              </div>
            </InfoTooltip>
            {draft.policy === "follow" ? (
              <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }} data-testid="resolved-policy">
                {"Following the deployment: scrub every " + formatCadence(policy.scrub.cadenceSeconds) + ", reading " + (policy.scrub.strategy || "auto") + ". "}
                {policy.recoveryTest.mode === "off"
                  ? "Recovery tests are off."
                  : "Recovery test " + policy.recoveryTest.mode + " every " + formatCadence(policy.recoveryTest.cadenceSeconds) + " with " + (policy.recoveryTest.validatorId || "no validator named") + "."}
                {policy.isOverride ? " Saving this removes the set's own policy." : ""}
              </div>
            ) : (
              <>
                <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                  An own policy pins every value below for this set. A later change to the deployment&apos;s values does not move it.
                </div>
                <div style={{ display: "flex", gap: 12, flexWrap: "wrap", alignItems: "flex-start" }}>
                  <FieldHelp label="Scrub every" help={FIELD_HELP.assuranceSetScrubEvery}>
                    {(helpId) => (
                      <input className="input input--mono" aria-label="Scrub every" aria-describedby={helpId} aria-invalid={scrubProblem !== null}
                        value={draft.scrubEvery} disabled={readOnly} onChange={(e) => set({ scrubEvery: e.target.value })} style={{ width: 90 }} />
                    )}
                  </FieldHelp>
                  <FieldHelp label="Scrub reads" help={FIELD_HELP.assuranceScrubStrategy}>
                    {(helpId) => (
                      <select className="input" aria-label="Scrub reads" aria-describedby={helpId} value={draft.strategy} disabled={readOnly}
                        onChange={(e) => set({ strategy: e.target.value })}>
                        {["auto", "existence", "content"].map((s) => <option key={s} value={s}>{s}</option>)}
                      </select>
                    )}
                  </FieldHelp>
                  <FieldHelp label="Recovery test" help={FIELD_HELP.assuranceRecoveryTestMode}>
                    {(helpId) => (
                      <select className="input" aria-label="Recovery test" aria-describedby={helpId} value={draft.mode} disabled={readOnly}
                        onChange={(e) => set({ mode: e.target.value as TestMode })}>
                        <option value="off">off</option>
                        <option value="required">required</option>
                        <option value="custom">custom</option>
                      </select>
                    )}
                  </FieldHelp>
                  {draft.mode === "custom" ? (
                    <FieldHelp label="Recovery test every" help={FIELD_HELP.assuranceRecoveryTestEvery}>
                      {(helpId) => (
                        <input className="input input--mono" aria-label="Recovery test every" aria-describedby={helpId} aria-invalid={testProblem !== null}
                          value={draft.testEvery} disabled={readOnly} onChange={(e) => set({ testEvery: e.target.value })} style={{ width: 90 }} />
                      )}
                    </FieldHelp>
                  ) : null}
                  {draft.mode !== "off" ? (
                    <FieldHelp label="Validator" help={FIELD_HELP.assuranceSetValidator}>
                      {(helpId) => (
                        <input className="input input--mono" aria-label="Validator" aria-describedby={helpId}
                          value={draft.validator} disabled={readOnly} onChange={(e) => set({ validator: e.target.value })} style={{ width: 220 }} />
                      )}
                    </FieldHelp>
                  ) : null}
                </div>
                {draft.mode === "off" ? (
                  <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                    The recovery test is off for this set, so its Recovery claim reads Not configured rather than passing. Scrubbing still runs.
                  </div>
                ) : (
                  <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                    The validator is a script of yours (<span className="mono">NAME.local.sh</span> under <span className="mono">{"recovery-test/" + source + "/" + setName}</span> in the workflow root). The engine re-reads the recovered copy and checks its digest, then runs the script and records the claim itself. The name above is checked against the server&apos;s registered validators when you save; it does not choose the script.
                  </div>
                )}
                {scrubProblem ? <div role="alert">{scrubProblem}</div> : null}
                {testProblem ? <div role="alert">{testProblem}</div> : null}
                {customNeedsValidator || validatorMissing ? (
                  <div role="alert">A recovery test needs a validator, so the server will refuse this until one is named.</div>
                ) : null}
              </>
            )}
            <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
              Staging root, staging TTL, the read ceilings and the download memory budget belong to the host. They are shown on Settings and have no per-set control.
            </div>
            <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
              <button className="btn btn--sm btn--primary"
                disabled={readOnly || scrubProblem !== null || testProblem !== null || customNeedsValidator || validatorMissing
                  || (draft.policy === "follow" && !policy.isOverride)}
                onClick={() => void save()}>Save policy</button>
              {saved ? <span role="status">Saved</span> : null}
            </div>

            <div data-testid="scrub-schedule" style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
              {due === null
                ? "The scrub schedule could not be read."
                : due.items.length === 0
                  ? "No copy of this set is waiting for a scrub."
                  : (never > 0 ? never + (never === 1 ? " copy has" : " copies have") + " never been checked. " : "")
                    + (overdue > 0 ? overdue + " overdue. " : "")
                    + (nextDue ? "Next scrub due " + stamp(nextDue) + "." : "")}
            </div>

            {operations ? (
              <>
                <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
                  <InfoTooltip id="assurance.scrub.now">
                    <button className="btn" disabled={readOnly || running || starting} onClick={() => void start("scrub")}>Scrub now</button>
                  </InfoTooltip>
                  <InfoTooltip id="assurance.recoveryTest.run">
                    <button className="btn" disabled={readOnly || running || starting} onClick={() => void start("recovery_test")}>Run recovery test</button>
                  </InfoTooltip>
                  <button className="btn btn--quiet" onClick={() => void showValidator()}>Show validator output</button>
                </div>
                <CommandEcho label="Same thing from the terminal" commands={[scrubCommand(setId)]} />
              </>
            ) : (
              <div data-testid="no-run-route" style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                  This server has no route to start a scrub or a recovery test from the browser. Scrubs run on the schedule above, and a recovery test runs on its own schedule when its mode is required. To scrub now, run this on the host:
                </div>
                <CommandEcho label="From the terminal" commands={[scrubCommand(setId), scrubContentCommand(setId)]} />
              </div>
            )}
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
