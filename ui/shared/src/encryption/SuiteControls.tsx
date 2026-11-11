/**
 * The suite surfaces of the Retained-copy encryption card (EPIC T, issue
 * #1318; design docs/design/1318-suite-agility-ui.html, sections 1 and 2).
 *
 * # The choice
 *
 * One radio group, "Suite", over the registry the route returns, in its own
 * order. Each option carries its id, a status badge (Default, Supported,
 * Deprecated with the version it retires in, Retired) and the suite's own
 * Describe() sentence, which is runtime data drawn as escaped text and never
 * put through the HTML tooltip registry. A retired suite is drawn, disabled,
 * with the reason. A native <select> could carry none of that beside an
 * option, and its disabled ink is the browser's, which cannot be held to
 * 4.5:1, so the choice is a radio group.
 *
 * # The change
 *
 * Choosing a suite opens one dialog that walks four steps in order: the
 * policy patch, then, when the answer says rotation_needed, Rotate key under
 * the new suite, Download bundle, and Confirm. Each step echoes its command.
 * Cancel before the patch closes with nothing written. Cancel after it does
 * not close: it lists what is still owed, with the command for each, and
 * offers to go back or to leave. Leaving keeps the policy as patched, and the
 * card and the Dashboard keep the degraded states (no active key for the
 * suite, no confirmed recovery bundle) until the work is done.
 *
 * Nothing here holds key material: the rotation's answer is mapped by the
 * client to an id, a status and a suite, and the bundle step is the same
 * download the card uses, whose bytes live only in the client function.
 */
import { useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { describeFailure } from "@shared/api/failure";
import { ConfirmationDialog } from "@shared/components/ConfirmationDialog";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { Icon } from "@shared/design-system/icons";
import { CommandEcho } from "@shared/pages/CommandEcho";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import type { EncryptionSuite } from "@shared/types/encryption";
import { BundleDialog } from "./BundleDialog";
import { ClippedText, clipText } from "./ClippedText";
import { keyConfirmCommand, keyCreateCommand, keyExportCommand, keyRotateCommand, policyPatchCommand, suitesCommand } from "./encryptionCommands";
import "./encryption.css";

function badgeOf(s: EncryptionSuite): { kind: string; label: string } | null {
  if (s.status === "retired") return { kind: "retired", label: "Retired" };
  if (s.status === "deprecated") return { kind: "deprecated", label: "Deprecated" + (s.retirementVersion ? ", retiring in " + s.retirementVersion : "") };
  if (s.status === "supported") return s.isDefault ? { kind: "default", label: "Default" } : { kind: "supported", label: "Supported" };
  // A status this build has never heard of draws no badge, never a guess.
  return null;
}

export function SuiteChoice({
  suites, current, configured, disabled, onChoose
}: {
  suites: EncryptionSuite[];
  /** The policy's resolved suite. */
  current: string | null;
  /** What the configuration says: "default" or a pinned id. */
  configured: string | null;
  disabled: boolean;
  onChoose(id: string): void;
}) {
  if (suites.length === 0) return null;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
      <InfoTooltip id="encryption.suite">
        <span className="esuites__legend">Suite</span>
      </InfoTooltip>
      <FieldHelp label="Suite" help={FIELD_HELP.encryptionSuite}>
        {(helpId) => (
          <div role="radiogroup" aria-label="Suite" aria-describedby={helpId} className="esuites" data-suite-group="">
            {suites.map((s) => {
              const retired = s.status === "retired";
              const selected = current === s.id;
              const badge = badgeOf(s);
              const descId = "suite-desc-" + s.id.replace(/[^A-Za-z0-9_-]/g, "-");
              return (
                <div
                  key={s.id}
                  className="esuite"
                  data-suite-option={s.id}
                  data-suite-status={s.status}
                  data-selected={selected ? "" : undefined}
                  data-disabled={retired ? "" : undefined}
                >
                  <label className="esuite__head">
                    <input
                      type="radio"
                      name="encryption-suite"
                      checked={selected}
                      disabled={disabled || retired}
                      aria-describedby={descId}
                      onChange={() => onChoose(s.id)}
                    />
                    <span className="esuite__id mono">{clipText(s.id)}</span>
                  </label>
                  {badge ? (
                    <InfoTooltip id="encryption.suite.status">
                      <span className="esuite__badge" data-suite-badge={badge.kind}>{badge.label}</span>
                    </InfoTooltip>
                  ) : null}
                  <span className="esuite__desc" id={descId} data-suite-description="">
                    <ClippedText value={s.description} />
                  </span>
                  {retired ? (
                    <span className="esuite__reason" data-suite-reason="">
                      {(s.retirementVersion ? "Retired in " + clipText(s.retirementVersion) + ". " : "") + "Copies under it still read; nothing new is written under it."}
                    </span>
                  ) : null}
                </div>
              );
            })}
          </div>
        )}
      </FieldHelp>
      {configured ? (
        <p style={{ margin: 0, fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
          {"Configured: " + (configured === "default" ? "default (follows the build's default suite)" : clipText(configured))}
        </p>
      ) : null}
      <CommandEcho label="Equivalent command" commands={[suitesCommand()]} />
    </div>
  );
}

type Step = "patch" | "rotate" | "bundle" | "confirm";
const STEPS: { id: Step; label: string }[] = [
  { id: "patch", label: "Change suite" },
  { id: "rotate", label: "Rotate key" },
  { id: "bundle", label: "Download bundle" },
  { id: "confirm", label: "Confirm" }
];

function StepStrip({ now }: { now: Step }) {
  const at = STEPS.findIndex((s) => s.id === now);
  return (
    <ol className="esteps" aria-label="Steps">
      {STEPS.map((s, i) => {
        const state = i < at ? "done" : i === at ? "now" : "todo";
        return (
          <li key={s.id} className="esteps__step" data-suite-step={s.id} data-state={state} aria-current={state === "now" ? "step" : undefined}>
            <span className="esteps__glyph" aria-hidden="true">{state === "done" ? <Icon name="success" /> : String(i + 1)}</span>
            {s.label}
          </li>
        );
      })}
    </ol>
  );
}

/**
 * The change, as one dialog with a still-owed guard. `hasActiveKey` decides
 * whether the key step rotates (an active key exists, under another suite) or
 * creates (no key at all).
 */
export function SuiteChangeFlow({
  suite, hasActiveKey, start = "patch", onChanged, onClose
}: {
  suite: string;
  hasActiveKey: boolean;
  /** "rotate" when the policy already names the suite and only the key is
   *  owed (the card's missing-key notice), so nothing is patched twice. */
  start?: "patch" | "rotate";
  /** Re-read the card: called after every write, so what it shows between
   *  the steps is what the engine now resolves. */
  onChanged(): void;
  onClose(): void;
}) {
  const api = useApi();
  const [step, setStep] = useState<Step>(start);
  const [owed, setOwed] = useState(false);
  const [keyId, setKeyId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const title = "Change the suite";
  const keyCommand = hasActiveKey ? keyRotateCommand(suite) : keyCreateCommand(suite);

  const run = async (work: () => Promise<void>, fallback: string) => {
    setBusy(true);
    setFailure(null);
    try {
      await work();
    } catch (e) {
      setFailure(describeFailure(e, fallback).message);
    } finally {
      setBusy(false);
    }
  };

  // Cancel before anything was written closes; after, it says what is owed.
  // While a write is in flight nothing closes: Escape or a click outside
  // during the patch would otherwise drop the flow with the patch landing
  // behind it and the rotate step gone.
  const cancel = () => {
    if (busy) return;
    if (step === "patch") onClose();
    else setOwed(true);
  };

  if (owed) {
    const items: { text: string; command: string }[] = [];
    if (step === "rotate") items.push({ text: "An active key for suite " + clipText(suite) + ". Until then copies under the policy are refused, before anything is deleted.", command: keyCommand });
    if (step === "rotate" || step === "bundle") items.push({ text: "A downloaded recovery bundle for the new key.", command: keyExportCommand(keyId ?? "<the new key>") });
    items.push({ text: "A confirmation that the bundle is stored away from this host.", command: keyConfirmCommand(keyId ?? "<the new key>") });
    return (
      <ConfirmationDialog
        open
        eyebrow={title}
        title="Still owed"
        confirmLabel="Back to the steps"
        cancelLabel="Leave, I will finish later"
        onConfirm={() => setOwed(false)}
        onCancel={onClose}
      >
        <div className="eowed" role="note">
          <strong>The suite change is not finished.</strong>
          <ul style={{ margin: "6px 0 0", paddingLeft: 18, display: "flex", flexDirection: "column", gap: 6 }}>
            {items.map((it) => (
              <li key={it.text} data-owed="">
                {it.text}
                <CommandEcho label="Equivalent command" commands={[it.command]} />
              </li>
            ))}
          </ul>
        </div>
        <p style={{ margin: 0, color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
          Leaving keeps the policy as patched. The card and the Dashboard keep saying what is owed until it is done.
        </p>
      </ConfirmationDialog>
    );
  }

  if (step === "bundle" && keyId) {
    return (
      <BundleDialog
        keyId={keyId}
        title={title}
        eyebrow="Retained-copy encryption"
        header={<StepStrip now="bundle" />}
        onCancel={cancel}
        onDone={() => { onChanged(); setStep("confirm"); }}
      />
    );
  }

  const failureLine = failure ? <p role="alert" style={{ margin: 0, color: "var(--danger)", fontWeight: 600 }}>{failure}</p> : null;

  if (step === "patch") {
    return (
      <ConfirmationDialog
        open
        eyebrow="Retained-copy encryption"
        title={title}
        confirmLabel="Change suite"
        disabled={busy}
        onCancel={cancel}
        onConfirm={() =>
          void run(async () => {
            const p = await api.patchEncryptionPolicy({ suite });
            onChanged();
            if (p.rotationNeeded) setStep("rotate");
            else onClose();
          }, "the suite was not changed")
        }
      >
        <StepStrip now="patch" />
        <p style={{ margin: 0 }}>{"New copies will be written under " + clipText(suite) + " once it has an active key. From then on:"}</p>
        <ul style={{ margin: 0, paddingLeft: 18 }}>
          <li>Every copy made so far keeps the suite and key recorded against it, and stays readable.</li>
          <li>Nothing is re-encrypted. Re-encrypting old copies is a separate operation, from the Dashboard.</li>
          <li>A suite with no active key needs a rotation, a bundle export and a confirmation, in that order.</li>
        </ul>
        <CommandEcho label="Equivalent command" commands={[policyPatchCommand({ suite })]} />
        {failureLine}
      </ConfirmationDialog>
    );
  }

  if (step === "rotate") {
    return (
      <ConfirmationDialog
        open
        eyebrow="Retained-copy encryption"
        title={title}
        confirmLabel="Rotate key"
        disabled={busy}
        onCancel={cancel}
        onConfirm={() =>
          void run(async () => {
            const k = hasActiveKey ? await api.rotateEncryptionKey(suite) : await api.createEncryptionKey(suite);
            setKeyId(k.id);
            onChanged();
            setStep("bundle");
          }, "the key was not rotated")
        }
      >
        <StepStrip now="rotate" />
        <p style={{ margin: 0 }}>
          {"The policy now says " + clipText(suite) + " and there is no active key under it, so copies under the policy are refused, before anything is deleted, until you rotate."}
        </p>
        <p style={{ margin: 0, color: "var(--text-2)", fontSize: "var(--text-sm)" }}>
          The current key stays in the registry, superseded, and keeps reading every copy that records it. The new key will have no confirmed bundle.
        </p>
        <CommandEcho label="Equivalent command" commands={[keyCommand]} />
        {failureLine}
      </ConfirmationDialog>
    );
  }

  return (
    <ConfirmationDialog
      open
      eyebrow="Retained-copy encryption"
      title={title}
      confirmLabel="I have stored the bundle safely"
      disabled={busy || keyId === null}
      onCancel={cancel}
      onConfirm={() =>
        void run(async () => {
          if (keyId === null) return;
          await api.confirmRecoveryBundle(keyId);
          onChanged();
          onClose();
        }, "the bundle was not confirmed")
      }
    >
      <StepStrip now="confirm" />
      <p style={{ margin: 0 }}>
        Confirm once the downloaded file is somewhere this machine's loss would not take with it. It is a statement and not an upload: nothing is checked and no key bytes are asked for.
      </p>
      <CommandEcho label="Equivalent command" commands={[keyConfirmCommand(keyId ?? "<the new key>")]} />
      {failureLine}
    </ConfirmationDialog>
  );
}
