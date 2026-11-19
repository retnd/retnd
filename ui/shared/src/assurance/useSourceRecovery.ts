/**
 * The one controller every recovery dialog uses for the "Source host (SFTP)"
 * destination (issue #1191, reworked by #1193's review). RecoveryTargetFields.tsx
 * draws it; this file decides what the engine is asked, when, and what a
 * dialog may do with the answers.
 *
 * # Describe on open, check on intent
 *
 * Opening a dialog asks the engine to DESCRIBE the source host: a GET built
 * from configuration alone, which never touches the production machine. The
 * write check is the call that writes (one probe file, removed again), so it
 * runs only when the operator says they mean it: ticking "I understand this
 * writes ..." or pressing "Check write access". It is never asked on a shut
 * gate, and its answer is kept for the dialog (the engine caches it for five
 * minutes too, and a recovery reuses it).
 *
 * # Consent names one destination
 *
 * The describe carries a destinationId, a digest of host, port, account,
 * remote path and folder. The tick sends that id back as
 * target.acknowledgedDestination, so the engine can refuse when what it would
 * write to is no longer what the operator saw.
 *
 * # The password is not kept
 *
 * It lives in a ref, leaves with exactly one request (beforeSubmit hands it
 * over and forgets it) and is never part of a request object a screen holds.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import { acknowledgeInsecureTransport } from "@shared/api/client";
import { apiErrorOf, describeFailure } from "@shared/api/failure";
import type { RecoveryTargetSpec, SourceHostDescription, StepUpOptions } from "@shared/api/contracts";
import { stepUpMessage } from "@shared/components/StepUpFields";
import { useDestructiveGate } from "@shared/hooks/useDestructiveGate";
import type { GateReading } from "@shared/hooks/useDestructiveGate";

export interface SourceRecovery {
  /** Whether the engine offers the destination at all. An engine older than
   *  #1191 answers the describe 404 or 405, and the dialogs then behave as
   *  they did before. */
  offered: boolean;
  /** False until the describe (and the gate) have answered. */
  ready: boolean;
  /** What the engine described, with the latest write check folded in. */
  description: SourceHostDescription | null;
  /** Why the source host cannot be chosen here at all, or null. */
  unavailable: string | null;
  /** Whether it should start out selected. */
  preselect: boolean;
  gate: GateReading;
  /** How many bytes the recovery would write, when the screen knows. */
  bytes: number | null;

  acknowledged: boolean;
  setAcknowledged(next: boolean): void;
  /** True while a write check is in flight. */
  checking: boolean;
  /** Runs the write check now ("Check write access"). */
  checkWriteAccess(): void;
  /** Whether a password has been typed; the password itself is in a ref. */
  hasPassword: boolean;
  setPassword(next: string): void;
  /** Changes whenever the password is forgotten, to clear the input. */
  passwordKey: string;
  /** The engine's refusal of the tick or the password, in words. */
  error: string | null;

  /** Everything the screen needs before Recover may be pressed. */
  canSubmit: boolean;
  /** The target to send, once canSubmit. */
  targetSpec: RecoveryTargetSpec | null;
  /** True between beforeSubmit and done: a second press is ignored. */
  submitting: boolean;
  /** Asks about an unencrypted connection BEFORE the password is sent, then
   *  hands the password over once and forgets it. Null means do not send. */
  beforeSubmit(): Promise<StepUpOptions | null>;
  /** The request has finished, either way. */
  done(): void;
  /** Words for a refusal this controller owns (the tick, the password, the
   *  connection), shown in its field. Null for any other code. */
  refusal(code: string | undefined): string | null;
}

/** The refusal codes the source-host field explains itself. */
export function sourceHostRefusal(code: string | undefined): string | null {
  if (code === "RECOVERY_SOURCE_HOST_NOT_ACKNOWLEDGED") {
    return "The destination changed after you ticked the box, or the box was not ticked. Nothing was written to the source host. Check where it goes and tick it again.";
  }
  return stepUpMessage(code, "source");
}

function setPartsOf(setId: string): [string, string] {
  const [source, ...rest] = setId.split("/");
  return [source, rest.join("/")];
}

/** A describe answered 404 without naming a set, or 405: this engine has no
 *  such route. A 404 BACKUP_SET_NOT_FOUND is an answer about the set. */
function notOffered(e: unknown): boolean {
  const a = apiErrorOf(e);
  if (!a) return false;
  return a.status === 405 || (a.status === 404 && a.code !== "BACKUP_SET_NOT_FOUND");
}

/**
 * `setId` null means the backups in question come from several sets (`spread`
 * of them), or none. `resetKey` forgets the tick, the password and the
 * answers when the thing being recovered changes under the same dialog.
 */
export function useSourceRecovery(
  setId: string | null,
  { resetKey = "", spread = 0, bytes = null }: { resetKey?: string; spread?: number; bytes?: number | null } = {}
): SourceRecovery {
  const api = useApi();
  const gate = useDestructiveGate();
  const key = (setId ?? "") + "\u0000" + resetKey;
  const [offered, setOffered] = useState(typeof api.describeSourceHost === "function");
  const [described, setDescribed] = useState<{ key: string; d: SourceHostDescription | null; failure: string | null } | null>(null);
  const [checkedFor, setChecked] = useState<{ key: string; d: SourceHostDescription } | null>(null);
  const [ackFor, setAckFor] = useState<string | null>(null);
  const [checkingFor, setCheckingFor] = useState<string | null>(null);
  const [errorFor, setErrorFor] = useState<{ key: string; message: string } | null>(null);
  const password = useRef("");
  const [hasPasswordFor, setHasPasswordFor] = useState<string | null>(null);
  const [forgotten, setForgotten] = useState(0);
  const submittingRef = useRef(false);
  const [submitting, setSubmitting] = useState(false);
  const checkAbort = useRef<AbortController | null>(null);

  // A different set or selection: nothing typed for the last one carries over
  // (the input remounts, keyed by `key`, and the ref is emptied here).
  useEffect(() => {
    password.current = "";
    checkAbort.current?.abort();
    checkAbort.current = null;
  }, [key]);

  // The describe: configuration only, on open, cancelled when the dialog goes.
  useEffect(() => {
    if (!offered || !setId) return;
    const abort = new AbortController();
    const [source, set] = setPartsOf(setId);
    api.describeSourceHost(source, set, { signal: abort.signal }).then(
      (d) => { if (!abort.signal.aborted) setDescribed({ key, d, failure: null }); },
      (e) => {
        if (abort.signal.aborted) return;
        if (notOffered(e)) { setOffered(false); return; }
        setDescribed({ key, d: null, failure: describeFailure(e, "The host could not say where this set's source is.").message });
      }
    );
    return () => abort.abort();
  }, [api, offered, setId, key]);

  useEffect(() => () => checkAbort.current?.abort(), []);

  const current = described && described.key === key ? described : null;
  const atOpen = current?.d ?? null;
  const checked = checkedFor && checkedFor.key === key ? checkedFor.d : null;
  const description = checked ?? atOpen;
  const acknowledged = ackFor === key;
  const checking = checkingFor === key;
  const error = errorFor && errorFor.key === key ? errorFor.message : null;
  const hasPassword = hasPasswordFor === key;

  let unavailable: string | null = null;
  if (offered && !setId && spread > 1) {
    unavailable = "These backups come from " + spread + " backup sets, and each set has its own source host, so one upload cannot send them all home. Tick the backups of one set to send them back.";
  } else if (current?.failure) {
    unavailable = current.failure;
  } else if (atOpen && !atOpen.available) {
    unavailable = "No source host for this set: " + (atOpen.reason ?? "the engine did not say why") + ".";
  } else if (atOpen && gate.state === "shut") {
    unavailable = "The web interface will not write onto the source host here: " + (gate.reason || "the destructive gate is shut on this deployment") + ". Pick another destination, or run retnd recover on the host.";
  } else if (atOpen && (atOpen.readOnly || atOpen.writeAccess === "read_only" || atOpen.writeAccess === "probe_left_file")) {
    // Known before this dialog opened, from configuration or a recent check:
    // the radio stays first, switched off, with the engine's reason.
    unavailable = "Not the source host here: " + (atOpen.detail ?? "retnd may not write there") +
      (atOpen.leftFile ? ". Delete " + atOpen.leftFile + " on " + (atOpen.host ?? "the host") + ", then try again" : "") + ".";
  }

  const ready = !offered || !setId ? true : current !== null && gate.state !== "unknown";
  const preselect = offered && !!atOpen?.available && unavailable === null;

  const runCheck = useCallback(() => {
    if (!setId || !atOpen?.available || gate.state !== "open" || atOpen.readOnly) return;
    checkAbort.current?.abort();
    const abort = new AbortController();
    checkAbort.current = abort;
    const at = key;
    setCheckingFor(at);
    const [source, set] = setPartsOf(setId);
    api.checkSourceHostWrite(source, set, { folder: atOpen.folder, signal: abort.signal }).then(
      (d) => { if (!abort.signal.aborted) { setChecked({ key: at, d }); setCheckingFor(null); } },
      (e) => {
        if (abort.signal.aborted) return;
        const f = describeFailure(e, "The write check could not be made.");
        setChecked({ key: at, d: { ...atOpen, writeAccess: "unreachable", detail: f.message, code: apiErrorOf(e)?.code } });
        setCheckingFor(null);
      }
    );
  }, [api, setId, atOpen, gate.state, key]);

  const setAcknowledged = (next: boolean) => {
    setAckFor(next ? key : null);
    // The tick is the operator's say-so: the write check runs once, now.
    if (next && !checking && (!description || description.writeAccess === "not_checked")) runCheck();
  };

  const stepUp = gate.stepUpRequired;
  const canSubmit = offered && !!description?.available && unavailable === null && description.writeAccess === "writable" &&
    acknowledged && !checking && (!stepUp || hasPassword) && !submitting;
  const targetSpec: RecoveryTargetSpec | null = description?.available
    ? { kind: "source", ...(description.folder ? { path: description.folder } : {}), ...(acknowledged && description.destinationId ? { acknowledgedDestination: description.destinationId } : {}) }
    : null;

  const forget = () => {
    password.current = "";
    setHasPasswordFor(null);
    setForgotten((n) => n + 1);
  };

  return {
    offered, ready, description, unavailable, preselect, gate, bytes,
    acknowledged, setAcknowledged, checking, checkWriteAccess: runCheck,
    hasPassword,
    setPassword: (next) => { password.current = next; setHasPasswordFor(next ? key : null); },
    passwordKey: key + "\u0000" + forgotten, error, canSubmit, targetSpec, submitting,
    beforeSubmit: async () => {
      if (submittingRef.current) return null;
      submittingRef.current = true;
      setSubmitting(true);
      setErrorFor(null);
      if (gate.stepUpRequired && gate.insecureTransportAckRequired && !(await acknowledgeInsecureTransport())) {
        setErrorFor({ key, message: sourceHostRefusal("INSECURE_TRANSPORT_ACK_REQUIRED") ?? "Nothing was sent." });
        submittingRef.current = false;
        setSubmitting(false);
        return null;
      }
      const opts: StepUpOptions = gate.stepUpRequired && password.current ? { stepUpPassword: password.current } : {};
      forget();
      return opts;
    },
    done: () => { submittingRef.current = false; setSubmitting(false); },
    refusal: (code) => {
      const message = sourceHostRefusal(code);
      if (message) {
        setErrorFor({ key, message });
        if (code === "RECOVERY_SOURCE_HOST_NOT_ACKNOWLEDGED") setAckFor(null);
      }
      return message;
    }
  };
}
