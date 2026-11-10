/**
 * The per-set Inherit / Off / Required control, beside the retention card
 * (EPIC N, issue #995; design section 4).
 *
 * Three values and only three. Inherit says what it resolves to, exactly as the
 * retention card attributes an inheriting set to the deployment's chain, so a
 * global edit is visibly what moved this set, and inherit and an explicit off
 * stay distinguishable on screen. Required says what it means in one sentence.
 *
 * Every write goes through the route and the card then re-reads the policy, so
 * what it shows is the engine's own resolution after Validate and the hot
 * reload. Rotation cadence, format and chunk size are not properties of a set
 * and have no control here; the key provider is drawn as a read-only
 * attribution, reachable for an override through the same reference-only flow
 * as the deployment card.
 *
 * A set on the kopia engine draws the control disabled and a statement of fact, in
 * the words ADR 0027 gives, with no pop-up: its repository is already
 * encrypted by kopia's own format, `required` is satisfied by that and layers
 * nothing.
 */
import { useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { describeFailure } from "@shared/api/failure";
import { Banner } from "@shared/components/Banner";
import { WarningBanner } from "@shared/components/WarningBanner";
import { useAsync } from "@shared/hooks/useAsync";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { SetEncryption, SetEncryptionMode } from "@shared/types/encryption";
import type { BackupEngine } from "@shared/types/snapshot";
import { EMPTY_REFERENCE, ExternalReferenceFields, referenceOf } from "./ExternalReferenceFields";
import type { ReferenceDraft } from "./ExternalReferenceFields";
import { setEncryptionCommand, setEncryptionResetCommand } from "./encryptionCommands";
import { readKeys, readPolicy } from "./offered";
import "./encryption.css";

const MODES: { mode: SetEncryptionMode; label: string }[] = [
  { mode: "inherit", label: "Inherit" },
  { mode: "off", label: "Off" },
  { mode: "required", label: "Required" }
];

const REQUIRED_WORDS =
  "Required: a copy to a destination is refused rather than written in plaintext. The refusal happens before anything is deleted at the source or on the medium.";

export function SetEncryptionCard({
  source, set, engine, readOnly
}: {
  source: string;
  set: string;
  engine: BackupEngine;
  readOnly: boolean;
}) {
  const api = useApi();
  const setId = source + "/" + set;
  const policy = useAsync(() => readPolicy(api), [api]);
  const keys = useAsync(() => readKeys(api), [api]);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [ownKey, setOwnKey] = useState(false);
  const [draft, setDraft] = useState<ReferenceDraft>(EMPTY_REFERENCE);

  const heading = (
    <InfoTooltip id="encryption.card">
      <h2 className="eyebrow">Retained-copy encryption (this set)</h2>
    </InfoTooltip>
  );
  const frame = (body: React.ReactNode) => (
    <section className="card" data-set-encryption="" aria-label="Retained-copy encryption for this set">
      <div className="card__header">{heading}</div>
      <div className="card__body" style={{ display: "flex", flexDirection: "column", gap: 12 }}>{body}</div>
    </section>
  );

  // The deployment says it cannot be asked (no file-backed configuration, or an
  // older engine): nothing is claimed, and no control that could only fail is drawn.
  if (policy.error) {
    return frame(<p style={{ margin: 0, fontSize: 13, color: "var(--text-2)" }}>{policy.error.code === "ENCRYPTION_UNAVAILABLE" || policy.error.code === "NOT_CONFIGURED"
          ? "Retained-copy encryption is not available here, because this deployment has no configuration file to write a policy into."
          : "Retained-copy encryption could not be read. " + policy.error.message}</p>);
  }
  if (policy.data === null) {
    return frame(<p style={{ margin: 0, fontSize: 13, color: "var(--text-2)" }}>Reading this set's encryption policy...</p>);
  }

  if (engine === "kopia") {
    return frame(
      <>
        <div role="radiogroup" aria-label="Encryption for this set" className="echoice">
          {MODES.map((m) => (
            <label key={m.mode}><input type="radio" disabled name={"enc-" + setId} checked={false} readOnly />{m.label}</label>
          ))}
        </div>
        <p data-engine-statement="" style={{ margin: 0, fontSize: 13, color: "var(--text-2)", maxWidth: "78ch" }}>
          This set's repository is already encrypted by the kopia engine under its repository passphrase. Required on such a set is satisfied by that and layers nothing.
        </p>
      </>
    );
  }

  const mine: SetEncryption | undefined = policy.data.sets.find((s) => s.backupSetId === setId);
  if (!mine) {
    return frame(<p style={{ margin: 0, fontSize: 13, color: "var(--text-2)" }}>The engine did not report this set's encryption policy.</p>);
  }
  const unresolvable = (keys.data ?? []).filter((k) => k.status !== "retired" && !k.resolvable);
  const refusing = mine.effectiveMode === "required" && unresolvable.length > 0;

  const change = async (work: () => Promise<unknown>, fallback: string) => {
    setBusy(true);
    setFailure(null);
    try {
      await work();
      // Re-read: the card shows what the engine now resolves for this set.
      policy.reload();
      keys.reload();
      return true;
    } catch (e) {
      setFailure(describeFailure(e, fallback).message);
      return false;
    } finally {
      setBusy(false);
    }
  };

  const pick = (mode: SetEncryptionMode) => {
    if (mode === mine.mode) return;
    void change(
      () => (mode === "inherit" ? api.clearBackupSetEncryption(source, set) : api.setBackupSetEncryption(source, set, { mode })),
      "the set's encryption was not changed"
    );
  };

  const reference = referenceOf(draft);
  const resolved = mine.effectiveMode === "required" ? "required" : "off";

  return frame(
    <>
      <InfoTooltip id="encryption.set.mode" block>
        <div role="radiogroup" aria-label="Encryption for this set" className="echoice">
          {MODES.map((m) => (
            <label key={m.mode}>
              <input
                type="radio"
                name={"enc-" + setId}
                checked={mine.mode === m.mode}
                disabled={readOnly || busy}
                onChange={() => pick(m.mode)}
              />
              {m.label}
            </label>
          ))}
        </div>
      </InfoTooltip>

      {mine.mode === "inherit" ? (
        <p data-resolved-mode={resolved} style={{ margin: 0, fontSize: 13, color: "var(--text-2)" }}>
          {"Inheriting: " + resolved + " (from the deployment setting). " +
            (resolved === "required"
              ? "Copies of this set's artifacts sent to a destination are encrypted; the local copy is not."
              : "Copies are written as they are today.")}
        </p>
      ) : mine.mode === "required" ? (
        <p data-resolved-mode="required" style={{ margin: 0, fontSize: 13, color: "var(--text-2)" }}>{REQUIRED_WORDS}</p>
      ) : (
        <p data-resolved-mode="off" style={{ margin: 0, fontSize: 13, color: "var(--text-2)" }}>
          This set's copies are written as plaintext whatever the deployment says.
        </p>
      )}

      {mine.effectiveMode === "required" ? (
        <p data-key-attribution="" style={{ margin: 0, fontSize: 13, color: "var(--text-2)" }}>
          {"Key: " +
            (mine.effectiveKeyProvider.kind === "managed"
              ? "the deployment's managed key"
              : mine.effectiveKeyProvider.kind === "external"
                ? (mine.effectiveKeyProvider.source ?? "an external reference")
                : "none declared")}
        </p>
      ) : null}

      {refusing ? (
        <WarningBanner tone="danger" title="Required, key missing" tip="encryption.health.keyMissing" dismissible={false}>
          {"Copies are being refused. Key " + unresolvable[0].id + " does not resolve" + (unresolvable[0].reason ? " (" + unresolvable[0].reason + ")" : "") + ". Nothing was deleted."}
        </WarningBanner>
      ) : null}

      {readOnly ? null : (
        <>
          <button type="button" className="btn btn--sm btn--quiet" style={{ alignSelf: "flex-start" }} disabled={busy} onClick={() => setOwnKey((v) => !v)}>
            Use a different key reference for this set
          </button>
          {ownKey ? (
            <>
              <ExternalReferenceFields idPrefix={"enc-set-" + setId} draft={draft} onChange={setDraft} disabled={busy} />
              <button
                type="button"
                className="btn btn--sm"
                style={{ alignSelf: "flex-start" }}
                disabled={busy || reference === null}
                onClick={() =>
                  reference &&
                  void change(
                    () => api.setBackupSetEncryption(source, set, { mode: "required", keyProvider: { external: reference } }),
                    "the set's key reference was not saved"
                  ).then((ok) => { if (ok) { setOwnKey(false); setDraft(EMPTY_REFERENCE); } })
                }
              >
                Save reference for this set
              </button>
            </>
          ) : null}
        </>
      )}

      {failure ? <Banner tone="danger" dismissKey={failure}><span style={{ fontSize: "var(--text-sm)" }}>{failure}</span></Banner> : null}

      <CommandEcho
        label="Equivalent command"
        commands={[
          // One line per value the control can take, each the command its press makes.
          // Inherit is the DELETE route, whose verb is --reset.
          setEncryptionResetCommand(setId),
          setEncryptionCommand(setId, "off"),
          setEncryptionCommand(setId, "required"),
          ...(ownKey && reference ? [setEncryptionCommand(setId, "required", { external: reference })] : [])
        ]}
      />
    </>
  );
}
