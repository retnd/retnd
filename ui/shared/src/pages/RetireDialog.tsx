/**
 * The Retire dialog (issue #1528), from the "Retire a failed backup" design.
 *
 * Retiring closes the books on a FAILED backup that can never be completed
 * because its source is gone. Everything this dialog says about a backup comes
 * from the engine's preview (GET /backups/{id}/retire): whether the source
 * object is still there, which newer backup replaces it, the refusal the
 * action would give and what retiring does. Nothing is worked out here, so the
 * browser and `retnd retire --dry-run` cannot disagree.
 *
 * Each backup is retired on its own request, in order, so one refusal never
 * undoes another; the result lists each one.
 *
 * Two places the design and the engine differ, and the engine wins: the
 * engine retires on its own after one pass that finds the object gone (and
 * undoes it if the object comes back), not after three checks; and the
 * no-replacement acknowledgement is `--no-successor` on the command line.
 * A source that is still there, or that could not be asked about, is refused
 * here with "Retry instead" as the design draws it; the terminal's
 * `--accept-source-present` is the only way past that refusal.
 */
import { useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { WireRetirementPreview } from "@shared/api/generated/contract";
import { describeFailure } from "@shared/api/failure";
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

type Outcome = { id: string; retired: boolean; detail: string };

function shellQuote(s: string): string {
  return "'" + s.replace(/'/g, "'\\''") + "'";
}

export function RetireDialog({
  artifactIds,
  setLabel,
  readOnly = false,
  onClose,
  onRetry
}: {
  artifactIds: string[];
  /** The backup set the backups are in, for the title. */
  setLabel: string;
  readOnly?: boolean;
  /** changed is true when at least one backup was retired. */
  onClose(changed: boolean): void;
  /** Offered when the source is still there: the design's "Retry instead". */
  onRetry?(id: string): void;
}) {
  const api = useApi();
  const [previews, setPreviews] = useState<Record<string, WireRetirementPreview | Error>>({});
  const [noSuccessorAck, setNoSuccessorAck] = useState(false);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [outcomes, setOutcomes] = useState<Outcome[] | null>(null);

  // Keyed by the ids' text, not the array: a caller writing [a.id] inline
  // hands a new array to every render, and keying on that refetched forever.
  const idsKey = artifactIds.join("\n");
  useEffect(() => {
    let live = true;
    for (const id of idsKey.split("\n")) {
      api
        .previewRetirement(id)
        .then((p) => live && setPreviews((cur) => ({ ...cur, [id]: p })))
        .catch((e: unknown) => live && setPreviews((cur) => ({ ...cur, [id]: e instanceof Error ? e : new Error(String(e)) })));
    }
    return () => {
      live = false;
    };
  }, [api, idsKey]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !busy) onClose(outcomes?.some((o) => o.retired) ?? false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [busy, onClose, outcomes]);

  const loaded = artifactIds.every((id) => previews[id] !== undefined);
  const rows = artifactIds.map((id) => ({ id, p: previews[id] }));
  const needsNoSuccessorAck = rows.some(({ p }) => p && !(p instanceof Error) && p.refusal?.overridable_with === "no_successor");
  // A backup goes ahead when the engine would retire it as asked: no refusal,
  // or only the no-replacement one with its acknowledgement ticked.
  const eligible = rows.filter(({ p }) => {
    if (!p || p instanceof Error) return false;
    if (!p.refusal) return true;
    return p.refusal.overridable_with === "no_successor" && noSuccessorAck;
  });

  const successor = rows.map(({ p }) => (p && !(p instanceof Error) ? p.proposed_successor : undefined)).find(Boolean);
  const effects = rows.map(({ p }) => (p && !(p instanceof Error) ? p.effects : [])).find((e) => e.length > 0) ?? [];
  const commands = eligible.map(({ p }) => {
    const line = (p as WireRetirementPreview).command;
    return (noSuccessorAck && (p as WireRetirementPreview).refusal ? line + " --no-successor" : line) + (note ? " --note " + shellQuote(note) : "");
  });
  const n = eligible.length;
  const changed = outcomes?.some((o) => o.retired) ?? false;

  const retire = async () => {
    setBusy(true);
    const done: Outcome[] = [];
    for (const { id, p } of eligible) {
      const wire = p as WireRetirementPreview;
      try {
        const res = await api.retireArtifact(id, {
          ...(note ? { note } : {}),
          ...(noSuccessorAck && wire.refusal?.overridable_with === "no_successor" ? { no_successor: true } : {})
        });
        done.push({
          id,
          retired: true,
          detail: res.retirement.superseded_by ? "replaced by " + res.retirement.superseded_by : "retired with no replacement"
        });
      } catch (e: unknown) {
        done.push({ id, retired: false, detail: describeFailure(e, "the retirement could not be made").message });
      }
    }
    setOutcomes(done);
    setBusy(false);
    // A retirement moves the set's verdict and its retired count, which the
    // set cards, the dashboard and the nav badges read from the app's shared
    // copies, so those are read again rather than left until the next poll.
    if (done.some((o) => o.retired)) {
      fetchResource(setsNode, () => api.listSets());
      fetchResource(healthNode, () => api.getHealth());
    }
  };

  const title =
    artifactIds.length === 1
      ? "Retire " + artifactIds[0].split("/").slice(2).join("/") + "?"
      : "Retire " + artifactIds.length + " failed backups in " + setLabel;

  return (
    <div className="dialog-scrim" onClick={(e) => e.target === e.currentTarget && !busy && onClose(changed)}>
      <div role="dialog" aria-modal="true" aria-labelledby="retire-title" className="dialog" style={{ maxWidth: 820, width: "100%" }}>
        <div style={{ padding: "20px 22px 6px", display: "grid", gap: 6 }}>
          <h2 id="retire-title" style={{ margin: 0, fontSize: 18 }}>
            {outcomes ? retiredTitle(outcomes) : title}
          </h2>
          {outcomes ? null : (
            <p style={{ margin: 0, color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
              Retiring closes the books on a backup that failed and can never be completed. The failure stays in the history,
              marked Retired, and a newer good backup stands in for it.
            </p>
          )}
        </div>

        <div style={{ padding: "8px 22px", display: "grid", gap: 16, fontSize: "var(--text-sm)" }}>
          {outcomes ? (
            <div role="status" style={{ display: "grid", gap: 8 }}>
              {outcomes.map((o) => (
                <div key={o.id} className="card" style={{ padding: "10px 14px", display: "flex", justifyContent: "space-between", gap: 12, flexWrap: "wrap" }}>
                  <span className="mono">{o.id.split("/").slice(2).join("/")}</span>
                  <span>
                    <StatusBadge tone={o.retired ? "neutral" : "danger"} icon={o.retired ? "status-idle" : "failure"}>{o.retired ? "Retired" : "Not retired"}</StatusBadge>{" "}
                    <span style={{ color: o.retired ? "var(--text-2)" : "var(--danger)" }}>{o.detail}</span>
                  </span>
                </div>
              ))}
              <p style={{ margin: 0, color: "var(--text-2)" }}>
                Each backup is retired on its own, so one refusal never undoes another. The terminal prints the same refusal.
              </p>
            </div>
          ) : !loaded ? (
            <p role="status" style={{ margin: 0, color: "var(--text-2)" }}>
              Asking the host whether each source file is still there…
            </p>
          ) : (
            <>
              <section aria-label="What will be retired">
                <h3 className="eyebrow" style={{ margin: "0 0 8px" }}>What will be retired</h3>
                <div className="card" style={{ overflow: "hidden" }}>
                  {rows.map(({ id, p }, i) => (
                    <RetireRow key={id} id={id} p={p} first={i === 0} onRetry={onRetry} />
                  ))}
                </div>
              </section>

              {successor ? (
                <section aria-label="What replaces them">
                  <h3 className="eyebrow" style={{ margin: "0 0 8px" }}>What replaces {artifactIds.length === 1 ? "it" : "them"}</h3>
                  <div className="card" style={{ padding: "12px 14px", display: "flex", justifyContent: "space-between", gap: 12, flexWrap: "wrap" }}>
                    <span>
                      <span className="mono">{successor.name}</span>
                      <span style={{ display: "block", color: "var(--text-2)" }}>
                        The newest good backup in this set, {successor.state}, found {new Date(successor.discovered_at).toLocaleString()}
                      </span>
                    </span>
                    <StatusBadge tone="ok" icon="success">Good</StatusBadge>
                  </div>
                </section>
              ) : null}

              {needsNoSuccessorAck ? (
                <Banner tone="danger" dismissible={false} style={{ display: "grid", gap: 8 }}>
                  <span>
                    <strong>This set has no newer good backup.</strong> Retiring closes the only record of a backup that never
                    completed.
                  </span>
                  <Acknowledgement checked={noSuccessorAck} onChange={setNoSuccessorAck} disabled={readOnly}>
                    I understand there is no replacement for this backup, and I want to retire it anyway. (Same as{" "}
                    <span className="mono">--no-successor</span> in the terminal.)
                  </Acknowledgement>
                </Banner>
              ) : null}

              {n > 0 && effects.length > 0 ? (
                <section aria-label="What happens">
                  <h3 className="eyebrow" style={{ margin: "0 0 8px" }}>What happens</h3>
                  <ul style={{ margin: 0, paddingLeft: 20, display: "grid", gap: 4 }}>
                    {effects.map((e) => (
                      <li key={e}>{e}</li>
                    ))}
                  </ul>
                </section>
              ) : null}

              {n > 0 ? (
                <div style={{ display: "grid", gap: 6 }}>
                  <label htmlFor="retire-note" style={{ color: "var(--text-2)" }}>Note (optional)</label>
                  <input id="retire-note" className="input" type="text" value={note} onChange={(e) => setNote(e.target.value)} disabled={readOnly} />
                </div>
              ) : null}

              {commands.length > 0 ? <CommandEcho label="Same thing from the terminal" commands={commands} /> : null}
            </>
          )}
        </div>

        <div style={{ padding: "14px 22px 18px", display: "flex", justifyContent: "flex-end", gap: 10, borderTop: "1px solid var(--border)", marginTop: 12 }}>
          {outcomes ? (
            <button className="btn" onClick={() => onClose(changed)}>Close</button>
          ) : (
            <>
              <button className="btn" onClick={() => onClose(false)} disabled={busy}>Cancel</button>
              <button className="btn btn--primary" onClick={retire} disabled={readOnly || busy || !loaded || n === 0}>
                {busy ? "Retiring…" : n === 1 ? "Retire 1 backup" : n === 0 ? "Retire" : "Retire " + n + " backups"}
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function retiredTitle(outcomes: Outcome[]): string {
  const ok = outcomes.filter((o) => o.retired).length;
  const left = outcomes.length - ok;
  if (left === 0) return ok === 1 ? "1 backup retired" : ok + " backups retired";
  return ok + " retired, " + left + " left as " + (left === 1 ? "it was" : "they were");
}

function RetireRow({ id, p, first, onRetry }: { id: string; p: WireRetirementPreview | Error | undefined; first: boolean; onRetry?(id: string): void }) {
  const name = id.split("/").slice(2).join("/");
  const border = first ? undefined : "1px solid var(--border)";
  if (!p) return null;
  if (p instanceof Error) {
    return (
      <div style={{ padding: "10px 14px", borderTop: border }}>
        <span className="mono">{name}</span>
        <span style={{ display: "block", color: "var(--danger)" }}>{describeFailure(p, "the host could not be asked about this backup").message}</span>
      </div>
    );
  }
  const chip = sourceChip(p.source_check);
  const blocked = p.refusal && p.refusal.overridable_with !== "no_successor";
  return (
    <div style={{ padding: "10px 14px", borderTop: border, display: "grid", gap: 6 }} data-testid={"retire-row-" + name}>
      <div style={{ display: "flex", justifyContent: "space-between", gap: 12, flexWrap: "wrap" }}>
        <span style={{ minWidth: 0 }}>
          <span className="mono">{name}</span>
          {p.failure_reason ? <span style={{ display: "block", color: "var(--text-2)" }}>Failed: {p.failure_reason}</span> : null}
        </span>
        <span style={{ textAlign: "right" }}>
          <StatusBadge tone={chip.tone} icon={chip.icon}>{chip.text}</StatusBadge>
          <span style={{ display: "block", color: "var(--text-2)", fontSize: "var(--text-xs)" }}>checked just now</span>
        </span>
      </div>
      {blocked ? (
        <Banner tone="warn" dismissible={false} style={{ display: "flex", justifyContent: "space-between", gap: 10, flexWrap: "wrap", alignItems: "center" }}>
          <span>{p.refusal!.sentence}</span>
          {p.refusal!.code === "RETIRE_SOURCE_PRESENT" && onRetry ? (
            <button className="btn btn--sm" onClick={() => onRetry(id)}>Retry instead</button>
          ) : null}
        </Banner>
      ) : null}
    </div>
  );
}
