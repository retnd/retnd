/**
 * The Backup detail's Assurance card and the Recover artifact dialog
 * (issue #985).
 *
 * # The cost is a decision, not a toast
 *
 * When the copy is archived the dialog replaces its action with a decision
 * panel: provider, tier and estimate as the service states them, an explicit
 * "I accept this cost" box, and a primary action that stays disabled until it
 * is ticked. Focus starts on the dialog itself, never on a button, so a stray
 * Enter cannot take either answer, and Escape means "do not recover". The
 * acknowledgement travels in the request: without it the service refuses.
 *
 * # A download cannot lie about its outcome
 *
 * The outcome is a closed state read from the service. Complete, cancelled
 * and failed are three different renderings, and only `complete` ever draws
 * the word Recovered. A progress read that errors leaves the state where it
 * was, running, with the error shown: it never becomes a success.
 *
 * # Plain http
 *
 * Nothing here touches the Clipboard API. The command is shown as selectable
 * text, and a browser download is started by an anchor click, which works in
 * an insecure context.
 */
import "./assurance.css";
import { useEffect, useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { bytes } from "@shared/utilities/format";
import { ClaimChips } from "./ClaimChips";
import { recoverCommand } from "./assuranceCommands";
import type { ArchivalCost, ArtifactAssurance, AssuranceSettings, Recovery, RecoveryTarget } from "./types";

export function ArtifactAssuranceCard({ artifactId, readOnly }: { artifactId: string; readOnly: boolean }) {
  const api = useApi().assurance;
  const [a, setA] = useState<ArtifactAssurance | null>(null);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!api) return;
    let live = true;
    api.getArtifactAssurance(artifactId).then((x) => { if (live) setA(x); }, () => { if (live) setA(null); });
    return () => { live = false; };
  }, [api, artifactId]);
  return (
    <section className="card" aria-label="Assurance" style={{ marginBottom: 14 }}>
      <div className="card__header">
        <InfoTooltip id="assurance.artifact.card"><h2 className="eyebrow">Assurance</h2></InfoTooltip>
      </div>
      <div className="card__body" style={{ display: "flex", gap: 16, alignItems: "flex-start", flexWrap: "wrap" }}>
        <ClaimChips assurance={a} available={!!api} />
        <span style={{ flex: 1 }} />
        {api ? (
          <InfoTooltip id="assurance.recover.artifact">
            <button className="btn btn--primary" disabled={readOnly} onClick={() => setOpen(true)}>Recover artifact</button>
          </InfoTooltip>
        ) : null}
      </div>
      {open ? <RecoverArtifactDialog artifactId={artifactId} onClose={() => setOpen(false)} /> : null}
    </section>
  );
}

const TARGETS: Array<{ id: RecoveryTarget; label: string }> = [
  { id: "browser", label: "Download in this browser" },
  { id: "staging", label: "Staging path on the host" },
  { id: "destination", label: "Configured destination" }
];

export function RecoverArtifactDialog({ artifactId, onClose }: { artifactId: string; onClose(): void }) {
  const api = useApi().assurance;
  const [target, setTarget] = useState<RecoveryTarget>("browser");
  const [where, setWhere] = useState("");
  const [cost, setCost] = useState<ArchivalCost | null>(null);
  const [host, setHost] = useState<AssuranceSettings["host"] | null>(null);
  const [accepted, setAccepted] = useState(false);
  const [recovery, setRecovery] = useState<Recovery | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const streamed = useRef(new Set<string>());

  useEffect(() => { box.current?.focus(); }, []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  useEffect(() => {
    if (!api) return;
    let live = true;
    void api.getArchivalCost(artifactId).then((c) => { if (live) setCost(c); }, () => undefined);
    void api.getSettings().then((s) => { if (live) setHost(s.host); }, () => undefined);
    return () => { live = false; };
  }, [api, artifactId]);

  // Follow the recovery from the service until it reaches a terminal state.
  const id = recovery?.id;
  const state = recovery?.state;
  useEffect(() => {
    if (!api || !id || state !== "running") return;
    let live = true;
    const t = setInterval(() => {
      api.getRecovery(id).then((r) => { if (live) { setRecovery(r); setError(null); } },
        (e: unknown) => { if (live) setError(e instanceof Error ? e.message : "could not read progress"); });
    }, 500);
    return () => { live = false; clearInterval(t); };
  }, [api, id, state]);

  // A browser target streams through an ordinary link click, which needs no
  // secure context. Only once per recovery.
  useEffect(() => {
    if (recovery?.target === "browser" && recovery.streamUrl && !streamed.current.has(recovery.id)) {
      streamed.current.add(recovery.id);
      const a = document.createElement("a");
      a.href = recovery.streamUrl;
      a.rel = "noopener";
      a.click();
    }
  }, [recovery]);

  if (!api) return null;
  const archived = cost !== null;
  const needsAcceptance = archived && !recovery;

  const go = async () => {
    setBusy(true);
    setError(null);
    try {
      setRecovery(await api.createRecovery(artifactId, { target, where: where || undefined, acknowledgeCost: archived ? accepted : undefined }));
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not start");
    } finally {
      setBusy(false);
    }
  };
  const cancel = async () => {
    if (!recovery) return;
    try {
      await api.cancelRecovery(recovery.id);
      setRecovery(await api.getRecovery(recovery.id));
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not cancel");
    }
  };

  const pct = recovery && recovery.totalBytes > 0 ? Math.round((recovery.bytes / recovery.totalBytes) * 100) : 0;
  return (
    <div className="dialog-scrim" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div
        ref={box}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby="recover-title"
        className="dialog"
        style={{ maxWidth: 520 }}
      >
        <div style={{ padding: "20px 22px 0" }}>
          <h2 id="recover-title" style={{ margin: 0, fontSize: 18 }}>Recover artifact</h2>
          <div className="mono" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)", marginTop: 4 }}>{artifactId}</div>
        </div>
        <div style={{ padding: "14px 22px 20px", display: "flex", flexDirection: "column", gap: 12, fontSize: 13.5 }}>
          {!recovery ? (
            <>
              <InfoTooltip id="assurance.recover.target" block>
                <div role="radiogroup" aria-label="Where should the artifact go?" style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                  {TARGETS.map((t) => (
                    <label key={t.id} style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                      <input type="radio" name="recover-target" checked={target === t.id} onChange={() => { setTarget(t.id); setWhere(""); }} />
                      {t.label}
                    </label>
                  ))}
                </div>
              </InfoTooltip>
              {target === "browser" ? (
                <div style={{ color: "var(--text-2)" }}>Streams to your downloads folder. It works over plain http. The grant is single-use and ends with this session.</div>
              ) : null}
              {target === "staging" ? (
                <label className="field">
                  <span className="field__label">{"Staging root: " + (host?.stagingRoot ?? "unknown") + " (set by the host)"}</span>
                  <input className="input input--mono" aria-label="Recover to a name under the staging root" value={where} onChange={(e) => setWhere(e.target.value)} />
                </label>
              ) : null}
              {target === "destination" ? (
                <label className="field">
                  <span className="field__label">Destination id</span>
                  <input className="input input--mono" aria-label="Recover to destination id" value={where} onChange={(e) => setWhere(e.target.value)} />
                </label>
              ) : null}
              <CommandEcho label="Same thing from the terminal" commands={[recoverCommand(artifactId, target, where, archived && accepted)]} />
              {archived ? (
                <div role="group" aria-label="This copy is archived" className="anotice">
                  <strong>Recovering this copy costs money and takes time.</strong>
                  <div style={{ marginTop: 4 }}>
                    {"Provider " + cost.provider + ", retrieval tier " + cost.tier + ". " + cost.estimate + ". Nothing happens until you answer."}
                  </div>
                  <InfoTooltip id="assurance.recover.thawCost">
                    <label style={{ display: "inline-flex", gap: 6, alignItems: "center", marginTop: 8 }}>
                      <input type="checkbox" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />
                      I accept this cost
                    </label>
                  </InfoTooltip>
                </div>
              ) : null}
            </>
          ) : (
            <div
              data-testid="download-progress"
              data-state={recovery.state}
              data-bytes={recovery.bytes}
              style={{ display: "flex", flexDirection: "column", gap: 8 }}
            >
              <div className="aop__bar" role="progressbar" aria-label="Recovery progress" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}>
                <i style={{ width: pct + "%" }} />
              </div>
              <div className="mono" style={{ fontSize: "var(--text-sm)" }}>{bytes(recovery.bytes) + " of " + bytes(recovery.totalBytes)}</div>
              {recovery.state === "running" ? <button className="btn btn--destructive" onClick={() => void cancel()}>Cancel download</button> : null}
              {recovery.state === "cancelled" ? <div className="aop__outcome">Cancelled: nothing saved</div> : null}
              {recovery.state === "failed" ? <div className="aop__outcome" role="alert">Failed: nothing saved</div> : null}
              {recovery.state === "complete" ? (
                <div className="aop__outcome">{recovery.target === "browser" ? "Recovered: your browser has the file" : "Recovered"}</div>
              ) : null}
            </div>
          )}
          {error ? <div role="alert" style={{ border: "1px solid var(--danger)", background: "var(--danger-quiet)", borderRadius: "var(--radius-md)", padding: 8 }}>{error}</div> : null}
        </div>
        <div className="card__footer" style={{ display: "flex", justifyContent: "flex-end", gap: 9, borderRadius: "0 0 10px 10px" }}>
          <button className="btn" onClick={onClose}>{recovery ? "Close" : needsAcceptance ? "Do not recover" : "Cancel"}</button>
          {!recovery ? (
            <button
              className={"btn " + (archived ? "btn--destructive" : "btn--primary")}
              disabled={busy || (archived && !accepted)}
              onClick={() => void go()}
            >
              {archived ? "Pay for the thaw and recover" : "Recover artifact"}
            </button>
          ) : null}
        </div>
      </div>
    </div>
  );
}
