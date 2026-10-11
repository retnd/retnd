/**
 * The Backup detail's Assurance card and the Recover artifact dialog
 * (issue #985, over the routes of #976).
 *
 * # What a recovery really is
 *
 * Asking for a recovery MINTS a grant: a short-lived, single-use token bound
 * to this sign-in. Nothing is read until the grant is redeemed, and when it is
 * the host reads one copy in full, checks ALL of it against the SHA-256 the
 * journal recorded, and only then delivers a byte. So the dialog has a phase
 * the first version of this screen did not: "checking the copy on the host".
 * For a download nothing has been sent to the browser during it, and the page
 * cannot see the browser's own transfer afterwards, so it never says the file
 * arrived. For a server-side target the host answers with the digest it
 * placed, and that is the only recovery this dialog calls Recovered.
 *
 * # An archived copy is a restore, then a recovery
 *
 * A copy on an archive class cannot be read until the provider restores it,
 * which takes hours and is billed. A grant lasts minutes, so the two cannot
 * be one step: the dialog asks for the restore (with the cost acknowledged,
 * never implied), then watches the copy's access change, and offers the
 * recovery once it is readable. A browser download is not offered for an
 * archived copy, because the grant would expire long before the thaw ended.
 * The page shows no price and no finishing time: retnd holds neither.
 *
 * # Plain http
 *
 * Nothing here touches the Clipboard API. The command is shown as selectable
 * text, and a browser download is started by an anchor click, which works in
 * an insecure context.
 */
import "./assurance.css";
import { useCallback, useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { useApi } from "@shared/api/ApiContext";
import { apiErrorOf } from "@shared/api/failure";
import type { RecoveryMint, RecoveryPlacement, RecoveryTargetSpec } from "@shared/api/contracts";
import type { BackupArtifact } from "@shared/types/backup";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { bytes } from "@shared/utilities/format";
import { ClaimChips } from "./ClaimChips";
import { recoverCommand, thawCommand } from "./assuranceCommands";
import { planOf, THAW_POLL_MS } from "./copyPlan";
import { recoveryRefusal } from "./recoveryMessages";
import type { RecoveryRefusal } from "./recoveryMessages";
import type { ArtifactAssurance } from "./types";

export function ArtifactAssuranceCard({ artifactId, readOnly }: { artifactId: string; readOnly: boolean }) {
  const api = useApi().assurance;
  const [a, setA] = useState<ArtifactAssurance | null>(null);
  const [open, setOpen] = useState(false);
  const opener = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!api) return;
    let live = true;
    api.getArtifactAssurance(artifactId).then((x) => { if (live) setA(x); }, () => { if (live) setA(null); });
    return () => { live = false; };
  }, [api, artifactId]);
  return (
    <section className="card" data-assurance="" aria-label="Assurance" style={{ marginBottom: 14 }}>
      <div className="card__header">
        <InfoTooltip id="assurance.artifact.card"><h2 className="eyebrow">Assurance</h2></InfoTooltip>
      </div>
      <div className="card__body" style={{ display: "flex", gap: 16, alignItems: "flex-start", flexWrap: "wrap" }}>
        <ClaimChips assurance={a} available={!!api} />
        <span style={{ flex: 1 }} />
        {api ? (
          <InfoTooltip id="assurance.recover.artifact">
            <button ref={opener} className="btn btn--primary" disabled={readOnly} onClick={() => setOpen(true)}>Recover artifact</button>
          </InfoTooltip>
        ) : null}
      </div>
      {open ? <RecoverArtifactDialog artifactId={artifactId} returnFocus={opener} onClose={() => setOpen(false)} /> : null}
    </section>
  );
}

const TARGETS: Array<{ id: RecoveryTargetSpec["kind"]; label: string }> = [
  { id: "download", label: "Download in this browser" },
  { id: "staging", label: "Staging path on the host" },
  { id: "storage", label: "Configured destination" }
];

type Phase =
  | { k: "choose" }
  | { k: "minting" }
  | { k: "thawing"; mint?: Extract<RecoveryMint, { kind: "restore" }> }
  | { k: "handed"; mint: Extract<RecoveryMint, { kind: "grant" }> }
  | { k: "verifying"; mint: Extract<RecoveryMint, { kind: "grant" }> }
  | { k: "placed"; placement: RecoveryPlacement }
  | { k: "refused"; refusal: RecoveryRefusal };

export function RecoverArtifactDialog({
  artifactId, onClose, returnFocus
}: {
  artifactId: string;
  onClose(): void;
  /** Where focus goes when the dialog closes. */
  returnFocus?: RefObject<HTMLElement>;
}) {
  const api = useApi();
  const [artifact, setArtifact] = useState<BackupArtifact | null>(null);
  const [stagingRoot, setStagingRoot] = useState<string | null>(null);
  const [target, setTarget] = useState<RecoveryTargetSpec["kind"]>("download");
  const [where, setWhere] = useState("");
  const [accepted, setAccepted] = useState(false);
  /** The service's own sentence when it refused a read for its cost although
   *  the placements said the copy was readable, e.g. a billed download. */
  const [costWall, setCostWall] = useState<string | null>(null);
  const [phase, setPhase] = useState<Phase>({ k: "choose" });
  const box = useRef<HTMLDivElement>(null);
  const setId = artifactId.split("/").slice(0, 2).join("/");

  useEffect(() => {
    const back = returnFocus?.current ?? (document.activeElement as HTMLElement | null);
    box.current?.focus();
    return () => back?.focus?.();
  }, [returnFocus]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { onClose(); return; }
      // aria-modal says the page behind is inert; this makes Tab honour it.
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
  }, [onClose]);

  // Read the copies once, and the staging root the host reports.
  useEffect(() => {
    let live = true;
    void api.getArtifact(artifactId).then((x) => {
      if (!live) return;
      setArtifact(x);
      // A browser download is not offered for an archived copy, so the choice
      // starts on the next target rather than on a disabled radio.
      if (planOf(x).archived) setTarget("staging");
      // A reload mid-restore re-attaches: the provider is still restoring, so
      // say so instead of offering to start a second one.
      if (planOf(x).restoring) setPhase({ k: "thawing" });
    }, () => undefined);
    void api.getAssuranceSettings().then((s) => { if (live) setStagingRoot(s.stagingRoot); }, () => undefined);
    return () => { live = false; };
  }, [api, artifactId]);

  const plan = artifact ? planOf(artifact) : null;
  const archived = !!plan?.archived;
  const needsAck = archived || costWall !== null;
  const kind = target;

  // While the provider restores the copy, watch its access. The page shows
  // what the host reports and nothing it could only guess: no percentage, no
  // finishing time.
  const thawing = phase.k === "thawing";
  useEffect(() => {
    if (!thawing) return;
    let live = true;
    const t = setInterval(() => {
      api.getArtifact(artifactId).then((x) => { if (live) setArtifact(x); }, () => undefined);
    }, THAW_POLL_MS);
    return () => { live = false; clearInterval(t); };
  }, [api, artifactId, thawing]);

  const targetSpec = useCallback((): RecoveryTargetSpec => {
    if (kind === "staging") return { kind: "staging", path: where };
    if (kind === "storage") return { kind: "storage", medium: where };
    return { kind: "download" };
  }, [kind, where]);

  const fail = (e: unknown) => setPhase({ k: "refused", refusal: recoveryRefusal(e, setId) });

  // A browser download is a link the browser follows itself: the bytes never
  // pass through this page, and a link click needs no secure context.
  const startDownload = (m: Extract<RecoveryMint, { kind: "grant" }>) => {
    const a = document.createElement("a");
    a.href = m.downloadUrl;
    a.rel = "noopener";
    a.download = "";
    a.click();
    setPhase({ k: "handed", mint: m });
  };

  const go = async () => {
    setPhase({ k: "minting" });
    try {
      const m = await api.mintRecovery(artifactId, { target: targetSpec(), acknowledged: accepted ? true : undefined });
      if (m.kind === "restore") { setPhase({ k: "thawing", mint: m }); return; }
      if (m.target.kind === "download") { startDownload(m); return; }
      setPhase({ k: "verifying", mint: m });
      try {
        setPhase({ k: "placed", placement: await api.redeemRecovery(m.grant) });
      } catch (e) { fail(e); }
    } catch (e) {
      // An unacknowledged cost starts nothing and sends the person to the
      // decision, whatever the placements said.
      const api_ = apiErrorOf(e);
      if (api_?.code === "RECOVERY_COST_NOT_ACKNOWLEDGED") {
        setCostWall(api_.message);
        setPhase({ k: "choose" });
        return;
      }
      fail(e);
    }
  };

  const needsTarget = kind !== "download" && where.trim() === "";
  const blocked = plan ? plan.none || plan.unreachable : false;
  const ackRequired = needsAck && !accepted;
  const ready = phase.k === "thawing" && plan?.readable;
  const idle = phase.k === "choose" || phase.k === "refused";
  const verifyingHost = phase.k === "verifying" || phase.k === "handed";
  const recorded = artifact && artifact.checksum ? (artifact.checksumAlgorithm || "sha256") + ":" + artifact.checksum : null;

  return (
    <div className="dialog-scrim" style={{ gridTemplateColumns: "minmax(0, 1fr)" }} onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div
        ref={box}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby="recover-title"
        className="dialog"
        data-assurance=""
        data-phase={phase.k}
        style={{ maxWidth: 560, minWidth: 0 }}
      >
        <div style={{ padding: "20px 22px 0" }}>
          <h2 id="recover-title" style={{ margin: 0, fontSize: 18 }}>Recover artifact</h2>
          <div className="mono" style={{ fontSize: "var(--text-sm)", color: "var(--text-2)", marginTop: 4, wordBreak: "break-all" }}>{artifactId}</div>
        </div>
        <div style={{ padding: "14px 22px 20px", display: "flex", flexDirection: "column", gap: 12, fontSize: 13.5 }}>
          {plan?.none ? (
            <div role="status" className="anotice" data-testid="no-copy">
              No copy of this backup is recorded yet, so there is nothing to recover. A copy still being transferred is not a copy.
            </div>
          ) : null}
          {plan?.unreachable ? (
            <div role="status" className="anotice" data-testid="unreachable">
              This deployment cannot reach the place holding this backup&apos;s copy right now. That is not the same as the copy being gone.
            </div>
          ) : null}

          {idle ? (
            <>
              <InfoTooltip id="assurance.recover.target" block>
                <div role="radiogroup" aria-label="Where should the artifact go?" style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                  {TARGETS.map((t) => {
                    const off = t.id === "download" && archived;
                    return (
                      <label key={t.id} style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                        <input type="radio" name="recover-target" checked={kind === t.id} disabled={off}
                          onChange={() => { setTarget(t.id); setWhere(""); }} />
                        {t.label}
                      </label>
                    );
                  })}
                </div>
              </InfoTooltip>
              {archived ? (
                <div style={{ color: "var(--text-2)" }} data-testid="no-browser-for-archive">
                  A browser download is not offered for an archived copy: the provider needs hours to restore it, and a download grant lasts minutes.
                </div>
              ) : null}
              {kind === "download" ? (
                <div style={{ color: "var(--text-2)" }}>
                  The host reads the whole copy and checks it against the digest it recorded before it sends the first byte, so the download may not start for a while. It works over plain http. The grant is single-use, lasts a few minutes and ends with this sign-in.
                </div>
              ) : null}
              {kind === "staging" ? (
                <label className="field">
                  <span className="field__label">{"Name under the staging root " + (stagingRoot ? stagingRoot + " " : "") + "(set by the host)"}</span>
                  <input className="input input--mono" aria-label="Recover to a name under the staging root" value={where} onChange={(e) => setWhere(e.target.value)} />
                </label>
              ) : null}
              {kind === "storage" ? (
                <label className="field">
                  <span className="field__label">Destination id</span>
                  <input className="input input--mono" aria-label="Recover to destination id" value={where} onChange={(e) => setWhere(e.target.value)} />
                </label>
              ) : null}
              <CommandEcho label="Same thing from the terminal" commands={[recoverCommand(artifactId, kind, where, needsAck && accepted)]} />
              {needsAck ? (
                <div role="group" aria-label={archived ? "This copy is archived" : "This read costs money"} className="anotice" data-testid="archived-decision">
                  {archived ? (
                    <>
                      <strong>
                        {"This copy is in " + (plan?.where?.storageClass || "an archive class") + (plan?.where?.medium ? " on " + plan.where.medium : "") + " and has to be restored by the provider before it can be read."}
                      </strong>
                      <div style={{ marginTop: 4 }}>
                        The restore takes hours, the provider does not say how many, and the provider bills for it. retnd holds no price list and cannot predict either, so it shows neither. Nothing starts until you tick the box and ask.
                      </div>
                    </>
                  ) : (
                    <>
                      <strong>The service refused this read because it costs money.</strong>
                      <div style={{ marginTop: 4 }}>{costWall}</div>
                    </>
                  )}
                  <InfoTooltip id="assurance.recover.thawCost">
                    <label style={{ display: "inline-flex", gap: 6, alignItems: "center", marginTop: 8 }}>
                      <input type="checkbox" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />
                      {archived ? "I accept that the provider bills for this restore" : "I accept this cost"}
                    </label>
                  </InfoTooltip>
                  {archived ? (
                    <div style={{ marginTop: 8 }}>
                      <CommandEcho label="The restore on its own, from the terminal" commands={[thawCommand(artifactId)]} />
                    </div>
                  ) : null}
                </div>
              ) : null}
            </>
          ) : null}

          {phase.k === "minting" ? <div role="status" data-testid="minting">Asking the host for a grant...</div> : null}

          {phase.k === "thawing" ? (
            <div data-testid="thaw" data-access={plan?.readable ? "immediate" : plan?.restoring ? "restoring" : "requires_restore"} className="aop">
              <strong>{ready ? "The provider has restored the copy" : "The provider is restoring the copy"}</strong>
              <div>
                {ready
                  ? "It is readable now. Recovering it reads the whole copy and checks it against the recorded digest. The provider keeps a restored copy readable for a limited time, so do this soon."
                  : "This takes hours. The provider reports no percentage and no finishing time, so this page shows none. You can close it: the restore carries on, and reopening this dialog picks it up."}
              </div>
              {phase.mint?.wait ? <div style={{ color: "var(--text-2)" }}>{"Published time for this class: " + phase.mint.wait}</div> : null}
              {phase.mint?.billing ? <div style={{ color: "var(--text-2)" }}>{phase.mint.billing}</div> : null}
              {phase.mint ? <div className="mono" style={{ fontSize: "var(--text-xs)" }}>{"restore operation " + phase.mint.operationId}</div> : null}
            </div>
          ) : null}

          {verifyingHost ? (
            <div className="aop" data-testid="host-check" data-phase={phase.k}>
              <strong>{phase.k === "verifying" ? "Checking the copy on the host" : "Download handed to your browser"}</strong>
              <div className="aop__bar" role="progressbar" aria-label="Host check, no progress is reported" aria-busy={phase.k === "verifying"} data-indeterminate=""><i /></div>
              {phase.k === "verifying" ? (
                <div>
                  The host is reading the whole copy and matching it to the digest it recorded. It writes nothing until that matches. This page gets no progress from it, and closing this window does not stop it.
                </div>
              ) : (
                <>
                  <div>
                    The host is reading the whole copy and checking it against the recorded digest. It sends the first byte only after that matches, so nothing has been sent yet and your browser may sit quiet for a while.
                  </div>
                  <div>
                    This page cannot see your browser&apos;s transfer, so it cannot tell you the file arrived, and a broken download cannot be resumed: ask for a new one. When your browser says it has finished, compare its checksum with the recorded one.
                  </div>
                  {recorded ? <div className="mono" style={{ wordBreak: "break-all" }} data-testid="recorded-digest">{recorded}</div> : null}
                  {phase.mint.sizeBytes !== undefined ? <div className="mono">{"Expect " + bytes(phase.mint.sizeBytes)}</div> : null}
                </>
              )}
            </div>
          ) : null}

          {phase.k === "placed" ? (
            <div className="aop" data-testid="placed" role="status">
              <strong className="aop__outcome">
                {phase.placement.target.kind === "storage" ? "Recovered to the destination" : "Recovered to staging"}
              </strong>
              <div>The host matched the whole copy to the recorded digest before it placed anything.</div>
              <div className="mono" style={{ wordBreak: "break-all" }}>{(phase.placement.path ?? phase.placement.key ?? "") + ", " + bytes(phase.placement.bytes)}</div>
              <div className="mono" style={{ wordBreak: "break-all" }} data-testid="placed-digest">{"sha256:" + phase.placement.sha256}</div>
              <div style={{ color: "var(--text-2)" }}>This recovered the artifact. Putting the application back stays yours to do.</div>
            </div>
          ) : null}

          {phase.k === "refused" ? (
            <div role="alert" data-testid="refusal" data-code={phase.refusal.code}
              style={{ border: "1px solid var(--danger)", background: "var(--danger-quiet)", borderRadius: "var(--radius-md)", padding: 8, display: "flex", flexDirection: "column", gap: 4 }}>
              <strong>Nothing was recovered</strong>
              <div>{phase.refusal.message}</div>
              {phase.refusal.next ? <div>{phase.refusal.next}</div> : null}
              {phase.refusal.command ? <CommandEcho label="From the terminal" commands={[phase.refusal.command]} /> : null}
            </div>
          ) : null}
        </div>
        <div className="card__footer" style={{ display: "flex", justifyContent: "flex-end", gap: 9, borderRadius: "0 0 10px 10px" }}>
          <button className="btn" onClick={onClose}>{idle && needsAck ? "Do not recover" : idle ? "Cancel" : "Close"}</button>
          {idle ? (
            <button
              className={"btn " + (needsAck ? "btn--caution" : "btn--primary")}
              disabled={blocked || needsTarget || ackRequired}
              onClick={() => void go()}
            >
              {archived ? "Ask the provider to restore this copy" : costWall !== null ? "Accept the cost and recover" : phase.k === "refused" ? "Ask for a new grant" : "Recover artifact"}
            </button>
          ) : null}
          {ready ? (
            <button className="btn btn--primary" disabled={needsTarget} onClick={() => void go()}>Recover the restored copy</button>
          ) : null}
        </div>
      </div>
    </div>
  );
}
