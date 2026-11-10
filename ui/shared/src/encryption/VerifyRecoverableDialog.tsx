/**
 * Verify recoverable: the operator-initiated decrypt-verify and its endings
 * (EPIC N, issue #995; design section 8).
 *
 * Nothing in the product schedules this. The dialog says in order what will
 * happen, shows what reading the copy back costs in the words the record has
 * (the stored size; this product holds no price list), and requires "I accept
 * this read" before Verify now is enabled. Egress is acknowledged in the
 * request itself, so the engine can refuse a client that skipped the tick.
 *
 * Three endings, three chips, each from the durable operation record and never
 * from a spinner's own memory: a match, a key that cannot be resolved (refused
 * before any byte is read, so nothing is spent), and a decrypt that failed
 * closed (with the engine's reason, and the plain statement that no partial
 * plaintext exists). A reconnect re-reads the same record by the same id, so a
 * reload shows the same state and starts no second transform.
 */
import { useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { AssuranceOperationRecord } from "@shared/api/contracts";
import { apiErrorOf, describeFailure } from "@shared/api/failure";
import { Acknowledgement } from "@shared/components/Acknowledgement";
import { ConfirmationDialog } from "@shared/components/ConfirmationDialog";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { Icon } from "@shared/design-system/icons";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { CopyEncryption } from "@shared/types/encryption";
import { bytes } from "@shared/utilities/format";
import { EncryptionChip } from "./EncryptionChips";
import { verifyCommand } from "./encryptionCommands";
import { verifyEnding, verifyRows } from "./verifyRows";
import "./encryption.css";

const POLL_MS = 1500;
/** About three minutes of looking for a record that never arrives. */
const MAX_POLLS = 120;

export function VerifyRecoverableDialog({
  artifactId, copy, onClose, onEnded
}: {
  artifactId: string;
  copy: CopyEncryption;
  onClose(): void;
  /** The run ended (any way): the page re-reads its copies and placements. */
  onEnded(result: { medium: string; ok: boolean; reason: string | null }): void;
}) {
  const api = useApi();
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState<{ keyMissing: boolean; message: string } | null>(null);
  const [opId, setOpId] = useState<string | null>(null);
  const [record, setRecord] = useState<AssuranceOperationRecord | null>(null);
  const [cancelling, setCancelling] = useState(false);

  useEffect(() => {
    if (opId === null) return;
    let live = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let polls = 0;
    const tick = async () => {
      polls += 1;
      try {
        const r = await api.getAssuranceOperation(opId);
        if (!live) return;
        setRecord(r);
        if (r.state !== "running") {
          onEnded({ medium: copy.medium, ok: r.state === "passed", reason: r.state === "passed" ? null : r.reason || null });
          return;
        }
      } catch {
        // The record is written by a batching writer: an id read right after its
        // start can be unknown for a moment. Keep looking, a bounded number of times.
        if (!live) return;
      }
      if (polls < MAX_POLLS) timer = setTimeout(() => void tick(), POLL_MS);
    };
    void tick();
    return () => { live = false; if (timer) clearTimeout(timer); };
  }, [api, opId, copy.medium, onEnded]);

  const start = async () => {
    setBusy(true);
    setRefusal(null);
    try {
      const started = await api.startDecryptVerify({ artifactId, medium: copy.medium, acknowledgeEgress: true });
      setOpId(started.operationId);
    } catch (e) {
      const code = apiErrorOf(e)?.code;
      setRefusal({ keyMissing: code === "ENCRYPTION_KEY_UNRESOLVABLE", message: describeFailure(e, "the decrypt-verify did not start").message });
    } finally {
      setBusy(false);
    }
  };

  const cancel = async () => {
    if (!opId) return;
    setCancelling(true);
    try { await api.cancelAssuranceOperation(opId); } catch { /* the record is the fact */ }
  };

  const ending = verifyEnding(record);
  const rows = opId ? verifyRows(record) : [];

  return (
    <ConfirmationDialog
      open
      eyebrow="Verify recoverable"
      title={"Verify recoverable: " + copy.medium}
      confirmLabel={opId ? "Close" : "Verify now"}
      cancelLabel={opId ? "Close" : "Cancel"}
      disabled={opId ? false : busy || !ack}
      onCancel={onClose}
      onConfirm={opId ? onClose : () => void start()}
    >
      {opId === null ? (
        <>
          <ol style={{ margin: 0, paddingLeft: 18 }}>
            <li>{"Resolve key " + (copy.keyId ?? "recorded on the copy")}</li>
            <li>Stream the stored object and decrypt it</li>
            <li>Compare the plaintext digest to the one the catalog holds</li>
            <li>Record the result. Nothing is written to disk.</li>
          </ol>
          <p data-egress="" style={{ margin: 0 }}>
            <strong>Reading this copy back costs egress.</strong>{" "}
            {(copy.storedSize !== null ? bytes(copy.storedSize) + " read. " : "") +
              "The provider bills it at its rate; I hold no price list. Nothing in this product runs this check on its own."}
          </p>
          <InfoTooltip id="encryption.verify.acceptCost" block>
            <Acknowledgement checked={ack} onChange={setAck}>I accept this read</Acknowledgement>
          </InfoTooltip>
          <CommandEcho label="Equivalent command" commands={[verifyCommand(artifactId, { medium: copy.medium, allowEgress: ack })]} />
          {refusal ? (
            <div role="alert" data-verify-refusal={refusal.keyMissing ? "key-missing" : "other"} style={{ display: "flex", flexDirection: "column", gap: 6 }}>
              {refusal.keyMissing ? <span><EncryptionChip state="key-missing" detail="nothing read" /></span> : null}
              <span style={{ color: "var(--danger)", fontWeight: 600 }}>{refusal.message}</span>
            </div>
          ) : null}
        </>
      ) : (
        <div data-verify-running={ending.kind === "running" ? "true" : "false"} style={{ display: "flex", flexDirection: "column", gap: 10 }}>
          <ul style={{ margin: 0, paddingLeft: 0, listStyle: "none", display: "flex", flexDirection: "column", gap: 4 }}>
            {rows.map((r) => (
              <li key={r.id} data-row={r.id} data-row-state={r.state}>
                {r.state === "done" ? <Icon name="success" /> : r.state === "failed" ? <Icon name="failure" /> : r.state === "active" ? "\u25B6" : <Icon name="status-idle" />}
                {" " + r.label}
              </li>
            ))}
          </ul>
          {ending.kind === "running" ? (
            <>
              <EncryptionChip state="live" />
              <p style={{ margin: 0, color: "var(--text-2)" }}>
                Progress is in Active operations and the terminal. A reload shows the same state and does not start a second read.
              </p>
              <button type="button" className="btn btn--sm" style={{ alignSelf: "flex-start" }} disabled={cancelling} onClick={() => void cancel()}>
                Cancel this check
              </button>
            </>
          ) : ending.kind === "match" ? (
            <>
              <EncryptionChip state="verified" age="just now" />
              <p style={{ margin: 0 }}>The plaintext digest equals the one the catalog holds. This copy can be read back with the key recorded against it.</p>
            </>
          ) : ending.kind === "failed" ? (
            <>
              <EncryptionChip state="decrypt-failed" />
              <p role="alert" style={{ margin: 0 }}>
                {ending.reason + ". No partial plaintext was written or shown, anywhere. Recover from another copy."}
              </p>
            </>
          ) : ending.kind === "cancelled" ? (
            <p style={{ margin: 0 }}>The check was cancelled. Nothing was written and the copy's state did not change.</p>
          ) : (
            <p style={{ margin: 0 }}>The check has no end on record. It may still be running in another process, or the process running it stopped.</p>
          )}
        </div>
      )}
    </ConfirmationDialog>
  );
}
