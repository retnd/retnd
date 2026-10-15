/**
 * The password and the tick a restore asks for on a profile whose gate is
 * open for local accounts (issue #1163).
 *
 * A live session is enough to look at retnd and not enough to write a tree
 * into a directory, so the engine asks for the administrator's password again
 * and an explicit acknowledgement. This is that, drawn once. It renders only
 * where the engine said it will check them (the capability field), so a
 * deployment that asks for neither shows neither.
 *
 * The password lives in the caller's state for as long as the screen does,
 * is sent in a header and nowhere else, and is never written to storage.
 */
import { Acknowledgement } from "@shared/components/Acknowledgement";

export function StepUpFields({
  password,
  onPassword,
  acknowledged,
  onAcknowledged,
  acknowledgement,
  error
}: {
  password: string;
  onPassword(next: string): void;
  acknowledged: boolean;
  onAcknowledged(next: boolean): void;
  /** What the tick says the operator understands, specific to the act. */
  acknowledgement: string;
  /** The engine's refusal of the password or the tick, in words. */
  error: string | null;
}) {
  return (
    <div data-step-up style={{ marginTop: 16, display: "flex", flexDirection: "column", gap: 12, maxWidth: 520 }}>
      <div>
        <label className="field">
          <span className="field__label">Your password</span>
          <input
            className="input"
            type="password"
            autoComplete="current-password"
            aria-describedby="step-up-help"
            // The shared input border is a hairline (about 1.7:1 on the
            // card), which is fine beside a label in a form full of them and
            // not fine for the one field that takes a password. A text-2
            // border is 3:1 or better in both themes, measured in
            // e2e/1163-local-account-gate.spec.ts.
            style={{ borderColor: "var(--text-2)" }}
            value={password}
            onChange={(e) => onPassword(e.target.value)}
          />
        </label>
        <span id="step-up-help" style={{ display: "block", marginTop: 6, fontSize: "var(--text-xs)", color: "var(--text-2)" }}>
          {"A restore asks for your password again. It is sent once with this request and not kept."}
        </span>
      </div>
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

/** What each of the engine's step-up refusals says to the operator. Null
 *  for any other code: those are not this component's to explain. */
export function stepUpMessage(code: string | undefined): string | null {
  switch (code) {
    case "STEP_UP_FAILED":
      return "That password was not accepted. Nothing was restored. Check it and try again.";
    case "STEP_UP_REQUIRED":
      return "Enter your password to start the restore.";
    case "RATE_LIMITED":
      // Not a wrong password: another try now is refused unchecked and keeps
      // the limit tripped, so the advice is the opposite of the one above.
      return "Too many password attempts in the last minute, so this one was not checked. Nothing was restored. Wait a minute, then try again.";
    case "STEP_UP_UNAVAILABLE":
      return "This deployment cannot re-check your password, so it will not run a restore from the web interface. Run retnd snapshot restore on the host.";
    case "RESTORE_TARGET_NOT_ACKNOWLEDGED":
    case "RECOVERY_COST_NOT_ACKNOWLEDGED":
      return "Tick the box to say you understand, then start the restore.";
    case "INSECURE_TRANSPORT_ACK_REQUIRED":
      return "Nothing was sent. This connection is not encrypted, so your password goes only after you say to continue.";
    default:
      return null;
  }
}
