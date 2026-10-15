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
 * screens start on it where it can work. It is also the one recovery that
 * writes onto a production machine, so nothing is implied: the destination is
 * spelled out exactly as the engine will write it (its own url), the write
 * check runs only once the operator ticks "I understand this writes N onto
 * ..." or presses "Check write access", and on a profile that re-checks the
 * password (#1163) it asks for that too. The engine checks every one of these
 * again; the screen only refuses to send a request the engine would refuse.
 *
 * The files go into a new retnd-recovered-* folder under the set's own remote
 * path, which the next backup run skips. The engine writes under a temporary
 * name and renames it into place, so nothing already there is ever replaced.
 *
 * A set that does not pull over SFTP, a set declared read_only, a host a
 * recent write check found read-only, and a shut destructive gate all keep
 * the radio first, switched off, with RecoverySourceNote saying why in the
 * engine's words, and the screen starts on what it offered before #1191.
 * What the engine is asked and when is assurance/useSourceRecovery.ts.
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
import { StepUpPassword } from "@shared/components/StepUpFields";
import { bytes as formatBytes } from "@shared/utilities/format";
import { DestinationSelect } from "./DestinationSelect";
import { HostFolderBrowser } from "./HostFolderBrowser";
import type { SourceRecovery } from "./useSourceRecovery";

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
 *  set's source host cannot be written to from here, with RecoverySourceNote
 *  saying why. */
export function RecoverySourceChoice({
  name, value, onChoose, source, labelStyle
}: {
  name: string;
  value: string;
  onChoose(place: "source"): void;
  /** The set's source host, from useSourceRecovery. */
  source: SourceRecovery;
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

function sourceSelectable(source: SourceRecovery): boolean {
  return !!source.description?.available && source.unavailable === null;
}

/** Why the source host cannot be chosen here, where the screen shows notes.
 *  Nothing when it can, or when the engine does not offer it. */
export function RecoverySourceNote({ source }: { source: SourceRecovery }) {
  if (!source.offered || !source.unavailable) return null;
  return (
    <div data-source-host="" data-testid="source-unavailable" role="status" style={{ color: "var(--text-2)" }}>
      {source.unavailable}
    </div>
  );
}

/** The field for the chosen place: the source host's destination, check,
 *  tick and password; a name under the staging root with the host's folders
 *  to pick from; or the drop-down of Settings' destinations. */
export type RecoveryTargetFieldProps =
  | { place: "source"; source: SourceRecovery }
  | {
      place: "staging";
      value: string;
      onChange(value: string): void;
      /** One backup's own file name, added to a folder picked in the browser. */
      fileName?: string;
      /** What the name means on this screen, said under the field. */
      meaning?: string;
    }
  | { place: "storage"; value: string; onChange(value: string): void };

export function RecoveryTargetField(props: RecoveryTargetFieldProps) {
  if (props.place === "source") return <SourceHostField source={props.source} />;
  if (props.place === "storage") {
    return (
      <div data-testid="destination-field">
        <DestinationSelect ariaLabel={PLACE_LABELS.storage} value={props.value} onChange={props.onChange} />
      </div>
    );
  }
  return <StagingFolderField value={props.value} onChange={props.onChange} fileName={props.fileName} meaning={props.meaning} />;
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
function SourceHostField({ source }: { source: SourceRecovery }) {
  const d = source.description;
  if (!d) {
    return (
      <div data-source-host="" role="status" aria-busy="true" data-testid="source-preflight" data-write-access="" style={{ color: "var(--text-2)" }}>
        Asking the host where this set&apos;s source is...
      </div>
    );
  }
  const host = d.host ?? "the source host";
  const access = source.checking ? "checking" : d.writeAccess;
  const refused = access === "read_only" || access === "probe_left_file";
  const failed = access === "unreachable";
  const shut = source.gate.state === "shut";
  const status =
    access === "checking" ? "Checking that retnd may write on " + host + ": one probe file is written there and removed again..."
    : access === "not_checked" ? "Nothing has been written to " + host + " yet. retnd checks that it may write there when you tick the box below, by writing one probe file and removing it again."
    : access === "writable" ? "Write check: " + (d.detail ?? "retnd can write there") + ". It goes into a new folder, so nothing already on " + host + " is replaced."
    : access === "probe_left_file" ? (d.detail ?? "a probe file was left on " + host) + ". Delete " + (d.leftFile ?? "the probe file") + " on " + host + ", then check again."
    : (d.detail ?? "retnd could not check this host") + ".";
  const amount = source.bytes !== null ? formatBytes(source.bytes) + " " : "";
  return (
    <div data-source-host="" style={{ display: "flex", flexDirection: "column", gap: 8 }}>
      <div>
        <div style={{ color: "var(--text-2)", fontSize: "var(--text-sm)" }}>The backup goes back to the host it came from, at</div>
        <div className="mono" data-testid="source-destination" style={{ wordBreak: "break-all", color: "var(--text)" }}>{d.url ?? ""}</div>
      </div>
      <div
        data-testid="source-preflight"
        data-write-access={access}
        role={refused || failed ? "alert" : "status"}
        aria-busy={source.checking}
        className={refused || failed ? "aop__alert" : undefined}
        style={refused || failed ? undefined : { color: "var(--text-2)" }}
      >
        {refused ? <strong>This upload will be refused. </strong> : null}
        {status}
      </div>
      {access === "writable" ? (
        <div data-testid="source-space" style={{ color: "var(--text-2)" }}>
          {d.freeBytes !== undefined
            ? host + " has " + formatBytes(d.freeBytes) + " free under " + (d.remotePath ?? "its remote path") + "."
            : host + " did not say how much space is free, so retnd cannot check that this fits before it writes."}
        </div>
      ) : null}
      {refused ? null : (
        <Acknowledgement checked={source.acknowledged} onChange={source.setAcknowledged} disabled={shut}>
          {"I understand this writes " + amount + "onto " + host + ", into " + (d.url ?? "a new folder there")}
        </Acknowledgement>
      )}
      {(access === "not_checked" || failed) && !refused ? (
        <div>
          <button
            type="button"
            className="btn btn--sm"
            disabled={shut || source.checking}
            title={shut ? source.gate.reason || "the destructive gate is shut" : undefined}
            onClick={source.checkWriteAccess}
          >
            Check write access
          </button>
        </div>
      ) : null}
      {!refused && source.gate.stepUpRequired ? (
        <StepUpPassword key={source.passwordKey} act="source" id="source-step-up-help" onPassword={source.setPassword} />
      ) : null}
      {source.error ? <div role="alert" data-source-step-up-error="" style={{ color: "var(--danger)", fontSize: "var(--text-sm)", fontWeight: 600 }}>{source.error}</div> : null}
    </div>
  );
}
