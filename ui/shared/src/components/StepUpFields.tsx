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
    case "STEP_UP_UNAVAILABLE":
      return "This deployment cannot re-check your password, so it will not run a restore from the web interface. Run retnd snapshot restore on the host.";
    case "ACKNOWLEDGEMENT_REQUIRED":
      return "Tick the box to say you understand, then start the restore.";
    default:
      return null;
  }
}
