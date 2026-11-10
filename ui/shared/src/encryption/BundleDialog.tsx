/**
 * The one-time export (EPIC N, #995), shared by the Settings card and the
 * suite change flow (EPIC T, #1318). One checkbox arms Download bundle; the
 * file is the only place the material goes. The password, where the
 * deployment asks for one, is held in a ref for the one request and
 * forgotten, as the validator upload does.
 *
 * The flow draws it as one of its steps, so the title, the eyebrow, a header
 * (the step strip) and the cancel path are the caller's: the dialog a person
 * is in keeps its name from the first step to the last, and Cancel on a
 * half-done change goes to the flow's still-owed list rather than closing.
 */
import { useRef, useState } from "react";
import type { ReactNode } from "react";
import { useApi } from "@shared/api/ApiContext";
import { acknowledgeInsecureTransport } from "@shared/api/client";
import { apiErrorOf, describeFailure } from "@shared/api/failure";
import { Acknowledgement } from "@shared/components/Acknowledgement";
import { ConfirmationDialog } from "@shared/components/ConfirmationDialog";
import { StepUpPassword, stepUpMessage } from "@shared/components/StepUpFields";
import { WarningBanner } from "@shared/components/WarningBanner";
import { useDestructiveGate } from "@shared/hooks/useDestructiveGate";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { keyConfirmCommand, keyExportCommand } from "./encryptionCommands";

export function BundleDialog({
  keyId, onCancel, onDone, title = "Export recovery bundle", eyebrow = "Export recovery bundle", header
}: {
  keyId: string;
  onCancel(): void;
  onDone(receipt: { keyId: string; fileName: string; digest: string }): void;
  title?: string;
  eyebrow?: string;
  header?: ReactNode;
}) {
  const api = useApi();
  const gate = useDestructiveGate();
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const password = useRef("");
  const [hasPassword, setHasPassword] = useState(false);
  const [passwordKey, setPasswordKey] = useState(0);
  const needsPassword = gate.stepUpRequired;
  const forget = () => { password.current = ""; setHasPassword(false); setPasswordKey((k) => k + 1); };

  const download = async () => {
    setBusy(true);
    setError(null);
    if (needsPassword && gate.insecureTransportAckRequired && !(await acknowledgeInsecureTransport())) {
      setError(stepUpMessage("INSECURE_TRANSPORT_ACK_REQUIRED", "bundle") ?? "Nothing was sent.");
      setBusy(false);
      return;
    }
    const opts = needsPassword && password.current ? { stepUpPassword: password.current } : {};
    forget();
    try {
      onDone(await api.exportRecoveryBundle(keyId, opts));
    } catch (e) {
      const code = apiErrorOf(e)?.code;
      setError(stepUpMessage(code, "bundle") ?? describeFailure(e, "the bundle was not exported").message);
      setBusy(false);
    }
  };

  return (
    <ConfirmationDialog
      open
      eyebrow={eyebrow}
      title={title}
      confirmLabel="Download bundle"
      disabled={busy || !ack || (needsPassword && !hasPassword)}
      onCancel={onCancel}
      onConfirm={() => void download()}
    >
      {header}
      <WarningBanner tone="warn" title="This is the one time the key leaves this deployment." dismissible={false}>
        {"The bundle is a file holding the key for " + keyId + ", integrity metadata and the instructions for decrypting a copy on a clean machine. " +
          "Whoever holds the file can read every copy made under this key. Store it away from this host."}
      </WarningBanner>
      <InfoTooltip id="encryption.bundle.acknowledge" block>
        <Acknowledgement checked={ack} onChange={setAck}>
          I understand this file can decrypt every copy made under this key
        </Acknowledgement>
      </InfoTooltip>
      {needsPassword ? (
        <StepUpPassword
          key={passwordKey}
          act="bundle"
          id="bundle-step-up-help"
          onPassword={(next) => { password.current = next; setHasPassword(next !== ""); }}
        />
      ) : null}
      <p style={{ margin: 0, color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
        Nothing is shown on screen. The key goes into the downloaded file and nowhere else.
      </p>
      <CommandEcho label="Equivalent command" commands={[keyExportCommand(keyId), keyConfirmCommand(keyId)]} />
      {error ? <p role="alert" data-step-up-error="" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{error}</p> : null}
    </ConfirmationDialog>
  );
}
