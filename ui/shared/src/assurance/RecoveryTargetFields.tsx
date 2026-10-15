/**
 * The places a recovery can go besides this browser, offered one way on every
 * screen that offers them (issue #1190): the backup's own source host (issue
 * #1191), a folder under the staging root on the host, and a configured
 * destination.
 *
 * # Why one module
 *
 * #1179 built the host folder browser and the destination drop-down, and the
 * three screens that offer these choices (Recover artifact, the Backups
 * list's selection bar, Recover this backup set) each wrapped them in their
 * own words: one radio said "Staging path on the host" and another "Folder
 * under the staging root on the host", and the text field and the drop-down
 * had a different name on each. The same choice read differently depending on
 * where it was made. Here the words, the field, its name and the staging root
 * it names live once, and src/test/shared-destination-pickers-1190.test.ts
 * fails if a screen spells either choice out for itself.
 *
 * # The source host (issue #1191)
 *
 * Sending a backup home is what someone recovering it usually wants, so the
 * screens start on it. It is also the one recovery that writes onto a
 * production machine, so nothing is implied: the destination is spelled out
 * (sftp://user@host/path/new-folder/), the host is asked before the run
 * whether the pull account may write there (the preflight), the act waits
 * for an "I understand" tick, and on a profile that re-checks the password
 * (#1163) it asks for that too. The engine checks every one of these again;
 * the screen only refuses to send a request the engine would refuse.
 *
 * The files go into a new retnd-recovered-<time> folder under the set's own
 * remote path, which the next backup run skips. The host refuses to replace a
 * file that is already there, so the screen never offers to.
 *
 * A set that does not pull over SFTP has nothing to send the backup back to,
 * and a destructive gate that is shut will not let the web interface write
 * there. In both cases the radio is drawn switched off, nothing is
 * preselected, and RecoverySourceNote says why in the engine's words. The
 * logic half (what the engine said, what may be preselected) is
 * sourceHostState.ts; everything drawn is here, and the same test fails if a
 * screen draws the source choice itself.
 *
 * # What a screen still decides
 *
 * Where the radios sit (each screen has its own radiogroup, and two of them
 * add "Download in this browser" beside these) and one sentence about what
 * the name means there. A single backup's name is folder/file, so the folder
 * browser hands it the folder plus the backup's own file name (`fileName`); a
 * set or a selection goes into a folder and passes none.
 */
import "./assurance.css";
import { useEffect, useState } from "react";
import type { CSSProperties } from "react";
import { useApi } from "@shared/api/ApiContext";
import { Acknowledgement } from "@shared/components/Acknowledgement";
import { DestinationSelect } from "./DestinationSelect";
import { HostFolderBrowser } from "./HostFolderBrowser";
import type { SourceHost } from "./sourceHostState";

export type RecoveryPlace = "source" | "staging" | "storage";

/** The words for each choice, the same on every screen. */
const PLACE_LABELS: Record<RecoveryPlace, string> = {
  source: "Source host (SFTP)",
  staging: "Folder under the staging root on the host",
  storage: "Configured destination"
};

/** The staging and destination radios. They go inside the caller's own
 *  radiogroup, which may hold "Download in this browser" and the source host
 *  (RecoverySourceChoice) as well. */
export function RecoveryTargetChoices({
  name, value, onChoose, labelStyle
}: {
  /** The radiogroup's `name`, shared with any other radio in it. */
  name: string;
  /** The chosen place, or anything else when neither of these is chosen. */
  value: string;
  onChoose(place: "staging" | "storage"): void;
  labelStyle?: CSSProperties;
}) {
  return (
    <>
      {(["staging", "storage"] as const).map((place) => (
        <label key={place} style={labelStyle}>
          <input type="radio" name={name} checked={value === place} onChange={() => onChoose(place)} />
          {PLACE_LABELS[place]}
        </label>
      ))}
    </>
  );
}

/** The source-host radio (issue #1191), first in the caller's radiogroup.
 *  Nothing when the engine does not offer it; drawn switched off when this
 *  set has no source host to write to, with RecoverySourceNote saying why. */
export function RecoverySourceChoice({
  name, value, onChoose, source, labelStyle
}: {
  name: string;
  value: string;
  onChoose(place: "source"): void;
  /** The set's source host, from useSourceHost. */
  source: SourceHost;
  labelStyle?: CSSProperties;
}) {
  if (!source.offered) return null;
  return (
    <label style={labelStyle}>
      <input type="radio" name={name} checked={value === "source"} disabled={!sourceSelectable(source)} onChange={() => onChoose("source")} />
      {PLACE_LABELS.source}
    </label>
  );
}

function sourceSelectable(source: SourceHost): boolean {
  return !!source.preflight?.available && source.unavailable === null;
}

/** Why the source host cannot be chosen here, where the screen shows notes.
 *  Nothing when it can, or when the engine does not offer it. */
export function RecoverySourceNote({ source }: { source: SourceHost }) {
  if (!source.offered || !source.unavailable) return null;
  return (
    <div data-source-host="" data-testid="source-unavailable" role="status" style={{ color: "var(--text-2)" }}>
      {source.unavailable}
    </div>
  );
}

/** What the source-host field needs from the screen that holds its state. */
export interface SourceHostFieldState {
  host: SourceHost;
  /** The folder the files go into; the preflight's own by default. */
  folder: string;
  acknowledged: boolean;
  onAcknowledged(next: boolean): void;
  password: string;
  onPassword(next: string): void;
  /** The engine's refusal of the tick or the password, in words. */
  error?: string | null;
}

/** The field for the chosen place: a name under the staging root with the
 *  host's folders to pick from, or the drop-down of Settings' destinations. */
export function RecoveryTargetField({
  place, value, onChange, fileName, meaning, source
}: {
  place: RecoveryPlace;
  value: string;
  onChange(value: string): void;
  /** One backup's own file name, added to a folder picked in the browser. */
  fileName?: string;
  /** What the name means on this screen, said under the field. */
  meaning?: string;
  /** The source host's state, which the "source" place draws. */
  source?: SourceHostFieldState;
}) {
  if (place === "source") return source ? <SourceHostField {...source} /> : null;
  if (place === "storage") {
    return (
      <div data-testid="destination-field">
        <DestinationSelect ariaLabel={PLACE_LABELS.storage} value={value} onChange={onChange} />
      </div>
    );
  }
  return <StagingFolderField value={value} onChange={onChange} fileName={fileName} meaning={meaning} />;
}

function StagingFolderField({ value, onChange, fileName, meaning }: { value: string; onChange(value: string): void; fileName?: string; meaning?: string }) {
  const api = useApi();
  // The root is the host's setting, read where it is shown, so no screen has
  // to fetch it and pass it down.
  const [root, setRoot] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    void api.getAssuranceSettings().then((s) => { if (live) setRoot(s.stagingRoot || null); }, () => undefined);
    return () => { live = false; };
  }, [api]);
  return (
    <div className="placefield" data-testid="staging-folder-field">
      <label className="field">
        <span className="field__label">{"Name under the staging root " + (root ? root + " " : "") + "(set by the host)"}</span>
        <input
          className="input input--mono input--bold"
          aria-label="Name under the staging root"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      </label>
      {meaning ? <div className="placefield__meaning">{meaning}</div> : null}
      <HostFolderBrowser fileName={fileName} onPick={onChange} />
    </div>
  );
}

/** What the source-host choice adds below the radios once it is chosen. */
function SourceHostField({ host, folder, acknowledged, onAcknowledged, password, onPassword, error }: SourceHostFieldState) {
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
      {error ? <div role="alert" data-source-step-up-error="" style={{ color: "var(--danger)", fontSize: "var(--text-sm)", fontWeight: 600 }}>{error}</div> : null}
    </div>
  );
}
