/**
 * How the last browser download of an artifact ended (issue #985, item 14).
 *
 * The browser's own download manager is the progress and cancel surface, so
 * this shows only the end, read from the engine's feed: completed,
 * interrupted at N of M bytes, or failed verification. Each is drawn as
 * itself, in its own shape, and none wears the success tokens (a download the
 * engine finished sending is not a file the browser kept, and a recovery
 * claim is the Recovery chip's to make). With nothing on the feed for the
 * artifact the component draws nothing: the feed is a bounded tail the
 * serving process holds, so no row means "not known", never "completed".
 */
import "./assurance.css";
import { useEffect, useState } from "react";
import { useApi } from "@shared/api/ApiContext";
import type { SetActivityEvent } from "@shared/types/activity";
import { downloadOutcomeBadge, downloadOutcomeOf, downloadOutcomeText } from "./feedText";
import type { DownloadOutcome as Outcome } from "./feedText";

/** Never faster than this, whatever the service suggests. */
const MIN_POLL_MS = 2000;

export function DownloadOutcome({ artifactId }: { artifactId: string }) {
  const api = useApi();
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  useEffect(() => {
    let live = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      let next = 5000;
      try {
        const l = await api.getLiveActivity();
        const lines: SetActivityEvent[] = [...(l.deployment?.events ?? []), ...l.sets.flatMap((s) => s.events)];
        const found = downloadOutcomeOf(lines, artifactId);
        // Keep what was shown when a poll finds the ring has moved on: an
        // outcome that vanished from the tail is still the last thing known.
        if (live && found) setOutcome(found);
        next = Math.max(MIN_POLL_MS, l.pollAfterMs);
      } catch { /* the feed is not readable: say nothing rather than guess */ }
      if (live) timer = setTimeout(tick, next);
    };
    void tick();
    return () => { live = false; if (timer) clearTimeout(timer); };
  }, [api, artifactId]);
  if (!outcome) return null;
  return (
    <div className="card__body adl" style={{ paddingTop: 0 }} data-testid="download-outcome" data-outcome={outcome.kind} role="status">
      <span className="adl__lab">Last download</span>
      <span className="adl__badge" data-badge="">
        {downloadOutcomeBadge(outcome)}
      </span>
      <span className="adl__text">{downloadOutcomeText(outcome)}</span>
    </div>
  );
}
