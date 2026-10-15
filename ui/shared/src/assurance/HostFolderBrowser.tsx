/**
 * The folder browser behind "Staging path on the host" (issue #1179).
 *
 * # What it browses
 *
 * The folders below the staging root of the machine retnd RUNS ON, which is
 * not the machine this page is open on and is the only one a recovery to the
 * host can write to. The listing is the host's (GET /system/host-directories):
 * read-only, confined to that root, never through a symbolic link. This
 * component cannot ask about anywhere else, because every name it sends is
 * relative to the root and is built from names the host just listed.
 *
 * # What choosing does
 *
 * Browsing changes nothing. "Use this folder" hands the caller a name under
 * the root and the caller puts it in its text field, which stays: a person who
 * knows the name can still type it, and a host that cannot list its folders
 * (the alert below) leaves the field exactly as usable as it was.
 *
 * `fileName` is for a single backup. A single recovery's target is
 * `folder/name` (the last part is the file the host writes, with a prefix of
 * its own), so choosing a folder offers that folder plus the backup's own file
 * name. A set or a selection takes a folder, so it passes none.
 */
import { useEffect, useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { HostDirectory } from "@shared/api/contracts";
import { apiErrorOf } from "@shared/api/failure";

export function HostFolderBrowser({ onPick, fileName }: { onPick(name: string): void; fileName?: string }) {
  const api = useApi();
  const [at, setAt] = useState("");
  const [listing, setListing] = useState<HostDirectory | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    api.listHostDirectory(at).then(
      (d) => { if (live) { setFailure(null); setListing(d); } },
      (e) => {
        if (!live) return;
        setListing(null);
        const x = apiErrorOf(e);
        setFailure(x?.message ?? "the host did not answer");
      }
    );
    return () => { live = false; };
  }, [api, at]);

  // Moving into a folder moves focus to the line that says where you are, once
  // the folder's list is there, so a keyboard or screen-reader user hears the
  // new place instead of being left on a button that has just been replaced.
  // Only a move the person made does it: the first listing, which arrives
  // while they are somewhere else on the page, must not take focus.
  const heading = useRef<HTMLDivElement>(null);
  const moved = useRef(false);
  const go = (to: string) => { moved.current = true; setAt(to); };

  // The listing is only the answer for the folder being asked about once its
  // own path says so. Until then the previous folder's list is stale, and a
  // click on "Use this folder" made in that gap would choose the folder the
  // person has just left.
  const current = listing && listing.path === at ? listing : null;
  useEffect(() => {
    if (current && moved.current) { moved.current = false; heading.current?.focus(); }
  }, [current]);
  const here = at;
  // Up is the listing's own parent. When a folder could not be listed there is
  // no listing to ask, so it falls back to the name's own parent: the way back
  // out of a folder that failed must not depend on the folder working.
  const up: string | null = current ? current.parent : failure && at !== "" ? (at.includes("/") ? at.slice(0, at.lastIndexOf("/")) : "") : null;
  const pick = [here, fileName ?? ""].filter((x) => x !== "").join("/");
  const canPick = !!current && pick !== "";

  return (
    <div role="group" aria-label="Folders on the host" className="hostbrowse" data-testid="host-browser">
      <div className="hostbrowse__where">
        <button type="button" className="btn btn--sm" disabled={up === null} onClick={() => go(up ?? "")}>
          Up one folder
        </button>
        <div ref={heading} tabIndex={-1} className="hostbrowse__here">
          <span className="hostbrowse__label">On the host, under</span>{" "}
          <span className="mono" data-testid="browser-root">{listing?.root ?? "..."}</span>
          <span className="hostbrowse__label">{" folder "}</span>
          <span className="mono" data-testid="browser-path">{here === "" ? "(the root)" : here}</span>
        </div>
      </div>

      {failure ? (
        <div role="alert" className="hostbrowse__fail">
          <strong>The host cannot list its folders.</strong> {failure} You can still type a name below.
        </div>
      ) : current ? (
        current.entries.length === 0 ? (
          <div className="hostbrowse__empty">No folders in here.</div>
        ) : (
          <ul className="hostbrowse__list" aria-label={"Folders in " + (here === "" ? "the root" : here)}>
            {current.entries.map((e) => (
              <li key={e.path}>
                <button type="button" className="btn btn--sm hostbrowse__folder" aria-label={"Open folder " + e.name} onClick={() => go(e.path)}>
                  {e.name}
                </button>
              </li>
            ))}
          </ul>
        )
      ) : (
        <div role="status" className="hostbrowse__empty">Asking the host for its folders...</div>
      )}
      {current?.truncated ? (
        <div className="hostbrowse__empty" data-testid="browser-truncated">
          This folder holds more folders than are listed. Type the name below to reach one that is not shown.
        </div>
      ) : null}

      <div className="hostbrowse__use">
        <button type="button" className="btn btn--sm" disabled={!canPick} onClick={() => onPick(pick)}>Use this folder</button>
      </div>
    </div>
  );
}
