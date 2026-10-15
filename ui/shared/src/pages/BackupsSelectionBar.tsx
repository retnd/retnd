/**
 * The bar that appears once something on the Backups list is ticked
 * (issue #1107), and carries the one destination the whole selection goes to.
 *
 * # The browser download (issue #1179)
 *
 * "Download in this browser" used to be a radio that was always off, on the
 * reasoning that many files would need an archive the host's digest check does
 * not cover. That reasoning is right about an archive and wrong about the
 * radio: a selection can be downloaded the way one backup is, one grant and
 * one browser download per backup, each checked against its recorded digest as
 * the host sends it (assurance/bulkDownload.ts). So the radio is on, it asks
 * for no place, and it says plainly what it will do and what it leaves out.
 * It was never tied to the destructive gate or to a capability.
 *
 * # What the bar announces
 *
 * The count is a polite live region, so a screen-reader user hears
 * "3 selected" as they tick, without the focus moving.
 */
import "@shared/assurance/assurance.css";
import { useState } from "react";
import { SourceHostDetails, SourceHostUnavailable } from "@shared/assurance/SourceHost";
import { SOURCE_LABEL, sourceHostReady, useSourceHost } from "@shared/assurance/sourceHostState";
import { DestinationSelect } from "@shared/assurance/DestinationSelect";
import { HostFolderBrowser } from "@shared/assurance/HostFolderBrowser";
import { recoverCommand } from "@shared/assurance/assuranceCommands";
import { bytes } from "@shared/utilities/format";
import { planOf } from "@shared/assurance/copyPlan";
import { recoverManyCommand } from "@shared/assurance/assuranceCommands";
import type { RunTarget } from "@shared/assurance/recoverMany";
import type { BackupArtifact } from "@shared/types/backup";

export function BackupsSelectionBar({
  selected, matching, stagingRoot, readOnly, readOnlyReason, downloading, onSelectAll, onClear, onRecover, onDownload
}: {
  /** The ticked rows, in list order. */
  selected: BackupArtifact[];
  /** How many rows the current filter matches and can be selected. */
  matching: number;
  stagingRoot: string | null;
  readOnly: boolean;
  readOnlyReason?: string;
  /** True while the links are being handed to the browser one by one. */
  downloading: boolean;
  onSelectAll(): void;
  onClear(): void;
  onRecover(target: RunTarget, acknowledged: boolean): void;
  onDownload(): void;
}) {
  // Issue #1191: null until the source host has answered. It is the default
  // when every ticked backup comes from one SFTP set; otherwise nothing is
  // preselected and the reason is shown.
  const sets = [...new Set(selected.map((a) => a.setId))];
  const setsKey = sets.join(",");
  // A choice and a tick are remembered with the selection they were made for:
  // a selection that moves to another set, or to several, asks again.
  const [choice, setChoice] = useState<{ kind: RunTarget["kind"] | "download"; key: string } | null>(null);
  const [where, setWhere] = useState("");
  const [accepted, setAccepted] = useState(false);
  const [ack, setAck] = useState<string | null>(null);
  const [sourcePassword, setSourcePassword] = useState("");
  const sourceHost = useSourceHost(sets.length === 1 ? sets[0] : null, sets.length);
  const stillChosen = choice && (choice.kind !== "source" || choice.key === setsKey) ? choice.kind : null;
  const kind: RunTarget["kind"] | "download" | null = stillChosen ?? (
    !sourceHost.ready ? null : sourceHost.preselect ? "source" : sourceHost.offered ? null : "staging");
  const setKind = (k: RunTarget["kind"] | "download") => setChoice({ kind: k, key: setsKey });
  const sourceAck = ack === setsKey;
  const setSourceAck = (yes: boolean) => setAck(yes ? setsKey : null);
  const sourceFolder = sourceHost.preflight?.folder ?? "";
  const total = selected.reduce((n, a) => n + a.sizeBytes, 0);
  const archived = selected.filter((a) => planOf(a).archived).length;
  const download = kind === "download";
  // An archived copy is not downloaded (see bulkDownload.ts), so a download
  // has no cost to accept and asks for no box.
  const needsAck = archived > 0 && !download;
  const placed = kind === "source" ? sourceHostReady(sourceHost, sourceAck, sourcePassword) : kind !== null && where.trim() !== "";
  const ready = download ? !readOnly && !downloading : placed && (!needsAck || accepted) && !readOnly;
  const command = download
    ? selected.slice(0, 5).map((a) => recoverCommand(a.id, "download", "", false)).join("\n") +
      (selected.length > 5 ? "\n# and one more for each of the other " + (selected.length - 5) : "")
    : kind === null ? "retnd recover " + selected.map((a) => a.id).join(" ") + " --to <destination>"
    : recoverManyCommand(selected.map((a) => a.id), kind, kind === "source" ? sourceFolder : where.trim(), needsAck && accepted, sourceAck);

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
          {sourceHost.offered ? (
            <label>
              <input type="radio" name="bulk-target" checked={kind === "source"}
                disabled={!sourceHost.preflight?.available || sourceHost.unavailable !== null}
                onChange={() => { setKind("source"); setWhere(""); }} />
              {SOURCE_LABEL}
            </label>
          ) : null}
          <label>
            <input type="radio" name="bulk-target" checked={download} onChange={() => { setKind("download"); setWhere(""); }} aria-describedby="bulk-download-note" />
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
        {kind === "source" ? (
          <div className="selbar__where">
            <SourceHostDetails host={sourceHost} folder={sourceFolder} acknowledged={sourceAck} onAcknowledged={setSourceAck}
              password={sourcePassword} onPassword={setSourcePassword} />
          </div>
        ) : download || kind === null ? null : kind === "staging" ? (
          <div className="selbar__where">
            <label className="field">
              <span className="field__label">
                {"Name under the staging root " + (stagingRoot ? stagingRoot + " " : "") + "(files go under it, with a manifest)"}
              </span>
              <input
                className="input input--mono input--bold"
                aria-label="Recover to a name under the staging root"
                value={where}
                onChange={(e) => setWhere(e.target.value)}
              />
            </label>
            {/* Issue #1179: the host's own folders, so the name need not be typed blind. */}
            <HostFolderBrowser onPick={setWhere} />
          </div>
        ) : (
          <div className="selbar__where">
            <DestinationSelect ariaLabel="Recover to destination" value={where} onChange={setWhere} />
          </div>
        )}
      </div>

      {sourceHost.offered && sourceHost.unavailable ? (
        <div className="selbar__note"><SourceHostUnavailable reason={sourceHost.unavailable} /></div>
      ) : null}

      <div id="bulk-download-note" data-testid="bulk-download-note" className="selbar__note">
        {download
          ? "One download per backup: the host mints a single-use link for each and your browser follows it, so it may ask whether to allow several downloads. Each file is checked against its recorded digest as the host sends it. An archived copy is left out, because a link lasts minutes and a restore takes hours."
          : "Several backups can also be downloaded in this browser, one file each, without choosing a place."}
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
          <code className="mono selbar__cmd" data-testid="recover-command" style={{ whiteSpace: "pre-wrap" }}>{command}</code>
        </div>
        <button
          type="button"
          className="btn btn--primary"
          disabled={!ready}
          title={readOnly ? readOnlyReason : undefined}
          onClick={() => {
            if (kind === "download") { onDownload(); return; }
            if (kind === "source") {
              onRecover({ kind, where: sourceFolder, acknowledged: true, ...(sourceHost.gate.stepUpRequired ? { stepUpPassword: sourcePassword } : {}) }, needsAck && accepted);
              return;
            }
            if (kind) onRecover({ kind, where: where.trim() }, needsAck && accepted);
          }}
        >
          {download ? "Download selected" : "Recover selected"}
        </button>
      </div>
    </section>
  );
}
