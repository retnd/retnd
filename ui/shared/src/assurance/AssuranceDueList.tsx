/**
 * The backups behind the Dashboard's Due, Overdue and Degraded counts, one row
 * each, with a Run assurance button per row (issue #1174).
 *
 * The rows are the tally's own ids (`tally.ids`), so the list and the counts
 * above it cannot disagree, and each row says why the backup is there in the
 * claims' own words. Run assurance starts the recovery test of the backup's
 * set through the same endpoint the set page uses (`useRunAssurance`), shows
 * the run on the row, and when it ends the model re-reads, which moves the
 * counts. A row that was run stays listed, saying how it ended, even after the
 * backup has left its bucket, so a row never vanishes under the pointer.
 *
 * A recovery test is per set, so two rows of one set share a run: the second
 * press joins the first and says so.
 */
import { useState } from "react";
import { GateRefusal } from "@shared/components/GateRefusal";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { recoveryTestCommand } from "./assuranceCommands";
import { stageLabel } from "./stages";
import type { Bucket } from "./claims";
import { recoveryTestWouldBeRefused } from "./verificationModel";
import type { BackupFacts, Tally } from "./verificationModel";
import { useRunAssurance } from "./useRunAssurance";

const SEVERITY: Bucket[] = ["degraded", "overdue", "due"];
const BUCKET_WORD: Partial<Record<Bucket, string>> = { degraded: "Degraded", overdue: "Overdue", due: "Due" };
const fileOf = (id: string) => id.split("/").pop() ?? id;
const NO_VALIDATOR = "No recovery-test validator is declared for this set, so a test can only be refused.";

/** "Content: never run. Recovery: could not complete 1d ago." */
function reasonOf(f: BackupFacts): string {
  const part = (name: string, c: { label: string; age?: string }) => name + ": " + c.label.toLowerCase() + (c.age ? " " + c.age : "");
  return part("Content", f.content) + ". " + part("Recovery", f.recovery) + ".";
}

function DueRow({ facts, buckets, readOnly, onStarted }: { facts: BackupFacts; buckets: Bucket[]; readOnly: boolean; onStarted(): void }) {
  const run = useRunAssurance(facts.setId);
  const file = fileOf(facts.artifactId);
  const cannot = recoveryTestWouldBeRefused(facts.policy);
  const reasonId = "assurance-row-why-" + facts.artifactId.replace(/[^A-Za-z0-9_-]/g, "-");
  const said =
    run.state === "running" ? "Running" + (run.operation ? ": " + stageLabel(run.operation.stage) : "") + "."
    : run.state === "complete" ? "Recovery test finished."
    : run.state === "failed" ? "Recovery test failed."
    : run.state === "cancelled" ? "Recovery test cancelled."
    : run.state === "untracked" ? "Recovery test is no longer on the live feed. Its result lands on the Recovery chip."
    : null;
  return (
    <li
      data-testid="assurance-due-row"
      data-artifact={facts.artifactId}
      data-state={run.state === "idle" ? undefined : run.state}
      style={{ display: "flex", gap: 16, alignItems: "flex-start", flexWrap: "wrap", padding: "8px 0", borderTop: "1px solid var(--border)" }}
    >
      <div style={{ flex: "1 1 320px", display: "flex", flexDirection: "column", gap: 2, minWidth: 0 }}>
        <span><span className="mono" style={{ overflowWrap: "anywhere" }}>{file}</span>{" in " + facts.setId}</span>
        <span style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
          {buckets.map((b) => BUCKET_WORD[b]).filter(Boolean).join(", ") + ". " + reasonOf(facts)}
        </span>
        {said ? <span role="status" style={{ fontSize: "var(--text-sm)" }}>{said + (run.joined && run.state === "running" ? " A test was already running for this set, so this joined it." : "")}</span> : null}
        {run.error ? <span role="alert" className="aop__alert">{run.error}</span> : null}
        {cannot && run.offered ? <span id={reasonId} data-testid="assurance-row-why" className="arefuse">{NO_VALIDATOR}</span> : null}
      </div>
      {run.offered ? (
        <button
          className="btn btn--sm"
          aria-label={"Run assurance for " + file}
          aria-describedby={cannot ? reasonId : undefined}
          disabled={readOnly || run.running || cannot}
          onClick={() => { onStarted(); void run.run(); }}
        >
          Run assurance
        </button>
      ) : (
        <code className="mono" style={{ fontSize: "var(--text-xs)", userSelect: "all" }}>{recoveryTestCommand(facts.setId)}</code>
      )}
      {run.refused ? <div style={{ flexBasis: "100%" }}><GateRefusal notice={run.refused} /></div> : null}
    </li>
  );
}

export function AssuranceDueList({ tally, byArtifact, readOnly }: { tally: Tally; byArtifact: Record<string, BackupFacts>; readOnly: boolean }) {
  const [kept, setKept] = useState<string[]>([]);
  const bucketsOfId = (id: string) => SEVERITY.filter((b) => tally.ids[b].includes(id));
  const ids = Array.from(new Set([...SEVERITY.flatMap((b) => tally.ids[b]), ...kept])).filter((id) => byArtifact[id]);
  if (!tally.known || ids.length === 0) return null;
  return (
    <div data-assurance="" style={{ flexBasis: "100%", display: "flex", flexDirection: "column", gap: 6 }}>
      <ul aria-label="Backups needing assurance" style={{ margin: 0, padding: 0, listStyle: "none", fontSize: "var(--text-sm)" }}>
        {ids.map((id) => (
          <DueRow
            key={id}
            facts={byArtifact[id]}
            buckets={bucketsOfId(id)}
            readOnly={readOnly}
            onStarted={() => setKept((k) => (k.includes(id) ? k : [...k, id]))}
          />
        ))}
      </ul>
      <CommandEcho label="Same thing from the terminal" commands={[recoveryTestCommand("<source/set>")]} />
    </div>
  );
}
