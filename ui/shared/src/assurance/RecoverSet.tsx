/**
 * The Recover this backup set dialog (#1106, over POST /recoveries).
 *
 * # What it is
 *
 * The set page's way to get a whole backup set back from the set itself: every
 * retained restore point, or only the newest, or those since a date, to ONE
 * destination, as one operation. It is the single-backup Recover dialog's rules
 * applied to a list, not a second idea of recovery: each file is checked
 * against the digest recorded when it was ingested before it is handed over, a
 * file that fails the check is reported and skipped, and nothing half-written
 * is left behind.
 *
 * # The pre-flight comes first
 *
 * Opening the dialog asks the host for the plan before anything is chosen to
 * be run: how many backups, how big, which are quarantined, lost or archived,
 * what reading them costs, and whether they fit the destination. The plan is
 * redrawn when the choices change, and Recover stays off while the host would
 * refuse the run (the cost not accepted, the files not fitting), with the
 * host's own sentence beside it because that sentence carries the numbers.
 *
 * # No browser download
 *
 * Many files do not fit one browser download without an archive the host would
 * have to build and keep, so this dialog offers a folder under the staging root
 * and another storage destination, and says why there is no third choice. A
 * browser can still download each backup from its own page.
 *
 * # Cancel
 *
 * One Cancel for the whole run: it closes the connection, the host discards the
 * file it was reading and writes a manifest of what it had done, and this page
 * says which files are complete. It does not claim to know the manifest's name
 * because the host's closing line cannot reach a page that has hung up.
 *
 * # An incremental (kopia) set
 *
 * Its backups are restore points inside an encrypted repository, not files, so
 * "the whole set" is a snapshot restore. The dialog says so and gives the
 * command rather than pretending the two are one operation.
 */
import "./assurance.css";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { RefObject } from "react";
import { useApi } from "@shared/api/ApiContext";
import type {
  RecoverManyItem, RecoverManyPlan, RecoverManyProgress, RecoverManyRequest, RecoverManyResult, RecoveryTargetSpec
} from "@shared/api/contracts";
import { describeFailure } from "@shared/api/failure";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { Icon } from "@shared/design-system/icons";
import type { IconName } from "@shared/design-system/icons";
import { bytes } from "@shared/utilities/format";
import { DestinationSelect } from "./DestinationSelect";
import { HostFolderBrowser } from "./HostFolderBrowser";
import { recoverSetCommand, recoverSetPreflightCommand, snapshotRestoreCommand } from "./assuranceCommands";
import { SourceHostDetails, SourceHostUnavailable } from "./SourceHost";
import { SOURCE_LABEL, sourceHostReady, sourceHostRefusal, useSourceHost } from "./sourceHostState";

type Narrow = "all" | "newest" | "since";
type Where = "staging" | "storage" | "source";

const STATUS_LABEL: Record<RecoverManyItem["status"], string> = {
  ready: "Will be recovered",
  recovered: "Recovered",
  failed: "Failed",
  skipped: "Skipped",
  needs_thaw: "Needs a thaw",
  quarantined: "Quarantined",
  unrecoverable: "Lost"
};
/** One picture per status from the shared icon registry, so the state is never
 *  carried by colour alone (the word beside it says it too). */
const STATUS_ICON: Record<RecoverManyItem["status"], IconName> = {
  ready: "status-idle", recovered: "success", failed: "failure", skipped: "status-idle",
  needs_thaw: "warning", quarantined: "quarantine", unrecoverable: "failure"
};

type Phase =
  | { k: "choose" }
  | { k: "running"; lines: RecoverManyProgress[]; current?: RecoverManyItem; done: number; total: number; index: number; of: number; cancelling: boolean }
  | { k: "done"; result: RecoverManyResult }
  | { k: "refused"; message: string; code: string };

export function RecoverSetDialog({
  setId, engine, onClose, returnFocus
}: {
  setId: string;
  /** "kopia" sets have snapshots rather than files. */
  engine: string;
  onClose(): void;
  returnFocus?: RefObject<HTMLElement>;
}) {
  const api = useApi();
  const [narrow, setNarrow] = useState<Narrow>("all");
  const [since, setSince] = useState("");
  // Issue #1191: null until the source host has answered; it is the default
  // where the set has one, and nothing is preselected where it has none.
  const [chosen, setTarget] = useState<Where | null>(null);
  const [sourceAck, setSourceAck] = useState(false);
  const [sourcePassword, setSourcePassword] = useState("");
  const [sourceError, setSourceError] = useState<string | null>(null);
  const [where, setWhere] = useState("");
  const [accepted, setAccepted] = useState(false);
  const [stagingRoot, setStagingRoot] = useState<string | null>(null);
  const [plan, setPlan] = useState<RecoverManyPlan | null>(null);
  const [planFailure, setPlanFailure] = useState<string | null>(null);
  const [phase, setPhase] = useState<Phase>({ k: "choose" });
  const box = useRef<HTMLDivElement>(null);
  const abort = useRef<AbortController | null>(null);
  const kopia = engine === "kopia";
  const sourceHost = useSourceHost(kopia ? null : setId);
  // Until something is chosen: the source host once it has answered and can
  // be used, nothing if it cannot, and the old default (staging) on an engine
  // that does not offer it.
  const target: Where | null = chosen ?? (
    kopia || !sourceHost.ready ? null : sourceHost.preselect ? "source" : sourceHost.offered ? null : "staging");
  const sourceFolder = sourceHost.preflight?.folder ?? "";

  useEffect(() => {
    const back = returnFocus?.current ?? (document.activeElement as HTMLElement | null);
    box.current?.focus();
    return () => back?.focus?.();
  }, [returnFocus]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { if (phase.k !== "running") onClose(); return; }
      if (e.key !== "Tab" || !box.current) return;
      const items = [...box.current.querySelectorAll<HTMLElement>("button:not(:disabled), input:not(:disabled), a[href], select:not(:disabled), [tabindex]:not([tabindex='-1'])")];
      if (items.length === 0) return;
      const first = items[0], last = items[items.length - 1];
      const at = document.activeElement;
      if (e.shiftKey && (at === first || at === box.current)) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && at === last) { e.preventDefault(); first.focus(); }
      else if (!box.current.contains(at)) { e.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, phase.k]);
  useEffect(() => {
    let live = true;
    void api.getAssuranceSettings().then((s) => { if (live) setStagingRoot(s.stagingRoot); }, () => undefined);
    return () => { live = false; };
  }, [api]);

  const sinceIso = narrow === "since" && since ? new Date(since + "T00:00:00Z").toISOString() : undefined;
  const sinceMissing = narrow === "since" && !since;
  const targetSpec = useMemo((): RecoveryTargetSpec => {
    // Until a destination is named the plan is asked for against a placeholder
    // name: what is in the set, what it costs and how big it is do not depend
    // on where it goes, and the pre-flight is the thing worth seeing early.
    if (target === "source") return { kind: "source", path: sourceFolder, ...(sourceAck ? { acknowledged: true } : {}) };
    if (target === "storage") return { kind: "storage", medium: where.trim() || "destination" };
    return { kind: "staging", path: where.trim() || "recovery" };
  }, [target, where, sourceFolder, sourceAck]);
  const request = useCallback((): RecoverManyRequest => ({
    set: setId,
    ...(narrow === "newest" ? { newestOnly: true } : {}),
    ...(sinceIso ? { since: sinceIso } : {}),
    ...(accepted ? { acknowledged: true } : {}),
    target: targetSpec,
    ...(target === "source" && sourceHost.gate.stepUpRequired ? { stepUpPassword: sourcePassword } : {})
  }), [setId, narrow, sinceIso, accepted, targetSpec, target, sourceHost.gate.stepUpRequired, sourcePassword]);

  // The pre-flight, redrawn when a choice changes. A stale answer never
  // overwrites a newer one.
  const idle = phase.k === "choose" || phase.k === "refused";
  const skipPlan = kopia || !idle || sinceMissing;
  // The plan does not depend on the tick or the password, so typing one does
  // not ask for it again.
  const planRequest = useCallback((): RecoverManyRequest => {
    const rest = { ...request() };
    delete rest.stepUpPassword;
    const t = rest.target.kind === "source" ? { kind: "source" as const, path: rest.target.path } : rest.target;
    return { ...rest, target: t };
  }, [request]);
  const planKey = JSON.stringify(planRequest());
  useEffect(() => {
    if (skipPlan) return;
    let live = true;
    const t = setTimeout(() => {
      api.planRecoverMany(JSON.parse(planKey) as RecoverManyRequest).then(
        (p) => { if (live) { setPlan(p); setPlanFailure(null); } },
        (e) => { if (live) { setPlan(null); setPlanFailure(describeFailure(e, "The pre-flight could not be made.").message); } }
      );
    }, 150);
    return () => { live = false; clearTimeout(t); };
  }, [api, skipPlan, planKey]);

  const go = async () => {
    abort.current = new AbortController();
    const total = plan?.totals.bytes ?? 0;
    const of = (plan?.totals.ready ?? 0) + (plan?.totals.needsThaw ?? 0);
    setPhase({ k: "running", lines: [], done: 0, total, index: 0, of, cancelling: false });
    try {
      const result = await api.recoverMany(request(), {
        signal: abort.current.signal,
        onProgress: (p) => setPhase((cur) => {
          if (cur.k !== "running") return cur;
          if (p.kind === "plan") return { ...cur, total: p.totalBytes };
          if (p.kind === "start") return { ...cur, current: p.item, index: p.index, of: p.total };
          return { ...cur, lines: [...cur.lines, p], current: undefined, done: p.doneBytes, total: p.totalBytes, index: p.index, of: p.total };
        })
      });
      setPhase({ k: "done", result });
    } catch (e) {
      const code = (e as { api?: { code?: string } })?.api?.code ?? "";
      // The tick or the password (#1191): back to the choice, with the reason.
      const stepUp = target === "source" ? sourceHostRefusal(code) : null;
      if (stepUp) {
        setSourceError(stepUp);
        setPhase({ k: "choose" });
        return;
      }
      const f = describeFailure(e, "The recovery could not be started.");
      setPhase({ k: "refused", message: f.message, code });
    }
  };
  const cancel = () => {
    setPhase((cur) => (cur.k === "running" ? { ...cur, cancelling: true } : cur));
    abort.current?.abort();
  };

  // A plan for choices that no longer apply is not shown (the date still
  // missing, a run under way).
  const shown = skipPlan ? null : plan;
  const costNeeded = (shown?.chargeableCount ?? 0) > 0;
  const refusal = shown?.refusal;
  // The cost refusal is what the checkbox answers; any other refusal blocks.
  const hardRefusal = refusal && refusal.code !== "RECOVERY_COST_NOT_ACKNOWLEDGED" ? refusal : null;
  const actionable = (shown?.totals.ready ?? 0) + (shown?.totals.needsThaw ?? 0);
  const needsTarget = target === null || (target === "source" ? !sourceHostReady(sourceHost, sourceAck, sourcePassword) : where.trim() === "");
  const ackRequired = costNeeded && !accepted;
  const canGo = !!shown && actionable > 0 && !hardRefusal && !needsTarget && !ackRequired && !sinceMissing;
  const echoWhere = target === "source" ? sourceFolder : where.trim();
  const command = target ? recoverSetCommand(setId, narrow, since, target, echoWhere, costNeeded && accepted, sourceAck) : null;

  return (
    <div className="dialog-scrim" style={{ gridTemplateColumns: "minmax(0, 1fr)" }} onClick={(e) => e.target === e.currentTarget && phase.k !== "running" && onClose()}>
      <div
        ref={box}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby="recover-set-title"
        className="dialog"
        data-assurance=""
        data-phase={phase.k}
        style={{ maxWidth: 720, minWidth: 0, maxHeight: "92vh", display: "flex", flexDirection: "column" }}
      >
        <div style={{ padding: "20px 22px 0" }}>
          <h2 id="recover-set-title" style={{ margin: 0, fontSize: 18 }}>Recover this backup set</h2>
          <div className="mono" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)", marginTop: 4, wordBreak: "break-all" }}>{setId}</div>
        </div>
        <div style={{ padding: "14px 22px 20px", display: "flex", flexDirection: "column", gap: 12, fontSize: 13.5, overflow: "auto" }}>
          {kopia ? (
            <div role="status" className="anotice" data-testid="incremental-engine-set">
              <strong>This set is stored by the incremental engine.</strong>
              <div style={{ marginTop: 4 }}>
                Its backups are restore points inside an encrypted repository, not files, so getting the whole set back is a snapshot restore and not a copy of files. That is a different operation, and this page does not pretend the two are the same.
              </div>
              <div style={{ marginTop: 8 }}>
                <CommandEcho label="From the terminal" commands={[snapshotRestoreCommand(setId)]} />
              </div>
            </div>
          ) : null}

          {!kopia && idle ? (
            <>
              <fieldset className="field" style={{ border: 0, margin: 0, padding: 0 }}>
                <legend className="field__label">Which backups</legend>
                <div role="radiogroup" aria-label="Which backups of this set" style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                  <label style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                    <input type="radio" name="recover-set-narrow" checked={narrow === "all"} onChange={() => setNarrow("all")} />
                    Every retained restore point
                  </label>
                  <label style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                    <input type="radio" name="recover-set-narrow" checked={narrow === "newest"} onChange={() => setNarrow("newest")} />
                    The newest restore point only
                  </label>
                  <label style={{ display: "inline-flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                    <input type="radio" name="recover-set-narrow" checked={narrow === "since"} onChange={() => setNarrow("since")} />
                    Everything since
                    <input
                      type="date" className="input input--mono" aria-label="Recover everything since this date" value={since}
                      disabled={narrow !== "since"} onChange={(e) => setSince(e.target.value)}
                    />
                  </label>
                </div>
              </fieldset>

              <fieldset className="field" style={{ border: 0, margin: 0, padding: 0 }}>
                <legend className="field__label">Where it goes</legend>
                <div role="radiogroup" aria-label="Where should the set go?" style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                  {sourceHost.offered ? (
                    <label style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                      <input type="radio" name="recover-set-target" checked={target === "source"}
                        disabled={!sourceHost.preflight?.available || sourceHost.unavailable !== null}
                        onChange={() => { setTarget("source"); setWhere(""); }} />
                      {SOURCE_LABEL}
                    </label>
                  ) : null}
                  <label style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                    <input type="radio" name="recover-set-target" checked={target === "staging"} onChange={() => { setTarget("staging"); setWhere(""); }} />
                    Folder under the staging root on the host
                  </label>
                  <label style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                    <input type="radio" name="recover-set-target" checked={target === "storage"} onChange={() => { setTarget("storage"); setWhere(""); }} />
                    Configured destination
                  </label>
                </div>
                <div style={{ color: "var(--text-2)", marginTop: 4 }} data-testid="no-browser-download">
                  There is no browser download for a set: that would be one archive the host has to build and keep, and every backup can still be downloaded from its own page.
                </div>
                {sourceHost.offered && sourceHost.unavailable ? (
                  <div style={{ marginTop: 6 }}><SourceHostUnavailable reason={sourceHost.unavailable} /></div>
                ) : null}
                {target === "source" ? (
                  <div style={{ marginTop: 6 }}>
                    <SourceHostDetails host={sourceHost} folder={sourceFolder} acknowledged={sourceAck} onAcknowledged={setSourceAck}
                      password={sourcePassword} onPassword={setSourcePassword} error={sourceError} />
                  </div>
                ) : target === "staging" ? (
                  <>
                    <label className="field" style={{ marginTop: 6 }}>
                      <span className="field__label">{"Folder name under the staging root " + (stagingRoot ? stagingRoot + " " : "") + "(set by the host)"}</span>
                      <input className="input input--mono" aria-label="Recover the set to a folder under the staging root" value={where} onChange={(e) => setWhere(e.target.value)} />
                    </label>
                    {/* Issue #1179. A set goes into a folder, so a chosen folder is the answer as it stands. */}
                    <HostFolderBrowser onPick={setWhere} />
                  </>
                ) : target === "storage" ? (
                  <div style={{ marginTop: 6 }}>
                    <DestinationSelect ariaLabel="Recover the set to destination" value={where} onChange={setWhere} />
                  </div>
                ) : null}
              </fieldset>

              <PreFlight plan={shown} failure={planFailure} sinceMissing={sinceMissing} />

              {costNeeded ? (
                <div role="group" aria-label="This recovery costs money" className="anotice" data-testid="set-cost">
                  <strong>
                    {shown!.chargeableCount + " of these " + shown!.totals.count + " files cost money to read (" + bytes(shown!.chargeableBytes) + " in all)."}
                  </strong>
                  <div style={{ marginTop: 4 }}>
                    A copy on a billed destination is downloaded, and a copy on an archive class needs the provider to restore it first, which takes hours. retnd holds no price list, so it shows no amount. One answer covers the whole set, and nothing starts until you give it.
                  </div>
                  <label style={{ display: "inline-flex", gap: 6, alignItems: "center", marginTop: 8 }}>
                    <input type="checkbox" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />
                    I accept the cost of recovering these files
                  </label>
                </div>
              ) : null}
              {hardRefusal ? (
                <div role="alert" data-testid="preflight-refusal" data-code={hardRefusal.code} className="aop__alert">
                  <strong>This would be refused: nothing has been read.</strong>
                  <div>{hardRefusal.message}</div>
                </div>
              ) : null}

              {command && target ? (
                <CommandEcho label="Same thing from the terminal" commands={[command, recoverSetPreflightCommand(setId, narrow, since, target, echoWhere)]} />
              ) : null}
              {phase.k === "refused" ? (
                <div role="alert" data-testid="refusal" data-code={phase.code} className="aop__alert">
                  <strong>Nothing was recovered</strong>
                  <div>{phase.message}</div>
                </div>
              ) : null}
            </>
          ) : null}

          {phase.k === "running" ? <Running phase={phase} /> : null}
          {phase.k === "done" ? <Summary result={phase.result} /> : null}
        </div>
        <div className="card__footer" style={{ display: "flex", justifyContent: "flex-end", gap: 9, borderRadius: "0 0 10px 10px" }}>
          {phase.k === "running" ? (
            <button className="btn btn--caution" disabled={phase.cancelling} onClick={cancel}>
              {phase.cancelling ? "Cancelling..." : "Cancel the recovery"}
            </button>
          ) : (
            <>
              <button className="btn" onClick={onClose}>{idle ? "Cancel" : "Close"}</button>
              {idle && !kopia ? (
                <button className="btn btn--primary" disabled={!canGo} onClick={() => void go()}>Recover this backup set</button>
              ) : null}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function Glyph({ status }: { status: RecoverManyItem["status"] }) {
  return <span aria-hidden="true" className="rset__glyph" data-status={status}><Icon name={STATUS_ICON[status]} /></span>;
}

function PreFlight({ plan, failure, sinceMissing }: { plan: RecoverManyPlan | null; failure: string | null; sinceMissing: boolean }) {
  if (sinceMissing) return <div role="status" data-testid="preflight" style={{ color: "var(--text-2)" }}>Pick a date to see what would be recovered.</div>;
  if (failure) return <div role="alert" data-testid="preflight-failure" className="aop__alert">{failure}</div>;
  if (!plan) return <div role="status" data-testid="preflight" aria-busy="true" style={{ color: "var(--text-2)" }}>Asking the host what would be recovered...</div>;
  const t = plan.totals;
  return (
    <section className="aop" aria-label="Pre-flight" data-testid="preflight">
      <strong>Pre-flight: nothing has been read yet</strong>
      <div data-testid="preflight-summary">
        {t.count + " backups: " + t.ready + " to recover (" + bytes(t.bytes) + "), " + t.needsThaw + " need a thaw, " + t.quarantined + " quarantined, " + t.unrecoverable + " lost, " + t.skipped + " not restore points."}
      </div>
      {plan.freeBytes !== undefined ? (
        <div style={{ color: "var(--text-2)" }} data-testid="preflight-space">
          {"The staging disk has " + bytes(plan.freeBytes) + " free and keeps " + bytes(plan.reserveBytes) + " in reserve."}
        </div>
      ) : null}
      <div style={{ color: "var(--text-2)" }}>
        {"Files go to " + plan.folder + ", with a manifest beside them. Quarantined and lost backups are listed here and are never recovered."}
      </div>
      <ul className="rset__rows" aria-label="Backups in this recovery">
        {plan.items.map((i) => (
          <li key={i.artifactId} data-status={i.status} className="rset__row">
            <Glyph status={i.status} />
            <span className="rset__name mono">{i.name}</span>
            <span className="rset__status">{STATUS_LABEL[i.status]}</span>
            {i.sizeBytes !== undefined && (i.status === "ready" || i.status === "needs_thaw") ? <span className="rset__size">{bytes(i.sizeBytes)}</span> : null}
            {i.message ? <span className="rset__why">{i.message}</span> : null}
          </li>
        ))}
      </ul>
    </section>
  );
}

function Running({ phase }: { phase: Extract<Phase, { k: "running" }> }) {
  const pct = phase.total > 0 ? Math.min(100, Math.round((phase.done / phase.total) * 100)) : 0;
  return (
    <section className="aop" aria-label="Recovery in progress" data-testid="running">
      <strong>{phase.cancelling ? "Cancelling: the file in flight is being discarded" : "Recovering"}</strong>
      <div
        className="aop__bar" role="progressbar" aria-label="Bytes recovered" aria-valuemin={0} aria-valuemax={phase.total} aria-valuenow={phase.done}
      >
        <i style={{ width: pct + "%" }} />
      </div>
      <div data-testid="running-total" aria-live="polite">
        {"File " + Math.min(phase.index, phase.of) + " of " + phase.of + ", " + bytes(phase.done) + " of " + bytes(phase.total) + " recovered."}
      </div>
      {phase.current ? <div className="mono" data-testid="running-current">{"Reading and checking " + phase.current.name}</div> : null}
      <ul className="aop__lines" aria-label="Files so far" data-testid="running-lines">
        {phase.lines.map((l, n) => l.kind === "item" ? (
          <li key={n} data-status={l.item.status}>
            {"[" + l.index + "/" + l.total + "] " + STATUS_LABEL[l.item.status].toLowerCase() + " " + l.item.name + (l.item.status === "recovered" ? " " + bytes(l.item.bytes ?? 0) : l.item.message ? ": " + l.item.message : "")}
          </li>
        ) : null)}
      </ul>
    </section>
  );
}

function Summary({ result }: { result: RecoverManyResult }) {
  const t = result.totals;
  const problems = result.items.filter((i) => i.status !== "recovered");
  return (
    <section className="aop" aria-label="Recovery result" data-testid="result" data-cancelled={result.cancelled ? "true" : "false"} role="status">
      <strong className="aop__outcome">
        {result.cancelled ? "Cancelled: " + t.recovered + " files are complete" : t.recovered + " of " + t.count + " backups recovered"}
      </strong>
      <ul className="rset__counts" data-testid="result-counts">
        <li>{"Recovered: " + t.recovered}</li>
        <li>{"Failed: " + t.failed}</li>
        <li>{"Skipped: " + t.skipped}</li>
        <li>{"Needs a thaw: " + t.needsThaw}</li>
        <li>{"Quarantined: " + t.quarantined}</li>
        <li>{"Lost: " + t.unrecoverable}</li>
      </ul>
      <div>{"Each recovered file was matched to the digest recorded when it was ingested before it was placed. " + bytes(t.bytes) + " in all."}</div>
      {result.cancelled ? (
        <div data-testid="result-cancelled-note">
          The run was cancelled. The files listed as recovered are complete; the file that was being read was discarded and no half-written file was kept. The host writes a manifest of what it did beside the files.
        </div>
      ) : result.manifestPath || result.manifestKey ? (
        <div className="mono" style={{ wordBreak: "break-all" }} data-testid="result-manifest">
          {"Manifest: " + (result.manifestPath ?? result.manifestKey)}
        </div>
      ) : null}
      {result.error ? <div role="alert" className="aop__alert">{result.error.message}</div> : null}
      {problems.length > 0 ? (
        <ul className="rset__rows" aria-label="Backups that were not recovered" data-testid="result-problems">
          {problems.map((i) => (
            <li key={i.artifactId} data-status={i.status} className="rset__row">
              <Glyph status={i.status} />
              <span className="rset__name mono">{i.name}</span>
              <span className="rset__status">{STATUS_LABEL[i.status]}</span>
              {i.message ? <span className="rset__why">{i.message}</span> : null}
            </li>
          ))}
        </ul>
      ) : null}
      <div style={{ color: "var(--text-2)" }}>This recovered the backups. Putting the application back stays yours to do.</div>
    </section>
  );
}
