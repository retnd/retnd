/**
 * Re-encrypt under current suite (EPIC T, issue #1318 over T2.2; design
 * docs/design/1318-suite-agility-ui.html, section 4).
 *
 * The dialog asks for the dry run the moment it opens. That first answer is
 * a proposal: each copy with the medium the engine would suggest. The
 * operator then picks the destination from those proposals, because the CLI
 * and the service never choose a medium for anybody (a run needs --to), and
 * the dialog asks for the dry run again scoped to that medium. Only that
 * scoped answer arms the run, and the button counts only the copies it says
 * will move: a copy already on the chosen medium comes back as a refusal
 * (same medium only) and is not counted.
 *
 * The run moves each copy to the chosen medium under the active suite and
 * key and retires the old copy only after the new one decrypt-verifies;
 * nothing here retires or deletes anything itself.
 */
import { useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { describeFailure } from "@shared/api/failure";
import { ConfirmationDialog } from "@shared/components/ConfirmationDialog";
import { useAsync } from "@shared/hooks/useAsync";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { ReencryptPlanCopy } from "@shared/types/encryption";
import { ClippedText, clipText } from "./ClippedText";
import { reencryptCommand } from "./encryptionCommands";

interface KeyGroup {
  keyId: string;
  moving: ReencryptPlanCopy[];
  refused: ReencryptPlanCopy[];
}

function byKey(copies: ReencryptPlanCopy[]): KeyGroup[] {
  const groups = new Map<string, KeyGroup>();
  for (const c of copies) {
    const g = groups.get(c.keyId) ?? { keyId: c.keyId, moving: [], refused: [] };
    (c.refusal === null ? g.moving : g.refused).push(c);
    groups.set(c.keyId, g);
  }
  return [...groups.values()];
}

export function ReencryptDialog({ suite, onClose }: { suite: string; onClose(): void }) {
  const api = useApi();
  const [to, setTo] = useState<string | null>(null);
  // The proposal: the dry run with no destination named.
  const proposal = useAsync(() => api.planReencrypt({ suite }), [api, suite]);
  // The dry run scoped to the chosen destination; nothing until one is chosen.
  const scoped = useAsync(() => (to === null ? Promise.resolve(null) : api.planReencrypt({ suite, to })), [api, suite, to]);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [started, setStarted] = useState<string | null>(null);

  const proposed = proposal.data?.copies ?? [];
  const destinations = [...new Set(proposed.filter((c) => c.refusal === null).map((c) => c.to).filter((t): t is string => t !== null))];
  // A scoped answer still in flight for an earlier choice is not shown as this one's.
  const current = to !== null && !scoped.loading ? scoped.data : null;
  const copies = current?.copies ?? proposed;
  const moving = current ? current.copies.filter((c) => c.refusal === null).length : 0;
  const error = scoped.error ?? proposal.error;

  const run = async () => {
    if (to === null) return;
    setBusy(true);
    setFailure(null);
    try {
      const r = await api.startReencrypt({ suite, to });
      setStarted(r.operationId);
    } catch (e) {
      setFailure(describeFailure(e, "the re-encryption was not started").message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <ConfirmationDialog
      open
      eyebrow="Encryption"
      title="Re-encrypt under current suite"
      confirmLabel={started ? "Close" : current ? "Re-encrypt " + moving + (moving === 1 ? " copy" : " copies") : "Choose a destination"}
      disabled={busy || (!started && (current === null || moving === 0))}
      onCancel={onClose}
      onConfirm={() => (started ? onClose() : void run())}
    >
      <CommandEcho label="Dry run" commands={[reencryptCommand({ suite, to: to ?? undefined, dryRun: true })]} />
      {error ? (
        <p role="alert" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{clipText(error.message)}</p>
      ) : proposal.data === null ? (
        <p style={{ margin: 0, color: "var(--text-2)" }}>Asking the engine for a dry run. Nothing moves until you choose a destination and press Re-encrypt.</p>
      ) : (
        <div data-reencrypt-plan="" style={{ display: "flex", flexDirection: "column", gap: 8 }}>
          {destinations.length === 0 ? (
            <p style={{ margin: 0 }}>{"No copy under " + clipText(suite) + " can move: the engine proposed no destination."}</p>
          ) : (
            <InfoTooltip id="encryption.reencrypt" block>
              <div role="radiogroup" aria-label="Destination" className="echoice" data-reencrypt-destinations="">
                {destinations.map((d) => (
                  <label key={d}>
                    <input type="radio" name="reencrypt-destination" checked={to === d} disabled={busy || started !== null} onChange={() => setTo(d)} />
                    <span className="mono">{clipText(d)}</span>
                  </label>
                ))}
              </div>
            </InfoTooltip>
          )}
          <p style={{ margin: 0 }}>
            {current
              ? "Dry run onto " + clipText(to ?? "") + ": " + current.copies.length + (current.copies.length === 1 ? " copy" : " copies") + " under " + clipText(suite) + ", " +
                moving + " would move, " + (current.copies.length - moving) + " refused."
              : "Dry run: " + copies.length + (copies.length === 1 ? " copy" : " copies") + " under " + clipText(suite) +
                ". The engine never picks the medium: choose one of the destinations it proposed, and the dry run is asked again for that medium."}
          </p>
          <div className="table-scroll">
            <table className="ekeys" aria-label="Dry run by key">
              <thead>
                <tr><th scope="col">Key</th><th scope="col">Moves</th><th scope="col">Refused</th></tr>
              </thead>
              <tbody>
                {byKey(copies).map((g) => (
                  <tr key={g.keyId}>
                    <td><ClippedText value={g.keyId} mono /></td>
                    <td>{g.moving.length}</td>
                    <td>
                      {g.refused.length === 0 ? "0" : (
                        <ul style={{ margin: 0, paddingLeft: 16 }}>
                          {[...new Set(g.refused.map((c) => c.reason ?? c.refusal ?? ""))].map((reason) => (
                            <li key={reason}>
                              {g.refused.filter((c) => (c.reason ?? c.refusal ?? "") === reason).length + ": "}
                              <ClippedText value={reason} />
                            </li>
                          ))}
                        </ul>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p style={{ margin: 0, color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
            Each copy moves to the chosen medium under the active suite and key. The old copy is retired only after the new one decrypt-verifies, and no plaintext is written to local disk.
          </p>
          {to !== null ? <CommandEcho label="Equivalent command" commands={[reencryptCommand({ suite, to })]} /> : null}
        </div>
      )}
      {started ? <p role="status" style={{ margin: 0, fontWeight: 600 }}>{"Started: operation " + clipText(started) + ". Its progress is in Activity."}</p> : null}
      {failure ? <p role="alert" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{failure}</p> : null}
    </ConfirmationDialog>
  );
}
