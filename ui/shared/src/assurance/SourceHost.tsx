/**
 * The "Source host (SFTP)" recovery destination (issue #1191), drawn once for
 * every recovery dialog: the Recover artifact dialog (Backups list and backup
 * page), the Backups list's selection bar and Recover this backup set.
 *
 * # Why it is the default and still asks
 *
 * Sending a backup home is what someone recovering it usually wants, so the
 * dialogs start on it. It is also the one recovery that writes onto a
 * production machine, so nothing is implied: the destination is spelled out
 * (sftp://user@host/path/new-folder/), the host is asked before the run
 * whether the pull account may write there (the preflight), the act waits
 * for an "I understand" tick, and on a profile that re-checks the password
 * (#1163) it asks for that too. The engine checks every one of these again;
 * the screen only refuses to send a request the engine would refuse.
 *
 * # Never an overwrite
 *
 * The files go into a new retnd-recovered-<time> folder under the set's own
 * remote path, which the next backup run skips. The host refuses to replace a
 * file that is already there, so the screen never offers to.
 *
 * # When there is no source host
 *
 * A set that does not pull over SFTP has nothing to send the backup back to,
 * and a destructive gate that is shut will not let the web interface write
 * there. In both cases nothing is preselected and the reason is shown where
 * the destination would be, in the engine's words.
 */
import type { SourceHost } from "./sourceHostState";
import { Acknowledgement } from "@shared/components/Acknowledgement";

/** What the source-host choice adds below the radios once it is chosen. */
export function SourceHostDetails({
  host, folder, acknowledged, onAcknowledged, password, onPassword, error
}: {
  host: SourceHost;
  /** The folder the files go into; the preflight's own by default. */
  folder: string;
  acknowledged: boolean;
  onAcknowledged(next: boolean): void;
  password: string;
  onPassword(next: string): void;
  /** The engine's refusal of the tick or the password, in words. */
  error?: string | null;
}) {
  const p = host.preflight;
  if (!p) {
    return (
      <div data-source-host="" role="status" aria-busy="true" data-testid="source-preflight" data-writable="" style={{ color: "var(--text-2)" }}>
        Asking the source host whether retnd may write there...
      </div>
    );
  }
  const where = p.url ? p.url.replace(/[^/]+\/$/, folder + "/") : "";
  const refused = p.writable === "no";
  return (
    <div data-source-host="" style={{ display: "flex", flexDirection: "column", gap: 8 }}>
      <div>
        <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>The backup goes back to the host it came from, at</div>
        <div className="mono" data-testid="source-destination" style={{ wordBreak: "break-all", color: "var(--text)" }}>{where}</div>
      </div>
      <div
        data-testid="source-preflight"
        data-writable={p.writable ?? "unknown"}
        role={refused ? "alert" : "status"}
        className={refused ? "aop__alert" : undefined}
        style={refused ? undefined : { color: "var(--text-2)" }}
      >
        <strong>{refused ? "This upload will be refused. " : "Pre-flight: "}</strong>
        {(p.detail ? p.detail + ". " : "") + "It goes into a new folder, so nothing already on " + (p.host ?? "the host") + " is overwritten."}
      </div>
      {refused ? null : (
        <Acknowledgement checked={acknowledged} onChange={onAcknowledged}>
          {"I understand this writes onto " + (p.host ?? "the source host") + ", the machine this backup came from"}
        </Acknowledgement>
      )}
      {!refused && host.gate.stepUpRequired ? (
        <div>
          <label className="field">
            <span className="field__label">Your password</span>
            <input
              className="input"
              type="password"
              autoComplete="current-password"
              aria-describedby="source-step-up-help"
              // The same text-2 border the restore's password field uses
              // (StepUpFields), 3:1 or better in both themes.
              style={{ borderColor: "var(--text-2)" }}
              value={password}
              onChange={(e) => onPassword(e.target.value)}
            />
          </label>
          <span id="source-step-up-help" style={{ display: "block", marginTop: 6, fontSize: "var(--text-xs)", color: "var(--text-2)" }}>
            Writing onto the source host asks for your password again. It is sent once with this request and not kept.
          </span>
        </div>
      ) : null}
      {error ? <div role="alert" style={{ color: "var(--danger)", fontSize: "var(--text-sm)", fontWeight: 600 }}>{error}</div> : null}
    </div>
  );
}

/** Why there is no source host, where the destination would be. */
export function SourceHostUnavailable({ reason }: { reason: string }) {
  return (
    <div data-source-host="" data-testid="source-unavailable" role="status" style={{ color: "var(--text-2)" }}>
      {reason}
    </div>
  );
}

