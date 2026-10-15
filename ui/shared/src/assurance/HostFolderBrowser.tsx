/**
 * The folder browser behind the staging folder choice (issue #1179). Screens
 * reach it through RecoveryTargetFields (issue #1190), never directly.
 *
 * # What it browses
 *
 * The folders below the staging root of the machine retnd RUNS ON, which is
 * not the machine this page is open on and is the only one a recovery to the
 * host can write to. The listing is the host's (GET /system/staging-directories):
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
 * # Where its answers go (the review of #1185)
 *
 * The listing is read through useAsync, so a refusal is the same typed error
 * every other read gets and an ended session takes the app to sign-in rather
 * than reading as a host that cannot list. A folder that is gone by the time
 * it is opened (removed between two clicks) is the host's 404,
 * DIRECTORY_NOT_FOUND, and the browser steps back up to the nearest folder
 * that is still there and says which one went.
 *
 * `fileName` is for a single backup. A single recovery's target is
 * `folder/name` (the last part is the file the host writes, with a prefix of
 * its own), so choosing a folder offers that folder plus the backup's own file
 * name. A set or a selection takes a folder, so it passes none.
 */
import { useEffect, useId, useRef, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { RetndApi, StagingDirectory } from "@shared/api/contracts";
import { asApiError } from "@shared/api/failure";
import { useAsync } from "@shared/hooks/useAsync";

const parentOf = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");

/** What one move asked for and what the host answered: the folder asked for,
 *  or the nearest one above it that is still there, with the name that went. */
interface Answer {
  asked: string;
  dir: StagingDirectory;
  gone: string | null;
}

async function listNearest(api: RetndApi, asked: string): Promise<Answer> {
  let at = asked;
  for (;;) {
    try {
      return { asked, dir: await api.listStagingDirectories(at), gone: at === asked ? null : asked };
    } catch (e) {
      if (at === "" || asApiError(e).code !== "DIRECTORY_NOT_FOUND") throw e;
      at = parentOf(at);
    }
  }
}

export function HostFolderBrowser({ onPick, fileName }: { onPick(name: string): void; fileName?: string }) {
  const api = useApi();
  const [at, setAt] = useState("");
  const listing = useAsync(() => listNearest(api, at), [api, at]);
  const pickId = useId();

  // Moving into a folder moves focus to the line that says where you are, once
  // the folder's list is there, so a keyboard or screen-reader user hears the
  // new place instead of being left on a button that has just been replaced.
  // Only a move the person made does it: the first listing, which arrives
  // while they are somewhere else on the page, must not take focus.
  const heading = useRef<HTMLDivElement>(null);
  const moved = useRef(false);
  const go = (to: string) => { moved.current = true; setAt(to); };

  // The listing is only the answer for the folder being asked about once it
  // says it answers that move. Until then the previous folder's list is stale
  // (useAsync keeps it while the next one loads), and a click on "Use this
  // folder" made in that gap would choose the folder the person has just left.
  const answer = !listing.loading && !listing.error && listing.data && listing.data.asked === at ? listing.data : null;
  const current = answer?.dir ?? null;
  const failure = !listing.loading && listing.error ? listing.error.message : null;
  useEffect(() => {
    if (current && moved.current) { moved.current = false; heading.current?.focus(); }
  }, [current]);
  // Where the browser is: the folder listed, which is the one above when the
  // one asked for has gone.
  const here = current ? current.path : at;
  // Up is the listing's own parent. When a folder could not be listed there is
  // no listing to ask, so it falls back to the name's own parent: the way back
  // out of a folder that failed must not depend on the folder working.
  const up: string | null = current ? current.parent : failure && at !== "" ? parentOf(at) : null;
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
          <span className="mono" data-testid="browser-root">{listing.data?.dir.root ?? "..."}</span>
          <span className="hostbrowse__label">{" folder "}</span>
          <span className="mono" data-testid="browser-path">{here === "" ? "(the root)" : here}</span>
        </div>
      </div>

      {answer?.gone ? (
        <div role="status" className="hostbrowse__empty" data-testid="browser-gone">
          <span className="mono">{answer.gone}</span> is no longer on the host, so this is the nearest folder above it.
        </div>
      ) : null}
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
        {canPick ? (
          <span id={pickId} className="hostbrowse__label hostbrowse__sets">
            Sets the name to <span className="mono" data-testid="browser-pick">{pick}</span>
          </span>
        ) : null}
        <button type="button" className="btn btn--sm" disabled={!canPick} aria-describedby={canPick ? pickId : undefined} onClick={() => onPick(pick)}>Use this folder</button>
      </div>
    </div>
  );
}
