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
 * The review of #1185 bounded it: the engine streams only so many downloads
 * at once, so the radio is offered for a selection no larger than that
 * ceiling (`downloadCeiling`, as the engine reports it) and switched off with
 * the reason above it. A selection that grows past it while the radio is
 * chosen falls back to the staging folder, which takes any number.
 *
 * # What the bar announces
 *
 * The count is a polite live region, so a screen-reader user hears
 * "3 selected" as they tick, without the focus moving.
 */
import "@shared/assurance/assurance.css";
import { useState } from "react";
import { RecoverySourceChoice, RecoverySourceNote, RecoveryTargetChoices, RecoveryTargetField } from "@shared/assurance/RecoveryTargetFields";
import { confirmSourceTransport, sourceHostReady, sourceHostRefusal, useSourceHost } from "@shared/assurance/sourceHostState";
import { recoverToFileCommand } from "@shared/assurance/assuranceCommands";
import { downloadable } from "@shared/assurance/bulkDownload";
import { bytes } from "@shared/utilities/format";
import { planOf } from "@shared/assurance/copyPlan";
import { recoverManyCommand } from "@shared/assurance/assuranceCommands";
import type { RunTarget } from "@shared/assurance/recoverMany";
import type { BackupArtifact } from "@shared/types/backup";

export function BackupsSelectionBar({
  selected, matching, readOnly, readOnlyReason, downloading, downloadCeiling, onSelectAll, onClear, onRecover, onDownload
}: {
  /** The ticked rows, in list order. */
  selected: BackupArtifact[];
  /** How many rows the current filter matches and can be selected. */
  matching: number;
  readOnly: boolean;
  readOnlyReason?: string;
  /** True while the download links are being made. */
  downloading: boolean;
  /** The most downloads the engine streams at once. */
  downloadCeiling: number;
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
  // Only the question this bar asks itself: the run's own refusals are shown
  // where the run is.
  const [sourceError, setSourceError] = useState<string | null>(null);
  const sourceHost = useSourceHost(sets.length === 1 ? sets[0] : null, sets.length);
  const stillChosen = choice && (choice.kind !== "source" || choice.key === setsKey) ? choice.kind : null;
  const chosen: RunTarget["kind"] | "download" | null = stillChosen ?? (
    !sourceHost.ready ? null : sourceHost.preselect ? "source" : sourceHost.offered ? null : "staging");
  const setKind = (k: RunTarget["kind"] | "download") => setChoice({ kind: k, key: setsKey });
  const sourceAck = ack === setsKey;
  const setSourceAck = (yes: boolean) => setAck(yes ? setsKey : null);
  const sourceFolder = sourceHost.preflight?.folder ?? "";
  const total = selected.reduce((n, a) => n + a.sizeBytes, 0);
  const archived = selected.filter((a) => planOf(a).archived).length;
  const files = downloadable(selected);
  // Past the ceiling the one download too many would fail, so the option is
  // off; a choice of it made before the selection grew falls back to the
  // staging folder rather than leaving nothing chosen.
  const tooMany = files.length > downloadCeiling;
  const kind = chosen === "download" && tooMany ? "staging" : chosen;
  const download = kind === "download";
  // An archived copy is not downloaded (see bulkDownload.ts), so a download
  // has no cost to accept and asks for no box.
  const needsAck = archived > 0 && !download;
  const placed = kind === "source" ? sourceHostReady(sourceHost, sourceAck, sourcePassword) : kind !== null && where.trim() !== "";
  const ready = download ? !readOnly && !downloading && files.length > 0 : placed && (!needsAck || accepted) && !readOnly;
  const command = download
    ? files.map((a) => recoverToFileCommand(a.id, a.filename)).join("\n")
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
          <RecoverySourceChoice name="bulk-target" value={kind ?? ""} source={sourceHost} onChoose={(t) => { setKind(t); setWhere(""); }} />
          <label>
            <input type="radio" name="bulk-target" checked={download} disabled={tooMany} onChange={() => { setKind("download"); setWhere(""); }} aria-describedby="bulk-download-note" />
            Download in this browser
          </label>
          {/* Issue #1190: offered the same way on every screen. */}
          <RecoveryTargetChoices name="bulk-target" value={kind ?? ""} onChoose={(t) => { setKind(t); setWhere(""); }} />
        </div>
        {download || kind === null ? null : (
          <div className="selbar__where">
            <RecoveryTargetField
              place={kind}
              value={where}
              onChange={setWhere}
              meaning="A folder: the backups go under it, with a manifest."
              source={{
                host: sourceHost, folder: sourceFolder, acknowledged: sourceAck, onAcknowledged: setSourceAck,
                password: sourcePassword, onPassword: setSourcePassword, error: sourceError
              }}
            />
          </div>
        )}
      </div>

      {sourceHost.offered && sourceHost.unavailable ? (
        <div className="selbar__note"><RecoverySourceNote source={sourceHost} /></div>
      ) : null}

      <div id="bulk-download-note" data-testid="bulk-download-note" className="selbar__note">
        {tooMany
          ? "Download in this browser takes at most " + downloadCeiling + " backups at a time, because the host streams " + downloadCeiling + " downloads at once and the next one would fail. This selection has " + files.length + ": recover it to a folder under the staging root or a configured destination, or tick fewer."
          : download
          ? "One link per backup: the host makes a single-use link for each, and you open each one, so every download is one you asked for. Each file is checked against its recorded digest as the host sends it. An archived copy is left out, because a link lasts minutes and a restore takes hours."
          : "Up to " + downloadCeiling + " backups can also be downloaded in this browser, one link each, without choosing a place."}
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
              // On an unencrypted connection the operator is asked BEFORE the
              // password is sent, as for a restore (#1163).
              setSourceError(null);
              void confirmSourceTransport(sourceHost.gate).then((ok) => {
                if (!ok) { setSourceError(sourceHostRefusal("INSECURE_TRANSPORT_ACK_REQUIRED")); return; }
                onRecover({ kind: "source", where: sourceFolder, acknowledged: true, ...(sourceHost.gate.stepUpRequired ? { stepUpPassword: sourcePassword } : {}) }, needsAck && accepted);
              });
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
