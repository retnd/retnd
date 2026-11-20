/**
 * The Retire dialog (issue #1528), from the "Retire a failed backup" design.
 *
 * Retiring closes the books on a FAILED backup that can never be completed
 * because its source is gone. Everything this dialog says about the backup
 * comes from the engine's preview (GET /backups/{id}/retire): whether the
 * source object is still there, which newer backups could replace it, the
 * refusal the action would give, what retiring does and the terminal line.
 * When the operator ticks the no-replacement acknowledgement or picks another
 * replacement, the dialog asks the preview again with that choice, so the
 * effects and the line it shows are the engine's for exactly what will be
 * sent. Nothing is worked out here, so the browser and `retnd retire
 * --dry-run` cannot disagree.
 *
 * It retires one backup. The design's several-at-once entry point (a banner
 * on the set page) is not built: the engine already retires that banner's
 * exact case (source gone, a newer good backup in the set) on its next cycle,
 * so the banner would mostly be empty. The cases left for a person are one at
 * a time by nature, and the dialog is opened for one.
 *
 * Two places the design and the engine differ, and the engine wins: the
 * engine retires on its own after one pass that finds the object gone (and
 * undoes it if the object comes back), not after three checks; and the
 * no-replacement acknowledgement is `--no-successor` on the command line. A
 * source that is still on the host is refused with "Retry instead", as the
 * design draws it; `--accept-source-present` in a terminal is the only way past
 * that refusal, and the dialog says so when the host could not be asked.
 *
 * A backup whose own copy already sits at its final name (issue #1594) is
 * retry's job while its source is there. With the source gone the engine
 * names the unverified-copy acknowledgement; ticking it asks again, and the
 * engine either retires it (the file stays) or, when the copy still matches
 * its recorded checksum, refuses with Retry instead, because retry keeps it.
 *
 * Keyboard: focus starts on Cancel (the safe action, as ConfirmationDialog
 * does), Tab and Shift+Tab stay inside, Escape closes, the result takes focus
 * when it replaces the buttons, and closing hands focus back to whatever
 * opened the dialog, or to the page's heading when that control is gone.
 */
import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { WireRetireArtifactRequest, WireRetirementPreview } from "@shared/api/generated/contract";
import { describeFailure } from "@shared/api/failure";
import { shellQuote } from "@shared/assurance/assuranceCommands";
import { Acknowledgement } from "@shared/components/Acknowledgement";
import { Banner } from "@shared/components/Banner";
import { StatusBadge } from "@shared/components/StatusBadge";
import type { StatusTone } from "@shared/components/StatusBadge";
import type { IconName } from "@shared/design-system/icons";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { fetchResource } from "@shared/state/resource";
import { healthNode, setsNode } from "@shared/state/appNodes";

/** The words for what the source said, as the design's chips put them. */
function sourceChip(check: string): { text: string; tone: StatusTone; icon: IconName } {
  switch (check) {
    case "absent":
      return { text: "Gone from the host", tone: "warn", icon: "warning" };
    case "present":
      return { text: "Still on the host", tone: "ok", icon: "success" };
    case "unknown":
      return { text: "Could not check the host", tone: "neutral", icon: "info" };
    default:
      return { text: "Not checked", tone: "neutral", icon: "status-idle" };
  }
}

type Ack = Pick<WireRetireArtifactRequest, "superseded_by" | "no_successor" | "accept_source_present" | "accept_unverified_copy">;
type Outcome = { retired: boolean; detail: string };

const FOCUSABLE = "button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])";
/** The dialog's buttons are touch targets (the design's 44px) with an edge a
 *  person can see (--control-edge, 3:1 and up in both themes). */
const TARGET = { minHeight: 44 } as const;

export function RetireDialog({
  artifactId,
  readOnly = false,
  onClose,
  onRetry
}: {
  artifactId: string;
  readOnly?: boolean;
  /** changed is true when the backup was retired. */
  onClose(changed: boolean): void;
  /** Offered when retrying can still fetch it: the design's "Retry instead". */
  onRetry?(): void;
}) {
  const api = useApi();
  const name = artifactId.split("/").slice(2).join("/");
  const [base, setBase] = useState<WireRetirementPreview | Error | null>(null);
  const [terminalOnly, setTerminalOnly] = useState<string | null>(null);
  const [noSuccessorAck, setNoSuccessorAck] = useState(false);
  const [unverifiedAck, setUnverifiedAck] = useState(false);
  const [chosen, setChosen] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const resultRef = useRef<HTMLHeadingElement>(null);
  const opener = useRef<Element | null>(typeof document === "undefined" ? null : document.activeElement);

  // Focus in on open, and back out on close: to what opened the dialog, or,
  // when a retirement took that control away, to the page's own heading.
  //
  // The page behind may re-read itself as the dialog closes (a retirement
  // changes the backup), which unmounts whatever had focus, so the hand-back
  // keeps watching for a moment and puts focus back whenever it has fallen
  // to the body.
  useEffect(() => {
    cancelRef.current?.focus();
    const back = opener.current;
    return () => {
      const pick = (): HTMLElement | null =>
        back instanceof HTMLElement && back.isConnected && !back.hasAttribute("disabled")
          ? back
          : (document.querySelector("main h1, h1") as HTMLElement | null);
      const settle = () => {
        if (document.activeElement && document.activeElement !== document.body) return;
        const target = pick();
        if (!target) return;
        if (target.tabIndex < 0 && !target.hasAttribute("tabindex")) target.setAttribute("tabindex", "-1");
        target.focus();
      };
      settle();
      let ticks = 0;
      const timer = window.setInterval(() => {
        settle();
        if (++ticks >= 30) window.clearInterval(timer);
      }, 50);
    };
  }, []);

  // The first answer: what the engine says with nothing acknowledged.
  useEffect(() => {
    let live = true;
    api
      .previewRetirement(artifactId)
      .then((p) => {
        if (!live) return;
        setBase(p);
        setChosen(p.proposed_successor?.name ?? "");
        // A host that could not be asked is refused here; the terminal can
        // still retire it, and the line comes from the engine too.
        if (p.refusal?.code === "RETIRE_SOURCE_UNCONFIRMED") {
          api.previewRetirement(artifactId, { accept_source_present: true }).then((q) => live && setTerminalOnly(q.command)).catch(() => undefined);
        }
      })
      .catch((e: unknown) => live && setBase(e instanceof Error ? e : new Error(String(e))));
    return () => {
      live = false;
    };
  }, [api, artifactId]);

  const preview = base && !(base instanceof Error) ? base : null;
  const proposed = preview?.proposed_successor?.name ?? "";
  const ack: Ack = {
    ...(unverifiedAck ? { accept_unverified_copy: true } : {}),
    ...(noSuccessorAck ? { no_successor: true } : {}),
    ...(chosen && chosen !== proposed ? { superseded_by: chosen } : {})
  };
  const ackKey = JSON.stringify(ack);

  // Asked again for exactly what will be sent, whenever the operator changes
  // the acknowledgement or the replacement. The answer is kept with the
  // choice it answers, so a stale answer is never shown for a newer choice.
  //
  // A re-ask that fails leaves no answer for that choice: Retire stays off and
  // the dialog says so. Falling back to the first answer would show one
  // replacement and send another.
  const [asked, setAsked] = useState<{ key: string; preview: WireRetirementPreview | null; error: string } | null>(null);
  useEffect(() => {
    if (!preview || ackKey === "{}") return;
    let live = true;
    api
      .previewRetirement(artifactId, JSON.parse(ackKey) as Ack)
      .then((p) => live && setAsked({ key: ackKey, preview: p, error: "" }))
      .catch((e: unknown) => live && setAsked({ key: ackKey, preview: null, error: describeFailure(e, "no answer came back").message }));
    return () => {
      live = false;
    };
  }, [api, artifactId, ackKey, preview]);
  const asking = ackKey !== "{}" && asked?.key !== ackKey;
  const current = ackKey === "{}" ? preview : asked?.key === ackKey ? asked.preview : null;
  const reaskError = ackKey !== "{}" && asked?.key === ackKey ? asked.error : "";

  useEffect(() => {
    if (outcome) resultRef.current?.focus();
  }, [outcome]);

  const close = () => {
    if (!busy) onClose(outcome?.retired ?? false);
  };
  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      close();
      return;
    }
    if (e.key !== "Tab" || !dialogRef.current) return;
    const items = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((el) => el.offsetParent !== null);
    const here = document.activeElement as HTMLElement | null;
    // Nothing to move to (both buttons off while the request is in flight):
    // focus stays on the panel itself.
    if (items.length === 0) {
      e.preventDefault();
      dialogRef.current.focus();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const at = here ? items.indexOf(here) : -1;
    // Wrap at either end, and whenever focus is on something that is not one
    // of the dialog's own controls (the result heading, the panel), so Tab
    // and Shift+Tab never reach the page behind.
    if (e.shiftKey && (at <= 0)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && (at === -1 || at === items.length - 1)) {
      e.preventDefault();
      first.focus();
    }
  };

  const refusal = current?.refusal;
  // The acknowledgements a person can give here, each shown once the engine
  // has named it, and kept on screen once ticked so it can be unticked. The
  // unverified-copy one (issue #1594) comes first, and the no-replacement one
  // can follow it once the engine has checked the copy.
  const needsUnverifiedAck = preview?.refusal?.overridable_with === "accept_unverified_copy";
  const needsAck = preview?.refusal?.overridable_with === "no_successor" || refusal?.overridable_with === "no_successor" || noSuccessorAck;
  const blocked = !!refusal && refusal.overridable_with !== "no_successor" && refusal.overridable_with !== "accept_unverified_copy";
  // The engine's sentence is shown for every refusal but the no-replacement
  // one, which has its own banner: for an own copy at the final name it is
  // the explanation the acknowledgement below answers.
  //
  // While the engine is asked again, or when asking again failed, the answer
  // it gave first stays on screen: its sentence and the way out (Retry
  // instead) do not blink away under a pointer that has just ticked a box.
  const shown = current ? refusal : preview?.refusal;
  const explained = !!shown && shown.overridable_with !== "no_successor";
  const ready = !!current && !asking && !refusal;
  const command = current ? current.command + (note ? " --note " + shellQuote(note) : "") : "";

  const retire = async () => {
    // The buttons turn off for the request, which would drop focus to the
    // page behind; the panel holds it until the result takes it.
    dialogRef.current?.focus();
    setBusy(true);
    try {
      const res = await api.retireArtifact(artifactId, { ...(note ? { note } : {}), ...ack });
      setOutcome({
        retired: true,
        detail: res.retirement.superseded_by ? "replaced by " + res.retirement.superseded_by : "retired with no replacement"
      });
      // The set's verdict and its retired count are read from the app's
      // shared copies, so those are read again rather than left until the
      // next poll.
      fetchResource(setsNode, () => api.listSets());
      fetchResource(healthNode, () => api.getHealth());
    } catch (e: unknown) {
      setOutcome({ retired: false, detail: describeFailure(e, "the retirement could not be made").message });
    } finally {
      setBusy(false);
    }
  };

  const title = outcome ? (outcome.retired ? "1 backup retired" : name + " was left as it was") : "Retire " + name + "?";

  return (
    <div className="dialog-scrim" onClick={(e) => e.target === e.currentTarget && close()}>
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="retire-title"
        className="dialog"
        tabIndex={-1}
        style={{ maxWidth: 820, width: "100%", outline: "none", display: "flex", flexDirection: "column", maxHeight: "calc(100vh - 48px)" }}
        onKeyDown={onKeyDown}
      >
        <div style={{ padding: "20px 22px 6px", display: "grid", gap: 6, flexShrink: 0 }}>
          <h2 id="retire-title" ref={resultRef} tabIndex={-1} style={{ margin: 0, fontSize: 18 }}>
            {title}
          </h2>
          {outcome ? null : (
            <p style={{ margin: 0, color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
              Retiring closes the books on a backup that failed and can never be completed. The failure stays in the history,
              marked Retired, and a newer good backup stands in for it.
            </p>
          )}
        </div>

        <div style={{ padding: "8px 22px", display: "grid", gap: 16, fontSize: "var(--text-sm)", overflowY: "auto", minHeight: 0, alignContent: "start" }}>
          {/* Mounted from the start and filled in when the answer arrives, so
              a screen reader announces the result rather than missing a
              region that appeared with its content already in it. */}
          <div role="status" aria-live="polite" data-testid="retire-result">
            {outcome ? (
              <div className="card" style={{ padding: "10px 14px", display: "flex", justifyContent: "space-between", gap: 12, flexWrap: "wrap" }}>
                <span className="mono">{name}</span>
                <span>
                  <StatusBadge tone={outcome.retired ? "neutral" : "danger"} icon={outcome.retired ? "status-idle" : "failure"}>
                    {outcome.retired ? "Retired" : "Not retired"}
                  </StatusBadge>{" "}
                  <span style={{ color: outcome.retired ? "var(--text-2)" : "var(--danger)" }}>{outcome.detail}</span>
                </span>
              </div>
            ) : null}
          </div>

          {outcome ? null : base === null ? (
            <p style={{ margin: 0, color: "var(--text-2)" }}>Asking the host whether the source file is still there…</p>
          ) : base instanceof Error ? (
            <p style={{ margin: 0, color: "var(--danger)" }}>{describeFailure(base, "the host could not be asked about this backup").message}</p>
          ) : (
            <>
              <section aria-label="What will be retired">
                <h3 className="eyebrow" style={{ margin: "0 0 8px" }}>What will be retired</h3>
                <div className="card" style={{ padding: "10px 14px", display: "grid", gap: 6 }} data-testid={"retire-row-" + name}>
                  <div style={{ display: "flex", justifyContent: "space-between", gap: 12, flexWrap: "wrap" }}>
                    <span style={{ minWidth: 0 }}>
                      <span className="mono">{name}</span>
                      {base.failure_reason ? <span style={{ display: "block", color: "var(--text-2)" }}>Failed: {base.failure_reason}</span> : null}
                    </span>
                    <span style={{ textAlign: "right" }}>
                      <StatusBadge tone={sourceChip(base.source_check).tone} icon={sourceChip(base.source_check).icon}>
                        {sourceChip(base.source_check).text}
                      </StatusBadge>
                      <span style={{ display: "block", color: "var(--text-2)", fontSize: "var(--text-xs)" }}>checked just now</span>
                    </span>
                  </div>
                  {explained && shown ? (
                    <Banner tone="warn" dismissible={false} style={{ display: "grid", gap: 8 }}>
                      <span>{shown.sentence}</span>
                      {(shown.code === "RETIRE_SOURCE_PRESENT" || shown.code === "RETIRE_OWN_COPY_AT_FINAL_NAME") && onRetry ? (
                        <span>
                          <button className="btn btn--strong-edge" style={TARGET} onClick={onRetry}>
                            Retry instead
                          </button>
                        </span>
                      ) : null}
                      {terminalOnly ? (
                        <span>
                          A terminal can retire it anyway, acknowledging that the host could not be asked:{" "}
                          <code className="mono">{terminalOnly}</code>
                        </span>
                      ) : null}
                    </Banner>
                  ) : null}
                </div>
              </section>

              {base.successor_candidates.length > 0 ? (
                <section aria-label="What replaces it">
                  <h3 className="eyebrow" style={{ margin: "0 0 8px" }}>What replaces it</h3>
                  <div className="card" style={{ padding: "12px 14px", display: "grid", gap: 8 }}>
                    {current?.proposed_successor ? (
                      <div style={{ display: "flex", justifyContent: "space-between", gap: 12, flexWrap: "wrap", alignItems: "center" }}>
                        <span>
                          <span className="mono">{current.proposed_successor.name}</span>
                          <span style={{ display: "block", color: "var(--text-2)" }}>
                            {current.proposed_successor.name === proposed ? "The newest good backup in this set" : "A newer good backup in this set"},{" "}
                            {current.proposed_successor.state}, found {new Date(current.proposed_successor.discovered_at).toLocaleString()}
                          </span>
                        </span>
                        <StatusBadge tone="ok" icon="success">Good</StatusBadge>
                      </div>
                    ) : null}
                    <label htmlFor="retire-successor" style={{ color: "var(--text-2)" }}>Replaced by</label>
                    <select
                      id="retire-successor"
                      className="input input--strong-edge"
                      style={TARGET}
                      value={chosen}
                      disabled={readOnly || busy}
                      onChange={(e) => setChosen(e.target.value)}
                    >
                      {base.successor_candidates.map((c) => (
                        <option key={c.name} value={c.name}>
                          {c.name + (c.name === proposed ? " (newest good)" : "")}
                        </option>
                      ))}
                    </select>
                  </div>
                </section>
              ) : null}

              {needsUnverifiedAck ? (
                <Banner tone="danger" dismissible={false} style={{ display: "grid", gap: 8 }}>
                  <span>
                    <strong>The file stays, but it stops counting.</strong> Retiring leaves it at its final name, where it is
                    not counted as a backup and nothing prunes it.
                  </span>
                  <Acknowledgement checked={unverifiedAck} onChange={setUnverifiedAck} disabled={readOnly || busy}>
                    I understand this backup&rsquo;s own copy could not be verified, and I want to retire it anyway and keep the
                    file. (Same as <span className="mono">--accept-unverified-copy</span> in the terminal.)
                  </Acknowledgement>
                </Banner>
              ) : null}

              {needsAck ? (
                <Banner tone="danger" dismissible={false} style={{ display: "grid", gap: 8 }}>
                  <span>
                    <strong>This set has no newer good backup.</strong> Retiring closes the only record of a backup that never
                    completed.
                  </span>
                  <Acknowledgement checked={noSuccessorAck} onChange={setNoSuccessorAck} disabled={readOnly || busy}>
                    I understand there is no replacement for this backup, and I want to retire it anyway. (Same as{" "}
                    <span className="mono">--no-successor</span> in the terminal.)
                  </Acknowledgement>
                </Banner>
              ) : null}

              {reaskError ? (
                <Banner tone="danger" dismissible={false}>
                  <span data-testid="retire-reask-error">
                    {"Asking the host again with this choice failed, so Retire stays off until it answers. " + reaskError}
                  </span>
                </Banner>
              ) : null}

              {ready && current && current.effects.length > 0 ? (
                <section aria-label="What happens">
                  <h3 className="eyebrow" style={{ margin: "0 0 8px" }}>What happens</h3>
                  <ul style={{ margin: 0, paddingLeft: 20, display: "grid", gap: 4 }}>
                    {current.effects.map((e) => (
                      <li key={e}>{e}</li>
                    ))}
                  </ul>
                </section>
              ) : null}

              {!blocked ? (
                <div style={{ display: "grid", gap: 6 }}>
                  <label htmlFor="retire-note" style={{ color: "var(--text-2)" }}>Note (optional)</label>
                  <input
                    id="retire-note"
                    className="input input--strong-edge"
                    style={TARGET}
                    type="text"
                    value={note}
                    onChange={(e) => setNote(e.target.value)}
                    disabled={readOnly || busy}
                  />
                </div>
              ) : null}

              {ready && command ? <CommandEcho label="Same thing from the terminal" commands={[command]} /> : null}
            </>
          )}
        </div>

        <div style={{ padding: "14px 22px 18px", display: "flex", justifyContent: "flex-end", gap: 10, borderTop: "1px solid var(--border)", marginTop: 12, flexShrink: 0 }}>
          {outcome ? (
            <button className="btn btn--strong-edge" style={TARGET} onClick={close}>Close</button>
          ) : (
            <>
              <button ref={cancelRef} className="btn btn--strong-edge" style={TARGET} onClick={close} disabled={busy}>Cancel</button>
              <button className="btn btn--primary" style={TARGET} onClick={retire} disabled={readOnly || busy || !ready}>
                {busy ? "Retiring…" : ready ? "Retire 1 backup" : "Retire"}
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
