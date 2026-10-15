/**
 * The places a recovery can go, offered one way on every screen that offers
 * them (issue #1190): this browser, the staging root on this retnd host, and
 * a configured destination.
 *
 * # Why one module
 *
 * #1179 built the host folder browser and the destination drop-down, and the
 * three screens that offer these choices (Recover artifact, the Backups
 * list's selection bar, Recover this backup set) each wrapped them in their
 * own words: one radio said "Staging path on the host" and another "Folder
 * under the staging root on the host", and the text field and the drop-down
 * had a different name on each. The same choice read differently depending on
 * where it was made. Here the radios, the field, its name and the staging root
 * it names live once. The words themselves are in recoveryPlaces.ts, so the
 * specs can read them without React.
 *
 * Two checks hold that. eslint.config.js lets only this file import the
 * folder browser and the drop-down, and
 * src/test/shared-destination-pickers-1190.test.ts fails if any other file
 * spells a label out. The state (what's chosen, the name, and clearing the
 * name when the choice changes) is useRecoveryTarget.ts.
 *
 * # What a screen still decides
 *
 * Where the radiogroup sits and what it's called, whether "Download in this
 * browser" is offered and when it's switched off, and one sentence about what
 * the name means there. A single backup's name is a file, so the folder
 * browser hands it the folder plus the backup's own file name (`fileName`); a
 * set or a selection goes into a folder and passes none.
 */
import "./assurance.css";
import { DestinationSelect } from "./DestinationSelect";
import { HostFolderBrowser } from "./HostFolderBrowser";
import { useAssuranceSettings } from "./assuranceSettings";
import { DESTINATION_SELECT_NAME, RECOVERY_PLACE_LABELS } from "./recoveryPlaces";
import type { RecoveryKind, RecoveryPlace } from "./recoveryPlaces";

/** The radios, inside the caller's own radiogroup. "Download in this
 *  browser" comes first, and only when the screen passes `download`. */
export function RecoveryTargetChoices<K extends RecoveryKind>({
  name, value, onChoose, download
}: {
  /** The radiogroup's `name`, shared with any other radio in it. */
  name: string;
  /** The chosen target, or null when none is. */
  value: K | null;
  onChoose(kind: K): void;
  /** Offer a browser download. Each screen has its own reason to switch it
   *  off, so it says when, and which note explains it. */
  download?: "download" extends K ? { disabled?: boolean; describedBy?: string } : never;
}) {
  return (
    <>
      {download ? (
        <label className="placechoice">
          <input
            type="radio" name={name} checked={value === "download"} disabled={download.disabled}
            aria-describedby={download.describedBy} onChange={() => onChoose("download" as K)}
          />
          Download in this browser
        </label>
      ) : null}
      {(["staging", "storage"] as const).map((place) => (
        <label key={place} className="placechoice">
          <input type="radio" name={name} checked={value === place} onChange={() => onChoose(place as K)} />
          {RECOVERY_PLACE_LABELS[place]}
        </label>
      ))}
    </>
  );
}

/** The field for the chosen place: a name under the staging root with the
 *  host's folders to pick from, or the drop-down of Settings' destinations. */
export function RecoveryTargetField({
  place, value, onChange, fileName, meaning
}: {
  place: RecoveryPlace;
  value: string;
  onChange(value: string): void;
  /** One backup's own file name, added to a folder picked in the browser. */
  fileName?: string;
  /** What the name means on this screen, said under the field. It starts
   *  "A file:" or "A folder:", since the staging label leaves that out. */
  meaning?: string;
}) {
  if (place === "storage") {
    return (
      <div data-testid="destination-field">
        <DestinationSelect ariaLabel={DESTINATION_SELECT_NAME} value={value} onChange={onChange} />
      </div>
    );
  }
  return <StagingFolderField value={value} onChange={onChange} fileName={fileName} meaning={meaning} />;
}

function StagingFolderField({ value, onChange, fileName, meaning }: { value: string; onChange(value: string): void; fileName?: string; meaning?: string }) {
  // The root is the host's setting, read where it is shown, so no screen has
  // to fetch it and pass it down. The read is shared (assuranceSettings.ts),
  // so switching the radio away and back doesn't fetch it again.
  const root = useAssuranceSettings()?.stagingRoot || null;
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
