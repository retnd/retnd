/**
 * Every retained artifact, filterable by set, with retention preview
 * reachable per set.
 *
 * The name is load-bearing. These are backups, not restore points: this
 * product transfers and verifies artifacts and never performs an
 * application restore, and calling a row a restore point would promise
 * something no code here does.
 *
 * One row is not like the others, and the page has to say so. A backup
 * whose set was removed stays on storage and stays listed, which is what
 * the removal dialog promises, but it left every retention chain on the
 * way out: nothing selects it, nothing expires it, and nothing here will
 * ever delete it. Rendered plainly beside the governed rows it reads as an
 * ordinary healthy backup, and the disk fills quietly. So the Retention
 * cell says the consequence for those rows and the list carries the same
 * footnote `retnd artifacts` prints under its own (issue #523).
 *
 * The set list behind the filter is the shared node rather than another
 * fetch, so the dropdown cannot offer a set the rest of the app has
 * forgotten. The preview dialog needs a set's two-part identity while the
 * dropdown is keyed by the flat id, and resolving one from the other here
 * rather than threading both through the select is the small price of
 * keeping every picker on this page keyed the same way.
 */
import { useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useApi } from "@shared/api/ApiContext";
import { useAsync } from "@shared/hooks/useAsync";
import { useCausl } from "@shared/state/graph";
import { setsNode } from "@shared/state/appNodes";
import { PageHeader } from "@shared/components/PageHeader";
import { FieldHelp } from "@shared/components/FieldHelp";
import { FIELD_HELP } from "@shared/components/fieldHelpCopy";
import { RetentionBadges, RetentionPolicyBadge } from "@shared/components/RetentionBadge";
import { StatusBadge } from "@shared/components/StatusBadge";
import { Icon } from "@shared/design-system/icons";
import { EmptyState, ErrorState } from "@shared/components/EmptyState";
import { isNotConfigured } from "@shared/api/failure";
import { ClaimChips } from "@shared/assurance/ClaimChips";
import { BUCKETS } from "@shared/assurance/claims";
import type { Bucket } from "@shared/assurance/claims";
import { bucketPhrase } from "@shared/assurance/buckets";
import { useFactsIndex, useTally } from "@shared/assurance/verificationModel";
import { RetentionPreviewDialog } from "./RetentionPreviewDialog";
import { bytes, stamp } from "@shared/utilities/format";
import { useHoverTitle } from "@shared/hooks/useTooltips";
import { InfoTooltip } from "@shared/tooltips/InfoTooltip";
import { artifactPath } from "@shared/utilities/routes";
import type { BackupArtifact } from "@shared/types/backup";
import type { RunTarget } from "@shared/assurance/recoverMany";
import { BackupsSelectionBar } from "./BackupsSelectionBar";
import { DEFAULT_DOWNLOAD_STREAMS, downloadable, downloadCeilingOf, mintDownloadLinks } from "@shared/assurance/bulkDownload";
import { BulkDownloadLinks } from "@shared/assurance/BulkDownloadLinks";
import { useAssuranceSettings } from "@shared/assurance/assuranceSettings";
import type { BulkDownloadState } from "@shared/assurance/BulkDownloadLinks";
import { asApiError } from "@shared/api/failure";
import { BackupsRecoverRun } from "./BackupsRecoverRun";
import type { StepUpOptions } from "@shared/api/contracts";
import { inReceivedRange, unselectableReason } from "./backupSelection";
import { PAGE_SIZES, allOffered, isPageSize, pageWindow, readPageSize, rememberPageSize } from "./backupsPaging";
import type { PageChoice, PageSize } from "./backupsPaging";

/** Called "Backups", never "Restore points" — the product does not perform
 *  application restore (§13). */
export function BackupsPage({ readOnly }: { readOnly: boolean }) {
  const api = useApi();
  const navigate = useNavigate();
  const [setFilter, setSetFilter] = useState("");
  const [previewFor, setPreviewFor] = useState<string | null>(null);
  // Issue #1107. Received-on-or-after / before bounds, as yyyy-mm-dd.
  const [after, setAfter] = useState("");
  const [before, setBefore] = useState("");
  // The ticked backups, by id. Every filter change CLEARS it (see
  // `changeFilter`): the list is refetched per filter, so a selected row the
  // filter has hidden is one this page can no longer name or size, and a bar
  // that counted it would be describing rows nobody can see.
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set());
  const [notice, setNotice] = useState("");
  // Issue #1180. The remembered 20, 50 or 100; "All" is a choice for this visit
  // and is held apart from it so it can never be the one that is remembered.
  const [remembered, setRemembered] = useState<PageSize>(readPageSize);
  const [choice, setChoice] = useState<PageChoice>(remembered);
  const [pageAt, setPageAt] = useState(0);
  // How many backups the deployment holds, read off the unfiltered list. A set
  // filter narrows the list but not the deployment, so "All" stays as offered.
  const [held, setHeld] = useState<number | null>(null);
  const [run, setRun] = useState<{ files: Array<{ id: string; filename: string }>; target: RunTarget; acknowledged: boolean } | null>(null);
  // Issue #1179 (the review of #1185): how many downloads the engine streams
  // at once. "In this browser" offers a selection no larger than this.
  // The links a browser download made for the selection, or null before one
  // was asked for, with the backups they were made for so fresh ones can be.
  const [dl, setDl] = useState<(BulkDownloadState & { files: BackupArtifact[] }) | null>(null);
  // One run at a time (a double click must not mint twice), and a run this
  // page has left stops asking the host for grants.
  // #1193's review: the password a run onto the source host carries lives here
  // only between the bar's press and the run's one request, in a ref, and a
  // refusal of the tick or the password goes back to the bar's own field.
  const runOnce = useRef<StepUpOptions>({});
  const [sourceRefusal, setSourceRefusal] = useState<{ code: string; seq: number } | null>(null);
  const dlBusy = useRef(false);
  const dlRun = useRef<AbortController | null>(null);
  useEffect(() => {
    const runs = dlRun;
    return () => runs.current?.abort();
  }, []);
  const startDownload = async (files: BackupArtifact[]) => {
    if (dlBusy.current || downloadable(files).length > downloadCeiling) return;
    dlBusy.current = true;
    const ac = new AbortController();
    dlRun.current = ac;
    setDl({ running: true, minted: null, failure: null, files });
    try {
      const minted = await mintDownloadLinks(api, files, ac.signal);
      if (!ac.signal.aborted) setDl({ running: false, minted, failure: null, files });
    } catch (e) {
      if (!ac.signal.aborted) setDl({ running: false, minted: null, failure: asApiError(e).message, files });
    } finally {
      dlBusy.current = false;
      if (!ac.signal.aborted) setDl((d) => (d && d.running ? { ...d, running: false } : d));
    }
  };
  const canRecover = !!api.assurance;
  // The page reads the settings only for the download ceiling. The read is
  // the one the staging folder field shares (assuranceSettings.ts, #1190).
  const assuranceSettings = useAssuranceSettings(canRecover);
  const downloadCeiling = assuranceSettings ? downloadCeilingOf(assuranceSettings) : DEFAULT_DOWNLOAD_STREAMS;
  const changeFilter = (apply: () => void) => {
    apply();
    setPageAt(0);
    setNotice(picked.size > 0 ? "The selection was cleared because the filter changed." : "");
    setPicked(new Set());
  };
  // #829: this button's hover copy says why it is disabled, and it is a
  // tooltip like any other — an operator who turned them off gets the
  // disabled state and no pop-up, which is what they asked for.
  const hoverTitle = useHoverTitle();

  // The shared sets node (App.tsx fetches it once, #106) — not this page's
  // own listSets() call. App.tsx always mounts above every route, so the
  // node is already populated by the time this page can render.
  const sets = useCausl(setsNode);
  const artifacts = useAsync(() => api.listArtifacts(setFilter || undefined), [api, setFilter]);
  // Adjusted while rendering rather than in an effect: it is derived from the
  // list and has nothing outside React to synchronise with.
  const unfilteredCount = !setFilter && artifacts.data ? artifacts.data.length : null;
  if (unfilteredCount !== null && unfilteredCount !== held) setHeld(unfilteredCount);
  // EPIC Q (issue #985): the three assurance claims per backup, read from the
  // verification model (issue #1134) like every other surface that shows them,
  // so this list, the backup's own page and the Dashboard count cannot say
  // different things. A server that does not offer them yields no facts and
  // every row renders `unknown`; a read that fails does the same, because
  // "could not ask" is not an answer to draw as one.
  const facts = useFactsIndex();
  const tally = useTally();
  // The Dashboard's counts link here as ?assurance=<bucket>, and the filter
  // is the same definition the count is made of (claims.bucketsOf).
  const [params, setParams] = useSearchParams();
  const asked = params.get("assurance");
  const bucket: Bucket | null = (BUCKETS as readonly string[]).includes(asked ?? "") ? (asked as Bucket) : null;
  // previewFor is the flat BackupSet.id the <select> below is keyed by
  // (matching every other set-picker in this file); RetentionPreviewDialog
  // itself takes source/set (BackupSetID's own two-part identity — see
  // BackupSet.source/set's own doc), so this resolves the one from the
  // other.
  const previewSet = previewFor ? (sets.data ?? []).find((s) => s.id === previewFor) : null;

  // #275: nothing to list, and nothing wrong either.
  if (isNotConfigured(artifacts.error))
    return (
      <>
        <PageHeader title="Backups" subtitle="Nothing retained yet" tip="nav.backups" />
        <EmptyState title="No backups yet">
          Backups appear here once a backup set exists and has run. This instance has no
          configuration yet, so nothing has run.
        </EmptyState>
      </>
    );

  if (artifacts.error) return <ErrorState {...artifacts.error} onRetry={artifacts.reload} />;

  const fetched = artifacts.data ?? [];
  const inRange = fetched.filter((a) => inReceivedRange(a, after, before));
  // Narrowed to one assurance bucket only once the model has read the claims:
  // an unread model is not "none of them", and a list that emptied while it
  // loaded would flash "No backups yet".
  const bucketIds = bucket && tally.known ? new Set(tally.ids[bucket]) : null;
  const rows = bucketIds ? inRange.filter((a) => bucketIds.has(a.id)) : inRange;
  // Select-all means "everything the filter matches that can be recovered",
  // and nothing else: the ticked set is always read back through `rows`.
  const selectable = rows.filter((a) => unselectableReason(a) === null);
  const ticked = selectable.filter((a) => picked.has(a.id));
  const toggle = (id: string, on: boolean) => {
    setNotice("");
    setPicked((cur) => { const n = new Set(cur); if (on) n.add(id); else n.delete(id); return n; });
  };
  const allTicked = selectable.length > 0 && ticked.length === selectable.length;
  const selectAll = () => { setNotice(""); setPicked(new Set(selectable.map((a) => a.id))); };
  // One page of the filtered list. Select-all above reads `rows`, never this.
  const offerAll = allOffered(held ?? fetched.length);
  const effective: PageChoice = choice === "all" && !offerAll ? remembered : choice;
  const win = pageWindow(rows.length, effective, pageAt);
  const shown = rows.slice(win.start, win.end);
  const pickSize = (v: string) => {
    setPageAt(0);
    if (v === "all") { setChoice("all"); return; }
    const n = Number(v);
    if (!isPageSize(n)) return;
    setChoice(n); setRemembered(n); rememberPageSize(n);
  };
  const totalBytes = rows.reduce((n, a) => n + a.sizeBytes, 0);
  // The Medium column exists because a backup on this deployment has a
  // copy somewhere other than the local backup root, and for no other
  // reason. FR-35: a configuration that names no storage medium gets the
  // page it already had, with the same columns and the same widths, and
  // nothing new to read past.
  const showsMedium = rows.some((a) => a.placements.some((p) => p.medium !== "local"));
  // Counted off the rows LISTED, so the filter dropdown narrowing the table
  // to a governed set takes the footnote with it. When the list runs over
  // more than one page the note says both numbers, this page's and the
  // list's, so page two never reads as if there were none (#1180).
  const paged = shown.length < rows.length;
  const ungoverned = { page: shown.filter((a) => a.retentionPolicy === "none").length, all: rows.filter((a) => a.retentionPolicy === "none").length };
  const unreported = { page: shown.filter((a) => a.retentionPolicy === "unknown").length, all: rows.filter((a) => a.retentionPolicy === "unknown").length };

  return (
    <>
      <PageHeader
        title="Backups"
        tip="nav.backups"
        subtitle={
          <InfoTooltip id="backups.summary">
            <span>{rows.length + " retained artifacts \u00b7 " + bytes(totalBytes)}</span>
          </InfoTooltip>
        }
        actions={
          <>
            <FieldHelp label="Filter by backup set" help={FIELD_HELP.backupsSetFilter}>
              {(helpId) => (
                <select
                  className="select"
                  style={{ height: 32 }}
                  aria-label="Filter by backup set"
                  aria-describedby={helpId}
                  value={setFilter}
                  onChange={(e) => changeFilter(() => setSetFilter(e.target.value))}
                >
                  <option value="">All backup sets</option>
                  {(sets.data ?? []).map((s) => (
                    <option key={s.id} value={s.id}>{s.name}</option>
                  ))}
                </select>
              )}
            </FieldHelp>
            <label style={{ display: "inline-flex", gap: 6, alignItems: "center", fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
              After
              <input
                type="date" className="input input--bold" style={{ height: 32 }}
                aria-label="Received on or after" value={after}
                onChange={(e) => changeFilter(() => setAfter(e.target.value))}
              />
            </label>
            <label style={{ display: "inline-flex", gap: 6, alignItems: "center", fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
              Before
              <input
                type="date" className="input input--bold" style={{ height: 32 }}
                aria-label="Received on or before" value={before}
                onChange={(e) => changeFilter(() => setBefore(e.target.value))}
              />
            </label>
            <button
              className="btn"
              disabled={readOnly || !setFilter}
              title={hoverTitle(setFilter ? undefined : "Choose a backup set to preview its retention plan")}
              onClick={() => setPreviewFor(setFilter)}
            >
              Preview retention
            </button>
          </>
        }
      />

      {/*
        artifacts.data is null while still loading and [] once genuinely
        empty — those are different states and must not be conflated (the
        identical bug BackupSetsPage fixed for `sets` in #141). Rendering
        nothing here until loading actually finishes also keeps the table
        (with its header row) and its data rows appearing atomically,
        instead of a header-only table flashing on screen first.

        Also gate on `artifacts.loading`, not only `!artifacts.data`
        (mandatory review on #144): useAsync resets loading/error on every
        reload triggered by the filter dropdown changing, but never resets
        data back to null, so without this the table kept showing the
        PREVIOUS filter's rows — fully clickable, navigating to the wrong
        artifact — until the new filter's fetch resolved.
      */}
      {/* A polite note, outside the table, for the one thing a filter does to a
          selection: it empties it. */}
      <div role="status" data-testid="selection-notice" style={notice ? { fontSize: "var(--text-sm)", color: "var(--text-2)", margin: "0 0 10px" } : { position: "absolute", width: 1, height: 1, overflow: "hidden" }}>
        {notice}
      </div>
      {canRecover && ticked.length > 0 && !artifacts.loading ? (
        <BackupsSelectionBar
          selected={ticked}
          matching={selectable.length}
          readOnly={readOnly}
          readOnlyReason="This session is read-only, so it cannot start a recovery."
          downloading={!!dl?.running}
          downloadCeiling={downloadCeiling}
          onDownload={() => void startDownload(ticked)}
          onSelectAll={selectAll}
          onClear={() => setPicked(new Set())}
          sourceRefusal={sourceRefusal}
          onRecover={(target, acknowledged, once) => {
            runOnce.current = once;
            setRun({ files: ticked.map((a) => ({ id: a.id, filename: a.filename })), target, acknowledged });
          }}
        />
      ) : null}
      {dl ? <BulkDownloadLinks state={dl} onFresh={() => void startDownload(dl.files)} /> : null}
      {bucketIds && !artifacts.loading ? (
        <div style={{ display: "flex", alignItems: "center", gap: 10, margin: "0 0 10px", fontSize: "var(--text-sm)", color: "var(--text-2)" }}>
          <span role="status" data-testid="assurance-filter-status">
            {"Showing " + rows.length + " of " + inRange.length + " backups " + bucketPhrase(bucket as Bucket)}
          </span>
          <button className="btn btn--sm" onClick={() => changeFilter(() => setParams((p) => { const n = new URLSearchParams(p); n.delete("assurance"); return n; }))}>
            Show all backups
          </button>
        </div>
      ) : null}
      {!artifacts.data || artifacts.loading ? null : rows.length === 0 && fetched.length > 0 ? (
        <EmptyState title="No backups in that range">
          Nothing was received between those dates. Clear a date to widen the list.
        </EmptyState>
      ) : rows.length === 0 ? (
        <EmptyState title="No backups yet">
          This backup set has not completed its first successful ingestion.
        </EmptyState>
      ) : (
        <div className="card">
          {/* A small app window must never hide a column (§31). */}
          <div className="table-scroll">
            <table className="table" style={{ minWidth: 1020 }}>
              <caption className="eyebrow" style={{ textAlign: "left", padding: "12px 16px", borderBottom: "1px solid var(--border)" }}>
                <InfoTooltip id="backups.table"><span>Retained backups</span></InfoTooltip>
              </caption>
              <thead>
                {/* The columns carry the explanations for the cells below
                    them: a row is a link to the artifact's own page, so a
                    pop-up inside one would be a pop-up whose own click
                    navigates away from the thing it is explaining. */}
                <tr>
                  {canRecover ? (
                    <th scope="col" className="selcell">
                      <input
                        type="checkbox"
                        aria-label="Select all matching backups"
                        aria-checked={allTicked ? true : ticked.length > 0 ? "mixed" : false}
                        checked={allTicked}
                        disabled={selectable.length === 0}
                        ref={(el) => { if (el) el.indeterminate = ticked.length > 0 && !allTicked; }}
                        onChange={() => (allTicked ? setPicked(new Set()) : selectAll())}
                      />
                    </th>
                  ) : null}
                  <th scope="col"><InfoTooltip id="backups.col.time"><span>Time</span></InfoTooltip></th>
                  <th scope="col"><InfoTooltip id="backups.col.set"><span>Backup set</span></InfoTooltip></th>
                  <th scope="col"><InfoTooltip id="backups.col.artifact"><span>Artifact</span></InfoTooltip></th>
                  <th scope="col" style={{ textAlign: "right" }}><InfoTooltip id="backups.col.size"><span>Size</span></InfoTooltip></th>
                  <th scope="col"><InfoTooltip id="assurance.col.claims"><span>Assurance</span></InfoTooltip></th>
                  <th scope="col"><InfoTooltip id="backups.col.retention"><span>Retention</span></InfoTooltip></th>
                  {showsMedium ? <th scope="col"><InfoTooltip id="backups.col.medium"><span>Medium</span></InfoTooltip></th> : null}
                  <th scope="col"><InfoTooltip id="backups.col.status"><span>Status</span></InfoTooltip></th>
                </tr>
              </thead>
              <tbody>
                {shown.map((a) => (
                  <tr
                    key={a.id}
                    // Through the builder, never by concatenation: an
                    // artifact id spans three path segments and each one
                    // has to be escaped on its own (issue #677).
                    onClick={() => navigate(artifactPath(a.id))}
                    style={{ cursor: "pointer" }}
                    data-selected={picked.has(a.id) && unselectableReason(a) === null ? "true" : undefined}
                  >
                    {canRecover ? (
                      <td className="selcell" onClick={(e) => e.stopPropagation()}>
                        <SelectBox artifact={a} on={picked.has(a.id)} onChange={(v) => toggle(a.id, v)} />
                      </td>
                    ) : null}
                    <td className="mono" style={{ whiteSpace: "nowrap", color: "var(--text-2)" }}>
                      {stamp(a.receivedAt)}
                    </td>
                    <td>{a.setName}</td>
                    <td className="mono" style={{ fontSize: "var(--text-sm)" }}>
                      {a.filename}
                      {/* The old Validation column drew a green "Verified" for an ingest-time
                          check, beside the three claims that say what is known now. That column
                          is retired. A validation that FAILED is still worth saying, and is
                          said as the failure it is, in the danger colour, never as a pass. */}
                      {a.validation === "failed" ? (
                        <div style={{ color: "var(--danger)", fontFamily: "var(--font-sans, inherit)" }}>
                          <span aria-hidden="true" style={{ display: "inline-flex", verticalAlign: "middle", marginRight: 4 }}>
                            <Icon name="warning" />
                          </span>
                          Failed validation
                        </div>
                      ) : null}
                    </td>
                    <td className="mono" style={{ textAlign: "right", whiteSpace: "nowrap" }}>{bytes(a.sizeBytes)}</td>
                    <td>
                      <ClaimChips facts={facts.byArtifact[a.id]} available={!!api.assurance && !facts.error} />
                    </td>
                    <td><RetentionCell artifact={a} /></td>
                    {showsMedium ? (
                      <td style={{ whiteSpace: "nowrap" }}><MediumCell artifact={a} /></td>
                    ) : null}
                    <td style={{ fontSize: "var(--text-sm)", color: "var(--text-2)", whiteSpace: "nowrap" }}>
                      {a.remoteSourceRemovedAt ? "Remote source removed" : "Remote source retained"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {(paged ? ungoverned.all : ungoverned.page) > 0 ? (
            <div className="card__footer" style={NOTE_STYLE}>
              <span aria-hidden="true" style={{ color: "var(--warn)", lineHeight: 1.5 }}>
                <Icon name="warning" />
              </span>
              <span>
                {backupsCounted(ungoverned, paged) + (ungoverned[paged ? "all" : "page"] === 1 ? " belongs" : " belong") +
                  " to a backup set whose configuration was removed. No retention policy selects them, so" +
                  " nothing here will ever delete them and they keep the space they occupy. Create the backup" +
                  " set again to put them back under a policy, or remove the files yourself."}
              </span>
            </div>
          ) : null}
          {(paged ? unreported.all : unreported.page) > 0 ? (
            <div className="card__footer" style={NOTE_STYLE}>
              <span aria-hidden="true" style={{ color: "var(--warn)", lineHeight: 1.5 }}>
                <Icon name="warning" />
              </span>
              <span>
                {"This server did not say which retention policy governs " +
                  backupsCounted(unreported, paged).replace(/^One/, "one") +
                  ", so this page cannot tell you which of them nothing will ever delete." +
                  " Updating retnd restores the answer; the retnd unconfigured" +
                  " command has it in the meantime."}
              </span>
            </div>
          ) : null}
          <div
            className="card__footer"
            data-testid="backups-paging"
            style={{ display: "flex", flexWrap: "wrap", alignItems: "center", justifyContent: "space-between", gap: 12, fontSize: "var(--text-sm)", color: "var(--text-2)" }}
          >
            <label style={{ display: "inline-flex", gap: 8, alignItems: "center" }}>
              Rows per page
              <select className="select" style={{ height: 32 }} value={effective} onChange={(e) => pickSize(e.target.value)}>
                {PAGE_SIZES.map((n) => <option key={n} value={n}>{n}</option>)}
                {offerAll ? <option value="all">All</option> : null}
              </select>
            </label>
            <span role="status" data-testid="backups-page-status">
              {"Page " + (win.page + 1) + " of " + win.pages + " \u00b7 " + (win.start + 1) + "\u2013" + win.end + " of " + rows.length}
            </span>
            <span style={{ display: "inline-flex", gap: 8 }}>
              <button className="btn btn--sm" aria-label="Previous page" disabled={win.page === 0} onClick={() => setPageAt(win.page - 1)}>Previous</button>
              <button className="btn btn--sm" aria-label="Next page" disabled={win.page >= win.pages - 1} onClick={() => setPageAt(win.page + 1)}>Next</button>
            </span>
          </div>
          <div
            className="card__footer"
            data-testid="backups-footer"
            style={{ display: "flex", justifyContent: "space-between", gap: 16, fontSize: "var(--text-sm)", color: "var(--text-2)" }}
          >
            <InfoTooltip id="backups.showing">
              <span>{shown.length === rows.length ? "Showing " + rows.length + " artifacts" : "Showing " + shown.length + " of " + rows.length + " artifacts"}</span>
            </InfoTooltip>
            {/* The recovery boundary (#987), in the secondary ink: --text-3 is
                3.35:1 on the light surface and fails AA for text (#1070). The
                old line called every copy "verified" beside chips that take
                care not to. */}
            <span data-testid="backups-boundary" style={{ color: "var(--text-2)" }}>
              {"This product recovers and exports the retained artifact; restoring it into an application remains the operator's responsibility."}
            </span>
          </div>
        </div>
      )}

      {run ? (
        <BackupsRecoverRun
          files={run.files}
          target={run.target}
          acknowledged={run.acknowledged}
          once={() => { const o = runOnce.current; runOnce.current = {}; return o; }}
          onSourceRefused={(code) => { setRun(null); setSourceRefusal((r) => ({ code, seq: (r?.seq ?? 0) + 1 })); }}
          onClose={() => { setRun(null); setPicked(new Set()); artifacts.reload(); }}
        />
      ) : null}

      {previewSet ? (
        <RetentionPreviewDialog source={previewSet.source} set={previewSet.set} open onClose={() => setPreviewFor(null)} />
      ) : null}
    </>
  );
}

/** One row's checkbox. A row that cannot be recovered is disabled and the
 *  reason is attached to the control, so a screen reader hears it and a
 *  sighted reader can read it on hover; "select all" never takes it. */
function SelectBox({ artifact, on, onChange }: { artifact: BackupArtifact; on: boolean; onChange(v: boolean): void }) {
  const why = unselectableReason(artifact);
  const reasonId = "unselectable-" + artifact.id;
  return (
    <>
      <input
        type="checkbox"
        aria-label={"Select " + artifact.filename}
        aria-describedby={why ? reasonId : undefined}
        title={why ?? undefined}
        disabled={why !== null}
        checked={on && why === null}
        onChange={(e) => onChange(e.target.checked)}
      />
      {why ? <span id={reasonId} style={{ position: "absolute", width: 1, height: 1, overflow: "hidden" }}>{why}</span> : null}
    </>
  );
}

/** "One backup above" on a page that holds the whole list, and "300 backups in
 *  this list (3 on this page)" on one that does not. */
function backupsCounted(n: { page: number; all: number }, paged: boolean): string {
  if (!paged) return n.page === 1 ? "One backup above" : n.page + " backups above";
  return (n.all === 1 ? "One backup in this list" : n.all + " backups in this list") +
    " (" + (n.page === 0 ? "none" : n.page) + " on this page)";
}

/** The two footnotes under the table read as one voice, so they share one
 *  style rather than each having its own near-miss of it. */
const NOTE_STYLE = {
  display: "flex",
  gap: 10,
  alignItems: "flex-start",
  fontSize: "var(--text-sm)",
  color: "var(--text-2)"
} as const;

/**
 * What retains one backup, in one cell.
 *
 * Three answers, and only one of them is the badge row this column used to
 * be (issue #523): the tiers keeping a governed backup, exactly as before,
 * or RetentionPolicyBadge's sentence for the two cases where naming a tier
 * would be a stale claim. The wording, and why the badge replaces the
 * tiers rather than sitting beside them, are argued where the badge lives
 * (components/RetentionBadge.tsx); the detail page renders the same badge
 * from the same field, so an operator who clicks a marked row does not
 * land on a page quietly disagreeing with the one they came from.
 */
function RetentionCell({ artifact }: { artifact: BackupArtifact }) {
  if (artifact.retentionPolicy !== "configured") {
    return <RetentionPolicyBadge policy={artifact.retentionPolicy} />;
  }
  return <RetentionBadges classes={artifact.retentionClasses} />;
}

/**
 * Where one backup's copies are, in one cell.
 *
 * Three answers, kept apart on purpose (issue #240):
 *
 *   - no copies at all: "No copy yet", because a backup still arriving has
 *     none, and its partial file on disk is not one;
 *   - copies that can be read: their medium names;
 *   - a copy nobody can confirm or that needs a restore: the medium name
 *     PLUS a badge saying so, because a row that listed only the name
 *     would read as "it is safely over there".
 *
 * The detail page is where the full story is; this is the smallest thing
 * that does not mislead in a list.
 */
function MediumCell({ artifact }: { artifact: BackupArtifact }) {
  if (artifact.placements.length === 0) {
    return <span style={{ fontSize: "var(--text-sm)", color: "var(--text-2)" }}>No copy yet</span>;
  }
  const unreachable = artifact.placements.some((p) => p.access === "unreachable");
  const needsRestore = artifact.placements.some((p) => p.access === "requires_restore" || p.access === "restoring");
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4, alignItems: "flex-start" }}>
      <span className="mono" style={{ fontSize: "var(--text-sm)" }}>
        {artifact.placements.map((p) => p.medium).join(", ")}
      </span>
      {unreachable ? (
        <StatusBadge tone="warn" icon="warning">Out of reach</StatusBadge>
      ) : needsRestore ? (
        <StatusBadge tone="warn" icon="warning">Needs a restore</StatusBadge>
      ) : null}
    </div>
  );
}

