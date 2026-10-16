/**
 * The backups behind the Dashboard's Due, Overdue and Degraded counts, one row
 * each, with a Run assurance button per row (issue #1174).
 *
 * The rows are the tally's own ids (`tally.ids`), so the list and the counts
 * above it cannot disagree, and each row says why the backup is there in the
 * claims' own words (`listingReason`, the sentence `retnd assurance list`
 * prints too). Run assurance starts the recovery test of THAT backup, named
 * by `artifact_id`, the same as `retnd recovery-test SET --artifact ID`.
 *
 * The engine runs one recovery test per set at a time: asking again for the
 * same backup joins the running test, asking for another backup of the set
 * is refused. So a run belongs to its row, and while one runs the other rows
 * of its set are disabled with that reason rather than offered a press the
 * engine would refuse.
 *
 * Nothing here fans out per row. The capabilities are read once for the list
 * (the gate reading only words a refusal), every operation the list started
 * is followed by one poller (`useOperationReadings`), and each bucket draws
 * at most ROW_CAP rows, with a link to the rest on the Backups page.
 *
 * A row that was run stays listed, even after its backup has left every
 * bucket, and says what its claim reads now, so a row never vanishes under
 * the pointer and never says "finished" about a backup that is still owed.
 */
import { useMemo, useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { GateRefusalNotice } from "@shared/api/gate";
import { GateRefusal } from "@shared/components/GateRefusal";
import { useDestructiveGate } from "@shared/hooks/useDestructiveGate";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { AssuranceLink } from "./AssuranceLink";
import { recoveryTestCommand } from "./assuranceCommands";
import { stageLabel } from "./stages";
import { listingReason } from "./claims";
import type { Bucket } from "./claims";
import { NO_VALIDATOR_REASON, noteAssuranceChanged, recoveryTestWouldBeRefused } from "./verificationModel";
import type { BackupFacts, Tally } from "./verificationModel";
import { startRecoveryRun } from "./useRunAssurance";
import type { RunState } from "./useRunAssurance";
import { useOperationReadings } from "./useOperationReadings";
import type { OperationReading } from "./useOperationReadings";

const SEVERITY: Bucket[] = ["degraded", "overdue", "due"];
const BUCKET_WORD: Partial<Record<Bucket, string>> = { degraded: "Degraded", overdue: "Overdue", due: "Due" };
/** Rows drawn per bucket. The rest are a link to the Backups list, filtered. */
const ROW_CAP = 20;
const fileOf = (id: string) => id.split("/").pop() ?? id;

/** One row's run, keyed by the backup it was asked for. */
interface RowRun {
  starting: boolean;
  opId?: string;
  joined: boolean;
  error: string | null;
  refused: GateRefusalNotice | null;
}

function stateOf(run: RowRun | undefined, reading: OperationReading | undefined): RunState {
  if (!run) return "idle";
  if (run.starting) return "running";
  if (!run.opId) return "idle";
  return reading?.operation?.state ?? "running";
}

function DueRow({
  facts, buckets, readOnly, offered, run, reading, setBusyWith, onRun
}: {
  facts: BackupFacts;
  buckets: Bucket[];
  readOnly: boolean;
  offered: boolean;
  run: RowRun | undefined;
  reading: OperationReading | undefined;
  /** Another backup of this set whose test is running, if one is. */
  setBusyWith: string | null;
  onRun(): void;
}) {
  const file = fileOf(facts.artifactId);
  const cannot = recoveryTestWouldBeRefused(facts.policy);
  const state = stateOf(run, reading);
  const op = reading?.operation ?? null;
  const reasonId = "assurance-row-why-" + facts.artifactId.replace(/[^A-Za-z0-9_-]/g, "-");
  const now = "Recovery now: " + facts.recovery.label.toLowerCase() + (facts.recovery.age ? " " + facts.recovery.age : "") + ".";
  const said =
    state === "running" ? "Running" + (op ? ": " + stageLabel(op.stage) : "") + "."
    : state === "complete" ? now
    : state === "failed" ? "Recovery test failed. " + now
    : state === "cancelled" ? "Recovery test cancelled."
    : state === "untracked" ? "Recovery test is no longer on the live feed. Its result lands on the Recovery chip."
    : null;
  const why = cannot
    ? NO_VALIDATOR_REASON
    : setBusyWith
      ? "A recovery test of " + fileOf(setBusyWith) + " is running for this set, and the engine runs one per set at a time."
      : null;
  return (
    <li
      data-testid="assurance-due-row"
      data-artifact={facts.artifactId}
      data-state={state === "idle" ? undefined : state}
      style={{ display: "flex", gap: 16, alignItems: "flex-start", flexWrap: "wrap", padding: "8px 0", borderTop: "1px solid var(--border)" }}
    >
      <div style={{ flex: "1 1 320px", display: "flex", flexDirection: "column", gap: 2, minWidth: 0 }}>
        <span><span className="mono" style={{ overflowWrap: "anywhere" }}>{file}</span>{" in " + facts.setId}</span>
        <span style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
          {buckets.map((b) => BUCKET_WORD[b]).filter(Boolean).join(", ") + (buckets.length > 0 ? ". " : "") + listingReason(facts.content, facts.recovery)}
        </span>
        {said ? <span role="status" style={{ fontSize: "var(--text-sm)" }}>{said + (run?.joined && state === "running" ? " This backup's test was already running, so this joined it." : "")}</span> : null}
        {run?.error ? <span role="alert" className="aop__alert">{run.error}</span> : null}
        {reading?.error && state === "running" ? <span role="alert" className="aop__alert">{"Progress could not be read: " + reading.error}</span> : null}
        {why && offered ? <span id={reasonId} data-testid="assurance-row-why" className="arefuse">{why}</span> : null}
      </div>
      {offered ? (
        <button
          className="btn btn--sm"
          aria-label={"Run assurance for " + file}
          aria-describedby={why ? reasonId : undefined}
          disabled={readOnly || state === "running" || cannot || setBusyWith !== null}
          onClick={onRun}
        >
          Run assurance
        </button>
      ) : (
        <code className="mono" style={{ fontSize: "var(--text-xs)", userSelect: "all" }}>{recoveryTestCommand(facts.setId) + " --artifact " + facts.artifactId}</code>
      )}
      {run?.refused ? <div style={{ flexBasis: "100%" }}><GateRefusal notice={run.refused} /></div> : null}
    </li>
  );
}

export function AssuranceDueList({ tally, byArtifact, readOnly }: { tally: Tally; byArtifact: Record<string, BackupFacts>; readOnly: boolean }) {
  const root = useApi();
  const api = root.assurance;
  const offered = !!api?.offers.recoveryTest;
  const gate = useDestructiveGate();
  const [runs, setRuns] = useState<Record<string, RowRun>>({});
  // Which rows have a start on the wire, read before state catches up, so
  // two presses in one tick send one POST.
  const starting = useRef(new Set<string>());
  const opIds = useMemo(
    () => Object.values(runs).map((r) => r.opId).filter((id): id is string => !!id),
    [runs]
  );
  const readings = useOperationReadings(offered ? api : undefined, opIds, () => noteAssuranceChanged());

  const bucketsById = useMemo(() => {
    const m = new Map<string, Bucket[]>();
    for (const b of SEVERITY) for (const id of tally.ids[b]) m.set(id, [...(m.get(id) ?? []), b]);
    return m;
  }, [tally]);

  // Every row is drawn under its most urgent bucket, at most ROW_CAP a
  // bucket. A row that was run is always drawn, in its bucket or after them.
  const { ids, more } = useMemo(() => {
    const drawn: string[] = [];
    const more: Array<{ bucket: Bucket; n: number }> = [];
    const placed = new Set<string>();
    for (const b of SEVERITY) {
      const group = tally.ids[b].filter((id) => byArtifact[id] && !placed.has(id) && bucketsById.get(id)?.[0] === b);
      let shown = 0;
      for (const id of group) {
        placed.add(id);
        if (shown < ROW_CAP || runs[id]) { drawn.push(id); if (!runs[id]) shown += 1; }
      }
      const hidden = group.filter((id) => !drawn.includes(id)).length;
      if (hidden > 0) more.push({ bucket: b, n: hidden });
    }
    for (const id of Object.keys(runs)) if (!placed.has(id) && byArtifact[id]) drawn.push(id);
    return { ids: drawn, more };
  }, [tally, byArtifact, bucketsById, runs]);

  if (!tally.known || ids.length === 0) return null;

  // The backup each set's running test is for, if one is.
  const busy = new Map<string, string>();
  for (const [id, run] of Object.entries(runs)) {
    if (stateOf(run, run.opId ? readings[run.opId] : undefined) === "running") busy.set(byArtifact[id]?.setId ?? "", id);
  }

  const start = async (facts: BackupFacts) => {
    const id = facts.artifactId;
    if (!api || starting.current.has(id)) return;
    starting.current.add(id);
    setRuns((r) => ({ ...r, [id]: { starting: true, joined: false, error: null, refused: null } }));
    const out = await startRecoveryRun(api, facts.setId, id, gate);
    starting.current.delete(id);
    setRuns((r) => ({
      ...r,
      [id]: { starting: false, opId: out.operation?.id, joined: !!out.operation?.joined, error: out.error ?? null, refused: out.refused ?? null }
    }));
  };

  return (
    <div data-assurance="" style={{ flexBasis: "100%", display: "flex", flexDirection: "column", gap: 6 }}>
      <ul aria-label="Backups needing assurance" style={{ margin: 0, padding: 0, listStyle: "none", fontSize: "var(--text-sm)" }}>
        {ids.map((id) => {
          const facts = byArtifact[id];
          const run = runs[id];
          const other = busy.get(facts.setId);
          return (
            <DueRow
              key={id}
              facts={facts}
              buckets={bucketsById.get(id) ?? []}
              readOnly={readOnly}
              offered={offered}
              run={run}
              reading={run?.opId ? readings[run.opId] : undefined}
              setBusyWith={other && other !== id ? other : null}
              onRun={() => void start(facts)}
            />
          );
        })}
      </ul>
      {more.map(({ bucket, n }) => (
        <span key={bucket} style={{ fontSize: "var(--text-sm)" }}>
          <AssuranceLink to={"/backups?assurance=" + bucket} label={n + " more " + bucket + (n === 1 ? " backup" : " backups") + " on Backups"}>
            {n + " more " + bucket + (n === 1 ? " backup" : " backups") + " on Backups"}
          </AssuranceLink>
        </span>
      ))}
      <CommandEcho label="Same thing from the terminal" commands={[recoveryTestCommand("<source/set>") + " --artifact <id>", "retnd assurance list"]} />
    </div>
  );
}
