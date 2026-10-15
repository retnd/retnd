/**
 * The links "Download in this browser" makes for a selection (issue #1179,
 * reworked after the review of #1185; bulkDownload.ts has the reasons).
 *
 * One real link per backup, which the person opens. What it counts is what
 * this page saw, a link opened, and it never says a file arrived: a browser's
 * transfer is not visible to the page, and the host checks each file against
 * its recorded digest as it sends it, so a file that stops short is a failed
 * download. An opened link is spent (a grant is single use), so it is not
 * offered twice, and once the grants' time is up the links that were not
 * opened say so instead of failing when clicked. "Make fresh links" asks for
 * new grants for the same backups.
 */
import "./assurance.css";
import { useEffect, useState } from "react";
import type { DownloadLink, MintedDownloads } from "./bulkDownload";

export interface BulkDownloadState {
  /** True while the grants are being asked for. */
  running: boolean;
  minted: MintedDownloads | null;
  /** Why no links could be made at all, in words. */
  failure?: string | null;
}

function timeOf(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function BulkDownloadLinks({ state, onFresh }: { state: BulkDownloadState; onFresh(): void }) {
  // Which links were opened, by grant url. A fresh set of links has fresh
  // urls, so nothing carries over from the last set.
  const [opened, setOpened] = useState<ReadonlySet<string>>(new Set());
  const [now, setNow] = useState(() => Date.now());
  const links = state.minted?.links ?? [];

  // Wake once, when the first link that is still on offer runs out, so it
  // stops being offered as if it worked.
  const live = links.filter((l) => !opened.has(l.url) && Date.parse(l.expiresAt) > now);
  const nextExpiry = live.reduce((m, l) => Math.min(m, Date.parse(l.expiresAt)), Infinity);
  useEffect(() => {
    if (!Number.isFinite(nextExpiry)) return;
    const id = window.setTimeout(() => setNow(Date.now()), Math.max(0, nextExpiry - Date.now()) + 50);
    return () => window.clearTimeout(id);
  }, [nextExpiry]);

  if (!state.running && state.failure) {
    return (
      <div data-testid="bulk-download" className="anotice bulkdl" style={{ margin: "0 0 10px" }}>
        <div role="alert"><strong>No download links were made.</strong> {state.failure}</div>
        <div style={{ marginTop: 6 }}>
          <button type="button" className="btn btn--sm" onClick={onFresh}>Make fresh links</button>
        </div>
      </div>
    );
  }
  if (state.running || !state.minted) {
    return (
      <div data-testid="bulk-download" className="anotice bulkdl" style={{ margin: "0 0 10px" }}>
        <strong role="status">Asking the host for one download link per backup...</strong>
      </div>
    );
  }
  const m = state.minted;
  const count = m.links.filter((l) => opened.has(l.url)).length;
  const until = m.links.length > 0 ? timeOf(m.links.reduce((a, l) => (Date.parse(l.expiresAt) < Date.parse(a.expiresAt) ? l : a)).expiresAt) : "";
  const expired = (l: DownloadLink) => !opened.has(l.url) && Date.parse(l.expiresAt) <= now;

  return (
    <section aria-label="Download links" data-testid="bulk-download" className="anotice bulkdl" style={{ margin: "0 0 10px" }}>
      <strong role="status">{count + " of " + m.links.length + " opened"}</strong>
      {m.links.length > 0 ? (
        <>
          <div style={{ marginTop: 4 }}>
            Open each link to download that backup. The links work once and stop working at {until}; make fresh ones if they run out.
            This page cannot see your browser&apos;s transfers, so it cannot tell you a file arrived. The host checks each file against its recorded digest as it sends it, so a file that stops short is a failed download.
          </div>
          <ul className="bulkdl__list">
            {m.links.map((l) => (
              <li key={l.url}>
                {opened.has(l.url) ? (
                  <span><span className="mono">{l.filename}</span>: opened in your browser</span>
                ) : expired(l) ? (
                  <span><span className="mono">{l.filename}</span>: this link expired before it was opened</span>
                ) : (
                  <a
                    href={l.url}
                    download={l.filename}
                    rel="noopener"
                    className="bulkdl__link mono"
                    onClick={() => setOpened((s) => new Set(s).add(l.url))}
                  >
                    {"Download " + l.filename}
                  </a>
                )}
              </li>
            ))}
          </ul>
        </>
      ) : null}
      {m.skipped.length > 0 ? (
        <ul className="bulkdl__list">
          {m.skipped.map((x) => (
            <li key={x.id}>
              <span className="mono">{x.filename}</span>
              {" gets no link: " + x.reason + "."}
            </li>
          ))}
        </ul>
      ) : null}
      {m.links.length > 0 || m.skipped.length > 0 ? (
        <div style={{ marginTop: 6 }}>
          <button type="button" className="btn btn--sm" onClick={onFresh}>Make fresh links</button>
        </div>
      ) : null}
    </section>
  );
}
