/**
 * Re-encrypt under current suite (EPIC T, issue #1318 over T2.2; design
 * docs/design/1318-suite-agility-ui.html, section 4).
 *
 * The dialog asks for the dry run the moment it opens, and the run is armed
 * only once that answer is on screen: every copy under the suite, grouped by
 * the key it records, the medium it would move to, and every copy the engine
 * will not move with the engine's own reason (same medium only, key
 * unavailable, already current). The run moves each copy to another medium
 * under the active suite and key and retires the old copy only after the new
 * one decrypt-verifies; nothing here retires or deletes anything itself.
 */
import { useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { describeFailure } from "@shared/api/failure";
import { ConfirmationDialog } from "@shared/components/ConfirmationDialog";
import { useAsync } from "@shared/hooks/useAsync";
import { CommandEcho } from "@shared/pages/CommandEcho";
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
  const plan = useAsync(() => api.planReencrypt({ suite }), [api, suite]);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [started, setStarted] = useState<string | null>(null);

  const copies = plan.data?.copies ?? [];
  const moving = copies.filter((c) => c.refusal === null).length;
  const destinations = [...new Set(copies.map((c) => c.to).filter((t): t is string => t !== null))];

  const run = async () => {
    setBusy(true);
    setFailure(null);
    try {
      const r = await api.startReencrypt({ suite });
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
      confirmLabel={started ? "Close" : "Re-encrypt " + moving + " copies"}
      disabled={busy || (!started && (plan.data === null || moving === 0))}
      onCancel={onClose}
      onConfirm={() => (started ? onClose() : void run())}
    >
      <CommandEcho label="Dry run" commands={[reencryptCommand({ suite, dryRun: true })]} />
      {plan.error ? (
        <p role="alert" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{clipText(plan.error.message)}</p>
      ) : plan.data === null ? (
        <p style={{ margin: 0, color: "var(--text-2)" }}>Asking the engine for a dry run. Nothing moves until you press Re-encrypt.</p>
      ) : (
        <div data-reencrypt-plan="" style={{ display: "flex", flexDirection: "column", gap: 8 }}>
          <p style={{ margin: 0 }}>
            {"Dry run: " + copies.length + (copies.length === 1 ? " copy" : " copies") + " under " + clipText(suite) + ". " +
              moving + " can move" + (destinations.length > 0 ? " to " + destinations.map(clipText).join(", ") : "") + ", " +
              (copies.length - moving) + " refused."}
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
                          {[...new Set(g.refused.map((c) => c.refusal ?? ""))].map((reason) => (
                            <li key={reason}>
                              {g.refused.filter((c) => c.refusal === reason).length + ": "}
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
            Each copy moves to another medium under the active suite and key. The old copy is retired only after the new one decrypt-verifies, and no plaintext is written to local disk.
          </p>
          <CommandEcho label="Equivalent command" commands={[reencryptCommand({ suite })]} />
        </div>
      )}
      {started ? <p role="status" style={{ margin: 0, fontWeight: 600 }}>{"Started: operation " + clipText(started) + ". Its progress is in Activity."}</p> : null}
      {failure ? <p role="alert" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{failure}</p> : null}
    </ConfirmationDialog>
  );
}
