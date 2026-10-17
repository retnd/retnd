/**
 * The password and the tick a gated act asks for on a profile whose gate is
 * open for local accounts (issue #1163): a restore, and since #1191 a
 * recovery onto the source host.
 *
 * A live session is enough to look at retnd and not enough to write a tree
 * into a directory or a file onto a production machine, so the engine asks
 * for the administrator's password again and an explicit acknowledgement.
 * This is that, drawn once and worded per act. It renders only where the
 * engine said it will check them (the capability field), so a deployment
 * that asks for neither shows neither.
 *
 * The password is sent in a header and nowhere else, and is never written to
 * storage. A restore keeps it in the page's state while the page is open; the
 * source host's controller (assurance/useSourceRecovery.ts) keeps it in a ref
 * and forgets it as soon as it is sent, which is why the input below can run
 * uncontrolled.
 */
import { Acknowledgement } from "@shared/components/Acknowledgement";

/** The act a password check is for, which decides the words. */
export type StepUpAct = "restore" | "source" | "validator";

const WORDS: Record<StepUpAct, { help: string; nothing: string; start: string; terminal: string }> = {
  restore: {
    help: "A restore asks for your password again. It is sent once with this request and not kept.",
    nothing: "Nothing was restored.",
    start: "Enter your password to start the restore.",
    terminal: "run a restore from the web interface. Run retnd snapshot restore on the host."
  },
  source: {
    help: "Writing onto the source host asks for your password again. It is sent once with this request and not kept.",
    nothing: "Nothing was written to the source host.",
    start: "Enter your password to write onto the source host.",
    terminal: "write onto the source host from the web interface. Run retnd recover --acknowledge-source-write on the host."
  },
  // #1203: an upload puts new code where the engine will run it, so it asks
  // the way a write onto the source host does.
  validator: {
    help: "Uploading a script asks for your password again. It is sent once with this request and not kept.",
    nothing: "No validator was uploaded or changed.",
    start: "Enter your password to upload the validator.",
    terminal: "upload a validator from the web interface. Run retnd assurance validator add on the host."
  }
};

/** The password field on its own, for an act that draws its own tick. */
export function StepUpPassword({
  act, id, password, onPassword
}: {
  act: StepUpAct;
  /** A page-unique id for the help text. */
  id: string;
  /** Controlled when given; otherwise the input keeps its own value and the
   *  caller hears each change (and remounts it, by key, to clear it). */
  password?: string;
  onPassword(next: string): void;
}) {
  return (
    <div>
      <label className="field">
        <span className="field__label">Your password</span>
        <input
          className="input"
          type="password"
          autoComplete="current-password"
          aria-describedby={id}
          // The shared input border is a hairline (about 1.7:1 on the
          // card), which is fine beside a label in a form full of them and
          // not fine for the one field that takes a password. A text-2
          // border is 3:1 or better in both themes, measured in
          // e2e/1163-local-account-gate.spec.ts.
          style={{ borderColor: "var(--text-2)" }}
          {...(password !== undefined ? { value: password } : {})}
          onChange={(e) => onPassword(e.target.value)}
        />
      </label>
      <span id={id} style={{ display: "block", marginTop: 6, fontSize: "var(--text-xs)", color: "var(--text-2)" }}>
        {WORDS[act].help}
      </span>
    </div>
  );
}

export function StepUpFields({
  password,
  onPassword,
  acknowledged,
  onAcknowledged,
  acknowledgement,
  error,
  act = "restore"
}: {
  password: string;
  onPassword(next: string): void;
  acknowledged: boolean;
  onAcknowledged(next: boolean): void;
  /** What the tick says the operator understands, specific to the act. */
  acknowledgement: string;
  /** The engine's refusal of the password or the tick, in words. */
  error: string | null;
  act?: StepUpAct;
}) {
  return (
    <div data-step-up style={{ marginTop: 16, display: "flex", flexDirection: "column", gap: 12, maxWidth: 520 }}>
      <StepUpPassword act={act} id="step-up-help" password={password} onPassword={onPassword} />
      <Acknowledgement checked={acknowledged} onChange={onAcknowledged}>
        {acknowledgement}
      </Acknowledgement>
      {error !== null ? (
        <div data-step-up-error role="alert" style={{ color: "var(--danger)", fontSize: "var(--text-sm)", fontWeight: 600 }}>
          {error}
        </div>
      ) : null}
    </div>
  );
}

/** What each of the engine's step-up refusals says to the operator, for the
 *  act it refused. Null for any other code: those are not this component's
 *  to explain. */
export function stepUpMessage(code: string | undefined, act: StepUpAct = "restore"): string | null {
  const w = WORDS[act];
  switch (code) {
    case "STEP_UP_FAILED":
      return "That password was not accepted. " + w.nothing + " Check it and try again.";
    case "STEP_UP_REQUIRED":
      return w.start;
    case "RATE_LIMITED":
      // Not a wrong password: another try now is refused unchecked and keeps
      // the limit tripped, so the advice is the opposite of the one above.
      return "Too many password attempts in the last minute, so this one was not checked. " + w.nothing + " Wait a minute, then try again.";
    case "STEP_UP_UNAVAILABLE":
      return "This deployment cannot re-check your password, so it will not " + w.terminal;
    case "RESTORE_TARGET_NOT_ACKNOWLEDGED":
    case "RECOVERY_COST_NOT_ACKNOWLEDGED":
      return act === "restore" ? "Tick the box to say you understand, then start the restore." : null;
    case "INSECURE_TRANSPORT_ACK_REQUIRED":
      return "Nothing was sent. This connection is not encrypted, so your password goes only after you say to continue.";
    default:
      return null;
  }
}
