/**
 * The Retained-copy encryption card on Settings (EPIC N, issue #995; design
 * docs/design/994-retained-copy-encryption.html, sections 2, 3, 6 and 7).
 *
 * # Off is one switch and one sentence
 *
 * The default card says what Off is (today's bytes, today's rows) and offers
 * the switch. There is no format, cipher, chunk-size, nonce or KDF field, in
 * either state. What the build uses is drawn once as a deployment-wide
 * readout, so the absence of a knob is visible.
 *
 * # On is a status card
 *
 * The active managed key by id, when it was created, its status, whether a
 * recovery bundle is confirmed, and the notice that nags durably until one
 * is. There is no Show key, Copy key or Reveal, and no field that could hold
 * one: a key here is an id, a status and two timestamps.
 *
 * # Every write goes through the route
 *
 * Enable, turn off, reset and the external reference are PATCH
 * /encryption/policy (or reset), so Validate and the hot reload run, and the
 * card then RE-READS the policy: what it shows is what the engine now
 * resolves, never what it hoped it wrote. A write nothing re-resolves would be
 * the retention-override silent no-op under a new name.
 *
 * # The recovery bundle
 *
 * Export is a download and nothing is rendered. exportRecoveryBundle (the
 * client) saves the file and hands back a receipt (key id, digest, file name);
 * the material is never in this component's state, a store or a cache.
 * Confirming is a separate act and a statement, never an upload: the bundle's
 * digest is not asked for, and the card accepts no key bytes anywhere.
 */
import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useApi } from "@shared/api/ApiContext";
import { acknowledgeInsecureTransport } from "@shared/api/client";
import { apiErrorOf, describeFailure, isNotConfigured } from "@shared/api/failure";
import { Acknowledgement } from "@shared/components/Acknowledgement";
import { Banner } from "@shared/components/Banner";
import { ConfirmationDialog } from "@shared/components/ConfirmationDialog";
import { Cell, CellGrid } from "@shared/components/Definitions";
import { ErrorState } from "@shared/components/EmptyState";
import { StepUpPassword, stepUpMessage } from "@shared/components/StepUpFields";
import { WarningBanner } from "@shared/components/WarningBanner";
import { useAsync } from "@shared/hooks/useAsync";
import { useDestructiveGate } from "@shared/hooks/useDestructiveGate";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { EncryptionKey, EncryptionPolicy } from "@shared/types/encryption";
import { stamp } from "@shared/utilities/format";
import { EncryptionChip } from "./EncryptionChips";
import { EMPTY_REFERENCE, ExternalReferenceFields, referenceOf } from "./ExternalReferenceFields";
import type { ReferenceDraft } from "./ExternalReferenceFields";
import {
  keyConfirmCommand, keyCreateCommand, keyExportCommand, keyRetireCommand, keyRotateCommand,
  policyPatchCommand, policyResetCommand
} from "./encryptionCommands";
import { readKeys, readPolicy } from "./offered";
import "./encryption.css";

type Dialog =
  | { kind: "enable" }
  | { kind: "external" }
  | { kind: "off" }
  | { kind: "reset" }
  | { kind: "rotate" }
  | { kind: "bundle"; key: EncryptionKey }
  | { kind: "retire"; key: EncryptionKey };

const KEY_STATUS_WORDS: Record<string, string> = { active: "Active", superseded: "Retired", retired: "Retired" };

export function EncryptionCard({ readOnly }: { readOnly: boolean }) {
  const api = useApi();
  const policy = useAsync(() => readPolicy(api), [api]);
  const keys = useAsync(() => readKeys(api), [api]);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const heading = (
    <InfoTooltip id="encryption.card">
      <h2 className="eyebrow">Retained-copy encryption</h2>
    </InfoTooltip>
  );

  const reload = () => { policy.reload(); keys.reload(); };
  const close = () => { setDialog(null); setFailure(null); };

  /** One mutation: busy while it runs, the engine's own words if it refuses,
   *  and a re-read of everything the card shows once it has landed. */
  const act = async (work: () => Promise<unknown>, fallback: string): Promise<boolean> => {
    setBusy(true);
    setFailure(null);
    try {
      await work();
      reload();
      return true;
    } catch (e) {
      setFailure(describeFailure(e, fallback).message);
      return false;
    } finally {
      setBusy(false);
    }
  };

  if (policy.error && (policy.error.code === "ENCRYPTION_UNAVAILABLE" || isNotConfigured(policy.error))) {
    return (
      <section className="card" data-encryption-card="" data-state="unavailable">
        <div className="card__header">{heading}</div>
        <div className="card__body">
          <Banner tone="info" dismissible={false}>
            <span style={{ fontSize: "var(--text-sm)" }}>
              {"This deployment has no configuration file to write the policy into, so retained-copy encryption cannot be set from here."}
            </span>
          </Banner>
        </div>
      </section>
    );
  }
  if (policy.error) {
    return (
      <section className="card" data-encryption-card="" data-state="error">
        <div className="card__header">{heading}</div>
        <div className="card__body">
          <ErrorState message={policy.error.message} remediation={policy.error.remediation} correlationId={policy.error.correlationId} onRetry={reload} />
        </div>
      </section>
    );
  }
  if (policy.data === null) {
    return (
      <section className="card" data-encryption-card="" data-state="loading">
        <div className="card__header">{heading}</div>
        <div className="card__body">
          <p style={{ margin: 0, fontSize: 13, color: "var(--text-2)" }}>Reading this deployment's encryption policy...</p>
        </div>
      </section>
    );
  }

  const p: EncryptionPolicy = policy.data;
  const on = p.mode === "required";
  const keyList = keys.data ?? [];
  const active = keyList.find((k) => k.status === "active") ?? null;
  const unconfirmed = keyList.filter((k) => k.status !== "retired" && !k.bundleConfirmed);
  const unresolvable = keyList.filter((k) => k.status !== "retired" && !k.resolvable);

  return (
    <section className="card" data-encryption-card="" data-state={on ? "on" : "off"}>
      <div
        className="card__header"
        style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, flexWrap: "wrap" }}
      >
        {heading}
        {on && active ? <EncryptionChip state="key-active" /> : null}
      </div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 14 }}>
        <InfoTooltip id="encryption.enable" block>
          <label style={{ display: "inline-flex", gap: 10, alignItems: "center", fontSize: "var(--text-sm)", fontWeight: 600 }}>
            <input
              type="checkbox"
              role="switch"
              aria-label="Encrypt copies sent to destinations"
              checked={on}
              disabled={readOnly || busy}
              onChange={() => setDialog(on ? { kind: "off" } : { kind: "enable" })}
            />
            Encrypt copies sent to destinations
          </label>
        </InfoTooltip>

        {!on ? (
          <p style={{ margin: 0, fontSize: 13, color: "var(--text-2)", maxWidth: "78ch" }}>
            Off. Copies are written exactly as they are today: the same bytes, the same catalog rows, the same
            verification classes. The local copy is never encrypted; this applies to copies sent to a destination.
          </p>
        ) : (
          <>
            <CellGrid min={220}>
              <Cell label="Key holder" value={p.keyProvider.kind === "managed" ? "This deployment, in its state directory" : p.keyProvider.kind === "external" ? p.keyProvider.source ?? "An external reference" : "None"} />
              {active ? <Cell label="Key" value={active.id} mono /> : null}
              {active ? <Cell label="Created" value={stamp(active.createdAt)} /> : null}
              {active ? (
                <Cell
                  label="Recovery bundle"
                  value={active.bundleConfirmed ? "confirmed" + (active.bundleConfirmedAt ? " " + stamp(active.bundleConfirmedAt) : "") : active.bundleExportedAt ? "exported " + stamp(active.bundleExportedAt) + ", not yet confirmed" : "not confirmed"}
                />
              ) : null}
            </CellGrid>

            {unconfirmed.map((k) => (
              <WarningBanner
                key={"bundle-" + k.id}
                tone="warn"
                eyebrow="Recovery bundle"
                title={k.bundleExportedAt ? "Bundle exported, not yet confirmed" : "No confirmed recovery bundle"}
                tip="encryption.health.bundle"
                dismissible={false}
                actions={
                  readOnly ? null : (
                    <span style={{ display: "inline-flex", gap: 8, flexWrap: "wrap" }}>
                      <InfoTooltip id="encryption.bundle.export">
                        <button type="button" className="btn btn--sm" disabled={busy} onClick={() => setDialog({ kind: "bundle", key: k })}>
                          Export recovery bundle
                        </button>
                      </InfoTooltip>
                      <InfoTooltip id="encryption.bundle.confirm">
                        <button
                          type="button"
                          className="btn btn--sm"
                          disabled={busy}
                          onClick={() => void act(() => api.confirmRecoveryBundle(k.id), "the bundle was not confirmed")}
                        >
                          I have stored the bundle safely
                        </button>
                      </InfoTooltip>
                    </span>
                  )
                }
              >
                {"Copies are being encrypted under key " + k.id + ", which exists only on this host. If this host is lost, so are they. " +
                  (k.bundleExportedAt
                    ? "A bundle was exported " + stamp(k.bundleExportedAt) + ". A download can land on this same host, which moves nothing: confirm once the file is somewhere this machine's loss would not take with it."
                    : "Export the bundle, store it away from this machine, then confirm here.") +
                  " This stays until you do, including across restarts."}
              </WarningBanner>
            ))}

            {unresolvable.map((k) => (
              <WarningBanner
                key={"missing-" + k.id}
                tone="danger"
                eyebrow="Key"
                title={"A required key cannot be resolved: " + k.id}
                tip="encryption.health.keyMissing"
                dismissible={false}
              >
                {(k.reason ?? "The key does not resolve right now.") + " Copies that need it are refused rather than written in plaintext, and nothing was deleted."}
              </WarningBanner>
            ))}

            {failure && dialog === null ? <Banner tone="danger" dismissKey={failure}><span style={{ fontSize: "var(--text-sm)" }}>{failure}</span></Banner> : null}

            {readOnly ? null : (
              <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
                {active ? (
                  <InfoTooltip id="encryption.bundle.export">
                    <button type="button" className="btn btn--sm" disabled={busy} onClick={() => setDialog({ kind: "bundle", key: active })}>
                      Export recovery bundle
                    </button>
                  </InfoTooltip>
                ) : null}
                <InfoTooltip id="encryption.key.rotate">
                  <button type="button" className="btn btn--sm" disabled={busy || !active} onClick={() => setDialog({ kind: "rotate" })}>
                    Rotate key
                  </button>
                </InfoTooltip>
                <InfoTooltip id="encryption.keyProvider">
                  <button type="button" className="btn btn--sm btn--quiet" disabled={busy} onClick={() => setDialog({ kind: "external" })}>
                    Use an external key reference instead
                  </button>
                </InfoTooltip>
                <button type="button" className="btn btn--sm btn--quiet" disabled={busy} onClick={() => setDialog({ kind: "reset" })}>
                  Remove the policy block
                </button>
              </div>
            )}

            {keyList.length > 0 ? (
              <div className="table-scroll">
                <table className="ekeys" aria-label="Managed keys">
                  <thead>
                    <tr>
                      <th scope="col">Key</th><th scope="col">Created</th><th scope="col">Status</th><th scope="col">Copies</th><th scope="col">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {keyList.map((k) => (
                      <tr key={k.id} data-key-id={k.id} data-key-status={k.status}>
                        <td className="mono">{k.id}</td>
                        <td>{stamp(k.createdAt)}</td>
                        <td>
                          {(KEY_STATUS_WORDS[k.status] ?? k.status) +
                            (k.resolvable ? "" : ", does not resolve") +
                            (k.retiredAt ? " " + stamp(k.retiredAt) : "")}
                        </td>
                        <td data-copies-recording={k.copiesRecording}>
                          {k.copiesRecording > 0 ? (
                            <Link to={"/backups?key=" + encodeURIComponent(k.id)}>{k.copiesRecording + " in Backups"}</Link>
                          ) : (
                            "0"
                          )}
                        </td>
                        <td>
                          {readOnly || k.status === "active" || k.status === "retired" ? null : (
                            <InfoTooltip id="encryption.key.retire">
                              <button type="button" className="btn btn--sm btn--destructive" disabled={busy} onClick={() => setDialog({ kind: "retire", key: k })}>
                                Retire
                              </button>
                            </InfoTooltip>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : null}
          </>
        )}

        <div className="ereadout" data-deployment-wide="">
          <span className="ereadout__lab">Deployment-wide</span>
          <span>Format: recorded per copy by the build. There is no cipher, chunk size or algorithm to choose.</span>
        </div>

        <CommandEcho
          label="Equivalent command"
          commands={on ? [policyPatchCommand({ mode: "required" }), keyRotateCommand()] : [policyPatchCommand({ mode: "required", keyProvider: { managed: true } }), keyCreateCommand()]}
        />
      </div>

      {dialog?.kind === "enable" ? (
        <EnableDialog
          hasActiveKey={active !== null}
          busy={busy}
          failure={failure}
          onCancel={close}
          onEnable={(external) =>
            void act(async () => {
              if (external) {
                await api.patchEncryptionPolicy({ mode: "required", keyProvider: { external } });
              } else {
                // The key first, so a required policy never exists without one.
                if (active === null) await api.createEncryptionKey();
                await api.patchEncryptionPolicy({ mode: "required", keyProvider: { managed: true } });
              }
            }, "encryption was not enabled").then((ok) => ok && close())
          }
        />
      ) : null}

      {dialog?.kind === "external" ? (
        <ExternalDialog
          busy={busy}
          failure={failure}
          onCancel={close}
          onSave={(external) =>
            void act(() => api.patchEncryptionPolicy({ keyProvider: { external } }), "the reference was not saved").then((ok) => ok && close())
          }
        />
      ) : null}

      <ConfirmationDialog
        open={dialog?.kind === "off"}
        eyebrow="Retained-copy encryption"
        title="Turn encryption off?"
        confirmLabel="Turn off"
        disabled={busy}
        onCancel={close}
        onConfirm={() => void act(() => api.patchEncryptionPolicy({ mode: "off" }), "encryption was not turned off").then((ok) => ok && close())}
      >
        <p style={{ margin: 0 }}>
          New copies sent to a destination are written as plaintext again. Copies already written stay encrypted and stay readable through the key recorded against each of them, so keep the recovery bundle.
        </p>
        <CommandEcho label="Equivalent command" commands={[policyPatchCommand({ mode: "off" })]} />
        {failure ? <p role="alert" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{failure}</p> : null}
      </ConfirmationDialog>

      <ConfirmationDialog
        open={dialog?.kind === "reset"}
        eyebrow="Retained-copy encryption"
        title="Remove the encryption block?"
        confirmLabel="Remove the block"
        disabled={busy}
        onCancel={close}
        onConfirm={() => void act(() => api.resetEncryptionPolicy(), "the policy block was not removed").then((ok) => ok && close())}
      >
        <p style={{ margin: 0 }}>
          The deployment resolves to off with no key provider. Copies already written stay encrypted and stay readable through their recorded keys.
        </p>
        <CommandEcho label="Equivalent command" commands={[policyResetCommand()]} />
        {failure ? <p role="alert" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{failure}</p> : null}
      </ConfirmationDialog>

      <ConfirmationDialog
        open={dialog?.kind === "rotate"}
        eyebrow="Rotate key"
        title="Rotate the managed key?"
        confirmLabel="Rotate key"
        disabled={busy}
        onCancel={close}
        onConfirm={() => void act(() => api.rotateEncryptionKey(), "the key was not rotated").then((ok) => ok && close())}
      >
        <p style={{ margin: 0 }}>Rotating creates a new active key. From now on:</p>
        <ul style={{ margin: 0, paddingLeft: 18 }}>
          <li>New copies are encrypted under the new key.</li>
          <li>Every copy made so far stays readable through the key recorded against it.</li>
          <li>The current key stays in the registry as Retired while any copy records it.</li>
          <li>Nothing is re-encrypted.</li>
        </ul>
        <WarningBanner tone="warn" title="The new key will have no confirmed bundle." dismissible={false}>
          The notice here and on the Dashboard returns until you export and confirm a bundle for the new key.
        </WarningBanner>
        <CommandEcho label="Equivalent command" commands={[keyRotateCommand()]} />
        {failure ? <p role="alert" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{failure}</p> : null}
      </ConfirmationDialog>

      {dialog?.kind === "retire" ? (
        <RetireDialog
          keyId={dialog.key.id}
          busy={busy}
          refusal={failure}
          onCancel={close}
          onRetire={() => void act(() => api.retireEncryptionKey(dialog.key.id), "the key was not retired").then((ok) => ok && close())}
        />
      ) : null}

      {dialog?.kind === "bundle" ? (
        <BundleDialog
          keyId={dialog.key.id}
          onCancel={close}
          onDone={() => { reload(); close(); }}
        />
      ) : null}
    </section>
  );
}

function EnableDialog({
  hasActiveKey, busy, failure, onCancel, onEnable
}: {
  hasActiveKey: boolean;
  busy: boolean;
  failure: string | null;
  onCancel(): void;
  onEnable(external: ReturnType<typeof referenceOf>): void;
}) {
  const [holder, setHolder] = useState<"managed" | "external">("managed");
  const [draft, setDraft] = useState<ReferenceDraft>(EMPTY_REFERENCE);
  const reference = referenceOf(draft);
  const commands =
    holder === "managed"
      ? [...(hasActiveKey ? [] : [keyCreateCommand()]), policyPatchCommand({ mode: "required", keyProvider: { managed: true } })]
      : [policyPatchCommand({ mode: "required", keyProvider: reference ? { external: reference } : undefined })];
  return (
    <ConfirmationDialog
      open
      eyebrow="Retained-copy encryption"
      title="Enable retained-copy encryption"
      confirmLabel={holder === "managed" ? "Enable and create key" : "Enable"}
      disabled={busy || (holder === "external" && reference === null)}
      onCancel={onCancel}
      onConfirm={() => onEnable(holder === "external" ? reference : null)}
    >
      <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
        <legend style={{ fontWeight: 600, marginBottom: 6 }}>Who holds the key?</legend>
        <InfoTooltip id="encryption.keyProvider" block>
          <div role="radiogroup" aria-label="Who holds the key" style={{ display: "flex", flexDirection: "column", gap: 8 }}>
            <label style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
              <input type="radio" name="encryption-holder" checked={holder === "managed"} onChange={() => setHolder("managed")} />
              <span>
                <strong>A key this deployment manages</strong> (default)
                <span style={{ display: "block", color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                  Created now, kept as a file only this service can read, in its state directory. Anyone who can read that file can read the copies, so the recovery bundle you export next is what moves the key off this host.
                </span>
              </span>
            </label>
            <label style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
              <input type="radio" name="encryption-holder" checked={holder === "external"} onChange={() => setHolder("external")} />
              <span>
                <strong>A key I keep elsewhere</strong>
                <span style={{ display: "block", color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
                  A file path, an environment variable or a command that prints the key. You give a reference; the key itself is never typed or pasted here.
                </span>
              </span>
            </label>
          </div>
        </InfoTooltip>
      </fieldset>
      {holder === "external" ? <ExternalReferenceFields idPrefix="enable" draft={draft} onChange={setDraft} disabled={busy} /> : null}
      <p style={{ margin: 0, color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
        Nothing else to decide. The format is the build's and is recorded against every copy.
      </p>
      <CommandEcho label="Equivalent command" commands={commands} />
      {failure ? <p role="alert" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{failure}</p> : null}
    </ConfirmationDialog>
  );
}

function ExternalDialog({
  busy, failure, onCancel, onSave
}: {
  busy: boolean;
  failure: string | null;
  onCancel(): void;
  onSave(external: NonNullable<ReturnType<typeof referenceOf>>): void;
}) {
  const [draft, setDraft] = useState<ReferenceDraft>(EMPTY_REFERENCE);
  const reference = referenceOf(draft);
  return (
    <ConfirmationDialog
      open
      eyebrow="External key reference"
      title="Use an external key reference"
      confirmLabel="Save reference"
      disabled={busy || reference === null}
      onCancel={onCancel}
      onConfirm={() => reference && onSave(reference)}
    >
      <p style={{ margin: 0 }}>
        The reference is resolved when a copy is written or verified. Its value is never shown here and never stored in the configuration or the catalog.
      </p>
      <ExternalReferenceFields idPrefix="external" draft={draft} onChange={setDraft} disabled={busy} />
      <CommandEcho label="Equivalent command" commands={[policyPatchCommand({ keyProvider: reference ? { external: reference } : undefined })]} />
      {failure ? <p role="alert" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{failure}</p> : null}
    </ConfirmationDialog>
  );
}

function RetireDialog({
  keyId, busy, refusal, onCancel, onRetire
}: {
  keyId: string;
  busy: boolean;
  /** The engine's refusal, naming the copies, in its own words. */
  refusal: string | null;
  onCancel(): void;
  onRetire(): void;
}) {
  return (
    <ConfirmationDialog
      open
      destructive
      eyebrow="Retire key"
      title={"Retire " + keyId + "?"}
      confirmLabel="Retire key"
      disabled={busy || refusal !== null}
      onCancel={onCancel}
      onConfirm={onRetire}
    >
      <p style={{ margin: 0 }}>
        A key that no active copy records can be retired. If one still does, the engine refuses and names the copies, because retiring the key would make them unreadable.
      </p>
      <CommandEcho label="Equivalent command" commands={[keyRetireCommand(keyId)]} />
      {refusal ? (
        <p role="alert" data-retire-refusal="" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{refusal}</p>
      ) : null}
    </ConfirmationDialog>
  );
}

/**
 * The one-time export. One checkbox arms Download bundle; the file is the only
 * place the material goes. The password, where the deployment asks for one, is
 * held in a ref for the one request and forgotten, as the validator upload does.
 */
function BundleDialog({
  keyId, onCancel, onDone
}: {
  keyId: string;
  onCancel(): void;
  onDone(receipt: { keyId: string; fileName: string; digest: string }): void;
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
      eyebrow="Export recovery bundle"
      title="Export recovery bundle"
      confirmLabel="Download bundle"
      disabled={busy || !ack || (needsPassword && !hasPassword)}
      onCancel={onCancel}
      onConfirm={() => void download()}
    >
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
