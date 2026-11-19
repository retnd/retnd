/**
 * The Backups row's Actions menu (issue #1202): the per-backup commands, one
 * row at a time, without opening the backup's own page.
 *
 * Every item is a control that already exists somewhere else, pressed through
 * the same code, so the menu cannot drift from it:
 *
 *   - Recover artifact opens the backup page's own RecoverArtifactDialog, with
 *     its source host choice, its step-up password and its cost decision;
 *   - Run recovery test is the Dashboard row's start (`startRecoveryRun`,
 *     #1174): it names this backup, the same as `retnd recovery-test SET
 *     --artifact ID`, and words a gate refusal the way every gated screen does;
 *   - Scrub this backup's set is the set page's Scrub now (`startScrub`). The
 *     route scrubs a SET, and the CLI's per-artifact scrub has no route behind
 *     it, so the item says it reads the whole set rather than pretending to
 *     read one file;
 *   - Retry ingestion is the backup page's Recovery card (#662), offered on the
 *     backups that card is drawn for and saying what that card says;
 *   - Open backup page is the row's own click, for a keyboard, which cannot
 *     click a row.
 *
 * Disabled items keep their place and say why, in the words the control they
 * mirror uses. A scrub or a test the menu started is followed in the panel
 * above the table (`MenuRuns`), with its progress, Cancel and its terminal
 * twin, and when it ends every claim on the page is re-read together
 * (`noteAssuranceChanged`), which is how the row's chips catch up.
 */
import { useEffect } from "react";
import { GateRefusal } from "@shared/components/GateRefusal";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { OperationPanel } from "@shared/assurance/OperationPanel";
import { useOperation } from "@shared/assurance/useOperation";
import { noteAssuranceChanged } from "@shared/assurance/verificationModel";
import type { AssuranceApi, Operation } from "@shared/assurance/types";
import { recoveryTestArtifactCommand, retryCommand, scrubCommand } from "@shared/assurance/assuranceCommands";
import type { MenuRun } from "./backupRowMenu";

/** The panel above the table: everything the row menus started. */
export function MenuRuns({
  runs, assurance, readOnly, onState, onDismiss
}: {
  runs: MenuRun[];
  assurance: AssuranceApi | undefined;
  readOnly: boolean;
  onState(key: string, state: Operation["state"]): void;
  onDismiss(key: string): void;
}) {
  if (runs.length === 0) return null;
  return (
    <section className="card" aria-label="Started from this list" data-assurance="" style={{ marginBottom: 14 }}>
      <div className="card__header">
        <h2 className="eyebrow">Started from this list</h2>
      </div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 14 }}>
        {runs.map((r) => (
          <MenuRunView key={r.key} run={r} assurance={assurance} readOnly={readOnly} onState={onState} onDismiss={onDismiss} />
        ))}
      </div>
    </section>
  );
}

function MenuRunView({
  run, assurance, readOnly, onState, onDismiss
}: {
  run: MenuRun;
  assurance: AssuranceApi | undefined;
  readOnly: boolean;
  onState(key: string, state: Operation["state"]): void;
  onDismiss(key: string): void;
}) {
  // The same follower the set page's Assurance section uses. When the run
  // ends, every surface that shows a claim re-reads together.
  const op = useOperation(run.opId ? assurance : undefined, run.opId ?? null, () => noteAssuranceChanged());
  const state = op.operation?.state;
  const { key } = run;
  useEffect(() => { if (state) onState(key, state); }, [state, key, onState]);
  const operations = !!assurance?.offers.operations;
  const title =
    run.kind === "scrub" ? "Scrub of " + run.setName
    : run.kind === "recovery_test" ? "Recovery test of " + run.file
    : "Retry ingestion of " + run.file;
  const twin = run.kind === "scrub" ? scrubCommand(run.setId) : run.kind === "recovery_test" ? recoveryTestArtifactCommand(run.setId, run.artifactId) : retryCommand(run.artifactId);
  const done = !run.starting && !(run.opId && (state === undefined || state === "running"));
  return (
    <div data-run={run.kind} data-artifact={run.artifactId} style={{ display: "flex", flexDirection: "column", gap: 8 }}>
      <div style={{ display: "flex", alignItems: "baseline", gap: 10, flexWrap: "wrap" }}>
        <strong>{title}</strong>
        {run.kind !== "scrub" ? <span style={{ fontSize: "var(--text-sm)", color: "var(--text-2)" }}>{"in " + run.setName}</span> : null}
        <span style={{ flex: 1 }} />
        {done ? <button className="btn btn--sm btn--quiet" aria-label={"Dismiss " + title} onClick={() => onDismiss(run.key)}>Dismiss</button> : null}
      </div>
      {run.starting ? <div role="status" style={{ fontSize: "var(--text-sm)" }}>Starting...</div> : null}
      {run.opId ? (
        <OperationPanel
          operation={op.operation}
          events={op.events}
          error={op.error}
          onCancel={() => void op.cancel()}
          readOnly={readOnly}
          detailLink={operations}
          cancellable={operations}
          cancelError={op.cancelError}
        />
      ) : null}
      {run.said ? (
        <p role="status" style={{ margin: 0, maxWidth: "72ch", fontSize: "var(--text-sm)", color: "var(--text)" }} data-tone={run.said.tone}>
          {run.said.text}
        </p>
      ) : null}
      {run.error ? <div role="alert" className="aop__alert">{run.error}</div> : null}
      {run.refused ? <GateRefusal notice={run.refused} /> : null}
      <CommandEcho label="Same thing from the terminal" commands={[twin]} />
    </div>
  );
}
