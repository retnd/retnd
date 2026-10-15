/**
 * The two places a recovery can go besides this browser, offered one way on
 * every screen that offers them (issue #1190): a folder under the staging root
 * on the host, and a configured destination.
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
import { DestinationSelect } from "./DestinationSelect";
import { HostFolderBrowser } from "./HostFolderBrowser";

export type RecoveryPlace = "staging" | "storage";

/** The words for each choice, the same on every screen. */
const PLACE_LABELS: Record<RecoveryPlace, string> = {
  staging: "Folder under the staging root on the host",
  storage: "Configured destination"
};

/** The two radios. They go inside the caller's own radiogroup, which may hold
 *  a "Download in this browser" radio as well. */
export function RecoveryTargetChoices({
  name, value, onChoose, labelStyle
}: {
  /** The radiogroup's `name`, shared with any other radio in it. */
  name: string;
  /** The chosen place, or anything else when neither of these is chosen. */
  value: string;
  onChoose(place: RecoveryPlace): void;
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
  /** What the name means on this screen, said under the field. */
  meaning?: string;
}) {
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
