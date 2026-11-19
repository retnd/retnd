/**
 * What the Backups row's Actions menu offers for one backup, and why an item
 * is not available (issue #1202). The menu itself is components/ActionMenu.tsx
 * and the panel that follows what it started is BackupRowActions.tsx; this
 * file is the part that decides, kept apart so it holds no component.
 */
import type { ActionMenuGroup, ActionMenuItem } from "@shared/components/ActionMenu";
import type { GateRefusalNotice } from "@shared/api/gate";
import { NO_VALIDATOR_REASON, recoveryTestWouldBeRefused } from "@shared/assurance/verificationModel";
import type { BackupFacts } from "@shared/assurance/verificationModel";
import type { AssuranceApi, Operation } from "@shared/assurance/types";
import { artifactCommand, artifactsListCommand, recoverCommand, recoveryTestArtifactCommand, retryCommand, scrubCommand } from "@shared/assurance/assuranceCommands";
import type { BackupArtifact } from "@shared/types/backup";
import { BUCKETS } from "@shared/assurance/claims";
import { isRetryable } from "./retryIngestion";

/** One thing the menu started, kept until the operator dismisses it. */
export interface MenuRun {
  key: string;
  kind: "scrub" | "recovery_test" | "retry";
  setId: string;
  setName: string;
  artifactId: string;
  file: string;
  starting: boolean;
  opId?: string;
  /** The operation's state, as its panel last read it. */
  state?: Operation["state"];
  error?: string | null;
  refused?: GateRefusalNotice | null;
  /** Retry ingestion's answer. */
  said?: { tone: "ok" | "bad"; text: string };
}

export interface RowMenuContext {
  readOnly: boolean;
  assurance: AssuranceApi | undefined;
  facts: BackupFacts | undefined;
  runs: MenuRun[];
  onRecover(): void;
  onTest(): void;
  onScrub(): void;
  onRetry(): void;
  onOpen(): void;
}

/** Whether a run is still going, for the busy reasons. */
export const isRunning = (r: MenuRun): boolean => r.starting || (r.opId !== undefined && (r.state === undefined || r.state === "running"));

/** The page's filters, for the terminal line under them. */
export interface ListFilters {
  /** The `?set=` value, from the address. */
  set: string;
  /** The sets this deployment lists. */
  known: ReadonlyArray<{ id: string }> | null | undefined;
  /** The received-date inputs, YYYY-MM-DD or empty. */
  after: string;
  before: string;
  /** The `?assurance=` bucket while it narrows the list, or null. */
  bucket: string | null;
}

/** The "Same list from the terminal" line for the page's filters, or null
 *  when nothing narrows the list or the CLI cannot say what the screen shows.
 *
 *  The address takes any text, and the line is something somebody pastes into
 *  a shell, so a `?set=` value is echoed only if this deployment lists it, or
 *  it has the shape of a set id (source/set) for a set that was removed and
 *  whose backups are still listed (#523). The builder quotes what it prints.
 *
 *  The date inputs become --since and --until (#1251), and the bucket
 *  --assurance (the review of #1256): `artifacts --assurance` classifies a
 *  backup the way `assurance list --bucket` and this page's filter do. A bound
 *  that is not a YYYY-MM-DD date, a range whose end is before its start (the
 *  CLI refuses that), or a bucket the CLI does not have gets no line, rather
 *  than one that lists something else. */
export function filterEcho(f: ListFilters): string | null {
  if (f.set && !f.known?.some((s) => s.id === f.set) && !/^[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/.test(f.set)) return null;
  const isDay = (d: string) => d === "" || /^\d{4}-\d{2}-\d{2}$/.test(d);
  if (!isDay(f.after) || !isDay(f.before) || (f.after && f.before && f.before < f.after)) return null;
  if (f.bucket && !(BUCKETS as readonly string[]).includes(f.bucket)) return null;
  if (!f.set && !f.after && !f.before && !f.bucket) return null;
  return artifactsListCommand({ set: f.set, bucket: f.bucket ?? "", since: f.after, until: f.before });
}

/** The menu for one row. */
export function rowMenuGroups(a: BackupArtifact, ctx: RowMenuContext): ActionMenuGroup[] {
  const { readOnly, assurance } = ctx;
  const ro = (doing: string) => (readOnly ? "This session is read-only, so it cannot " + doing + "." : null);
  const testTwin = recoveryTestArtifactCommand(a.setId, a.id);
  const scrubTwin = scrubCommand(a.setId);
  const recoverTwin = recoverCommand(a.id, "staging", "", false);
  const busyTest = ctx.runs.find((r) => r.kind === "recovery_test" && r.setId === a.setId && isRunning(r));
  const busyScrub = ctx.runs.find((r) => r.kind === "scrub" && r.setId === a.setId && isRunning(r));

  const recover: ActionMenuItem = {
    id: "recover",
    label: "Recover artifact…",
    description: "To a browser download, staging, a storage destination or the source host.",
    disabledReason: ro("start a recovery") ?? (!assurance ? "This server does not offer recovery from the browser. From the terminal: " + recoverTwin : null),
    echo: recoverTwin,
    onSelect: ctx.onRecover
  };
  const test: ActionMenuItem = {
    id: "test",
    label: "Run recovery test",
    description: "Recovers this backup to scratch space and runs its set's check on it.",
    disabledReason:
      ro("start a recovery test")
      ?? (!assurance?.offers.recoveryTest ? "This server has no route to start a recovery test. From the terminal: " + testTwin : null)
      ?? (recoveryTestWouldBeRefused(ctx.facts?.policy) ? NO_VALIDATOR_REASON : null)
      ?? (busyTest ? "A recovery test of " + busyTest.file + " is running for this set, and the engine runs one per set at a time." : null),
    echo: testTwin,
    onSelect: ctx.onTest
  };
  const scrub: ActionMenuItem = {
    id: "scrub",
    label: "Scrub this backup's set",
    description: "Reads every copy in " + a.setName + ", not only this backup's: a scrub is per set.",
    disabledReason:
      ro("start a scrub")
      ?? (!assurance?.offers.operations ? "This server has no route to start a scrub. From the terminal: " + scrubTwin : null)
      ?? (busyScrub ? "A scrub of " + a.setName + " started here is still running." : null),
    echo: scrubTwin,
    onSelect: ctx.onScrub
  };
  const groups: ActionMenuGroup[] = [
    { id: "get", label: "Get it back", items: [recover] },
    { id: "check", label: "Check it", items: [test, scrub] }
  ];
  if (isRetryable(a)) {
    const busyRetry = ctx.runs.find((r) => r.kind === "retry" && r.artifactId === a.id && r.starting);
    groups.push({
      id: "repair",
      label: "Repair",
      items: [{
        id: "retry",
        label: "Retry ingestion",
        description: "This backup FAILED and is not quarantined, so no cycle will attempt it again on its own.",
        disabledReason: ro("re-attempt a backup") ?? (busyRetry ? "A retry of this backup is on its way to the service." : null),
        echo: retryCommand(a.id),
        onSelect: ctx.onRetry
      }]
    });
  }
  groups.push({
    id: "more",
    label: "More",
    items: [{ id: "open", label: "Open backup page", description: "Its copies, lifecycle and the full assurance card.", echo: artifactCommand(a.id), onSelect: ctx.onOpen }]
  });
  return groups;
}
