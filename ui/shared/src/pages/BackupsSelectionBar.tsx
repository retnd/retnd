/**
 * The bar that appears once something on the Backups list is ticked
 * (issue #1107), and carries the one destination the whole selection goes to.
 *
 * # Why there is no browser download here
 *
 * A single recovery can be a browser download because the browser follows one
 * link and the host checks each piece against the digest as it sends it. Many
 * files would need a streamed archive, which is a second format the digest
 * check does not cover, or many downloads a browser may throttle or refuse.
 * Neither is something this page can promise, so a selection is recovered to
 * the host (a name under the staging root) or to a configured destination, and
 * the bar says so rather than leaving a radio that quietly does something else.
 *
 * # What the bar announces
 *
 * The count is a polite live region, so a screen-reader user hears
 * "3 selected" as they tick, without the focus moving.
 */
import "@shared/assurance/assurance.css";
import { useState } from "react";
import { bytes } from "@shared/utilities/format";
import { planOf } from "@shared/assurance/copyPlan";
import { recoverManyCommand } from "@shared/assurance/assuranceCommands";
import type { RunTarget } from "@shared/assurance/recoverMany";
import type { BackupArtifact } from "@shared/types/backup";

export function BackupsSelectionBar({
  selected, matching, stagingRoot, readOnly, readOnlyReason, onSelectAll, onClear, onRecover
}: {
  /** The ticked rows, in list order. */
  selected: BackupArtifact[];
  /** How many rows the current filter matches and can be selected. */
  matching: number;
  stagingRoot: string | null;
  readOnly: boolean;
  readOnlyReason?: string;
  onSelectAll(): void;
  onClear(): void;
  onRecover(target: RunTarget, acknowledged: boolean): void;
}) {
  const [kind, setKind] = useState<RunTarget["kind"]>("staging");
  const [where, setWhere] = useState("");
  const [accepted, setAccepted] = useState(false);
  const total = selected.reduce((n, a) => n + a.sizeBytes, 0);
  const archived = selected.filter((a) => planOf(a).archived).length;
  const needsAck = archived > 0;
  const ready = where.trim() !== "" && (!needsAck || accepted) && !readOnly;
  const command = recoverManyCommand(selected.map((a) => a.id), kind, where.trim(), needsAck && accepted);

  return (
    <section
      aria-label="Selected backups"
      className="selbar"
      data-testid="selection-bar"
    >
      <div className="selbar__row">
        <div role="status" aria-live="polite" aria-atomic="true" data-testid="selection-count" className="selbar__count">
          <strong>{selected.length + " selected"}</strong>
          {" of " + matching + " matching · " + bytes(total)}
        </div>
        {selected.length < matching ? (
          <button type="button" className="btn btn--sm" onClick={onSelectAll}>{"Select all " + matching + " matching"}</button>
        ) : null}
        <button type="button" className="btn btn--sm" onClick={onClear}>Clear selection</button>
      </div>

      <div className="selbar__row" style={{ alignItems: "flex-start" }}>
        <div role="radiogroup" aria-label="Where should the backups go?" className="selbar__targets">
          <label>
            <input type="radio" name="bulk-target" disabled aria-describedby="no-bulk-download" />
            Download in this browser
          </label>
          <label>
            <input type="radio" name="bulk-target" checked={kind === "staging"} onChange={() => { setKind("staging"); setWhere(""); }} />
            Staging path on the host
          </label>
          <label>
            <input type="radio" name="bulk-target" checked={kind === "storage"} onChange={() => { setKind("storage"); setWhere(""); }} />
            Configured destination
          </label>
        </div>
        <label className="field selbar__where">
          <span className="field__label">
            {kind === "staging"
              ? "Name under the staging root " + (stagingRoot ? stagingRoot + " " : "") + "(files go under it by set, with a manifest)"
              : "Destination id"}
          </span>
          <input
            className="input input--mono input--bold"
            aria-label={kind === "staging" ? "Recover to a name under the staging root" : "Recover to destination id"}
            value={where}
            onChange={(e) => setWhere(e.target.value)}
          />
        </label>
      </div>

      <div id="no-bulk-download" data-testid="no-bulk-download" className="selbar__note">
        Several backups are recovered to the host, not downloaded one by one in a browser: choose a staging name or a configured destination. Download a single backup from its own page.
      </div>

      {needsAck ? (
        <div role="group" aria-label="Some selected copies are archived" className="anotice" data-testid="archived-decision">
          <strong>
            {archived + " of the " + selected.length + " selected " + (archived === 1 ? "is" : "are") + " in an archive class and must be restored by the provider before " + (archived === 1 ? "it" : "they") + " can be read."}
          </strong>
          <div style={{ marginTop: 4 }}>
            Each restore takes hours and the provider bills for it. retnd holds no price list, so it shows no amount. Nothing starts until you tick the box once for the whole selection.
          </div>
          <label style={{ display: "inline-flex", gap: 6, alignItems: "center", marginTop: 8 }}>
            <input type="checkbox" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />
            {"I accept that the provider bills for " + (archived === 1 ? "this restore" : "these restores")}
          </label>
        </div>
      ) : null}

      <div className="selbar__row" style={{ alignItems: "flex-end" }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="eyebrow" style={{ fontSize: 10, color: "var(--text-2)" }}>Same thing from the terminal</div>
          <code className="mono selbar__cmd" data-testid="recover-command">{command}</code>
        </div>
        <button
          type="button"
          className="btn btn--primary"
          disabled={!ready}
          title={readOnly ? readOnlyReason : undefined}
          onClick={() => onRecover({ kind, where: where.trim() }, needsAck && accepted)}
        >
          Recover selected
        </button>
      </div>
    </section>
  );
}
