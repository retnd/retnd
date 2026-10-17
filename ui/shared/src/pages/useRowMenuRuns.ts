/**
 * What the Backups row menu has started, and how it starts it (issue #1202,
 * the review of #1214): a recovery test, a scrub or a retry, each kept as a
 * `MenuRun` until the operator dismisses it.
 *
 * A hook of its own so the page only wires it. The key counter lives in the
 * hook's own ref, not at module level, so two mounted pages never share it. A
 * request that finishes after the page has gone changes nothing and reloads
 * nothing.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import type { RetndApi } from "@shared/api/contracts";
import { describeFailure } from "@shared/api/failure";
import { startRecoveryRun } from "@shared/assurance/useRunAssurance";
import type { GateReading } from "@shared/hooks/useDestructiveGate";
import type { Operation } from "@shared/assurance/types";
import type { BackupArtifact } from "@shared/types/backup";
import { isRunning } from "./backupRowMenu";
import type { MenuRun } from "./backupRowMenu";
import { RETRY_ACCEPTED, retryRefusalSentence } from "./retryIngestion";

export interface RowMenuRuns {
  runs: MenuRun[];
  test(a: BackupArtifact): Promise<void>;
  scrub(a: BackupArtifact): Promise<void>;
  retry(a: BackupArtifact): Promise<void>;
  dismiss(key: string): void;
  /** A run's operation changed state, as its panel read it. */
  onState(key: string, state: Operation["state"]): void;
}

export function useRowMenuRuns({ api, gate, onRetried }: { api: RetndApi; gate: GateReading; onRetried(): void }): RowMenuRuns {
  const [runs, setRuns] = useState<MenuRun[]>([]);
  const counter = useRef(0);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);
  // The latest of the things the handlers read, so the handlers can be stable.
  const latest = useRef({ api, gate, onRetried });
  useEffect(() => { latest.current = { api, gate, onRetried }; });

  const patch = useCallback((key: string, p: Partial<MenuRun>) => {
    if (!alive.current) return;
    setRuns((rs) => rs.map((r) => (r.key === key ? { ...r, ...p } : r)));
  }, []);
  const start = useCallback((a: BackupArtifact, kind: MenuRun["kind"]) => {
    counter.current += 1;
    const key = kind + ":" + a.id + ":" + counter.current;
    const run: MenuRun = { key, kind, setId: a.setId, setName: a.setName, artifactId: a.id, file: a.filename, starting: true };
    setRuns((rs) => [run, ...rs.filter((r) => isRunning(r) || r.kind !== kind || r.artifactId !== a.id)]);
    return key;
  }, []);

  const test = useCallback(async (a: BackupArtifact) => {
    const { api, gate } = latest.current;
    if (!api.assurance) return;
    const key = start(a, "recovery_test");
    const out = await startRecoveryRun(api.assurance, a.setId, a.id, gate);
    patch(key, { starting: false, opId: out.operation?.id, error: out.error ?? null, refused: out.refused ?? null });
  }, [start, patch]);
  const scrub = useCallback(async (a: BackupArtifact) => {
    const { api } = latest.current;
    if (!api.assurance) return;
    const key = start(a, "scrub");
    try {
      const o = await api.assurance.startScrub(a.setId);
      patch(key, { starting: false, opId: o.id });
    } catch (e) {
      patch(key, { starting: false, error: describeFailure(e, "could not start").message });
    }
  }, [start, patch]);
  const retry = useCallback(async (a: BackupArtifact) => {
    const key = start(a, "retry");
    try {
      await latest.current.api.retryFailedIngestion(a.id);
      patch(key, { starting: false, said: { tone: "ok", text: RETRY_ACCEPTED } });
      if (alive.current) latest.current.onRetried();
    } catch (e) {
      patch(key, { starting: false, error: retryRefusalSentence(e) });
    }
  }, [start, patch]);
  const dismiss = useCallback((key: string) => setRuns((rs) => rs.filter((r) => r.key !== key)), []);
  const onState = useCallback((key: string, state: Operation["state"]) => patch(key, { state }), [patch]);
  return { runs, test, scrub, retry, dismiss, onState };
}
