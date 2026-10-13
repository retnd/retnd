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
 * # Starting, cancelling and reading
 *
 * Four routes back the controls: POST /backup-sets/{source}/{set}/recovery-test
 * (Run recovery test), POST .../scrub (Scrub now), POST
 * /assurance/operations/{op}/cancel (Cancel) and GET
 * .../recovery-test/output (Show validator output). "Run recovery test" is a
 * real control whenever the server offers it, and the other three are drawn
 * only on a server that offers `operations`; elsewhere this shows the
 * terminal command that exists and says what the schedule does. It never
 * starts a fake run. Each button prints its terminal twin beside it.
 *
 * # What it says the schedule does
 *
 * Only what the engine does by default, read from the code: every poll of the
 * serving process (and of `retnd daemon`) runs a scrub pass over the copies
 * that are due, at the cadence shown, and then the recovery tests that are due
 * for sets whose mode is required or custom. A mode of off runs none.
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
import { recoveryTestCommand, scrubCommand, scrubContentCommand, setAssuranceCommand, showOutputCommand } from "./assuranceCommands";
import { BUILTIN_CHECKS, asBuiltinCheck, builtinOffProblem, reservedValidatorProblem } from "./builtinCheck";
import { MIN_RECOVERY_TEST_CADENCE_SECONDS, cadenceProblem, formatCadence, parseCadence } from "./duration";
import { useOperation } from "./useOperation";
import type { ArtifactAssurance, OperationKind, ValidatorResult } from "./types";

const RECOVERY_DOCS = "https://github.com/retnd/retnd/blob/main/docs/recovery.md#scrubbing-and-recovery-tests-what-runs-on-its-own-and-what-it-costs";

type Policy = "follow" | "own";
type TestMode = "off" | "builtin" | "required" | "custom";

interface Draft { policy: Policy; scrubEvery: string; strategy: string; mode: TestMode; testEvery: string; validator: string; builtinCheck: string }

const draftOf = (s: BackupSetAssurance): Draft => ({
  policy: s.isOverride ? "own" : "follow",
  scrubEvery: formatCadence(s.scrub.cadenceSeconds),
  strategy: s.scrub.strategy || "auto",
  mode: s.recoveryTest.mode === "required" || s.recoveryTest.mode === "custom" || s.recoveryTest.mode === "builtin" ? s.recoveryTest.mode : "off",
  testEvery: formatCadence(s.recoveryTest.cadenceSeconds),
  validator: s.recoveryTest.validatorId,
  builtinCheck: asBuiltinCheck(s.recoveryTest.builtinCheck)
});

/** The validator an own policy sends: none for off, and none for builtin, which
 *  the loader refuses to see one beside ("mode builtin runs no validator"). */
const ownValidator = (d: Draft): string => (d.mode === "off" || d.mode === "builtin" ? "" : d.validator);

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
  // The newest copy's own recovery record, for a server that does not send
  // validator_declared: true when every live copy was recorded not_configured.
  const [copyNotConfigured, setCopyNotConfigured] = useState(false);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [opId, setOpId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [settled, setSettled] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [validatorResult, setValidatorResult] = useState<ValidatorResult | null | undefined>(undefined);
  const operations = !!api?.offers.operations;
  const canRunTest = !!api?.offers.recoveryTest;
  // Following a run needs either: a scrub run (operations) or a recovery test
  // run (recoveryTest) both read their progress through the same seam.
  const follow = operations || canRunTest;
  // A test on a set with no validator can only be refused (#1108). The engine's
  // own dry run says so before the first press (validatorDeclared); a server
  // that does not send it is read from the newest copy's recorded state.
  const declared = policy?.recoveryTest.validatorDeclared;
  // effective_check is the engine's answer to "what would a test run now"
  // (#1111): "builtin" is a test that can run, so it is never the refusal
  // state; "none" is, with the engine's reason; absent falls back to the
  // pre-#1111 reading of validatorDeclared and the newest copy's record.
  const effective = policy?.recoveryTest.effectiveCheck;
  const noValidator = effective === "builtin" || effective === "validator" ? false
    : effective === "none" ? true
    : declared === false || (declared === undefined && copyNotConfigured);
  const noCheckReason = effective === "none" ? policy?.recoveryTest.effectiveCheckReason : undefined;
  const reasonId = "recovery-test-no-validator-" + setId.replace(/[^A-Za-z0-9_-]/g, "-");
  const policyRadioName = "assurance-policy-" + setId;

  const refresh = useCallback(async () => {
    const p = await root.getBackupSetAssurance(source, setName);
    root.listAssuranceDue({ backupSetId: setId }).then(setDue, () => setDue(null));
    // The copies are read before the policy is shown, so the Run recovery test
    // button is never drawn enabled for the instant before the copy's own
    // record (the fallback for a server without validator_declared) is known.
    let newestClaim: ArtifactAssurance | undefined;
    let recordedNotConfigured = false;
    if (api) {
      try {
        const [artifacts, claims] = await Promise.all([root.listArtifacts(setId), api.listArtifactAssurance(setId)]);
        const newest = [...artifacts].sort((a, b) => b.producedAt.localeCompare(a.producedAt))[0];
        newestClaim = newest ? claims.find((c) => c.artifactId === newest.id) : undefined;
        const recorded = (newest?.placements ?? []).filter((x) => x.status === "ACTIVE" && x.recoveryState).map((x) => x.recoveryState);
        recordedNotConfigured = recorded.length > 0 && recorded.every((r) => r === "not_configured");
      } catch { newestClaim = undefined; recordedNotConfigured = false; }
    }
    setLatest(newestClaim);
    setCopyNotConfigured(recordedNotConfigured);
    setPolicy(p);
    setDraft(draftOf(p));
  }, [root, api, setId, source, setName]);

  // Mount: read the set, and attach to a running operation if there is one.
  useEffect(() => {
    let live = true;
    void (async () => {
      try {
        await refresh();
        const running = follow && api ? await api.activeOperation(setId) : null;
        if (live && running) setOpId(running.id);
      } catch (e) {
        if (live) setError(describeFailure(e, "could not read assurance").message);
      }
    })();
    return () => { live = false; };
  }, [api, follow, setId, refresh]);

  // The old answer stays marked as "being re-checked" from the start of a
  // run until the claims have been re-read AFTER it ended, so a failing
  // scrub never leaves a pass on screen for the gap between the two.
  const op = useOperation(follow ? api : undefined, opId, (o) => { void refresh().finally(() => setSettled(o.id)); });
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
  const testProblem = own && draft && draft.mode === "custom" ? cadenceProblem(draft.testEvery, MIN_RECOVERY_TEST_CADENCE_SECONDS) : null;
  const validatorMissing = own && !!draft && draft.mode !== "off" && draft.mode !== "builtin" && draft.validator.trim() === "" && !policy?.recoveryTest.validatorId;
  const builtinProblem = own && draft ? builtinOffProblem(draft.mode, draft.builtinCheck) : null;
  const reservedProblem = own && draft && draft.mode !== "off" && draft.mode !== "builtin" ? reservedValidatorProblem(draft.validator) : null;
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
          recoveryTestValidatorId: ownValidator(draft),
          recoveryTestBuiltinCheck: draft.builtinCheck
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
                  <input type="radio" name={"assurance-policy-" + setId} value="own" checked={draft.policy === "own"} disabled={readOnly}
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
                  : policy.recoveryTest.mode === "builtin"
                    ? "Recovery test builtin (the product's structure check, no validator) every " + formatCadence(policy.recoveryTest.cadenceSeconds) + "."
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
                        <option value="builtin">builtin</option>
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
                  <FieldHelp label="Built-in check when no validator" help={FIELD_HELP.assuranceSetBuiltinCheck}>
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
                  {draft.mode !== "off" && draft.mode !== "builtin" ? (
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
                ) : draft.mode === "builtin" ? (
                  <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                    The builtin mode runs the product&apos;s own structure check on the schedule, with no validator, and never counts as the required mode. It needs the built-in check set to structure.
                  </div>
                ) : (
                  <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                    The validator is the name of a script of yours: <span className="mono">NAME.local.sh</span> under <span className="mono">{"recovery-test/" + source + "/" + setName}</span> in the workflow root. Write the name without the <span className="mono">.local.sh</span> suffix. The engine re-reads the recovered copy and checks its digest, then runs the script and records the claim itself.
                  </div>
                )}
                {scrubProblem ? <div role="alert">{scrubProblem}</div> : null}
                {builtinProblem ? <div role="alert">{builtinProblem}</div> : null}
                {reservedProblem ? <div role="alert">{reservedProblem}</div> : null}
                <CommandEcho label="Saving this policy from the terminal" commands={[setAssuranceCommand(setId, {
                  scrubCadenceSeconds: parseCadence(draft.scrubEvery) ?? 0,
                  scrubStrategy: draft.strategy,
                  recoveryTestMode: draft.mode,
                  recoveryTestCadenceSeconds: draft.mode === "custom" ? parseCadence(draft.testEvery) ?? 0 : 0,
                  recoveryTestValidatorId: ownValidator(draft),
                  recoveryTestBuiltinCheck: draft.builtinCheck
                })]} />
                {testProblem ? <div role="alert">{testProblem}</div> : null}
                {customNeedsValidator || validatorMissing ? (
                  <div role="alert">A recovery test needs a validator, so the server will refuse this until one is named.</div>
                ) : null}
              </>
            )}
            <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
              The staging root, its TTL and the read ceilings belong to the host. They are shown on Settings, take effect when the serving process next starts, and have no per-set control.
            </div>
            <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
              <button className="btn btn--sm btn--primary"
                disabled={readOnly || scrubProblem !== null || testProblem !== null || customNeedsValidator || validatorMissing || builtinProblem !== null || reservedProblem !== null
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

            <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
              {operations ? (
                <InfoTooltip id="assurance.scrub.now">
                  <button className="btn" disabled={readOnly || running || starting} onClick={() => void start("scrub")}>Scrub now</button>
                </InfoTooltip>
              ) : null}
              {canRunTest ? (
                <InfoTooltip id="assurance.recoveryTest.run">
                  <button className="btn" disabled={readOnly || running || starting || noValidator}
                    aria-describedby={noValidator ? reasonId : undefined} onClick={() => void start("recovery_test")}>Run recovery test</button>
                </InfoTooltip>
              ) : null}
              {operations ? (
                <InfoTooltip id="assurance.validator.show">
                  <button className="btn btn--quiet" onClick={() => void showValidator()}>Show validator output</button>
                </InfoTooltip>
              ) : null}
            </div>
            {policy?.recoveryTest.effectiveCheck === "builtin" ? (
              <InfoTooltip id="assurance.set.builtinCheck" block>
                <p data-testid="effective-check" style={{ margin: 0, color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                  No validator is declared for this set, so Run recovery test uses the built-in structure check: it recovers the newest known good copy, matches the digest and reads the file end to end.
                </p>
              </InfoTooltip>
            ) : null}
            {canRunTest && noValidator ? (
              <p id={reasonId} data-testid="recovery-test-no-validator" className="arefuse">
                {noCheckReason
                  ? <>{noCheckReason[0].toUpperCase() + noCheckReason.slice(1)}, so a test can only be refused.{" "}</>
                  : <>No recovery-test validator is declared for this set, so a test can only be refused.{" "}</>}
                To declare one, name a validator in{" "}
                <a href={"#" + policyRadioName} onClick={(e) => {
                  e.preventDefault();
                  document.querySelector<HTMLInputElement>('input[type="radio"][name="' + policyRadioName + '"][value="own"]')?.focus();
                }}>this set&apos;s policy settings</a>
                {" "}or put one script in its recovery-test directory; see{" "}
                <a href={RECOVERY_DOCS} target="_blank" rel="noreferrer">docs/recovery.md</a>.
              </p>
            ) : null}
            {operations ? (
              <>
                <CommandEcho label="Same thing from the terminal" commands={[scrubCommand(setId), recoveryTestCommand(setId), showOutputCommand(setId)]} />
                <div data-testid="scrub-schedule-note" style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                  Scrub now does not replace the schedule: every poll of the running service checks the copies that are due, at the cadence above. A scrub started here reads this set&apos;s copies now and is stopped with Cancel.
                </div>
              </>
            ) : (
              <div data-testid="no-run-route" style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                  This server has no route to start a scrub, so there is no Scrub now button. The scrub does not wait for one: every poll of the running service checks the copies that are due, at the cadence above. To scrub now, run this on the host:
                </div>
                <CommandEcho label="From the terminal" commands={[scrubCommand(setId), scrubContentCommand(setId), recoveryTestCommand(setId)]} />
              </div>
            )}
            {canRunTest ? (
              <div data-testid="recovery-test-note" style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                A recovery test also runs on its own, at its cadence, but only for a set whose mode is required or custom and whose validator script is in place, or whose mode is builtin; with the mode off none runs. Run recovery test starts one now and acknowledges no cost: a copy that would cost money to read is refused and recorded as skipped, and nothing is read.
              </div>
            ) : null}
            <OperationPanel operation={op.operation} events={op.events} error={op.error} onCancel={() => void op.cancel()} readOnly={readOnly} detailLink={operations} cancellable={operations} cancelError={op.cancelError} />
            {validatorResult === null ? <div role="status">No recovery test has run for this set yet.</div> : null}
            {validatorResult ? <ValidatorLog result={validatorResult} /> : null}
          </>
        ) : null}
        {error ? <div role="alert" style={{ color: "var(--text)", border: "1px solid var(--danger)", background: "var(--danger-quiet)", borderRadius: "var(--radius-md)", padding: 8 }}>{error}</div> : null}
      </div>
    </section>
  );
}
